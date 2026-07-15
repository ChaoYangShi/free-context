package appserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ChaoYangShi/free-context/internal/codexrpc"
	"github.com/ChaoYangShi/free-context/internal/orchestrator"
)

type NotificationHandler func(runID string, message json.RawMessage)

type Runtime interface {
	StartThread(context.Context, codexrpc.StartThreadInput) (codexrpc.Thread, error)
	ResumeThread(context.Context, string) (codexrpc.Thread, error)
	StartTurn(context.Context, string, string, string, string, bool) (codexrpc.Turn, error)
	SteerTurn(context.Context, string, string, string) error
	Interrupt(context.Context, string, string) error
	UpdateThreadSettings(context.Context, string, string, string) error
	ArchiveThread(context.Context, string) error
	Endpoint() string
	PID() int
	Close() error
}

type Session struct {
	RunID     string
	Transport *codexrpc.SocketTransport
	Client    *codexrpc.Client
	server    *exec.Cmd
	socket    string
	done      chan error
}

func (s *Session) Close() error {
	if s == nil {
		return nil
	}
	if s.Transport != nil {
		_ = s.Transport.Close()
	}
	if s.server != nil && s.server.Process != nil {
		_ = syscall.Kill(-s.server.Process.Pid, syscall.SIGTERM)
		if s.done != nil {
			select {
			case <-s.done:
			case <-time.After(2 * time.Second):
				_ = syscall.Kill(-s.server.Process.Pid, syscall.SIGKILL)
				<-s.done
			}
		}
	}
	if s.socket != "" {
		_ = os.Remove(s.socket)
	}
	return nil
}

func (s *Session) Endpoint() string { return "unix://" + s.socket }

func (s *Session) PID() int {
	if s == nil || s.server == nil || s.server.Process == nil {
		return 0
	}
	return s.server.Process.Pid
}

func (s *Session) StartThread(ctx context.Context, input codexrpc.StartThreadInput) (codexrpc.Thread, error) {
	return s.Client.StartThread(ctx, input)
}

func (s *Session) ResumeThread(ctx context.Context, threadID string) (codexrpc.Thread, error) {
	return s.Client.ResumeThread(ctx, threadID)
}

func (s *Session) StartTurn(ctx context.Context, threadID, prompt, workspace, model string, readOnly bool) (codexrpc.Turn, error) {
	return s.Client.StartTurn(ctx, threadID, prompt, workspace, model, readOnly)
}

func (s *Session) SteerTurn(ctx context.Context, threadID, turnID, prompt string) error {
	return s.Client.SteerTurn(ctx, threadID, turnID, prompt)
}

func (s *Session) Interrupt(ctx context.Context, threadID, turnID string) error {
	return s.Client.Interrupt(ctx, threadID, turnID)
}

func (s *Session) UpdateThreadSettings(ctx context.Context, threadID, model, sandbox string) error {
	return s.Client.UpdateThreadSettings(ctx, threadID, model, sandbox)
}

func (s *Session) ArchiveThread(ctx context.Context, threadID string) error {
	return s.Client.ArchiveThread(ctx, threadID)
}

type Manager struct {
	Context      context.Context
	Binary       string
	DaemonSocket string
	RuntimeRoot  string
	HookCommand  string
	OnNotify     NotificationHandler
	OnExit       func(string, error)

	mu       sync.Mutex
	sessions map[string]*Session
}

func NewManager(ctx context.Context, binary, daemonSocket, runtimeRoot, hookCommand string, onNotify NotificationHandler) *Manager {
	if ctx == nil {
		ctx = context.Background()
	}
	return &Manager{Context: ctx, Binary: binary, DaemonSocket: daemonSocket, RuntimeRoot: runtimeRoot, HookCommand: hookCommand, OnNotify: onNotify, sessions: make(map[string]*Session)}
}

func (m *Manager) Start(ctx context.Context, run orchestrator.Run) (Runtime, error) {
	m.mu.Lock()
	if _, exists := m.sessions[run.ID]; exists {
		m.mu.Unlock()
		return nil, errors.New("app-server already exists for run")
	}
	m.mu.Unlock()
	version, err := cliVersion(ctx, m.Binary)
	if err != nil {
		return nil, err
	}
	socket := SocketPath(m.RuntimeRoot, run.ID)
	if err := os.MkdirAll(filepath.Dir(socket), 0o700); err != nil {
		return nil, err
	}
	_ = os.Remove(socket)
	env := []string{"FREE_CONTEXT_RUN_ID=" + run.ID, "FREE_CONTEXT_DAEMON_SOCKET=" + m.DaemonSocket}
	serverArgs := []string{"--dangerously-bypass-hook-trust", "app-server", "--listen", "unix://" + socket, "--enable", "codex_hooks"}
	serverArgs = append(serverArgs, hookConfigArgs(m.HookCommand)...)
	serverArgs = append(serverArgs, "-c", "developer_instructions="+strconv.Quote(managedInstructions))
	binary := m.Binary
	if binary == "" {
		binary = "codex"
	}
	server := exec.CommandContext(m.Context, binary, serverArgs...)
	server.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	server.Env = append(os.Environ(), env...)
	server.Stdout = os.Stderr
	server.Stderr = os.Stderr
	if err := server.Start(); err != nil {
		return nil, fmt.Errorf("start run app-server: %w", err)
	}
	done := make(chan error, 1)
	go func() { done <- server.Wait() }()
	if err := waitForSocket(ctx, socket, done); err != nil {
		_ = syscall.Kill(-server.Process.Pid, syscall.SIGKILL)
		return nil, err
	}
	transport, client, err := codexrpc.DialUnix(ctx, socket, version, func(message json.RawMessage) {
		if m.OnNotify != nil {
			m.OnNotify(run.ID, message)
		}
	})
	if err != nil {
		_ = syscall.Kill(-server.Process.Pid, syscall.SIGKILL)
		return nil, err
	}
	session := &Session{RunID: run.ID, Transport: transport, Client: client, server: server, socket: socket, done: done}
	if err := client.Initialize(ctx); err != nil {
		_ = session.Close()
		return nil, err
	}
	m.mu.Lock()
	m.sessions[run.ID] = session
	m.mu.Unlock()
	go m.observeExit(run.ID, session)
	return session, nil
}

func (m *Manager) observeExit(runID string, session *Session) {
	err := <-session.done
	m.mu.Lock()
	owned := m.sessions[runID] == session
	m.mu.Unlock()
	if owned && m.OnExit != nil {
		m.OnExit(runID, err)
	}
}

func (m *Manager) Get(runID string) (Runtime, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	session, exists := m.sessions[runID]
	if !exists {
		return nil, fmt.Errorf("app-server for run %s is not active", runID)
	}
	return session, nil
}

func (m *Manager) Stop(runID string) error {
	m.mu.Lock()
	session := m.sessions[runID]
	delete(m.sessions, runID)
	m.mu.Unlock()
	if session == nil {
		return nil
	}
	return session.Close()
}

func (m *Manager) StopAll() error {
	m.mu.Lock()
	sessions := make([]*Session, 0, len(m.sessions))
	for runID, session := range m.sessions {
		sessions = append(sessions, session)
		delete(m.sessions, runID)
	}
	m.mu.Unlock()
	var first error
	for _, session := range sessions {
		if err := session.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func SocketPath(runtimeRoot, runID string) string {
	return filepath.Join(runtimeRoot, "runs", runID, "app-server.sock")
}

func cliVersion(ctx context.Context, binary string) (string, error) {
	if binary == "" {
		binary = "codex"
	}
	output, err := exec.CommandContext(ctx, binary, "--version").Output()
	if err != nil {
		return "", fmt.Errorf("read Codex CLI version: %w", err)
	}
	for _, field := range strings.Fields(string(output)) {
		if len(field) > 0 && strings.Count(field, ".") >= 2 && field[0] >= '0' && field[0] <= '9' {
			return field, nil
		}
	}
	return "", errors.New("Codex CLI version output did not contain a semantic version")
}

func hookConfigArgs(hookCommand string) []string {
	if hookCommand == "" {
		return nil
	}
	preCompact := fmt.Sprintf(`hooks.PreCompact=[{matcher="manual|auto",hooks=[{type="command",command=%s,timeout=1800}]}]`, strconv.Quote(shellQuote(filepath.Clean(hookCommand))+" pre-compact"))
	preToolUse := fmt.Sprintf(`hooks.PreToolUse=[{matcher=".*",hooks=[{type="command",command=%s,timeout=30}]}]`, strconv.Quote(shellQuote(filepath.Clean(hookCommand))+" pre-tool-use"))
	mcpCommand := `mcp_servers.free_context.command=` + strconv.Quote(filepath.Clean(hookCommand))
	return []string{"-c", preCompact, "-c", preToolUse, "-c", mcpCommand, "-c", `mcp_servers.free_context.args=["mcp"]`, "-c", `mcp_servers.free_context.default_tools_approval_mode="approve"`}
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

const managedInstructions = `This is a Free Context managed run. The root agent owns the plan and may create as many subagents as the plan requires. Use the free_context MCP server as the authoritative lifecycle channel. Call report_progress when accepting a task, completing a plan step, changing next_action, becoming blocked, or completing the task. Before every turn ends, report active with a concrete next_action, blocked with blockers, or completed with no unfinished work. When instructed that a handoff is ready, inspect run state, explicitly call accept_handoff, replan from workspace evidence, then call resolve_handoff. Do not request context compaction; Free Context replaces threads at PreCompact.`

func waitForSocket(ctx context.Context, socket string, done <-chan error) error {
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		if info, err := os.Stat(socket); err == nil && info.Mode()&os.ModeSocket != 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-done:
			return fmt.Errorf("run app-server exited before creating its socket: %w", err)
		case <-ticker.C:
		}
	}
}
