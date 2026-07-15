package handoff

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/ChaoYangShi/free-context/internal/orchestrator"
)

type ExecRequest struct {
	WorkspacePath string
	Model         string
	Sandbox       string
	SchemaPath    string
	OutputPath    string
	Prompt        string
}

type Executor interface {
	Run(context.Context, ExecRequest) error
}

type CodexExecutor struct {
	Binary string
}

func (e CodexExecutor) Run(ctx context.Context, request ExecRequest) error {
	binary := e.Binary
	if binary == "" {
		binary = "codex"
	}
	command := exec.CommandContext(ctx, binary,
		"exec",
		"--ephemeral",
		"--skip-git-repo-check",
		"--color", "never",
		"--model", request.Model,
		"--sandbox", request.Sandbox,
		"--ask-for-approval", "never",
		"--cd", request.WorkspacePath,
		"--output-schema", request.SchemaPath,
		"--output-last-message", request.OutputPath,
		request.Prompt,
	)
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("handoff agent failed: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

type Runner struct {
	Executor Executor
	Now      func() time.Time
	NewID    func() string
}

type GenerateInput struct {
	Run             orchestrator.Run
	Thread          orchestrator.Thread
	Scope           orchestrator.HandoffScope
	ChildHandoffIDs []string
}

func (r Runner) Generate(ctx context.Context, input GenerateInput) (orchestrator.Handoff, error) {
	if r.Executor == nil {
		return orchestrator.Handoff{}, errors.New("handoff executor is required")
	}
	if input.Thread.TranscriptPath == "" || !filepath.IsAbs(input.Thread.TranscriptPath) {
		return orchestrator.Handoff{}, errors.New("source transcript path must be absolute")
	}
	now := time.Now
	if r.Now != nil {
		now = r.Now
	}
	newID := randomID
	if r.NewID != nil {
		newID = r.NewID
	}
	handoffID := newID()
	if handoffID == "" {
		return orchestrator.Handoff{}, errors.New("handoff id is empty")
	}
	temporaryDirectory, err := os.MkdirTemp("", "free-context-handoff-")
	if err != nil {
		return orchestrator.Handoff{}, fmt.Errorf("create handoff temporary directory: %w", err)
	}
	defer os.RemoveAll(temporaryDirectory)
	schemaPath := filepath.Join(temporaryDirectory, "schema.json")
	outputPath := filepath.Join(temporaryDirectory, "handoff.json")
	if err := os.WriteFile(schemaPath, schema(), 0o600); err != nil {
		return orchestrator.Handoff{}, fmt.Errorf("write handoff schema: %w", err)
	}
	expected := orchestrator.Handoff{
		ID:                 handoffID,
		RunID:              input.Run.ID,
		CreatedAt:          now().UTC(),
		Scope:              input.Scope,
		SourceSessionID:    input.Thread.ID,
		SourceTurnID:       input.Thread.CurrentTurnID,
		SourceThreadID:     input.Thread.ID,
		ParentThreadID:     nullableString(input.Thread.ParentThreadID),
		WorkspacePath:      input.Run.WorkspacePath,
		AssignedTask:       input.Thread.AssignedTask,
		Model:              input.Thread.Model,
		Objective:          input.Run.Objective,
		CompletionCriteria: copyStrings(input.Run.CompletionCriteria),
		Constraints:        []string{},
		Decisions:          []orchestrator.Decision{},
		CompletedWork:      []orchestrator.CompletedWork{},
		InProgressWork:     []string{},
		NextAction:         "",
		Blockers:           []string{},
		ArtifactReferences: []string{},
		SuggestedSkills:    []string{},
		ChildHandoffIDs:    copyStrings(input.ChildHandoffIDs),
	}
	request := ExecRequest{
		WorkspacePath: input.Run.WorkspacePath,
		Model:         input.Thread.Model,
		Sandbox:       input.Run.Sandbox,
		SchemaPath:    schemaPath,
		OutputPath:    outputPath,
		Prompt:        prompt(expected, input.Thread.TranscriptPath),
	}
	if err := r.Executor.Run(ctx, request); err != nil {
		return orchestrator.Handoff{}, err
	}
	data, err := os.ReadFile(outputPath)
	if err != nil {
		return orchestrator.Handoff{}, fmt.Errorf("read handoff output: %w", err)
	}
	var generated orchestrator.Handoff
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&generated); err != nil {
		return orchestrator.Handoff{}, fmt.Errorf("decode handoff output: %w", err)
	}
	if err := validate(expected, generated); err != nil {
		return orchestrator.Handoff{}, err
	}
	return generated, nil
}

func prompt(expected orchestrator.Handoff, transcriptPath string) string {
	identity, _ := json.Marshal(expected)
	return fmt.Sprintf(`You are the dedicated Free Context handoff agent. Read the source transcript at %q and inspect the workspace at %q. Produce only one JSON object matching the supplied output schema. Preserve every identity, scope, model, objective, completion criterion, and child_handoff_ids value from this template exactly: %s

Summarize only confirmed decisions and execution state. Workspace files and command results are authoritative for execution state. Reference artifacts by path; do not copy their contents. Redact credentials, tokens, cookies, private keys, and environment secrets. Every array field is required and may be empty. next_action must be concrete when work remains.`, transcriptPath, expected.WorkspacePath, identity)
}

func validate(expected, actual orchestrator.Handoff) error {
	if actual.ID != expected.ID || actual.RunID != expected.RunID || !actual.CreatedAt.Equal(expected.CreatedAt) || actual.Scope != expected.Scope || actual.SourceSessionID != expected.SourceSessionID || actual.SourceTurnID != expected.SourceTurnID || actual.SourceThreadID != expected.SourceThreadID || !equalStringPointer(actual.ParentThreadID, expected.ParentThreadID) || actual.WorkspacePath != expected.WorkspacePath || actual.AssignedTask != expected.AssignedTask || actual.Model != expected.Model || actual.Objective != expected.Objective {
		return errors.New("handoff identity or ownership fields do not match the source thread")
	}
	if !equalStrings(actual.CompletionCriteria, expected.CompletionCriteria) || !equalStrings(actual.ChildHandoffIDs, expected.ChildHandoffIDs) {
		return errors.New("handoff completion criteria or child references changed")
	}
	if actual.Constraints == nil || actual.Decisions == nil || actual.CompletedWork == nil || actual.InProgressWork == nil || actual.Blockers == nil || actual.ArtifactReferences == nil || actual.SuggestedSkills == nil || actual.ChildHandoffIDs == nil || actual.CompletionCriteria == nil {
		return errors.New("every handoff array field must be present")
	}
	if len(actual.InProgressWork) != 0 && strings.TrimSpace(actual.NextAction) == "" {
		return errors.New("handoff with in-progress work requires next_action")
	}
	encoded, err := json.Marshal(actual)
	if err != nil {
		return err
	}
	if sensitivePattern.Match(encoded) {
		return errors.New("handoff contains a value that looks like a secret")
	}
	return nil
}

func schema() []byte {
	return []byte(`{
  "$schema":"http://json-schema.org/draft-07/schema#",
  "type":"object",
  "additionalProperties":false,
  "required":["handoff_id","run_id","created_at","scope","source_session_id","source_turn_id","source_thread_id","parent_thread_id","workspace_path","assigned_task","model","objective","completion_criteria","constraints","decisions","completed_work","in_progress_work","next_action","blockers","artifact_references","suggested_skills","child_handoff_ids"],
  "properties":{
    "handoff_id":{"type":"string"},"run_id":{"type":"string"},"created_at":{"type":"string","format":"date-time"},"scope":{"type":"string","enum":["agent","tree"]},"source_session_id":{"type":"string"},"source_turn_id":{"type":"string"},"source_thread_id":{"type":"string"},"parent_thread_id":{"type":["string","null"]},"workspace_path":{"type":"string"},"assigned_task":{"type":"string"},"model":{"type":"string"},"objective":{"type":"string"},
    "completion_criteria":{"type":"array","items":{"type":"string"}},"constraints":{"type":"array","items":{"type":"string"}},"decisions":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["decision","reason"],"properties":{"decision":{"type":"string"},"reason":{"type":"string"}}}},"completed_work":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["result","evidence"],"properties":{"result":{"type":"string"},"evidence":{"type":"array","items":{"type":"string"}}}}},"in_progress_work":{"type":"array","items":{"type":"string"}},"next_action":{"type":"string"},"blockers":{"type":"array","items":{"type":"string"}},"artifact_references":{"type":"array","items":{"type":"string"}},"suggested_skills":{"type":"array","items":{"type":"string"}},"child_handoff_ids":{"type":"array","items":{"type":"string"}}
  }
}`)
}

func nullableString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func equalStringPointer(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func copyStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return append([]string{}, values...)
}

func randomID() string {
	data := make([]byte, 16)
	if _, err := rand.Read(data); err != nil {
		panic(err)
	}
	return hex.EncodeToString(data)
}

var sensitivePattern = regexp.MustCompile(`(?i)(sk-[a-z0-9_-]{12,}|gh[pousr]_[a-z0-9]{20,}|bearer\s+[a-z0-9._-]{12,}|-----begin [a-z ]*private key-----|aws_secret_access_key)`)
