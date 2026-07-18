package handoff

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ChaoYangShi/free-context/internal/codexconfig"
	"github.com/ChaoYangShi/free-context/internal/orchestrator"
)

type fakeExecutor struct {
	mutate func(*orchestrator.Handoff)
}

func (f fakeExecutor) Run(_ context.Context, request ExecRequest) error {
	start := strings.Index(request.Prompt, "template exactly: ")
	end := strings.Index(request.Prompt[start:], "\n\nSummarize")
	data := request.Prompt[start+len("template exactly: ") : start+end]
	var output orchestrator.Handoff
	if err := json.Unmarshal([]byte(data), &output); err != nil {
		return err
	}
	output.Constraints = []string{"approval_policy=never"}
	output.InProgressWork = []string{"implement the next step"}
	output.NextAction = "continue the implementation"
	if f.mutate != nil {
		f.mutate(&output)
	}
	encoded, err := json.Marshal(output)
	if err != nil {
		return err
	}
	return os.WriteFile(request.OutputPath, encoded, 0o600)
}

func TestGenerateProducesValidatedHandoff(t *testing.T) {
	workspace := t.TempDir()
	transcript := filepath.Join(workspace, "transcript.jsonl")
	if err := os.WriteFile(transcript, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 14, 8, 0, 0, 0, time.UTC)
	runner := Runner{Executor: fakeExecutor{}, Now: func() time.Time { return now }, NewID: func() string { return "handoff-1" }}
	handoff, err := runner.Generate(context.Background(), GenerateInput{
		Run:    orchestrator.Run{ID: "run-1", WorkspacePath: workspace, Objective: "finish", CompletionCriteria: []string{"tests pass"}, Sandbox: codexconfig.DangerFullAccessSandbox},
		Thread: orchestrator.Thread{ID: "worker-1", ParentThreadID: "root-1", AssignedTask: "implement", Model: "gpt-5", TranscriptPath: transcript, CurrentTurnID: "turn-1"},
		Scope:  orchestrator.HandoffAgent,
	})
	if err != nil {
		t.Fatal(err)
	}
	if handoff.ID != "handoff-1" || handoff.ParentThreadID == nil || *handoff.ParentThreadID != "root-1" || handoff.NextAction == "" {
		t.Fatalf("unexpected handoff %#v", handoff)
	}
}

func TestCodexExecArgsUseCurrentApprovalConfig(t *testing.T) {
	args := codexExecArgs(ExecRequest{
		WorkspacePath: "/workspace",
		Model:         "gpt-test",
		SchemaPath:    "/tmp/schema.json",
		OutputPath:    "/tmp/output.json",
		Prompt:        "summarize",
	})
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "--ask-for-approval") {
		t.Fatalf("deprecated approval flag present in %q", joined)
	}
	if !strings.Contains(joined, `approval_policy="never"`) {
		t.Fatalf("approval policy override missing from %q", joined)
	}
	if !strings.Contains(joined, codexconfig.DangerouslyBypassApprovalsAndSandboxFlag) {
		t.Fatalf("dangerous sandbox bypass flag missing from %q", joined)
	}
	if strings.Contains(joined, "--sandbox") {
		t.Fatalf("sandbox flag should be replaced by bypass flag in %q", joined)
	}
	if strings.Contains(joined, "--ignore-user-config") {
		t.Fatalf("handoff agent discarded provider and authentication config in %q", joined)
	}
	if !strings.Contains(joined, `model_reasoning_effort="low"`) {
		t.Fatalf("low reasoning effort missing from %q", joined)
	}
}

func TestPromptLimitsWorkspaceInspectionToReferencedArtifacts(t *testing.T) {
	prompt := prompt(orchestrator.Handoff{WorkspacePath: "/workspace"}, "/sessions/root.jsonl")
	if !strings.Contains(prompt, "Do not perform broad workspace exploration") {
		t.Fatalf("handoff prompt permits broad workspace exploration: %s", prompt)
	}
	if !strings.Contains(prompt, "artifact paths referenced by the transcript") {
		t.Fatalf("handoff prompt does not identify its workspace boundary: %s", prompt)
	}
}

func TestGenerateRejectsChangedIdentity(t *testing.T) {
	workspace := t.TempDir()
	transcript := filepath.Join(workspace, "transcript.jsonl")
	if err := os.WriteFile(transcript, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	runner := Runner{Executor: fakeExecutor{mutate: func(value *orchestrator.Handoff) { value.RunID = "other" }}, NewID: func() string { return "handoff-1" }}
	_, err := runner.Generate(context.Background(), GenerateInput{
		Run:    orchestrator.Run{ID: "run-1", WorkspacePath: workspace, Objective: "finish", CompletionCriteria: []string{}, Sandbox: codexconfig.DangerFullAccessSandbox},
		Thread: orchestrator.Thread{ID: "root-1", AssignedTask: "finish", Model: "gpt-5", TranscriptPath: transcript, CurrentTurnID: "turn-1"},
		Scope:  orchestrator.HandoffTree,
	})
	if err == nil {
		t.Fatal("expected changed identity to be rejected")
	}
}

func TestGenerateRejectsSecrets(t *testing.T) {
	workspace := t.TempDir()
	transcript := filepath.Join(workspace, "transcript.jsonl")
	if err := os.WriteFile(transcript, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	runner := Runner{Executor: fakeExecutor{mutate: func(value *orchestrator.Handoff) { value.NextAction = "use sk-1234567890abcdef" }}, NewID: func() string { return "handoff-1" }}
	_, err := runner.Generate(context.Background(), GenerateInput{
		Run:    orchestrator.Run{ID: "run-1", WorkspacePath: workspace, Objective: "finish", CompletionCriteria: []string{}, Sandbox: codexconfig.DangerFullAccessSandbox},
		Thread: orchestrator.Thread{ID: "root-1", AssignedTask: "finish", Model: "gpt-5", TranscriptPath: transcript, CurrentTurnID: "turn-1"},
		Scope:  orchestrator.HandoffTree,
	})
	if err == nil {
		t.Fatal("expected secret to be rejected")
	}
}
