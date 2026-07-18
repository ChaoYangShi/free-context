package orchestrator

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/ChaoYangShi/free-context/internal/codexconfig"
)

func TestStartRunPersistsRunAndRequestsAppServer(t *testing.T) {
	t.Parallel()

	repository := newMemoryRepository()
	engine := New(repository, func() time.Time {
		return time.Date(2026, 7, 14, 8, 0, 0, 0, time.UTC)
	}, func() string { return "run-1" })

	workspace := t.TempDir()
	outcome, err := engine.Execute(context.Background(), StartRun{
		WorkspacePath:      filepath.Join(workspace, "."),
		Objective:          "finish the migration",
		CompletionCriteria: []string{"all records migrated", "checks pass"},
		Sandbox:            "workspace-write",
	})
	if err != nil {
		t.Fatalf("start run: %v", err)
	}

	if outcome.Run.ID != "run-1" {
		t.Fatalf("run id = %q, want run-1", outcome.Run.ID)
	}
	if outcome.Run.Status != RunStarting {
		t.Fatalf("status = %q, want %q", outcome.Run.Status, RunStarting)
	}
	if outcome.Run.WorkspacePath != workspace {
		t.Fatalf("workspace = %q, want %q", outcome.Run.WorkspacePath, workspace)
	}
	if outcome.Run.Sandbox != codexconfig.DangerFullAccessSandbox {
		t.Fatalf("sandbox = %q, want %q", outcome.Run.Sandbox, codexconfig.DangerFullAccessSandbox)
	}
	if len(outcome.Effects) != 1 || outcome.Effects[0].Kind != EffectStartAppServer {
		t.Fatalf("effects = %#v, want one start-app-server effect", outcome.Effects)
	}

	persisted, err := repository.Load(context.Background(), "run-1")
	if err != nil {
		t.Fatalf("load persisted run: %v", err)
	}
	if persisted.Objective != "finish the migration" {
		t.Fatalf("persisted objective = %q", persisted.Objective)
	}
}

func TestStartRunRejectsSecondNonTerminalRunForWorkspace(t *testing.T) {
	t.Parallel()

	repository := newMemoryRepository()
	ids := []string{"run-1", "run-2"}
	engine := New(repository, time.Now, func() string {
		id := ids[0]
		ids = ids[1:]
		return id
	})
	workspace := t.TempDir()
	command := StartRun{
		WorkspacePath:      workspace,
		Objective:          "first task",
		CompletionCriteria: []string{"done"},
		Sandbox:            codexconfig.DangerFullAccessSandbox,
	}
	if _, err := engine.Execute(context.Background(), command); err != nil {
		t.Fatalf("start first run: %v", err)
	}
	command.Objective = "second task"

	_, err := engine.Execute(context.Background(), command)
	if !errors.Is(err, ErrWorkspaceActive) {
		t.Fatalf("error = %v, want ErrWorkspaceActive", err)
	}
}

func TestRegisterThreadsAndReportProgress(t *testing.T) {
	t.Parallel()

	engine, repository := startTestRun(t)
	ctx := context.Background()

	if _, err := engine.Execute(ctx, RegisterThread{
		RunID:          "run-1",
		ThreadID:       "root-1",
		Role:           RoleRoot,
		AssignedTask:   "own the migration plan",
		Model:          "gpt-test",
		TranscriptPath: "/sessions/root.jsonl",
		TurnID:         "turn-root",
	}); err != nil {
		t.Fatalf("register root: %v", err)
	}
	if _, err := engine.Execute(ctx, RegisterThread{
		RunID:          "run-1",
		ThreadID:       "worker-1",
		ParentThreadID: "root-1",
		Role:           RoleWorker,
		AssignedTask:   "migrate accounts",
		Model:          "gpt-test-mini",
		TranscriptPath: "/sessions/worker.jsonl",
		TurnID:         "turn-worker",
	}); err != nil {
		t.Fatalf("register worker: %v", err)
	}

	outcome, err := engine.Execute(ctx, ReportProgress{
		RunID:          "run-1",
		ThreadID:       "worker-1",
		Status:         ProgressActive,
		CompletedWork:  []string{"mapped account fields"},
		InProgressWork: []string{"copying account rows"},
		NextAction:     "copy remaining rows",
		Blockers:       []string{},
		Artifacts:      []string{"docs/account-map.md"},
	})
	if err != nil {
		t.Fatalf("report progress: %v", err)
	}

	if outcome.Run.Status != RunActive || outcome.Run.RootThreadID != "root-1" {
		t.Fatalf("run = %#v, want active root-1", outcome.Run)
	}
	worker := outcome.Run.Threads["worker-1"]
	if worker.ParentThreadID != "root-1" || worker.Progress.NextAction != "copy remaining rows" {
		t.Fatalf("worker = %#v", worker)
	}
	persisted, err := repository.Load(ctx, "run-1")
	if err != nil {
		t.Fatalf("load run: %v", err)
	}
	if persisted.Threads["worker-1"].Progress.Status != ProgressActive {
		t.Fatalf("progress was not persisted: %#v", persisted.Threads["worker-1"].Progress)
	}
}

func TestRecordTokenCapacityPersistsLatestThreadSnapshot(t *testing.T) {
	t.Parallel()

	engine, repository := startTestRun(t)
	ctx := context.Background()
	if _, err := engine.Execute(ctx, RegisterThread{
		RunID:          "run-1",
		ThreadID:       "root-1",
		Role:           RoleRoot,
		AssignedTask:   "own the migration plan",
		Model:          "gpt-test",
		TranscriptPath: "/sessions/root.jsonl",
		TurnID:         "turn-root",
	}); err != nil {
		t.Fatalf("register root: %v", err)
	}

	outcome, err := engine.Execute(ctx, RecordTokenCapacity{
		RunID:    "run-1",
		ThreadID: "root-1",
		Snapshot: TokenCapacitySnapshot{
			TurnID:                "turn-root",
			TotalTokens:           164000,
			InputTokens:           150000,
			CachedInputTokens:     10000,
			OutputTokens:          9000,
			ReasoningOutputTokens: 5000,
			LastTotalTokens:       1200,
			ModelContextWindow:    200000,
		},
	})
	if err != nil {
		t.Fatalf("record token capacity: %v", err)
	}
	if len(outcome.Effects) != 0 || outcome.Run.Status != RunActive {
		t.Fatalf("recording capacity must not affect lifecycle: %#v", outcome)
	}
	snapshot := outcome.Run.Threads["root-1"].TokenCapacity
	if snapshot == nil {
		t.Fatal("token capacity snapshot was not set")
	}
	if snapshot.TotalTokens != 164000 || snapshot.ModelContextWindow != 200000 || snapshot.LastTotalTokens != 1200 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	if snapshot.ObservedAt.IsZero() {
		t.Fatal("observed_at was not set")
	}

	persisted, err := repository.Load(ctx, "run-1")
	if err != nil {
		t.Fatalf("load run: %v", err)
	}
	if persisted.Threads["root-1"].TokenCapacity == nil || persisted.Threads["root-1"].TokenCapacity.TotalTokens != 164000 {
		t.Fatalf("snapshot was not persisted: %#v", persisted.Threads["root-1"].TokenCapacity)
	}
}

func TestRecordTokenCapacityRejectsTotalsBeyondKnownWindow(t *testing.T) {
	t.Parallel()

	engine, _ := startTestRun(t)
	ctx := context.Background()
	if _, err := engine.Execute(ctx, RegisterThread{
		RunID:          "run-1",
		ThreadID:       "root-1",
		Role:           RoleRoot,
		AssignedTask:   "own the migration plan",
		Model:          "gpt-test",
		TranscriptPath: "/sessions/root.jsonl",
	}); err != nil {
		t.Fatalf("register root: %v", err)
	}
	_, err := engine.Execute(ctx, RecordTokenCapacity{
		RunID:    "run-1",
		ThreadID: "root-1",
		Snapshot: TokenCapacitySnapshot{
			TotalTokens:        200001,
			ModelContextWindow: 200000,
		},
	})
	if err == nil {
		t.Fatal("expected impossible token capacity to be rejected")
	}
}

func TestWorkerCompactionPersistsHandoffBeforeStoppingAndSteeringParent(t *testing.T) {
	t.Parallel()

	engine, repository := startTestRunWithTree(t)
	ctx := context.Background()

	outcome, err := engine.Execute(ctx, BeginCompaction{
		RunID:    "run-1",
		ThreadID: "worker-1",
		TurnID:   "turn-worker",
		Trigger:  "auto",
	})
	if err != nil {
		t.Fatalf("begin compaction: %v", err)
	}
	if outcome.Run.Threads["worker-1"].Status != ThreadCheckpointing {
		t.Fatalf("worker status = %q", outcome.Run.Threads["worker-1"].Status)
	}
	assertEffectKinds(t, outcome.Effects, EffectGenerateHandoff)

	handoff := validWorkerHandoff()
	handoff.WorkspacePath = outcome.Run.WorkspacePath
	outcome, err = engine.Execute(ctx, RecordHandoff{RunID: "run-1", Handoff: handoff})
	if err != nil {
		t.Fatalf("record handoff: %v", err)
	}
	if _, exists := repository.handoffs[handoff.ID]; !exists {
		t.Fatal("handoff was not persisted")
	}
	record := outcome.Run.Handoffs[handoff.ID]
	if record.Status != HandoffReady || record.SourceThreadID != "worker-1" {
		t.Fatalf("handoff record = %#v", record)
	}
	assertEffectKinds(t, outcome.Effects, EffectStopThread, EffectSteerParent)
	if outcome.Effects[1].ThreadID != "root-1" || outcome.Effects[1].HandoffID != handoff.ID {
		t.Fatalf("steer effect = %#v", outcome.Effects[1])
	}

	outcome, err = engine.Execute(ctx, ThreadStopped{RunID: "run-1", ThreadID: "worker-1"})
	if err != nil {
		t.Fatalf("stop worker: %v", err)
	}
	if outcome.Run.Threads["worker-1"].Status != ThreadRetired {
		t.Fatalf("worker status = %q, want retired", outcome.Run.Threads["worker-1"].Status)
	}

	outcome, err = engine.Execute(ctx, AcceptHandoff{
		RunID:     "run-1",
		ThreadID:  "root-1",
		HandoffID: handoff.ID,
	})
	if err != nil {
		t.Fatalf("accept handoff: %v", err)
	}
	if outcome.Run.Handoffs[handoff.ID].OwnerThreadID != "root-1" {
		t.Fatalf("handoff was not transferred to parent: %#v", outcome.Run.Handoffs[handoff.ID])
	}

	outcome, err = engine.Execute(ctx, ResolveHandoff{
		RunID:      "run-1",
		ThreadID:   "root-1",
		HandoffID:  handoff.ID,
		Resolution: ResolutionReplanned,
	})
	if err != nil {
		t.Fatalf("resolve handoff: %v", err)
	}
	if outcome.Run.Handoffs[handoff.ID].Status != HandoffResolved {
		t.Fatalf("handoff status = %q", outcome.Run.Handoffs[handoff.ID].Status)
	}
}

func TestRootCompactionTransfersTreeOnlyAfterNewRootAccepts(t *testing.T) {
	t.Parallel()

	engine, _ := startTestRunWithTree(t)
	ctx := context.Background()

	outcome, err := engine.Execute(ctx, BeginCompaction{
		RunID: "run-1", ThreadID: "root-1", TurnID: "turn-root", Trigger: "manual",
	})
	if err != nil {
		t.Fatalf("begin root compaction: %v", err)
	}
	if outcome.Run.Status != RunTransitioning || outcome.Run.Transition.Phase != PhaseQuiescing {
		t.Fatalf("run transition = %#v", outcome.Run.Transition)
	}
	assertEffectKinds(t, outcome.Effects, EffectQuiesceTree)

	outcome, err = engine.Execute(ctx, TreeQuiesced{RunID: "run-1"})
	if err != nil {
		t.Fatalf("quiesce tree: %v", err)
	}
	assertEffectKinds(t, outcome.Effects, EffectGenerateHandoff, EffectGenerateHandoff)
	if outcome.Effects[0].ThreadID != "worker-1" || outcome.Effects[1].ThreadID != "root-1" {
		t.Fatalf("handoff effects must checkpoint children before root: %#v", outcome.Effects)
	}

	workerHandoff := validWorkerHandoff()
	workerHandoff.WorkspacePath = outcome.Run.WorkspacePath
	outcome, err = engine.Execute(ctx, RecordHandoff{RunID: "run-1", Handoff: workerHandoff})
	if err != nil {
		t.Fatalf("record worker handoff: %v", err)
	}
	assertEffectKinds(t, outcome.Effects, EffectStopThread)

	treeHandoff := validTreeHandoff(outcome.Run.WorkspacePath, workerHandoff.ID)
	outcome, err = engine.Execute(ctx, RecordHandoff{RunID: "run-1", Handoff: treeHandoff})
	if err != nil {
		t.Fatalf("record tree handoff: %v", err)
	}
	if outcome.Run.Transition.Phase != PhaseAwaitingRoot {
		t.Fatalf("transition phase = %q", outcome.Run.Transition.Phase)
	}
	assertEffectKinds(t, outcome.Effects, EffectStartRootReadOnly)

	outcome, err = engine.Execute(ctx, RegisterReplacementRoot{
		RunID: "run-1", ThreadID: "root-2", Model: "gpt-test",
		TranscriptPath: "/sessions/root-2.jsonl", TurnID: "turn-root-2",
	})
	if err != nil {
		t.Fatalf("register replacement root: %v", err)
	}
	if outcome.Run.Threads["root-2"].Status != ThreadValidating {
		t.Fatalf("replacement status = %q", outcome.Run.Threads["root-2"].Status)
	}

	outcome, err = engine.Execute(ctx, AcceptHandoff{
		RunID: "run-1", ThreadID: "root-2", HandoffID: treeHandoff.ID,
	})
	if err != nil {
		t.Fatalf("accept tree handoff: %v", err)
	}
	if outcome.Run.Transition.Phase != PhaseRetiringOldTree {
		t.Fatalf("transition phase = %q", outcome.Run.Transition.Phase)
	}
	assertEffectKinds(t, outcome.Effects, EffectRetireTree)

	outcome, err = engine.Execute(ctx, CompleteRootTransfer{RunID: "run-1"})
	if err != nil {
		t.Fatalf("complete root transfer: %v", err)
	}
	if outcome.Run.Status != RunActive || outcome.Run.RootThreadID != "root-2" {
		t.Fatalf("run did not activate replacement root: %#v", outcome.Run)
	}
	if outcome.Run.Threads["root-1"].Status != ThreadRetired || outcome.Run.Threads["root-2"].Status != ThreadActive {
		t.Fatalf("root statuses = old:%q new:%q", outcome.Run.Threads["root-1"].Status, outcome.Run.Threads["root-2"].Status)
	}
	if outcome.Run.Threads["worker-1"].ParentThreadID != "root-2" {
		t.Fatalf("worker parent was not transferred: %#v", outcome.Run.Threads["worker-1"])
	}
	assertEffectKinds(t, outcome.Effects, EffectGrantSandbox)
	if _, err = engine.Execute(ctx, ResolveHandoff{RunID: "run-1", ThreadID: "root-2", HandoffID: treeHandoff.ID, Resolution: ResolutionReplanned}); err != nil {
		t.Fatalf("resolve tree handoff: %v", err)
	}
	if _, err = engine.Execute(ctx, AcceptHandoff{RunID: "run-1", ThreadID: "root-2", HandoffID: workerHandoff.ID}); err != nil {
		t.Fatalf("accept transferred worker handoff: %v", err)
	}
	if _, err = engine.Execute(ctx, ResolveHandoff{RunID: "run-1", ThreadID: "root-2", HandoffID: workerHandoff.ID, Resolution: ResolutionReplanned}); err != nil {
		t.Fatalf("resolve transferred worker handoff: %v", err)
	}
	if _, err = engine.Execute(ctx, ReportProgress{RunID: "run-1", ThreadID: "root-2", Status: ProgressCompleted}); err != nil {
		t.Fatalf("report replacement completion: %v", err)
	}
	outcome, err = engine.Execute(ctx, TurnEnded{RunID: "run-1", ThreadID: "root-2"})
	if err != nil || outcome.Run.Status != RunComplete {
		t.Fatalf("replacement root could not complete: outcome=%#v err=%v", outcome, err)
	}
}

func TestRootTurnEndUsesExplicitProgressAsItsOnlyContinuationSignal(t *testing.T) {
	t.Parallel()

	t.Run("active with next action starts another turn", func(t *testing.T) {
		engine := startTestRunWithRoot(t)
		ctx := context.Background()
		if _, err := engine.Execute(ctx, ReportProgress{
			RunID: "run-1", ThreadID: "root-1", Status: ProgressActive,
			CompletedWork: []string{"planned migration"}, InProgressWork: []string{"running migration"},
			NextAction: "continue the migration", Blockers: []string{}, Artifacts: []string{},
		}); err != nil {
			t.Fatalf("report progress: %v", err)
		}
		outcome, err := engine.Execute(ctx, TurnEnded{RunID: "run-1", ThreadID: "root-1"})
		if err != nil {
			t.Fatalf("end turn: %v", err)
		}
		if outcome.Run.Status != RunActive {
			t.Fatalf("status = %q", outcome.Run.Status)
		}
		assertEffectKinds(t, outcome.Effects, EffectStartNextTurn)
		if outcome.Effects[0].Prompt != "continue the migration" {
			t.Fatalf("next prompt = %q", outcome.Effects[0].Prompt)
		}
	})

	t.Run("missing next action blocks the run", func(t *testing.T) {
		engine := startTestRunWithRoot(t)
		outcome, err := engine.Execute(context.Background(), TurnEnded{RunID: "run-1", ThreadID: "root-1"})
		if err != nil {
			t.Fatalf("end turn: %v", err)
		}
		if outcome.Run.Status != RunBlocked || outcome.Run.BlockedReason == "" {
			t.Fatalf("run = %#v", outcome.Run)
		}
		assertEffectKinds(t, outcome.Effects, EffectBlockTree)
	})

	t.Run("completed root completes the run", func(t *testing.T) {
		engine := startTestRunWithRoot(t)
		ctx := context.Background()
		if _, err := engine.Execute(ctx, ReportProgress{
			RunID: "run-1", ThreadID: "root-1", Status: ProgressCompleted,
			CompletedWork: []string{"migration finished"}, InProgressWork: []string{},
			NextAction: "", Blockers: []string{}, Artifacts: []string{"report.json"},
		}); err != nil {
			t.Fatalf("report completion: %v", err)
		}
		outcome, err := engine.Execute(ctx, TurnEnded{RunID: "run-1", ThreadID: "root-1"})
		if err != nil {
			t.Fatalf("end turn: %v", err)
		}
		if outcome.Run.Status != RunComplete {
			t.Fatalf("status = %q", outcome.Run.Status)
		}
		assertEffectKinds(t, outcome.Effects, EffectStopAppServer, EffectDeleteRun)
	})
}

func startTestRunWithTree(t *testing.T) (*Engine, *memoryRepository) {
	t.Helper()
	engine, repository := startTestRun(t)
	ctx := context.Background()
	commands := []RegisterThread{
		{
			RunID: "run-1", ThreadID: "root-1", Role: RoleRoot,
			AssignedTask: "own the migration", Model: "gpt-test",
			TranscriptPath: "/sessions/root.jsonl", TurnID: "turn-root",
		},
		{
			RunID: "run-1", ThreadID: "worker-1", ParentThreadID: "root-1", Role: RoleWorker,
			AssignedTask: "migrate accounts", Model: "gpt-test-mini",
			TranscriptPath: "/sessions/worker.jsonl", TurnID: "turn-worker",
		},
	}
	for _, command := range commands {
		if _, err := engine.Execute(ctx, command); err != nil {
			t.Fatalf("register %s: %v", command.ThreadID, err)
		}
	}
	return engine, repository
}

func startTestRunWithRoot(t *testing.T) *Engine {
	t.Helper()
	engine, _ := startTestRun(t)
	_, err := engine.Execute(context.Background(), RegisterThread{
		RunID: "run-1", ThreadID: "root-1", Role: RoleRoot,
		AssignedTask: "own the migration", Model: "gpt-test",
		TranscriptPath: "/sessions/root.jsonl", TurnID: "turn-root",
	})
	if err != nil {
		t.Fatalf("register root: %v", err)
	}
	return engine
}

func validWorkerHandoff() Handoff {
	return Handoff{
		ID:                 "handoff-worker-1",
		RunID:              "run-1",
		CreatedAt:          time.Date(2026, 7, 14, 8, 30, 0, 0, time.UTC),
		Scope:              HandoffAgent,
		SourceSessionID:    "worker-1",
		SourceTurnID:       "turn-worker",
		SourceThreadID:     "worker-1",
		ParentThreadID:     pointer("root-1"),
		WorkspacePath:      "/workspace",
		AssignedTask:       "migrate accounts",
		Model:              "gpt-test-mini",
		Objective:          "finish the migration",
		CompletionCriteria: []string{"all records migrated"},
		Constraints:        []string{},
		Decisions:          []Decision{},
		CompletedWork:      []CompletedWork{},
		InProgressWork:     []string{"copying rows"},
		NextAction:         "copy remaining rows",
		Blockers:           []string{},
		ArtifactReferences: []string{},
		SuggestedSkills:    []string{},
		ChildHandoffIDs:    []string{},
	}
}

func pointer(value string) *string {
	return &value
}

func validTreeHandoff(workspace, childID string) Handoff {
	return Handoff{
		ID:                 "handoff-tree-1",
		RunID:              "run-1",
		CreatedAt:          time.Date(2026, 7, 14, 8, 31, 0, 0, time.UTC),
		Scope:              HandoffTree,
		SourceSessionID:    "root-1",
		SourceTurnID:       "turn-root",
		SourceThreadID:     "root-1",
		ParentThreadID:     nil,
		WorkspacePath:      workspace,
		AssignedTask:       "own the migration",
		Model:              "gpt-test",
		Objective:          "finish the migration",
		CompletionCriteria: []string{"all records migrated"},
		Constraints:        []string{},
		Decisions:          []Decision{},
		CompletedWork:      []CompletedWork{},
		InProgressWork:     []string{"coordinating migration"},
		NextAction:         "replan unfinished migration work",
		Blockers:           []string{},
		ArtifactReferences: []string{},
		SuggestedSkills:    []string{},
		ChildHandoffIDs:    []string{childID},
	}
}

func assertEffectKinds(t *testing.T, effects []Effect, want ...EffectKind) {
	t.Helper()
	if len(effects) != len(want) {
		t.Fatalf("effect count = %d, want %d: %#v", len(effects), len(want), effects)
	}
	for index, kind := range want {
		if effects[index].Kind != kind {
			t.Fatalf("effect[%d] = %q, want %q", index, effects[index].Kind, kind)
		}
	}
}

func startTestRun(t *testing.T) (*Engine, *memoryRepository) {
	t.Helper()
	repository := newMemoryRepository()
	engine := New(repository, func() time.Time {
		return time.Date(2026, 7, 14, 8, 0, 0, 0, time.UTC)
	}, func() string { return "run-1" })
	_, err := engine.Execute(context.Background(), StartRun{
		WorkspacePath:      t.TempDir(),
		Objective:          "finish the migration",
		CompletionCriteria: []string{"all records migrated"},
		Sandbox:            codexconfig.DangerFullAccessSandbox,
	})
	if err != nil {
		t.Fatalf("start run: %v", err)
	}
	return engine, repository
}

type memoryRepository struct {
	runs     map[string]Run
	handoffs map[string]Handoff
}

func newMemoryRepository() *memoryRepository {
	return &memoryRepository{
		runs:     make(map[string]Run),
		handoffs: make(map[string]Handoff),
	}
}

func (r *memoryRepository) Create(_ context.Context, run Run) error {
	r.runs[run.ID] = cloneRun(run)
	return nil
}

func (r *memoryRepository) Save(_ context.Context, run Run) error {
	r.runs[run.ID] = cloneRun(run)
	return nil
}

func (r *memoryRepository) Load(_ context.Context, id string) (Run, error) {
	return cloneRun(r.runs[id]), nil
}

func (r *memoryRepository) List(context.Context) ([]Run, error) {
	runs := make([]Run, 0, len(r.runs))
	for _, run := range r.runs {
		runs = append(runs, cloneRun(run))
	}
	return runs, nil
}

func (r *memoryRepository) Delete(_ context.Context, id string) error {
	delete(r.runs, id)
	return nil
}

func (r *memoryRepository) SaveHandoff(_ context.Context, handoff Handoff) error {
	r.handoffs[handoff.ID] = handoff
	return nil
}

func cloneRun(run Run) Run {
	run.CompletionCriteria = append([]string(nil), run.CompletionCriteria...)
	run.Threads = cloneMap(run.Threads)
	for id, thread := range run.Threads {
		if thread.TokenCapacity != nil {
			snapshot := *thread.TokenCapacity
			thread.TokenCapacity = &snapshot
			run.Threads[id] = thread
		}
	}
	run.Handoffs = cloneMap(run.Handoffs)
	return run
}

func cloneMap[K comparable, V any](source map[K]V) map[K]V {
	cloned := make(map[K]V, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}
