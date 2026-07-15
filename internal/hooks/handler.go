package hooks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/ChaoYangShi/free-context/internal/daemon"
	"github.com/ChaoYangShi/free-context/internal/orchestrator"
)

const (
	RunIDEnv          = "FREE_CONTEXT_RUN_ID"
	DaemonSocketEnv   = "FREE_CONTEXT_DAEMON_SOCKET"
	PreCompactCommand = "pre-compact"
	PreToolUseCommand = "pre-tool-use"
)

type PreCompactInput struct {
	SessionID      string  `json:"session_id"`
	TurnID         string  `json:"turn_id"`
	TranscriptPath *string `json:"transcript_path"`
	CWD            string  `json:"cwd"`
	HookEventName  string  `json:"hook_event_name"`
	Model          string  `json:"model"`
	Trigger        string  `json:"trigger"`
}

type PreToolUseInput struct {
	SessionID      string          `json:"session_id"`
	TurnID         string          `json:"turn_id"`
	TranscriptPath *string         `json:"transcript_path"`
	CWD            string          `json:"cwd"`
	HookEventName  string          `json:"hook_event_name"`
	Model          string          `json:"model"`
	PermissionMode string          `json:"permission_mode"`
	ToolName       string          `json:"tool_name"`
	ToolUseID      string          `json:"tool_use_id"`
	ToolInput      json.RawMessage `json:"tool_input"`
}

type Commander interface {
	Execute(context.Context, daemon.CommandKind, any) (orchestrator.Outcome, error)
	Run(context.Context, string) (orchestrator.Run, error)
	State(context.Context, string) (daemon.RunState, error)
}

type Handler struct {
	Commander Commander
	RunID     string
}

func New(commander Commander, runID string) *Handler {
	return &Handler{Commander: commander, RunID: runID}
}

func FromEnvironment() (*Handler, error) {
	socket := strings.TrimSpace(os.Getenv(DaemonSocketEnv))
	runID := strings.TrimSpace(os.Getenv(RunIDEnv))
	if socket == "" || runID == "" {
		return nil, errors.New("free-context hook environment is incomplete")
	}
	return New(daemon.NewClient(socket), runID), nil
}

func (h *Handler) PreCompact(ctx context.Context, input PreCompactInput) ([]byte, error) {
	if err := validatePreCompact(input); err != nil {
		return stopResponse("free-context rejected PreCompact: " + err.Error())
	}
	if h == nil || h.Commander == nil || strings.TrimSpace(h.RunID) == "" {
		return stopResponse("free-context daemon is unavailable")
	}
	_, err := h.Commander.Execute(ctx, daemon.CommandBeginCompaction, orchestrator.BeginCompaction{
		RunID:    h.RunID,
		ThreadID: input.SessionID,
		TurnID:   input.TurnID,
		Trigger:  input.Trigger,
	})
	if err != nil {
		return stopResponse("free-context could not checkpoint the session: " + err.Error())
	}
	return stopResponse("free-context is rotating this session")
}

func (h *Handler) PreToolUse(ctx context.Context, input PreToolUseInput) ([]byte, error) {
	if err := validatePreToolUse(input); err != nil {
		return denyResponse("free-context rejected PreToolUse: " + err.Error())
	}
	if h == nil || h.Commander == nil || strings.TrimSpace(h.RunID) == "" {
		return denyResponse("free-context daemon is unavailable")
	}
	run, err := h.Commander.Run(ctx, h.RunID)
	if err != nil {
		return denyResponse("free-context could not read run state: " + err.Error())
	}
	if run.Status == orchestrator.RunTransitioning && strings.HasPrefix(input.ToolName, "mcp__free_context__") {
		return []byte("{}"), nil
	}
	if run.Status == orchestrator.RunTransitioning || run.Status == orchestrator.RunBlocked || run.Status == orchestrator.RunStopped || run.Status == orchestrator.RunComplete {
		return denyResponse("free-context is quiescing this run")
	}
	return []byte("{}"), nil
}

func Serve(ctx context.Context, command string, input io.Reader, output io.Writer, handler *Handler) error {
	data, err := io.ReadAll(input)
	if err != nil {
		return err
	}
	var response []byte
	switch command {
	case PreCompactCommand:
		var request PreCompactInput
		if err := decode(data, &request); err != nil {
			response, _ = stopResponse("free-context could not parse PreCompact input: " + err.Error())
		} else {
			response, err = handler.PreCompact(ctx, request)
		}
	case PreToolUseCommand:
		var request PreToolUseInput
		if err := decode(data, &request); err != nil {
			response, _ = denyResponse("free-context could not parse PreToolUse input: " + err.Error())
		} else {
			response, err = handler.PreToolUse(ctx, request)
		}
	default:
		return fmt.Errorf("unknown hook command %q", command)
	}
	if err != nil {
		return err
	}
	_, err = output.Write(append(response, '\n'))
	return err
}

func decode(data []byte, target any) error {
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	return nil
}

func validatePreCompact(input PreCompactInput) error {
	if input.HookEventName != "PreCompact" {
		return errors.New("hook_event_name must be PreCompact")
	}
	if input.SessionID == "" || input.TurnID == "" || input.CWD == "" || input.Model == "" {
		return errors.New("session_id, turn_id, cwd, and model are required")
	}
	if input.Trigger != "manual" && input.Trigger != "auto" {
		return fmt.Errorf("unsupported trigger %q", input.Trigger)
	}
	return nil
}

func validatePreToolUse(input PreToolUseInput) error {
	if input.HookEventName != "PreToolUse" {
		return errors.New("hook_event_name must be PreToolUse")
	}
	if input.SessionID == "" || input.TurnID == "" || input.CWD == "" || input.Model == "" || input.ToolName == "" || input.ToolUseID == "" {
		return errors.New("session_id, turn_id, cwd, model, tool_name, and tool_use_id are required")
	}
	return nil
}

func stopResponse(reason string) ([]byte, error) {
	return json.Marshal(map[string]any{"continue": false, "stopReason": reason})
}

func denyResponse(reason string) ([]byte, error) {
	return json.Marshal(map[string]any{"hookSpecificOutput": map[string]any{
		"hookEventName":            "PreToolUse",
		"permissionDecision":       "deny",
		"permissionDecisionReason": reason,
	}})
}
