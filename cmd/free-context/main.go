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
	"github.com/ChaoYangShi/free-context/internal/daemon"
	"github.com/ChaoYangShi/free-context/internal/handoff"
	"github.com/ChaoYangShi/free-context/internal/hooks"
	"github.com/ChaoYangShi/free-context/internal/mcp"
	"github.com/ChaoYangShi/free-context/internal/orchestrator"
	"github.com/ChaoYangShi/free-context/internal/paths"
	"github.com/ChaoYangShi/free-context/internal/store"
)

const version = "0.1.0"

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		_ = json.NewEncoder(os.Stderr).Encode(map[string]string{"error": err.Error()})
		os.Exit(1)
	}
}

func run(ctx context.Context, arguments []string) error {
	if len(arguments) == 0 {
		return errors.New("command is required")
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
		if len(arguments) != 2 {
			return errors.New("usage: free-context delete <run_id>")
		}
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
		if len(arguments) != 2 {
			return errors.New("usage: free-context hook <pre-compact|pre-tool-use>")
		}
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
		return fmt.Errorf("unknown command %q", arguments[0])
	}
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
	outcome, err := client.StartRun(ctx, daemon.StartRunInput{WorkspacePath: workspace, Objective: objective, CompletionCriteria: criteria, Sandbox: "workspace-write"})
	if err != nil {
		return err
	}
	endpoint := "unix://" + appserver.SocketPath(layout.RuntimeRoot, outcome.Run.ID)
	command := exec.CommandContext(ctx, codexBinary(), "--remote", endpoint, "-C", workspace, "-a", "never", "-s", "workspace-write", objective)
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("remote Codex TUI exited: %w", err)
	}
	current, err := client.Run(ctx, outcome.Run.ID)
	return output(current, err)
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
	command := exec.CommandContext(ctx, codexBinary(), "--remote", "unix://"+appserver.SocketPath(layout.RuntimeRoot, id), "-C", run.WorkspacePath, "-a", "never", "-s", run.Sandbox)
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	return command.Run()
}

func daemonCommand(ctx context.Context, layout paths.Layout, arguments []string) error {
	if len(arguments) != 1 {
		return errors.New("usage: free-context daemon <start|stop|status|serve>")
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
		return fmt.Errorf("unknown daemon command %q", arguments[0])
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
	if filepath.Clean(actualExecutable) != filepath.Clean(executable) {
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
