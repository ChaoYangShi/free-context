package appserver

import (
	"strings"
	"testing"
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
}

func TestManagedInstructionsRequireProgressAndExplicitHandoffAcceptance(t *testing.T) {
	if !strings.Contains(managedInstructions, "report_progress") || !strings.Contains(managedInstructions, "accept_handoff") || !strings.Contains(managedInstructions, "root agent owns the plan") {
		t.Fatalf("managed instructions are incomplete: %s", managedInstructions)
	}
}
