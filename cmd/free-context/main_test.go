package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ChaoYangShi/free-context/internal/daemon"
	"github.com/ChaoYangShi/free-context/internal/orchestrator"
	"github.com/ChaoYangShi/free-context/internal/store"
)

func TestRunHelp(t *testing.T) {
	command := exec.Command(os.Args[0], "-test.run=TestCLIProcess")
	command.Env = append(os.Environ(),
		"FREE_CONTEXT_TEST_CLI=help",
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
	switch os.Getenv("FREE_CONTEXT_TEST_CLI") {
	case "help":
		os.Args = []string{"free-context", "--help"}
		main()
	case "run":
		os.Args = []string{"free-context", "run"}
		main()
	default:
		return
	}
}

type completionRecorder struct {
	engine *orchestrator.Engine
	exits  atomic.Int32
}

func (r *completionRecorder) Execute(ctx context.Context, command any) (orchestrator.Outcome, error) {
	if completion, ok := command.(orchestrator.FinalizeReportedCompletion); ok {
		r.exits.Add(1)
		run, err := r.engine.Execute(ctx, completion)
		return run, err
	}
	return r.engine.Execute(ctx, command)
}

func TestCLIReportsForegroundExitAfterInterrupt(t *testing.T) {
	stateBase := t.TempDir()
	runtimeBase := t.TempDir()
	stateRoot := filepath.Join(stateBase, "free-context")
	runtimeRoot := filepath.Join(runtimeBase, "free-context")
	daemonSocket := filepath.Join(runtimeRoot, "daemon.sock")
	repository := store.NewFS(stateRoot)
	engine := orchestrator.New(repository, time.Now, func() string { return "run-1" })
	recorder := &completionRecorder{engine: engine}
	serverContext, cancelServer := context.WithCancel(context.Background())
	serverDone := make(chan error, 1)
	ready := make(chan struct{})
	go func() {
		serverDone <- daemon.ServeReady(serverContext, daemonSocket, daemon.NewHandler(recorder, repository), ready)
	}()
	<-ready
	defer func() {
		cancelServer()
		<-serverDone
	}()

	fakeCodex := filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(fakeCodex, []byte("#!/bin/sh\nkill -INT \"$PPID\"\nkill -INT \"$$\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(os.Args[0], "-test.run=^TestCLIProcess$")
	command.Env = append(os.Environ(),
		"FREE_CONTEXT_TEST_CLI=run",
		"FREE_CONTEXT_CODEX_BIN="+fakeCodex,
		"XDG_STATE_HOME="+stateBase,
		"XDG_RUNTIME_DIR="+runtimeBase,
	)
	command.Stdin = strings.NewReader("finish migration\nall rows copied\n\n")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("free-context run: %v\n%s", err, output)
	}
	if recorder.exits.Load() != 1 {
		t.Fatalf("foreground exit events = %d, want 1", recorder.exits.Load())
	}
}
