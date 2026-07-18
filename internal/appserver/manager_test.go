package appserver

import (
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/ChaoYangShi/free-context/internal/codexconfig"
)

func TestHookConfigInjectsBothLifecycleHooks(t *testing.T) {
	args := hookConfigArgs("/opt/free context/free-context")
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "hooks.PreCompact") || !strings.Contains(joined, "hooks.PreToolUse") {
		t.Fatalf("hook args = %q", joined)
	}
	if !strings.Contains(joined, "pre-compact") || !strings.Contains(joined, "pre-tool-use") {
		t.Fatalf("hook commands = %q", joined)
	}
	if !strings.Contains(joined, "mcp_servers.free_context") {
		t.Fatalf("MCP config missing from %q", joined)
	}
	if !strings.Contains(joined, "default_tools_approval_mode") {
		t.Fatalf("MCP approval config missing from %q", joined)
	}
	if !strings.Contains(joined, `mcp_servers.free_context.env_vars=["FREE_CONTEXT_RUN_ID","FREE_CONTEXT_DAEMON_SOCKET"]`) {
		t.Fatalf("MCP environment forwarding missing from %q", joined)
	}
}

func TestAppServerArgsUseCurrentHooksFeature(t *testing.T) {
	joined := strings.Join(appServerArgs("/tmp/app-server.sock", "/opt/free-context"), " ")
	if !strings.Contains(joined, "--enable hooks") {
		t.Fatalf("hooks feature missing from %q", joined)
	}
	if !strings.Contains(joined, codexconfig.DangerouslyBypassApprovalsAndSandboxFlag) {
		t.Fatalf("dangerous sandbox bypass flag missing from %q", joined)
	}
	if strings.Contains(joined, "codex_hooks") {
		t.Fatalf("deprecated codex_hooks feature present in %q", joined)
	}
}

func TestManagedInstructionsRequireProgressAndExplicitHandoffAcceptance(t *testing.T) {
	if !strings.Contains(managedInstructions, "report_progress") || !strings.Contains(managedInstructions, "accept_handoff") || !strings.Contains(managedInstructions, "root agent owns the plan") {
		t.Fatalf("managed instructions are incomplete: %s", managedInstructions)
	}
	for _, requirement := range []string{
		"Only when you are the root agent: before starting execution and whenever the plan changes, evaluate the remaining work for independent tasks.",
		"explicitly spawn the appropriate number of subagents",
		"chooses the subagent count from task dependencies and conflict risk",
		"Non-root agents must execute their assigned task and must not create subagents.",
	} {
		if !strings.Contains(managedInstructions, requirement) {
			t.Fatalf("managed instructions do not require autonomous subagent evaluation: missing %q", requirement)
		}
	}
}

func TestSessionCloseReturnsAfterExitObserverRuns(t *testing.T) {
	command := exec.Command("sh", "-c", "exit 0")
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	exit := &serverExit{done: make(chan struct{})}
	go func() {
		exit.err = command.Wait()
		close(exit.done)
	}()

	observed := make(chan struct{})
	session := &Session{RunID: "run-1", server: command, exit: exit}
	manager := &Manager{
		sessions: map[string]*Session{"run-1": session},
		OnExit:   func(string, error) { close(observed) },
	}
	go manager.observeExit("run-1", session)
	<-observed
	if _, err := manager.Get("run-1"); err == nil {
		t.Fatal("exited app-server remained active in manager")
	}

	closed := make(chan error, 1)
	go func() { closed <- session.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("Session.Close blocked after observeExit consumed the process result")
	}
}
