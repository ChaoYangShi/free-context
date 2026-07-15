package daemon

import (
	"context"
	"errors"
	"net"
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
