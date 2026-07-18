package main

import (
	"context"
	"encoding/json"
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
	output, err := runCLI(t, "--help")
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

func TestCLICompletesCommandsByPrefix(t *testing.T) {
	output, err := runCLI(t, "__complete", "st")
	if err != nil {
		t.Fatalf("free-context __complete st: %v\n%s", err, output)
	}
	if got, want := string(output), "status\nstop\n"; got != want {
		t.Fatalf("completion output = %q, want %q", got, want)
	}
}

func TestCLICompletesSubcommandsByPrefix(t *testing.T) {
	for _, test := range []struct {
		arguments []string
		want      string
	}{
		{[]string{"__complete", "daemon", "st"}, "start\nstop\nstatus\n"},
		{[]string{"__complete", "daemon", ""}, "start\nstop\nstatus\nserve\n"},
		{[]string{"__complete", "hook", "pre-"}, "pre-compact\npre-tool-use\n"},
		{[]string{"__complete", "completion", ""}, "bash\n"},
		{[]string{"__complete", "pre-"}, "pre-compact\npre-tool-use\n"},
	} {
		output, err := runCLI(t, test.arguments...)
		if err != nil {
			t.Fatalf("%v: %v\n%s", test.arguments, err, output)
		}
		if got := string(output); got != test.want {
			t.Errorf("%v completion = %q, want %q", test.arguments, got, test.want)
		}
	}
}

func TestCLIPrintsBashCompletionScript(t *testing.T) {
	output, err := runCLI(t, "completion", "bash")
	if err != nil {
		t.Fatalf("free-context completion bash: %v\n%s", err, output)
	}
	for _, expected := range []string{
		"complete -F _free_context_complete free-context",
		"free-context __complete",
	} {
		if !strings.Contains(string(output), expected) {
			t.Errorf("completion script does not contain %q:\n%s", expected, output)
		}
	}
	command := exec.Command("bash")
	command.Stdin = strings.NewReader(string(output) + `
free-context() {
  if [[ "$1" == "__complete" && "$#" == "2" && "$2" == "daemon" ]]; then
    printf 'daemon\n'
  fi
}
COMP_WORDS=(free-context daemon st)
COMP_CWORD=1
_free_context_complete
printf '%s\n' "${COMPREPLY[@]}"
`)
	cursorOutput, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("execute completion script: %v\n%s", err, cursorOutput)
	}
	if got, want := string(cursorOutput), "daemon\n"; got != want {
		t.Fatalf("completion at earlier word = %q, want %q", got, want)
	}
}

func TestCLIUsesSuggestionForUnknownCommand(t *testing.T) {
	output, err := runCLI(t, "stauts")
	if err == nil {
		t.Fatal("free-context stauts succeeded, want an error")
	}
	for _, expected := range []string{"unknown command", "did you mean", "status"} {
		if !strings.Contains(string(output), expected) {
			t.Errorf("error output does not contain %q:\n%s", expected, output)
		}
	}
}

func TestCLIUsesSuggestionForUnknownSubcommand(t *testing.T) {
	output, err := runCLI(t, "daemon", "statsu")
	if err == nil {
		t.Fatal("free-context daemon statsu succeeded, want an error")
	}
	for _, expected := range []string{"unknown daemon command", "did you mean", "status"} {
		if !strings.Contains(string(output), expected) {
			t.Errorf("error output does not contain %q:\n%s", expected, output)
		}
	}
}

func TestCLIPointsToUsageWhenRequiredArgumentIsMissing(t *testing.T) {
	output, err := runCLI(t, "delete")
	if err == nil {
		t.Fatal("free-context delete succeeded, want an error")
	}
	for _, expected := range []string{"usage: free-context delete <run_id>", "free-context --help"} {
		if !strings.Contains(string(output), expected) {
			t.Errorf("error output does not contain %q:\n%s", expected, output)
		}
	}
}

func TestSameExecutablePathAcceptsDeletedProcSymlink(t *testing.T) {
	if !sameExecutablePath("/home/kent/go/bin/free-context (deleted)", "/home/kent/go/bin/free-context") {
		t.Fatal("deleted proc executable symlink should match its replacement path")
	}
}

func TestCLICompletesRunIDsAllowedByCommand(t *testing.T) {
	stateBase := t.TempDir()
	runtimeBase := t.TempDir()
	stateRoot := filepath.Join(stateBase, "free-context")
	runtimeRoot := filepath.Join(runtimeBase, "free-context")
	t.Setenv("XDG_STATE_HOME", stateBase)
	t.Setenv("XDG_RUNTIME_DIR", runtimeBase)
	repository := store.NewFS(stateRoot)
	created := time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC)
	for index, run := range []orchestrator.Run{
		{ID: "active-1", Status: orchestrator.RunActive},
		{ID: "blocked-1", Status: orchestrator.RunBlocked},
		{ID: "stopped-1", Status: orchestrator.RunStopped},
	} {
		run.CreatedAt = created.Add(time.Duration(index) * time.Minute)
		if err := repository.Create(t.Context(), run); err != nil {
			t.Fatal(err)
		}
	}
	serverContext, cancelServer := context.WithCancel(context.Background())
	serverDone := make(chan error, 1)
	ready := make(chan struct{})
	go func() {
		engine := orchestrator.New(repository, time.Now, func() string { return "unused" })
		serverDone <- daemon.ServeReady(serverContext, filepath.Join(runtimeRoot, "daemon.sock"), daemon.NewHandler(engine, repository), ready)
	}()
	<-ready
	defer func() {
		cancelServer()
		if err := <-serverDone; err != nil {
			t.Fatal(err)
		}
	}()

	output, err := runCLIWithEnvironment(t, []string{
		"XDG_STATE_HOME=" + stateBase,
		"XDG_RUNTIME_DIR=" + runtimeBase,
	}, "__complete", "attach", "")
	if err != nil {
		t.Fatalf("complete attach: %v", err)
	}
	if got, want := string(output), "active-1\nblocked-1\n"; got != want {
		t.Fatalf("attach completion = %q, want %q", got, want)
	}
	output, err = runCLIWithEnvironment(t, []string{
		"XDG_STATE_HOME=" + stateBase,
		"XDG_RUNTIME_DIR=" + runtimeBase,
	}, "__complete", "delete", "")
	if err != nil {
		t.Fatalf("complete delete: %v", err)
	}
	if got, want := string(output), "stopped-1\n"; got != want {
		t.Fatalf("delete completion = %q, want %q", got, want)
	}
}

func runCLI(t *testing.T, arguments ...string) ([]byte, error) {
	return runCLIWithEnvironment(t, []string{"XDG_STATE_HOME=" + t.TempDir()}, arguments...)
}

func runCLIWithEnvironment(t *testing.T, environment []string, arguments ...string) ([]byte, error) {
	t.Helper()
	encoded, err := json.Marshal(arguments)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(os.Args[0], "-test.run=^TestCLIProcess$")
	command.Env = append(os.Environ(),
		"FREE_CONTEXT_TEST_CLI=invoke",
		"FREE_CONTEXT_TEST_ARGS="+string(encoded),
	)
	command.Env = append(command.Env, environment...)
	return command.CombinedOutput()
}

func TestCLIProcess(t *testing.T) {
	switch os.Getenv("FREE_CONTEXT_TEST_CLI") {
	case "invoke":
		var arguments []string
		if err := json.Unmarshal([]byte(os.Getenv("FREE_CONTEXT_TEST_ARGS")), &arguments); err != nil {
			t.Fatal(err)
		}
		os.Args = append([]string{"free-context"}, arguments...)
		main()
		os.Exit(0)
	case "run":
		os.Args = []string{"free-context", "run"}
		main()
		os.Exit(0)
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
