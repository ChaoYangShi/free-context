package mcp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ChaoYangShi/free-context/internal/daemon"
	"github.com/ChaoYangShi/free-context/internal/mcp"
	"github.com/ChaoYangShi/free-context/internal/orchestrator"
)

func TestServerListsToolsAndReportsProgress(t *testing.T) {
	t.Parallel()

	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"report_progress","arguments":{"status":"active","completed_work":[],"in_progress_work":["migrating"],"next_action":"continue","blockers":[],"artifact_references":[]},"_meta":{"threadId":"root-1"}}}`,
	}, "\n") + "\n"
	var output bytes.Buffer
	commander := &fakeCommander{}
	server := mcp.NewServer("run-1", commander)
	if err := server.Serve(context.Background(), strings.NewReader(input), &output); err != nil {
		t.Fatalf("serve MCP: %v", err)
	}
	if len(commander.commands) != 1 || commander.commands[0].kind != daemon.CommandReportProgress {
		t.Fatalf("commands = %#v", commander.commands)
	}
	progress, ok := commander.commands[0].command.(orchestrator.ReportProgress)
	if !ok || progress.RunID != "run-1" || progress.ThreadID != "root-1" || progress.NextAction != "continue" {
		t.Fatalf("progress = %#v", commander.commands[0].command)
	}

	decoder := json.NewDecoder(&output)
	responses := 0
	for decoder.More() {
		var response map[string]any
		if err := decoder.Decode(&response); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		responses++
	}
	if responses != 3 {
		t.Fatalf("responses = %d, want 3; output=%s", responses, output.String())
	}
}

func TestServerRejectsToolCallsWithoutCodexThreadMetadata(t *testing.T) {
	input := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_run_state","arguments":{}}}` + "\n"
	var output bytes.Buffer
	server := mcp.NewServer("run-1", &fakeCommander{})
	if err := server.Serve(context.Background(), strings.NewReader(input), &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "_meta.threadId") {
		t.Fatalf("expected missing identity error, got %s", output.String())
	}
}

type recordedCommand struct {
	kind    daemon.CommandKind
	command any
}

type fakeCommander struct {
	commands []recordedCommand
}

func (f *fakeCommander) Execute(_ context.Context, kind daemon.CommandKind, command any) (orchestrator.Outcome, error) {
	f.commands = append(f.commands, recordedCommand{kind, command})
	return orchestrator.Outcome{}, nil
}

func (f *fakeCommander) Run(context.Context, string) (orchestrator.Run, error) {
	return orchestrator.Run{ID: "run-1", Status: orchestrator.RunActive}, nil
}
