package codexrpc_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ChaoYangShi/free-context/internal/codexconfig"
	"github.com/ChaoYangShi/free-context/internal/codexrpc"
)

func TestClientInitializesAndStartsThreadUsingExplicitPermissions(t *testing.T) {
	transport := &fakeTransport{
		responses: []json.RawMessage{
			json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":{"platformOs":"linux"}}`),
			json.RawMessage(`{"jsonrpc":"2.0","id":2,"result":{"thread":{"id":"thread-1","status":{"type":"idle"}},"model":"gpt-test"}}`),
		},
	}
	client := codexrpc.New(transport, "0.144.4")
	if err := client.Initialize(context.Background()); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	thread, err := client.StartThread(context.Background(), codexrpc.StartThreadInput{
		WorkspacePath: t.TempDir(), Model: "gpt-test", Sandbox: codexconfig.DangerFullAccessSandbox,
	})
	if err != nil {
		t.Fatalf("start thread: %v", err)
	}
	if thread.ID != "thread-1" {
		t.Fatalf("thread id = %q", thread.ID)
	}
	if !strings.Contains(transport.requests[1], "\"approvalPolicy\":\"never\"") {
		t.Fatalf("start request did not force approval policy: %s", transport.requests[1])
	}
	if !strings.Contains(transport.requests[1], `"sandbox":"danger-full-access"`) {
		t.Fatalf("start request did not use danger full access: %s", transport.requests[1])
	}
}

func TestStartTurnUsesStructuredDangerFullAccessSandboxPolicy(t *testing.T) {
	transport := &fakeTransport{responses: []json.RawMessage{json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":{"turn":{"id":"turn-1","status":"inProgress"}}}`)}}
	client := codexrpc.New(transport, "0.144.4")
	if _, err := client.StartTurn(context.Background(), "thread-1", "verify", t.TempDir(), "gpt-test", codexconfig.DangerFullAccessSandbox); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(transport.requests[0], `"sandboxPolicy":{"type":"dangerFullAccess"}`) {
		t.Fatalf("unexpected turn request: %s", transport.requests[0])
	}
}

func TestSteerTurnIncludesExpectedTurnID(t *testing.T) {
	transport := &fakeTransport{responses: []json.RawMessage{json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":{}}`)}}
	client := codexrpc.New(transport, "0.144.4")
	if err := client.SteerTurn(context.Background(), "thread-1", "turn-1", "continue"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(transport.requests[0], `"expectedTurnId":"turn-1"`) {
		t.Fatalf("steer request omitted expected turn: %s", transport.requests[0])
	}
}

func TestMinimumSupportedVersionIsExact(t *testing.T) {
	for _, version := range []string{"0.143.9", "0.144.0", "0.144.3"} {
		client := codexrpc.New(&fakeTransport{}, version)
		if err := client.Initialize(context.Background()); err == nil {
			t.Fatalf("expected %s to be rejected", version)
		}
	}
}

type fakeTransport struct {
	responses []json.RawMessage
	requests  []string
}

func (f *fakeTransport) Call(_ context.Context, request []byte) ([]byte, error) {
	f.requests = append(f.requests, string(request))
	response := f.responses[0]
	f.responses = f.responses[1:]
	return response, nil
}
