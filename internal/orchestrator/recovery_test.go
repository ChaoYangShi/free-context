package orchestrator

import (
	"context"
	"testing"
)

func TestRecoverRunRebuildsCheckpointEffectsFromPersistedTree(t *testing.T) {
	engine := startTestRunWithRoot(t)
	ctx := context.Background()
	if _, err := engine.Execute(ctx, RegisterThread{
		RunID: "run-1", ThreadID: "worker-1", ParentThreadID: "root-1", Role: RoleWorker,
		AssignedTask: "migrate accounts", Model: "gpt-test", TranscriptPath: "/sessions/worker.jsonl", TurnID: "turn-worker",
	}); err != nil {
		t.Fatal(err)
	}
	outcome, err := engine.Execute(ctx, RecoverRun{RunID: "run-1"})
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.Run.Recovering || outcome.Run.Status != RunTransitioning || outcome.Run.Transition.Phase != PhaseGenerating {
		t.Fatalf("recovery state = %#v", outcome.Run)
	}
	if len(outcome.Effects) != 2 || outcome.Effects[0].ThreadID != "worker-1" || outcome.Effects[1].ThreadID != "root-1" {
		t.Fatalf("recovery effects = %#v", outcome.Effects)
	}
}
