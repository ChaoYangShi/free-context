package daemon

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/ChaoYangShi/free-context/internal/appserver"
	"github.com/ChaoYangShi/free-context/internal/codexconfig"
	"github.com/ChaoYangShi/free-context/internal/codexrpc"
	"github.com/ChaoYangShi/free-context/internal/handoff"
	"github.com/ChaoYangShi/free-context/internal/orchestrator"
	"github.com/ChaoYangShi/free-context/internal/store"
)

type fakeRuntime struct {
	nextThread codexrpc.Thread
	nextTurn   codexrpc.Turn
	interrupts []string
	archives   []string
	steers     []string
	settings   []string
}

func (f *fakeRuntime) StartThread(context.Context, codexrpc.StartThreadInput) (codexrpc.Thread, error) {
	return f.nextThread, nil
}

func (f *fakeRuntime) ResumeThread(_ context.Context, threadID string) (codexrpc.Thread, error) {
	return codexrpc.Thread{ID: threadID, Model: "gpt-test"}, nil
}

func (f *fakeRuntime) StartTurn(context.Context, string, string, string, string, string) (codexrpc.Turn, error) {
	return f.nextTurn, nil
}

func (f *fakeRuntime) SteerTurn(_ context.Context, threadID, turnID, prompt string) error {
	f.steers = append(f.steers, threadID+":"+turnID+":"+prompt)
	return nil
}

func (f *fakeRuntime) Interrupt(_ context.Context, threadID, turnID string) error {
	f.interrupts = append(f.interrupts, threadID+":"+turnID)
	return nil
}

func (f *fakeRuntime) UpdateThreadSettings(_ context.Context, threadID, model, sandbox string) error {
	f.settings = append(f.settings, threadID+":"+model+":"+sandbox)
	return nil
}

func (f *fakeRuntime) ArchiveThread(_ context.Context, threadID string) error {
	f.archives = append(f.archives, threadID)
	return nil
}

func (f *fakeRuntime) Close() error { return nil }

func (f *fakeRuntime) Endpoint() string { return "unix:///tmp/fake.sock" }

func (f *fakeRuntime) PID() int { return 1234 }

type fakeAppServers struct {
	runtime *fakeRuntime
	stopped bool
}

func (f *fakeAppServers) Start(context.Context, orchestrator.Run) (appserver.Runtime, error) {
	return f.runtime, nil
}

func (f *fakeAppServers) Get(string) (appserver.Runtime, error) { return f.runtime, nil }

func (f *fakeAppServers) Stop(string) error {
	f.stopped = true
	return nil
}

type fakeHandoffs struct {
	next int
}

func (f *fakeHandoffs) Generate(_ context.Context, input handoff.GenerateInput) (orchestrator.Handoff, error) {
	f.next++
	id := fmt.Sprintf("handoff-%d", f.next)
	return orchestrator.Handoff{
		ID:                 id,
		RunID:              input.Run.ID,
		CreatedAt:          time.Date(2026, 7, 14, 9, f.next, 0, 0, time.UTC),
		Scope:              input.Scope,
		SourceSessionID:    input.Thread.ID,
		SourceTurnID:       input.Thread.CurrentTurnID,
		SourceThreadID:     input.Thread.ID,
		ParentThreadID:     pointerOrNil(input.Thread.ParentThreadID),
		WorkspacePath:      input.Run.WorkspacePath,
		AssignedTask:       input.Thread.AssignedTask,
		Model:              input.Thread.Model,
		Objective:          input.Run.Objective,
		CompletionCriteria: append([]string{}, input.Run.CompletionCriteria...),
		Constraints:        []string{"approval_policy=never"},
		Decisions:          []orchestrator.Decision{},
		CompletedWork:      []orchestrator.CompletedWork{},
		InProgressWork:     []string{"unfinished work"},
		NextAction:         "continue",
		Blockers:           []string{},
		ArtifactReferences: []string{},
		SuggestedSkills:    []string{},
		ChildHandoffIDs:    append([]string{}, input.ChildHandoffIDs...),
	}, nil
}

func TestControllerCompletesWorkerRotationAndSteersParent(t *testing.T) {
	controller, servers, runID := newTestController(t)
	ctx := context.Background()
	if _, err := controller.Execute(ctx, orchestrator.RegisterThread{RunID: runID, ThreadID: "worker-1", ParentThreadID: "root-1", Role: orchestrator.RoleWorker, AssignedTask: "copy rows", Model: "gpt-test", TranscriptPath: "/tmp/worker.jsonl", TurnID: "turn-worker"}); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.Execute(ctx, orchestrator.BeginCompaction{RunID: runID, ThreadID: "worker-1", TurnID: "turn-worker", Trigger: "auto"}); err != nil {
		t.Fatal(err)
	}
	run, err := controller.Repository.Load(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Threads["worker-1"].Status != orchestrator.ThreadRetired || len(run.Handoffs) != 1 {
		t.Fatalf("worker rotation state = %#v", run)
	}
	if len(servers.runtime.steers) != 1 || len(servers.runtime.archives) != 1 {
		t.Fatalf("runtime calls: steers=%v archives=%v", servers.runtime.steers, servers.runtime.archives)
	}
}

func TestControllerCompletesRootTransferOnlyAfterExplicitAcceptance(t *testing.T) {
	controller, servers, runID := newTestController(t)
	ctx := context.Background()
	if _, err := controller.Execute(ctx, orchestrator.RegisterThread{RunID: runID, ThreadID: "worker-1", ParentThreadID: "root-1", Role: orchestrator.RoleWorker, AssignedTask: "copy rows", Model: "gpt-test", TranscriptPath: "/tmp/worker.jsonl", TurnID: "turn-worker"}); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.Execute(ctx, orchestrator.BeginCompaction{RunID: runID, ThreadID: "root-1", TurnID: "turn-root", Trigger: "manual"}); err != nil {
		t.Fatal(err)
	}
	run, err := controller.Repository.Load(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != orchestrator.RunTransitioning || run.Transition.NewRootID != "root-2" || run.Transition.Phase != orchestrator.PhaseAwaitingRoot {
		t.Fatalf("run should await explicit acceptance: %#v", run.Transition)
	}
	if _, err := controller.Execute(ctx, orchestrator.AcceptHandoff{RunID: runID, ThreadID: "root-2", HandoffID: run.Transition.TreeHandoffID}); err != nil {
		t.Fatal(err)
	}
	run, err = controller.Repository.Load(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != orchestrator.RunActive || run.RootThreadID != "root-2" || run.Threads["root-1"].Status != orchestrator.ThreadRetired {
		t.Fatalf("root transfer state = %#v", run)
	}
	if len(servers.runtime.settings) != 1 || servers.runtime.settings[0] != "root-2:gpt-test:"+codexconfig.DangerFullAccessSandbox {
		t.Fatalf("sandbox was not restored: %v", servers.runtime.settings)
	}
}

func TestControllerRemovesCompletedRunWhenForegroundExits(t *testing.T) {
	controller, servers, runID := newTestController(t)
	ctx := context.Background()
	if _, err := controller.Execute(ctx, orchestrator.ReportProgress{
		RunID: runID, ThreadID: "root-1", Status: orchestrator.ProgressCompleted,
		CompletedWork: []string{"migration finished"}, InProgressWork: []string{},
		NextAction: "", Blockers: []string{}, Artifacts: []string{"report.json"},
	}); err != nil {
		t.Fatal(err)
	}

	outcome, err := controller.Execute(ctx, orchestrator.FinalizeReportedCompletion{RunID: runID})
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Run.Status != orchestrator.RunComplete {
		t.Fatalf("run status = %q, want completed", outcome.Run.Status)
	}
	if !servers.stopped {
		t.Fatal("app-server was not stopped")
	}
	if _, err := controller.Repository.Load(ctx, runID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("load completed run error = %v, want not found", err)
	}
}

func TestControllerKeepsIncompleteRunWhenForegroundExits(t *testing.T) {
	controller, servers, runID := newTestController(t)
	ctx := context.Background()

	outcome, err := controller.Execute(ctx, orchestrator.FinalizeReportedCompletion{RunID: runID})
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Run.Status != orchestrator.RunActive {
		t.Fatalf("run status = %q, want active", outcome.Run.Status)
	}
	if servers.stopped {
		t.Fatal("app-server was stopped for incomplete run")
	}
	if _, err := controller.Repository.Load(ctx, runID); err != nil {
		t.Fatalf("load incomplete run: %v", err)
	}
}

func TestControllerRecordsTokenCapacityNotification(t *testing.T) {
	controller, _, runID := newTestController(t)
	ctx := context.Background()

	controller.HandleNotification(runID, []byte(`{
		"method": "thread/tokenUsage/updated",
		"params": {
			"threadId": "root-1",
			"turnId": "turn-root",
			"tokenUsage": {
				"total": {
					"totalTokens": 164000,
					"inputTokens": 150000,
					"cachedInputTokens": 10000,
					"outputTokens": 9000,
					"reasoningOutputTokens": 5000
				},
				"last": {"totalTokens": 1200},
				"modelContextWindow": 200000
			}
		}
	}`))

	run, err := controller.Repository.Load(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	root := run.Threads["root-1"]
	if run.Status != orchestrator.RunActive || root.Status != orchestrator.ThreadActive {
		t.Fatalf("token capacity notification changed lifecycle: run=%s root=%s", run.Status, root.Status)
	}
	if root.TokenCapacity == nil {
		t.Fatal("token capacity snapshot was not recorded")
	}
	if root.TokenCapacity.TurnID != "turn-root" || root.TokenCapacity.TotalTokens != 164000 || root.TokenCapacity.LastTotalTokens != 1200 || root.TokenCapacity.ModelContextWindow != 200000 {
		t.Fatalf("token capacity = %#v", root.TokenCapacity)
	}
}

func newTestController(t *testing.T) (*Controller, *fakeAppServers, string) {
	t.Helper()
	workspace := t.TempDir()
	repository := store.NewFS(filepath.Join(t.TempDir(), "state"))
	ids := []string{"run-1"}
	engine := orchestrator.New(repository, func() time.Time { return time.Date(2026, 7, 14, 9, 0, 0, 0, time.UTC) }, func() string {
		id := ids[0]
		ids = ids[1:]
		return id
	})
	rootPath := "/tmp/root.jsonl"
	replacementPath := "/tmp/root-2.jsonl"
	runtime := &fakeRuntime{nextThread: codexrpc.Thread{ID: "root-2", Path: &replacementPath, Model: "gpt-test"}, nextTurn: codexrpc.Turn{ID: "turn-root-2"}}
	servers := &fakeAppServers{runtime: runtime}
	controller := NewController(engine, repository, servers, &fakeHandoffs{})
	outcome, err := controller.Execute(context.Background(), orchestrator.StartRun{WorkspacePath: workspace, Objective: "finish migration", CompletionCriteria: []string{"all rows copied"}, Sandbox: codexconfig.DangerFullAccessSandbox})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := controller.Execute(context.Background(), orchestrator.RegisterThread{RunID: outcome.Run.ID, ThreadID: "root-1", Role: orchestrator.RoleRoot, AssignedTask: "finish migration", Model: "gpt-test", TranscriptPath: rootPath}); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.Execute(context.Background(), orchestrator.ThreadTurnStarted{RunID: outcome.Run.ID, ThreadID: "root-1", TurnID: "turn-root"}); err != nil {
		t.Fatal(err)
	}
	return controller, servers, outcome.Run.ID
}

func pointerOrNil(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
