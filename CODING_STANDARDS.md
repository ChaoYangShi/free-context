# Coding Standards

- Keep the orchestration state machine deterministic. External I/O belongs in adapters.
- Test behavior through package interfaces, not private implementation details.
- Use the Go standard library unless a dependency removes substantial complexity.
- Persist state atomically before emitting effects that act on Codex threads.
- Return explicit errors. Do not add compatibility, retry, or fallback paths unless the spec requires them.
- Keep hook, MCP, CLI, and app-server protocol details out of the orchestration module.

