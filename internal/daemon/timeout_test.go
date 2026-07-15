package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/ChaoYangShi/free-context/internal/orchestrator"
)

func TestWorkerHandoffDeadlineBlocksRun(t *testing.T) {
	controller, _, runID := newTestController(t)
	ctx := context.Background()
	if _, err := controller.Execute(ctx, orchestrator.RegisterThread{RunID: runID, ThreadID: "worker-1", ParentThreadID: "root-1", Role: orchestrator.RoleWorker, AssignedTask: "copy", Model: "gpt-test", TranscriptPath: "/tmp/worker.jsonl", TurnID: "turn-worker"}); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.Execute(ctx, orchestrator.BeginCompaction{RunID: runID, ThreadID: "worker-1", TurnID: "turn-worker", Trigger: "auto"}); err != nil {
		t.Fatal(err)
	}
	if err := controller.CheckTimeouts(ctx, time.Date(2026, 7, 14, 9, 31, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	run, err := controller.Repository.Load(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != orchestrator.RunBlocked {
		t.Fatalf("run status = %s, want blocked", run.Status)
	}
}
