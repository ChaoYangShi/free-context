package daemon_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/ChaoYangShi/free-context/internal/codexconfig"
	"github.com/ChaoYangShi/free-context/internal/daemon"
	"github.com/ChaoYangShi/free-context/internal/orchestrator"
	"github.com/ChaoYangShi/free-context/internal/store"
)

func TestUnixServerAndClientRoundTrip(t *testing.T) {
	t.Parallel()

	repository := store.NewFS(t.TempDir())
	engine := orchestrator.New(repository, time.Now, func() string { return "run-1" })
	socket := filepath.Join(t.TempDir(), "daemon.sock")
	ctx, cancel := context.WithCancel(context.Background())
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- daemon.Serve(ctx, socket, daemon.NewHandler(engine, repository))
	}()

	client := daemon.NewClient(socket)
	deadline := time.Now().Add(2 * time.Second)
	for {
		if err := client.Ping(context.Background()); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("daemon did not become ready")
		}
		time.Sleep(10 * time.Millisecond)
	}

	outcome, err := client.StartRun(context.Background(), daemon.StartRunInput{
		WorkspacePath:      t.TempDir(),
		Objective:          "migrate",
		CompletionCriteria: []string{"done"},
		Sandbox:            codexconfig.DangerFullAccessSandbox,
	})
	if err != nil {
		t.Fatalf("start run: %v", err)
	}
	if outcome.Run.ID != "run-1" {
		t.Fatalf("run id = %q", outcome.Run.ID)
	}
	if outcome.Run.Sandbox != codexconfig.DangerFullAccessSandbox {
		t.Fatalf("sandbox = %q, want %q", outcome.Run.Sandbox, codexconfig.DangerFullAccessSandbox)
	}
	if _, err := client.Execute(context.Background(), daemon.CommandRegisterThread, orchestrator.RegisterThread{
		RunID: "run-1", ThreadID: "root-1", Role: orchestrator.RoleRoot,
		AssignedTask: "migrate", Model: "gpt-test", TranscriptPath: "/sessions/root.jsonl", TurnID: "turn-1",
	}); err != nil {
		t.Fatalf("register root: %v", err)
	}
	if _, err := client.Execute(context.Background(), daemon.CommandReportProgress, orchestrator.ReportProgress{
		RunID: "run-1", ThreadID: "root-1", Status: orchestrator.ProgressActive,
		CompletedWork: []string{}, InProgressWork: []string{"migrating"}, NextAction: "continue migration",
		Blockers: []string{}, Artifacts: []string{},
	}); err != nil {
		t.Fatalf("report progress: %v", err)
	}
	loaded, err := client.Run(context.Background(), "run-1")
	if err != nil {
		t.Fatalf("load run: %v", err)
	}
	if loaded.Objective != "migrate" || loaded.Threads["root-1"].Progress.NextAction != "continue migration" {
		t.Fatalf("loaded = %#v", loaded)
	}
	if _, err := client.Run(context.Background(), "missing"); !errors.Is(err, daemon.ErrNotFound) {
		t.Fatalf("missing run error = %v, want ErrNotFound", err)
	}

	cancel()
	if err := <-serverDone; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("serve: %v", err)
	}
}
