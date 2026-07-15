package store_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ChaoYangShi/free-context/internal/orchestrator"
	"github.com/ChaoYangShi/free-context/internal/store"
)

func TestFSRepositoryPersistsRunsAndImmutableHandoffsAcrossReopen(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "state")
	repository := store.NewFS(root)
	run := orchestrator.Run{
		ID:                 "run-1",
		CreatedAt:          time.Date(2026, 7, 14, 8, 0, 0, 0, time.UTC),
		UpdatedAt:          time.Date(2026, 7, 14, 8, 0, 0, 0, time.UTC),
		WorkspacePath:      "/workspace",
		Objective:          "finish migration",
		CompletionCriteria: []string{"done"},
		Sandbox:            "workspace-write",
		Status:             orchestrator.RunStarting,
		Threads:            map[string]orchestrator.Thread{},
		Handoffs:           map[string]orchestrator.HandoffRecord{},
		Revision:           1,
	}
	if err := repository.Create(ctx, run); err != nil {
		t.Fatalf("create run: %v", err)
	}

	run.Status = orchestrator.RunActive
	run.RootThreadID = "root-1"
	run.Threads["root-1"] = orchestrator.Thread{
		ID:     "root-1",
		Role:   orchestrator.RoleRoot,
		Status: orchestrator.ThreadActive,
		TokenCapacity: &orchestrator.TokenCapacitySnapshot{
			TurnID:             "turn-root",
			TotalTokens:        164000,
			LastTotalTokens:    1200,
			ModelContextWindow: 200000,
			ObservedAt:         time.Date(2026, 7, 14, 8, 1, 0, 0, time.UTC),
		},
	}
	run.Revision = 2
	if err := repository.Save(ctx, run); err != nil {
		t.Fatalf("save run: %v", err)
	}
	handoff := orchestrator.Handoff{
		ID: "handoff-1", RunID: "run-1", CreatedAt: run.UpdatedAt,
		Scope: orchestrator.HandoffAgent, SourceSessionID: "worker", SourceTurnID: "turn",
		SourceThreadID: "worker", ParentThreadID: stringPointer("root"), WorkspacePath: "/workspace",
		AssignedTask: "migrate records", Model: "gpt-test", Objective: "finish migration",
		CompletionCriteria: []string{"done"}, Constraints: []string{}, Decisions: []orchestrator.Decision{},
		CompletedWork: []orchestrator.CompletedWork{}, InProgressWork: []string{}, NextAction: "continue",
		Blockers: []string{}, ArtifactReferences: []string{}, SuggestedSkills: []string{}, ChildHandoffIDs: []string{},
	}
	if err := repository.SaveHandoff(ctx, handoff); err != nil {
		t.Fatalf("save handoff: %v", err)
	}
	if err := repository.SaveHandoff(ctx, handoff); !errors.Is(err, store.ErrAlreadyExists) {
		t.Fatalf("second handoff save error = %v, want ErrAlreadyExists", err)
	}

	reopened := store.NewFS(root)
	loaded, err := reopened.Load(ctx, "run-1")
	if err != nil {
		t.Fatalf("load run: %v", err)
	}
	if loaded.Status != orchestrator.RunActive || loaded.Revision != 2 {
		t.Fatalf("loaded run = %#v", loaded)
	}
	if loaded.Threads["root-1"].TokenCapacity == nil || loaded.Threads["root-1"].TokenCapacity.ModelContextWindow != 200000 {
		t.Fatalf("loaded token capacity = %#v", loaded.Threads["root-1"].TokenCapacity)
	}
	loadedHandoff, err := reopened.LoadHandoff(ctx, "run-1", "handoff-1")
	if err != nil {
		t.Fatalf("load handoff: %v", err)
	}
	if loadedHandoff.AssignedTask != "migrate records" {
		t.Fatalf("loaded handoff = %#v", loadedHandoff)
	}

	runInfo, err := os.Stat(filepath.Join(root, "runs", "run-1", "run.json"))
	if err != nil {
		t.Fatalf("stat run: %v", err)
	}
	if runInfo.Mode().Perm() != 0o600 {
		t.Fatalf("run permissions = %o, want 600", runInfo.Mode().Perm())
	}
}

func stringPointer(value string) *string {
	return &value
}
