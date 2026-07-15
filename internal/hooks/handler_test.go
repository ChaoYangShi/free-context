package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/ChaoYangShi/free-context/internal/daemon"
	"github.com/ChaoYangShi/free-context/internal/orchestrator"
)

type fakeCommander struct {
	run        orchestrator.Run
	executed   []any
	executeErr error
	runErr     error
}

func (f *fakeCommander) Execute(_ context.Context, _ daemon.CommandKind, command any) (orchestrator.Outcome, error) {
	f.executed = append(f.executed, command)
	return orchestrator.Outcome{}, f.executeErr
}

func (f *fakeCommander) Run(context.Context, string) (orchestrator.Run, error) {
	return f.run, f.runErr
}

func (f *fakeCommander) State(context.Context, string) (daemon.RunState, error) {
	return daemon.RunState{Run: f.run}, f.runErr
}

func validCompactInput() PreCompactInput {
	return PreCompactInput{SessionID: "thread-1", TurnID: "turn-1", CWD: "/workspace", HookEventName: "PreCompact", Model: "gpt-5", Trigger: "auto"}
}

func validToolInput() PreToolUseInput {
	return PreToolUseInput{SessionID: "thread-1", TurnID: "turn-1", CWD: "/workspace", HookEventName: "PreToolUse", Model: "gpt-5", PermissionMode: "default", ToolName: "Bash", ToolUseID: "tool-1", ToolInput: json.RawMessage(`{"command":"ls"}`)}
}

func TestPreCompactStopsCompactionAfterRecordingCommand(t *testing.T) {
	commander := &fakeCommander{}
	response, err := New(commander, "run-1").PreCompact(context.Background(), validCompactInput())
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(response, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["continue"] != false {
		t.Fatalf("expected continue=false, got %#v", payload)
	}
	command, ok := commander.executed[0].(orchestrator.BeginCompaction)
	if !ok || command.RunID != "run-1" || command.ThreadID != "thread-1" || command.Trigger != "auto" {
		t.Fatalf("unexpected command %#v", commander.executed)
	}
}

func TestPreCompactFailsClosedWhenDaemonFails(t *testing.T) {
	commander := &fakeCommander{executeErr: errors.New("daemon down")}
	response, err := New(commander, "run-1").PreCompact(context.Background(), validCompactInput())
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(response, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["continue"] != false {
		t.Fatalf("expected fail-closed response, got %#v", payload)
	}
}

func TestPreToolUseDeniesDuringTransition(t *testing.T) {
	commander := &fakeCommander{run: orchestrator.Run{Status: orchestrator.RunTransitioning}}
	response, err := New(commander, "run-1").PreToolUse(context.Background(), validToolInput())
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		HookSpecificOutput struct {
			PermissionDecision string `json:"permissionDecision"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(response, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.HookSpecificOutput.PermissionDecision != "deny" {
		t.Fatalf("expected deny, got %#v", payload)
	}
}

func TestPreToolUseAllowsActiveRun(t *testing.T) {
	commander := &fakeCommander{run: orchestrator.Run{Status: orchestrator.RunActive}}
	response, err := New(commander, "run-1").PreToolUse(context.Background(), validToolInput())
	if err != nil {
		t.Fatal(err)
	}
	if string(response) != "{}" {
		t.Fatalf("expected empty allow response, got %s", response)
	}
}

func TestServeAllowsSubagentPreToolUseInput(t *testing.T) {
	input := `{"session_id":"parent-thread","turn_id":"turn-1","transcript_path":null,"cwd":"/workspace","hook_event_name":"PreToolUse","model":"gpt-5","permission_mode":"default","tool_name":"Bash","tool_use_id":"tool-1","tool_input":{"command":"pwd"},"agent_id":"worker-1","agent_type":"general-purpose"}`
	commander := &fakeCommander{run: orchestrator.Run{Status: orchestrator.RunActive}}
	var output bytes.Buffer
	if err := Serve(context.Background(), PreToolUseCommand, strings.NewReader(input), &output, New(commander, "run-1")); err != nil {
		t.Fatal(err)
	}
	if output.String() != "{}\n" {
		t.Fatalf("expected subagent tool use to be allowed, got %s", output.String())
	}
}

func TestPreToolUseAllowsLifecycleMCPDuringTransition(t *testing.T) {
	commander := &fakeCommander{run: orchestrator.Run{Status: orchestrator.RunTransitioning}}
	input := validToolInput()
	input.ToolName = "mcp__free_context__accept_handoff"
	response, err := New(commander, "run-1").PreToolUse(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if string(response) != "{}" {
		t.Fatalf("expected lifecycle MCP allow response, got %s", response)
	}
}
