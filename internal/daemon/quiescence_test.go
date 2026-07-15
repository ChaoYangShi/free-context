package daemon

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/ChaoYangShi/free-context/internal/orchestrator"
)

func TestRootQuiescenceWaitsForInFlightToolCompletion(t *testing.T) {
	controller, servers, runID := newTestController(t)
	controller.HandleNotification(runID, json.RawMessage(`{"method":"item/started","params":{"threadId":"root-1","turnId":"turn-root","item":{"id":"tool-1","type":"commandExecution"}}}`))
	if _, err := controller.Execute(context.Background(), orchestrator.BeginCompaction{RunID: runID, ThreadID: "root-1", TurnID: "turn-root", Trigger: "auto"}); err != nil {
		t.Fatal(err)
	}
	run, err := controller.Repository.Load(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Transition.Phase != orchestrator.PhaseQuiescing || len(servers.runtime.interrupts) != 0 {
		t.Fatalf("run advanced before tool completion: phase=%s interrupts=%v", run.Transition.Phase, servers.runtime.interrupts)
	}
	controller.HandleNotification(runID, json.RawMessage(`{"method":"item/completed","params":{"threadId":"root-1","turnId":"turn-root","item":{"id":"tool-1","type":"commandExecution"}}}`))
	run, err = controller.Repository.Load(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Transition.Phase != orchestrator.PhaseAwaitingRoot {
		t.Fatalf("run did not advance after tool completion: phase=%s", run.Transition.Phase)
	}
}
