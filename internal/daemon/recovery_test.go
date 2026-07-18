package daemon

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ChaoYangShi/free-context/internal/orchestrator"
	"github.com/ChaoYangShi/free-context/internal/store"
)

func TestRecoverPersistedRunsRemovesCompletedRun(t *testing.T) {
	ctx := context.Background()
	repository := store.NewFS(t.TempDir())
	run := orchestrator.Run{
		ID: "run-1", CreatedAt: time.Now(), UpdatedAt: time.Now(),
		WorkspacePath: t.TempDir(), Objective: "finished", CompletionCriteria: []string{"done"},
		Sandbox: "workspace-write", AppServerPID: 99999999,
		AppServerSocket: filepath.Join(t.TempDir(), "gone.sock"), Status: orchestrator.RunComplete,
		Threads: map[string]orchestrator.Thread{}, Handoffs: map[string]orchestrator.HandoffRecord{}, Revision: 1,
	}
	if err := repository.Create(ctx, run); err != nil {
		t.Fatal(err)
	}

	if err := RecoverPersistedRuns(ctx, nil, nil, repository); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Load(ctx, run.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("load recovered completed run error = %v, want not found", err)
	}
}

func TestStopOwnedAppServerAcceptsGoneProcessAndInactiveSocket(t *testing.T) {
	err := stopOwnedAppServer(context.Background(), orchestrator.Run{
		AppServerPID:    99999999,
		AppServerSocket: filepath.Join(t.TempDir(), "gone.sock"),
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestStopOwnedAppServerBlocksWhenSocketIsActiveButPIDIsGone(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "active.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	err = stopOwnedAppServer(context.Background(), orchestrator.Run{AppServerPID: 99999999, AppServerSocket: socket})
	if err == nil {
		t.Fatal("expected uncertain ownership to block recovery")
	}
}

func TestRunRecoverableOnDaemonStartIncludesOnlyAutomaticRecoveryBlocks(t *testing.T) {
	if !runRecoverableOnDaemonStart(orchestrator.Run{Status: orchestrator.RunActive}) {
		t.Fatal("active run should recover")
	}
	if !runRecoverableOnDaemonStart(orchestrator.Run{Status: orchestrator.RunBlocked, BlockedReason: "automatic recovery failed: missing model"}) {
		t.Fatal("automatic recovery failure should be retried")
	}
	if runRecoverableOnDaemonStart(orchestrator.Run{Status: orchestrator.RunBlocked, BlockedReason: "user blocker"}) {
		t.Fatal("user blocked run should not recover automatically")
	}
}

func TestRefreshRecoverableThreadMetadataBackfillsModelFromTranscript(t *testing.T) {
	ctx := context.Background()
	repository := store.NewFS(t.TempDir())
	engine := orchestrator.New(repository, func() time.Time {
		return time.Date(2026, 7, 15, 8, 0, 0, 0, time.UTC)
	}, func() string { return "unused" })
	transcript := filepath.Join(t.TempDir(), "root.jsonl")
	if err := os.WriteFile(transcript, []byte(
		`{"type":"turn_context","payload":{"turn_id":"turn-root","model":"gpt-5.5"}}`+"\n",
	), 0o600); err != nil {
		t.Fatal(err)
	}
	run := orchestrator.Run{
		ID:                 "run-1",
		CreatedAt:          time.Now(),
		UpdatedAt:          time.Now(),
		WorkspacePath:      t.TempDir(),
		Objective:          "migrate",
		CompletionCriteria: []string{"done"},
		Sandbox:            "workspace-write",
		Status:             orchestrator.RunActive,
		RootThreadID:       "root-1",
		Threads: map[string]orchestrator.Thread{
			"root-1": {
				ID:             "root-1",
				Role:           orchestrator.RoleRoot,
				Status:         orchestrator.ThreadActive,
				TranscriptPath: transcript,
			},
		},
		Handoffs: map[string]orchestrator.HandoffRecord{},
		Revision: 1,
	}
	if err := repository.Create(ctx, run); err != nil {
		t.Fatal(err)
	}
	if err := refreshRecoverableThreadMetadata(ctx, engine, repository, run); err != nil {
		t.Fatal(err)
	}
	loaded, err := repository.Load(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Threads["root-1"].Model != "gpt-5.5" {
		t.Fatalf("model = %q, want gpt-5.5", loaded.Threads["root-1"].Model)
	}
	if loaded.Threads["root-1"].CurrentTurnID != "turn-root" {
		t.Fatalf("turn = %q, want turn-root", loaded.Threads["root-1"].CurrentTurnID)
	}
}

func TestRefreshRecoverableThreadMetadataClearsInvalidTokenCapacity(t *testing.T) {
	ctx := context.Background()
	repository := store.NewFS(t.TempDir())
	engine := orchestrator.New(repository, func() time.Time {
		return time.Date(2026, 7, 15, 8, 0, 0, 0, time.UTC)
	}, func() string { return "unused" })
	run := orchestrator.Run{
		ID:                 "run-1",
		CreatedAt:          time.Now(),
		UpdatedAt:          time.Now(),
		WorkspacePath:      t.TempDir(),
		Objective:          "migrate",
		CompletionCriteria: []string{"done"},
		Sandbox:            "workspace-write",
		Status:             orchestrator.RunBlocked,
		RootThreadID:       "root-1",
		Threads: map[string]orchestrator.Thread{
			"root-1": {
				ID:             "root-1",
				Role:           orchestrator.RoleRoot,
				Status:         orchestrator.ThreadActive,
				Model:          "gpt-5.5",
				TranscriptPath: "/tmp/root.jsonl",
				TokenCapacity: &orchestrator.TokenCapacitySnapshot{
					TotalTokens:        2328391,
					ModelContextWindow: 258400,
				},
			},
		},
		Handoffs: map[string]orchestrator.HandoffRecord{},
		Revision: 1,
	}
	if err := repository.Create(ctx, run); err != nil {
		t.Fatal(err)
	}
	if err := refreshRecoverableThreadMetadata(ctx, engine, repository, run); err != nil {
		t.Fatal(err)
	}
	loaded, err := repository.Load(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Threads["root-1"].TokenCapacity != nil {
		t.Fatalf("token capacity = %#v, want nil", loaded.Threads["root-1"].TokenCapacity)
	}
}
