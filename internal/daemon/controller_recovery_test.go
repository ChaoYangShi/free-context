package daemon

import (
	"context"
	"testing"

	"github.com/ChaoYangShi/free-context/internal/orchestrator"
)

func TestControllerRecoversPersistedRootIntoReadOnlyReplacement(t *testing.T) {
	controller, _, runID := newTestController(t)
	if err := controller.Recover(context.Background(), runID); err != nil {
		t.Fatal(err)
	}
	run, err := controller.Repository.Load(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if !run.Recovering || run.Transition.Phase != orchestrator.PhaseAwaitingRoot || run.Transition.NewRootID != "root-2" {
		t.Fatalf("recovered run = %#v", run)
	}
	if _, err := controller.Execute(context.Background(), orchestrator.AcceptHandoff{RunID: runID, ThreadID: "root-2", HandoffID: run.Transition.TreeHandoffID}); err != nil {
		t.Fatal(err)
	}
	run, err = controller.Repository.Load(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Recovering || run.RootThreadID != "root-2" || run.Status != orchestrator.RunActive {
		t.Fatalf("completed recovery = %#v", run)
	}
}
