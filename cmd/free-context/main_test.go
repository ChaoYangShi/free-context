package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ChaoYangShi/free-context/internal/daemon"
	"github.com/ChaoYangShi/free-context/internal/orchestrator"
	"github.com/ChaoYangShi/free-context/internal/paths"
	"github.com/ChaoYangShi/free-context/internal/store"
)

func TestRunHelp(t *testing.T) {
	command := exec.Command(os.Args[0], "-test.run=TestCLIProcess")
	command.Env = append(os.Environ(),
		"FREE_CONTEXT_TEST_CLI=1",
		"XDG_STATE_HOME=relative-path-that-must-not-be-resolved",
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("free-context --help: %v\n%s", err, output)
	}

	for _, expected := range []string{
		"Usage:\n  free-context <command> [arguments]",
		"free-context run",
		"free-context daemon start|stop|status",
		"free-context --version",
	} {
		if !strings.Contains(string(output), expected) {
			t.Errorf("help output does not contain %q:\n%s", expected, output)
		}
	}
}

func TestCLIProcess(t *testing.T) {
	if os.Getenv("FREE_CONTEXT_TEST_CLI") != "1" {
		return
	}
	os.Args = []string{"free-context", "--help"}
	main()
}

type foregroundExitRecorder struct {
	engine *orchestrator.Engine
	exits  int
}

func (r *foregroundExitRecorder) Execute(ctx context.Context, command any) (orchestrator.Outcome, error) {
	if exited, ok := command.(orchestrator.ForegroundExited); ok {
		r.exits++
		run, err := r.engine.Execute(ctx, exited)
		return run, err
	}
	return r.engine.Execute(ctx, command)
}

func TestRunSessionReportsForegroundExitAfterInterrupt(t *testing.T) {
	stateRoot := filepath.Join(t.TempDir(), "state")
	runtimeRoot := filepath.Join(t.TempDir(), "runtime")
	layout := paths.Layout{
		StateRoot:    stateRoot,
		RuntimeRoot:  runtimeRoot,
		DaemonSocket: filepath.Join(runtimeRoot, "daemon.sock"),
		PIDFile:      filepath.Join(stateRoot, "daemon.pid"),
		LogFile:      filepath.Join(stateRoot, "daemon.log"),
	}
	repository := store.NewFS(stateRoot)
	engine := orchestrator.New(repository, time.Now, func() string { return "run-1" })
	recorder := &foregroundExitRecorder{engine: engine}
	serverContext, cancelServer := context.WithCancel(context.Background())
	serverDone := make(chan error, 1)
	ready := make(chan struct{})
	go func() {
		serverDone <- daemon.ServeReady(serverContext, layout.DaemonSocket, daemon.NewHandler(recorder, repository), ready)
	}()
	<-ready

	fakeCodex := filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(fakeCodex, []byte("#!/bin/sh\nkill -INT \"$PPID\"\nkill -INT \"$$\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FREE_CONTEXT_CODEX_BIN", fakeCodex)
	input, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.WriteString("finish migration\nall rows copied\n\n"); err != nil {
		t.Fatal(err)
	}
	writer.Close()
	previousStdin := os.Stdin
	os.Stdin = input
	defer func() {
		os.Stdin = previousStdin
		input.Close()
		cancelServer()
		<-serverDone
	}()

	if err := runSession(context.Background(), layout); err != nil {
		t.Fatalf("run session: %v", err)
	}
	if recorder.exits != 1 {
		t.Fatalf("foreground exit events = %d, want 1", recorder.exits)
	}
}
