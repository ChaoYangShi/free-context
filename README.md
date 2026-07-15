# Free Context

Free Context supervises long-running Codex agent trees. It checkpoints structured progress and replaces root or worker threads before Codex compacts their context.

The implementation targets Linux and Codex CLI `0.144.4` or newer. See [the specification](docs/spec.md) for the behavioral contract.

## Install

Requirements:

- Linux
- Go 1.26 or newer
- An authenticated `codex` CLI, version `0.144.4` or newer

```bash
go install ./cmd/free-context
```

## Run

Start a managed session from the workspace it should supervise:

```bash
free-context run
```

The command asks for the objective and completion criteria, starts the user daemon when needed, then opens Codex against the run's Unix app-server endpoint. Runtime hooks and the `free_context` MCP server are injected only into that app-server process; user Codex configuration is not modified.

Operational commands:

```text
free-context list
free-context status [run_id]
free-context attach [run_id]
free-context stop [run_id]
free-context inspect <run_id>
free-context delete <run_id>
free-context daemon start|stop|status
```

Run state and immutable handoffs are stored under `$XDG_STATE_HOME/free-context`. Runtime sockets are stored under `$XDG_RUNTIME_DIR/free-context` when it is set.

## Lifecycle

`PreCompact` never allows a managed thread to compact. Free Context waits for currently executing tool items to complete, blocks new tool calls, creates schema-constrained handoff JSON with an ephemeral handoff agent, and then retires or replaces the affected thread. A replacement root starts read-only and receives the original sandbox only after explicitly accepting the tree handoff.
