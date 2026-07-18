# Free Context

Free Context supervises long-running Codex agent trees. It checkpoints structured progress and replaces root or worker threads before Codex compacts their context.

The implementation targets Linux and Codex CLI `0.144.4` or newer. See [the specification](docs/spec.md) for the behavioral contract.

## Install

Requirements:

- Linux
- Go 1.26 or newer
- An authenticated `codex` CLI, version `0.144.4` or newer

Clone the repository and install the executable into `/usr/local/bin`:

```bash
git clone https://github.com/ChaoYangShi/free-context.git
cd free-context
sudo env GOBIN=/usr/local/bin "$(command -v go)" install ./cmd/free-context
```

`/usr/local/bin` is the system location for locally installed executables, so every terminal can call `free-context` regardless of its shell or current directory. Verify the installation:

```bash
command -v free-context
free-context --help
```

## Terminal usage

Start a managed session from the workspace it should supervise:

```bash
cd /path/to/your/workspace
free-context run
```

The command asks for the objective and completion criteria, starts the user daemon when needed, then opens Codex against the run's Unix app-server endpoint. Runtime hooks and the `free_context` MCP server are injected only into that app-server process; user Codex configuration is not modified.

Show the built-in command reference at any time:

```bash
free-context --help
```

Enable Bash completion for the current shell:

```bash
source <(free-context completion bash)
```

To enable it for future Bash sessions, save the generated script in the standard completion directory:

```bash
mkdir -p ~/.local/share/bash-completion/completions
free-context completion bash > ~/.local/share/bash-completion/completions/free-context
```

Completion includes command values, daemon and hook subcommands, and run IDs valid for the selected operation. Run-ID candidates require the Free Context daemon to be running.

Operational commands can be called from any terminal:

```text
free-context list
free-context status [run_id]
free-context attach [run_id]
free-context stop [run_id]
free-context inspect [run_id]
free-context delete <run_id>
free-context daemon start|stop|status|serve
free-context completion bash
free-context mcp
free-context hook <pre-compact|pre-tool-use>
free-context pre-compact|pre-tool-use
free-context --version
```

Run state and immutable handoffs are stored under `$XDG_STATE_HOME/free-context`. Runtime sockets are stored under `$XDG_RUNTIME_DIR/free-context` when it is set.

Completed runs stop their managed app-server and are removed automatically. `Ctrl+C` reports the foreground TUI exit before `run` or `attach` ends, so a root that already reported completion is finalized; incomplete runs remain available to `attach`. Stopped runs remain until removed with `free-context delete <run_id>`.

## Lifecycle

`PreCompact` never allows a managed thread to compact. Free Context waits for currently executing tool items to complete, blocks new tool calls, creates schema-constrained handoff JSON with an ephemeral handoff agent, and then retires or replaces the affected thread. A replacement root starts with the run sandbox and still must explicitly accept the tree handoff before continuing work.
