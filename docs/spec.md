# Free Context Specification

## Objective

Build a Linux-only Codex CLI supervisor that prevents managed agents from continuing after context compaction. Before any root or worker agent compacts, save structured progress, replace the affected agent with a clean thread, and continue the run.

## Runtime Model

- A user-level `free-context` daemon owns one Codex app-server per active run.
- A workspace may have one active root run. A run may contain multiple subagents.
- The root agent owns the plan and decides how many subagents to create.
- Free Context manages agent lifecycle, state, handoff, and rotation. It does not manage Git worktrees, file ownership, merge conflicts, or task correctness.
- Managed sessions use `approval_policy=never`. Handoff and replacement agents inherit the source model and do not receive broader sandbox permissions.
- The minimum supported Codex CLI version is `0.144.4`. Older versions are rejected; no compatibility layer is provided.

## Integration

- The daemon starts Codex app-server with run-scoped runtime configuration. Global `~/.codex/config.toml` is not modified.
- App-server supplies thread lifecycle, token usage, steering, interruption, and thread creation.
- Command hooks handle every `PreCompact`, whether automatic or manually requested, and `PreToolUse` enforces quiescence.
- A local MCP server exposes `report_progress`, `accept_handoff`, `resolve_handoff`, and `get_run_state`.
- App-server identifies the calling thread. The tool must not infer thread identity or semantic status from natural-language transcript parsing.

## Progress

Agents report progress only when they accept a task, complete a plan step, change `next_action`, become blocked, or complete their task.

Lifecycle state comes from app-server. Semantic progress comes from MCP.

When a background root turn ends while the run is active, the daemon starts another turn only when the root reported an explicit `next_action`. Missing terminal status and missing `next_action` blocks the run.

## Handoff

Handoffs are immutable JSON documents. Every field is required; arrays may be empty. Existing files, diffs, plans, issues, and other artifacts are referenced rather than copied. Sensitive values are redacted.

```json
{
  "handoff_id": "...",
  "run_id": "...",
  "created_at": "...",
  "scope": "agent",
  "source_session_id": "...",
  "source_turn_id": "...",
  "source_thread_id": "...",
  "parent_thread_id": null,
  "workspace_path": "...",
  "assigned_task": "...",
  "model": "...",
  "objective": "...",
  "completion_criteria": ["..."],
  "constraints": ["..."],
  "decisions": [{"decision": "...", "reason": "..."}],
  "completed_work": [{"result": "...", "evidence": ["..."]}],
  "in_progress_work": ["..."],
  "next_action": "...",
  "blockers": ["..."],
  "artifact_references": ["..."],
  "suggested_skills": ["..."],
  "child_handoff_ids": []
}
```

One ephemeral `codex exec --ephemeral` handoff agent is created per source thread. It inherits the source model, reads that thread's transcript and the workspace, emits schema-constrained JSON, and exits. Handoff agents do not load Free Context hooks or MCP and cannot recursively rotate.

Workspace files and command results are authoritative for execution state. Handoff JSON is authoritative for objective, constraints, and confirmed decisions.

## Worker Rotation

1. A worker enters `PreCompact` and pauses.
2. The daemon generates, validates, and atomically persists its handoff.
3. The hook stops compaction and retires the worker.
4. The daemon steers the parent with the handoff reference.
5. The parent accepts ownership of the handoff and resolves it by continuing itself, creating newly planned work, or confirming completion.

The old worker stops after durable handoff validation. It does not wait for a replacement because the parent may be waiting for the old worker, which would deadlock.

## Root Rotation

1. The root enters `PreCompact` and pauses.
2. The run enters quiescing. `PreToolUse` blocks new tool calls.
3. In-flight tool calls finish, active worker turns stop, and handoffs are produced for unfinished threads.
4. A tree handoff references child handoff IDs without embedding their content.
5. The daemon creates a clean, read-only top-level thread through app-server `thread/start` and `turn/start`.
6. The new root reads the handoffs, verifies the workspace, and explicitly accepts ownership.
7. The old tree retires. The new root receives the original sandbox and replans unfinished work.

Subagents are never promoted to root. The new root decides the new worker count and topology rather than recreating the old tree mechanically.

## Failure And Recovery

- Quiescence, handoff generation, parent acceptance, and root acceptance each have a fixed 30-minute timeout.
- Any transition failure is fail-closed: no unvalidated successor may write, compaction does not proceed, and the run becomes `blocked`.
- User input requested by a background agent blocks the run. The user resumes interaction with `free-context attach <run_id>`.
- Run state and handoffs are atomically persisted under `$XDG_STATE_HOME/free-context/runs/<run_id>/` and retained after terminal states.
- The daemon automatically recovers incomplete runs after restart. It must first establish that the prior managed app-server is no longer running; uncertain process ownership blocks recovery.

## CLI Interface

```text
free-context run
free-context list
free-context status [run_id]
free-context attach [run_id]
free-context stop [run_id]
free-context inspect <run_id>
free-context delete <run_id>
free-context daemon start|stop|status
free-context mcp
free-context hook
```

`run` interactively requests an objective and one or more completion criteria before opening the initial remote Codex TUI.

