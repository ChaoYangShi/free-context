package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ChaoYangShi/free-context/internal/appserver"
	"github.com/ChaoYangShi/free-context/internal/codexconfig"
	"github.com/ChaoYangShi/free-context/internal/daemon"
	"github.com/ChaoYangShi/free-context/internal/handoff"
	"github.com/ChaoYangShi/free-context/internal/hooks"
	"github.com/ChaoYangShi/free-context/internal/mcp"
	"github.com/ChaoYangShi/free-context/internal/orchestrator"
	"github.com/ChaoYangShi/free-context/internal/paths"
	"github.com/ChaoYangShi/free-context/internal/store"
	"github.com/ChaoYangShi/free-context/internal/tui"
)

const version = "0.1.0"

type runIDCompletion int

const (
	noRunIDs runIDCompletion = iota
	allRunIDs
	nonTerminalRunIDs
	terminalRunIDs
)

type commandDefinition struct {
	name             string
	usage            string
	minimumArguments int
	maximumArguments int
	values           []string
	valueKind        string
	runIDs           runIDCompletion
}

var commandDefinitions = []commandDefinition{
	{name: "run", usage: "free-context run"},
	{name: "list", usage: "free-context list"},
	{name: "status", usage: "free-context status [run_id]", maximumArguments: 1, runIDs: allRunIDs},
	{name: "inspect", usage: "free-context inspect [run_id]", maximumArguments: 1, runIDs: allRunIDs},
	{name: "tui", usage: "free-context tui"},
	{name: "attach", usage: "free-context attach [run_id]", maximumArguments: 1, runIDs: nonTerminalRunIDs},
	{name: "stop", usage: "free-context stop [run_id]", maximumArguments: 1, runIDs: nonTerminalRunIDs},
	{name: "delete", usage: "free-context delete <run_id>", minimumArguments: 1, maximumArguments: 1, runIDs: terminalRunIDs},
	{name: "daemon", usage: "free-context daemon <start|stop|status|serve>", minimumArguments: 1, maximumArguments: 1, values: []string{"start", "stop", "status", "serve"}, valueKind: "daemon command"},
	{name: "completion", usage: "free-context completion <bash>", minimumArguments: 1, maximumArguments: 1, values: []string{"bash"}, valueKind: "completion shell"},
	{name: "mcp", usage: "free-context mcp"},
	{name: "hook", usage: "free-context hook <pre-compact|pre-tool-use>", minimumArguments: 1, maximumArguments: 1, values: []string{"pre-compact", "pre-tool-use"}, valueKind: "hook"},
	{name: "version", usage: "free-context --version"},
	{name: "--version", usage: "free-context --version"},
	{name: "-v", usage: "free-context --version"},
	{name: "pre-compact", usage: "free-context pre-compact"},
	{name: "pre-tool-use", usage: "free-context pre-tool-use"},
}

const helpText = `Free Context supervises long-running Codex agent trees.

Usage:
  free-context <command> [arguments]
  free-context --help

Commands:
  free-context run                         Start a managed Codex session
  free-context list                        List runs
  free-context status [run_id]             Show a run's status
  free-context inspect [run_id]            Show a run's persisted state
  free-context tui                         Monitor active runs and token capacity
  free-context attach [run_id]             Attach to an active run
  free-context stop [run_id]               Stop an active run
  free-context delete <run_id>             Delete a completed or stopped run
  free-context daemon start|stop|status|serve
                                             Manage or serve the user daemon
  free-context completion bash             Print the Bash completion script
  free-context mcp                         Start the MCP server
  free-context hook <hook_name>            Serve a Codex command hook
  free-context pre-compact|pre-tool-use     Serve an injected command hook
  free-context --version                   Show the version
`

const bashCompletionScript = `# Bash completion for free-context.
_free_context_complete() {
  local -a candidates=()
  local candidate
  while IFS= read -r candidate; do
    candidates+=("$candidate")
  done < <(free-context __complete "${COMP_WORDS[@]:1:COMP_CWORD}" 2>/dev/null)
  COMPREPLY=("${candidates[@]}")
}
complete -F _free_context_complete free-context
`

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		encoder := json.NewEncoder(os.Stderr)
		encoder.SetEscapeHTML(false)
		_ = encoder.Encode(map[string]string{"error": err.Error()})
		os.Exit(1)
	}
}

func run(ctx context.Context, arguments []string) error {
	if len(arguments) == 0 {
		return errors.New("command is required; run free-context --help for command details")
	}
	if arguments[0] == "__complete" {
		return complete(ctx, arguments[1:])
	}
	if arguments[0] == "--help" {
		if len(arguments) != 1 {
			return usageError("free-context --help")
		}
		_, err := fmt.Fprint(os.Stdout, helpText)
		return err
	}
	definition, exists := findCommand(arguments[0])
	if !exists {
		return unknownValueError("command", arguments[0], commandNames())
	}
	if err := validateArguments(definition, arguments[1:]); err != nil {
		return err
	}
	if definition.name == "completion" {
		_, err := fmt.Fprint(os.Stdout, bashCompletionScript)
		return err
	}
	layout, err := paths.Resolve()
	if err != nil {
		return err
	}
	switch arguments[0] {
	case "run":
		return runSession(ctx, layout)
	case "list":
		client, err := liveClient(ctx, layout)
		if err != nil {
			return err
		}
		runs, err := client.List(ctx)
		return output(runs, err)
	case "status":
		client, err := liveClient(ctx, layout)
		if err != nil {
			return err
		}
		id, err := resolveRunID(ctx, client, arguments[1:])
		if err != nil {
			return err
		}
		run, err := client.Run(ctx, id)
		return output(run, err)
	case "inspect":
		client, err := liveClient(ctx, layout)
		if err != nil {
			return err
		}
		id, err := resolveRunID(ctx, client, arguments[1:])
		if err != nil {
			return err
		}
		state, err := client.State(ctx, id)
		return output(state, err)
	case "tui":
		client, err := liveClient(ctx, layout)
		if err != nil {
			return err
		}
		attachRunID, err := tui.Run(ctx, client)
		if err != nil || attachRunID == "" {
			return err
		}
		return attach(ctx, layout, []string{attachRunID})
	case "attach":
		return attach(ctx, layout, arguments[1:])
	case "stop":
		client, err := liveClient(ctx, layout)
		if err != nil {
			return err
		}
		id, err := resolveRunID(ctx, client, arguments[1:])
		if err != nil {
			return err
		}
		outcome, err := client.Execute(ctx, daemon.CommandStopRun, orchestrator.StopRun{RunID: id})
		return output(outcome.Run, err)
	case "delete":
		client, err := liveClient(ctx, layout)
		if err != nil {
			return err
		}
		run, err := client.Run(ctx, arguments[1])
		if err != nil {
			return err
		}
		if run.Status != orchestrator.RunComplete && run.Status != orchestrator.RunStopped {
			return errors.New("run must be completed or stopped before deletion")
		}
		if err := client.Delete(ctx, arguments[1]); err != nil {
			return err
		}
		return output(map[string]string{"deleted_run_id": arguments[1]}, nil)
	case "daemon":
		return daemonCommand(ctx, layout, arguments[1:])
	case "mcp":
		handler, err := hooks.FromEnvironment()
		if err != nil {
			return err
		}
		return mcp.NewServer(handler.RunID, handler.Commander).Serve(ctx, os.Stdin, os.Stdout)
	case "hook":
		handler, err := hooks.FromEnvironment()
		if err != nil {
			return err
		}
		return hooks.Serve(ctx, arguments[1], os.Stdin, os.Stdout, handler)
	case "pre-compact", "pre-tool-use":
		handler, err := hooks.FromEnvironment()
		if err != nil {
			return err
		}
		return hooks.Serve(ctx, arguments[0], os.Stdin, os.Stdout, handler)
	case "version", "--version", "-v":
		return output(map[string]string{"version": version}, nil)
	default:
		return unknownValueError("command", arguments[0], commandNames())
	}
}

func validateArguments(definition commandDefinition, arguments []string) error {
	if len(arguments) < definition.minimumArguments || len(arguments) > definition.maximumArguments {
		return usageError(definition.usage)
	}
	if len(definition.values) == 0 || contains(definition.values, arguments[0]) {
		return nil
	}
	return unknownValueError(definition.valueKind, arguments[0], definition.values)
}

func findCommand(name string) (commandDefinition, bool) {
	for _, definition := range commandDefinitions {
		if definition.name == name {
			return definition, true
		}
	}
	return commandDefinition{}, false
}

func commandNames() []string {
	values := []string{"--help"}
	for _, definition := range commandDefinitions {
		values = append(values, definition.name)
	}
	return values
}

func runIDAllowed(completion runIDCompletion, status orchestrator.RunStatus) bool {
	terminal := status == orchestrator.RunComplete || status == orchestrator.RunStopped
	switch completion {
	case allRunIDs:
		return true
	case nonTerminalRunIDs:
		return !terminal
	case terminalRunIDs:
		return terminal
	default:
		return false
	}
}

func complete(ctx context.Context, arguments []string) error {
	values := commandNames()
	if len(arguments) == 0 {
		return outputCompletions(values)
	}
	if len(arguments) > 2 {
		return nil
	}
	if len(arguments) == 1 {
		return outputCompletions(completionValues(values, arguments[0]))
	}
	definition, exists := findCommand(arguments[0])
	if !exists {
		return nil
	}
	prefix := arguments[1]
	if definition.runIDs == noRunIDs {
		return outputCompletions(completionValues(definition.values, prefix))
	}
	layout, err := paths.Resolve()
	if err != nil {
		return err
	}
	client, err := liveClient(ctx, layout)
	if err != nil {
		return err
	}
	runs, err := client.List(ctx)
	if err != nil {
		return err
	}
	runIDs := make([]string, 0, len(runs))
	for _, run := range runs {
		if runIDAllowed(definition.runIDs, run.Status) {
			runIDs = append(runIDs, run.ID)
		}
	}
	return outputCompletions(completionValues(runIDs, prefix))
}

func completionValues(values []string, prefix string) []string {
	matches := make([]string, 0, len(values))
	for _, value := range values {
		if strings.HasPrefix(value, prefix) {
			matches = append(matches, value)
		}
	}
	return matches
}

func outputCompletions(values []string) error {
	for _, value := range values {
		if _, err := fmt.Fprintln(os.Stdout, value); err != nil {
			return err
		}
	}
	return nil
}

func usageError(usage string) error {
	return fmt.Errorf("usage: %s; run free-context --help for command details", usage)
}

func unknownValueError(kind, value string, candidates []string) error {
	message := fmt.Sprintf("unknown %s %q", kind, value)
	if suggestion := closest(value, candidates); suggestion != "" {
		message += fmt.Sprintf("; did you mean %q?", suggestion)
	}
	if kind == "command" {
		message += "; run free-context --help for command details"
	}
	return errors.New(message)
}

func closest(value string, candidates []string) string {
	best := ""
	bestDistance := 0
	for _, candidate := range candidates {
		distance := editDistance(strings.ToLower(value), strings.ToLower(candidate))
		if best == "" || distance < bestDistance {
			best, bestDistance = candidate, distance
		}
	}
	if best == "" || bestDistance > 2 {
		return ""
	}
	return best
}

func editDistance(left, right string) int {
	previous := make([]int, len(right)+1)
	for index := range previous {
		previous[index] = index
	}
	for i, leftChar := range left {
		current := make([]int, len(right)+1)
		current[0] = i + 1
		for j, rightChar := range right {
			cost := 0
			if leftChar != rightChar {
				cost = 1
			}
			current[j+1] = min(previous[j+1]+1, current[j]+1, previous[j]+cost)
		}
		previous = current
	}
	return previous[len(right)]
}

func min(values ...int) int {
	best := values[0]
	for _, value := range values[1:] {
		if value < best {
			best = value
		}
	}
	return best
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func runSession(ctx context.Context, layout paths.Layout) error {
	if err := ensureDaemon(ctx, layout); err != nil {
		return err
	}
	reader := bufio.NewReader(os.Stdin)
	fmt.Fprint(os.Stderr, "Objective: ")
	objective, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	objective = strings.TrimSpace(objective)
	if objective == "" {
		return errors.New("objective is required")
	}
	fmt.Fprintln(os.Stderr, "Completion criteria (one per line; blank line finishes):")
	criteria := make([]string, 0)
	for {
		criterion, readErr := reader.ReadString('\n')
		criterion = strings.TrimSpace(criterion)
		if criterion == "" {
			break
		}
		criteria = append(criteria, criterion)
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return readErr
		}
	}
	if len(criteria) == 0 {
		return errors.New("at least one completion criterion is required")
	}
	workspace, err := filepath.Abs(".")
	if err != nil {
		return err
	}
	client := daemon.NewClient(layout.DaemonSocket)
	outcome, err := client.StartRun(ctx, daemon.StartRunInput{WorkspacePath: workspace, Objective: objective, CompletionCriteria: criteria, Sandbox: codexconfig.DangerFullAccessSandbox})
	if err != nil {
		return err
	}
	endpoint := "unix://" + appserver.SocketPath(layout.RuntimeRoot, outcome.Run.ID)
	command := exec.CommandContext(ctx, codexBinary(), codexconfig.DangerouslyBypassApprovalsAndSandboxFlag, "--remote", endpoint, "-C", workspace, objective)
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	current, err := runForeground(ctx, client, outcome.Run.ID, command)
	if err != nil || current.ID == "" {
		return err
	}
	return output(current, nil)
}

func runForeground(ctx context.Context, client *daemon.Client, runID string, command *exec.Cmd) (orchestrator.Run, error) {
	interrupts := make(chan os.Signal, 1)
	signal.Notify(interrupts, os.Interrupt)
	defer signal.Stop(interrupts)
	commandErr := command.Run()
	outcome, exitErr := client.Execute(ctx, daemon.CommandFinalizeCompletion, orchestrator.FinalizeReportedCompletion{RunID: runID})
	if errors.Is(exitErr, daemon.ErrNotFound) {
		return orchestrator.Run{}, nil
	}
	if exitErr != nil {
		return orchestrator.Run{}, exitErr
	}
	if commandErr != nil {
		select {
		case <-interrupts:
			return outcome.Run, nil
		default:
			return orchestrator.Run{}, fmt.Errorf("remote Codex TUI exited: %w", commandErr)
		}
	}
	return outcome.Run, nil
}

func attach(ctx context.Context, layout paths.Layout, arguments []string) error {
	client, err := liveClient(ctx, layout)
	if err != nil {
		return err
	}
	id, err := resolveRunID(ctx, client, arguments)
	if err != nil {
		return err
	}
	run, err := client.Run(ctx, id)
	if err != nil {
		return err
	}
	if run.Status == orchestrator.RunComplete || run.Status == orchestrator.RunStopped {
		return errors.New("terminal runs cannot be attached")
	}
	if run.Status == orchestrator.RunBlocked {
		outcome, err := client.Execute(ctx, daemon.CommandResumeRun, orchestrator.ResumeRun{RunID: id})
		if err != nil {
			return err
		}
		run = outcome.Run
	}
	command := exec.CommandContext(ctx, codexBinary(), codexconfig.DangerouslyBypassApprovalsAndSandboxFlag, "--remote", "unix://"+appserver.SocketPath(layout.RuntimeRoot, id), "-C", run.WorkspacePath)
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	_, err = runForeground(ctx, client, id, command)
	return err
}

func daemonCommand(ctx context.Context, layout paths.Layout, arguments []string) error {
	if len(arguments) != 1 {
		return usageError("free-context daemon <start|stop|status|serve>")
	}
	switch arguments[0] {
	case "start":
		if err := ensureDaemon(ctx, layout); err != nil {
			return err
		}
		return output(map[string]any{"status": "running", "socket": layout.DaemonSocket}, nil)
	case "status":
		err := daemon.NewClient(layout.DaemonSocket).Ping(ctx)
		status := "stopped"
		if err == nil {
			status = "running"
		}
		return output(map[string]any{"status": status, "socket": layout.DaemonSocket}, nil)
	case "stop":
		if err := daemon.NewClient(layout.DaemonSocket).Ping(ctx); err != nil {
			return errors.New("daemon is not running")
		}
		data, err := os.ReadFile(layout.PIDFile)
		if err != nil {
			return fmt.Errorf("read daemon pid: %w", err)
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
		if err != nil {
			return err
		}
		if err := verifyDaemonProcess(pid); err != nil {
			return err
		}
		if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
			return err
		}
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if daemon.NewClient(layout.DaemonSocket).Ping(ctx) != nil {
				return output(map[string]string{"status": "stopped"}, nil)
			}
			time.Sleep(25 * time.Millisecond)
		}
		return errors.New("daemon did not stop within five seconds")
	case "serve":
		return serveDaemon(layout)
	default:
		return unknownValueError("daemon command", arguments[0], []string{"start", "stop", "status", "serve"})
	}
}

func serveDaemon(layout paths.Layout) error {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	if err := os.MkdirAll(layout.StateRoot, 0o700); err != nil {
		return err
	}
	if err := os.MkdirAll(layout.RuntimeRoot, 0o700); err != nil {
		return err
	}
	pidFile, err := os.OpenFile(layout.PIDFile, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer pidFile.Close()
	if err := syscall.Flock(int(pidFile.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return errors.New("daemon is already starting or running")
	}
	defer syscall.Flock(int(pidFile.Fd()), syscall.LOCK_UN)
	if err := pidFile.Truncate(0); err != nil {
		return err
	}
	if _, err := pidFile.WriteString(strconv.Itoa(os.Getpid()) + "\n"); err != nil {
		return err
	}
	if err := pidFile.Sync(); err != nil {
		return err
	}
	defer os.Remove(layout.PIDFile)
	repository := store.NewFS(layout.StateRoot)
	engine := orchestrator.New(repository, time.Now, randomID)
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	var controller *daemon.Controller
	manager := appserver.NewManager(ctx, codexBinary(), layout.DaemonSocket, layout.RuntimeRoot, executable, func(runID string, message json.RawMessage) {
		if controller != nil {
			controller.HandleNotification(runID, message)
		}
	})
	controller = daemon.NewController(engine, repository, manager, handoff.Runner{Executor: handoff.CodexExecutor{Binary: codexBinary()}, Now: time.Now, NewID: randomID})
	manager.OnExit = func(runID string, exitErr error) {
		controller.HandleAppServerExit(context.Background(), runID, exitErr)
	}
	defer manager.StopAll()
	ready := make(chan struct{})
	go func() {
		<-ready
		_ = daemon.RecoverPersistedRuns(ctx, controller, engine, repository)
	}()
	go monitorTimeouts(ctx, controller)
	return daemon.ServeReady(ctx, layout.DaemonSocket, daemon.NewHandler(controller, repository), ready)
}

func verifyDaemonProcess(pid int) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	actualExecutable, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
	if err != nil {
		return fmt.Errorf("verify daemon process: %w", err)
	}
	if !sameExecutablePath(actualExecutable, executable) {
		return errors.New("daemon pid does not belong to this executable")
	}
	commandLine, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return fmt.Errorf("verify daemon command: %w", err)
	}
	if !strings.Contains(string(commandLine), "daemon\x00serve") {
		return errors.New("daemon pid does not belong to a daemon serve process")
	}
	return nil
}

func sameExecutablePath(actual, expected string) bool {
	actual = strings.TrimSuffix(actual, " (deleted)")
	return filepath.Clean(actual) == filepath.Clean(expected)
}

func monitorTimeouts(ctx context.Context, controller *daemon.Controller) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case now := <-ticker.C:
			_ = controller.CheckTimeouts(ctx, now.UTC())
		case <-ctx.Done():
			return
		}
	}
}

func ensureDaemon(ctx context.Context, layout paths.Layout) error {
	client := daemon.NewClient(layout.DaemonSocket)
	probe, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	err := client.Ping(probe)
	cancel()
	if err == nil {
		return nil
	}
	if err := os.MkdirAll(layout.StateRoot, 0o700); err != nil {
		return err
	}
	logFile, err := os.OpenFile(layout.LogFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		logFile.Close()
		return err
	}
	command := exec.Command(executable, "daemon", "serve")
	command.Stdout = logFile
	command.Stderr = logFile
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := command.Start(); err != nil {
		logFile.Close()
		return err
	}
	_ = logFile.Close()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		probe, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
		err := client.Ping(probe)
		cancel()
		if err == nil {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("daemon did not start; inspect %s", layout.LogFile)
}

func liveClient(ctx context.Context, layout paths.Layout) (*daemon.Client, error) {
	client := daemon.NewClient(layout.DaemonSocket)
	if err := client.Ping(ctx); err != nil {
		return nil, errors.New("free-context daemon is not running")
	}
	return client, nil
}

func resolveRunID(ctx context.Context, client *daemon.Client, arguments []string) (string, error) {
	if len(arguments) > 1 {
		return "", errors.New("at most one run_id is allowed")
	}
	if len(arguments) == 1 && arguments[0] != "" {
		return arguments[0], nil
	}
	runs, err := client.List(ctx)
	if err != nil {
		return "", err
	}
	candidates := make([]string, 0)
	for _, run := range runs {
		if run.Status != orchestrator.RunComplete && run.Status != orchestrator.RunStopped {
			candidates = append(candidates, run.ID)
		}
	}
	if len(candidates) != 1 {
		return "", errors.New("run_id is required unless exactly one non-terminal run exists")
	}
	return candidates[0], nil
}

func output(value any, err error) error {
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
}

func randomID() string {
	data := make([]byte, 16)
	if _, err := rand.Read(data); err != nil {
		panic(err)
	}
	return hex.EncodeToString(data)
}

func codexBinary() string {
	if binary := strings.TrimSpace(os.Getenv("FREE_CONTEXT_CODEX_BIN")); binary != "" {
		return binary
	}
	return "codex"
}
