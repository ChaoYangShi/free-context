package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/ChaoYangShi/free-context/internal/daemon"
	"github.com/ChaoYangShi/free-context/internal/orchestrator"
)

type Commander interface {
	Execute(context.Context, daemon.CommandKind, any) (orchestrator.Outcome, error)
	State(context.Context, string) (daemon.RunState, error)
}

type Server struct {
	runID     string
	commander Commander
}

func NewServer(runID string, commander Commander) *Server {
	return &Server{runID: runID, commander: commander}
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

func (s *Server) Serve(ctx context.Context, input io.Reader, output io.Writer) error {
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	encoder := json.NewEncoder(output)
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		var message request
		if err := json.Unmarshal(scanner.Bytes(), &message); err != nil {
			return fmt.Errorf("decode MCP request: %w", err)
		}
		if len(message.ID) == 0 {
			continue
		}
		result, rpcError := s.handle(ctx, message)
		response := map[string]any{"jsonrpc": "2.0", "id": message.ID}
		if rpcError != nil {
			response["error"] = map[string]any{"code": rpcError.code, "message": rpcError.message}
		} else {
			response["result"] = result
		}
		if err := encoder.Encode(response); err != nil {
			return fmt.Errorf("write MCP response: %w", err)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read MCP request: %w", err)
	}
	return nil
}

type protocolError struct {
	code    int
	message string
}

func (s *Server) handle(ctx context.Context, message request) (any, *protocolError) {
	switch message.Method {
	case "initialize":
		return map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]string{"name": "free-context", "version": "0.1.0"},
		}, nil
	case "tools/list":
		return map[string]any{"tools": tools()}, nil
	case "tools/call":
		result, err := s.callTool(ctx, message.Params)
		if err != nil {
			return toolResult(map[string]string{"error": err.Error()}, true), nil
		}
		return toolResult(result, false), nil
	default:
		return nil, &protocolError{code: -32601, message: "method not found"}
	}
}

type toolCall struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
	Meta      struct {
		ThreadID string `json:"threadId"`
	} `json:"_meta"`
}

func (s *Server) callTool(ctx context.Context, params json.RawMessage) (any, error) {
	var call toolCall
	if err := json.Unmarshal(params, &call); err != nil {
		return nil, fmt.Errorf("decode tool call: %w", err)
	}
	if call.Meta.ThreadID == "" {
		return nil, errors.New("Codex did not provide _meta.threadId")
	}
	switch call.Name {
	case "report_progress":
		var arguments struct {
			Status             orchestrator.ProgressStatus `json:"status"`
			CompletedWork      []string                    `json:"completed_work"`
			InProgressWork     []string                    `json:"in_progress_work"`
			NextAction         string                      `json:"next_action"`
			Blockers           []string                    `json:"blockers"`
			ArtifactReferences []string                    `json:"artifact_references"`
		}
		if err := strictArguments(call.Arguments, &arguments); err != nil {
			return nil, err
		}
		outcome, err := s.commander.Execute(ctx, daemon.CommandReportProgress, orchestrator.ReportProgress{
			RunID:          s.runID,
			ThreadID:       call.Meta.ThreadID,
			Status:         arguments.Status,
			CompletedWork:  arguments.CompletedWork,
			InProgressWork: arguments.InProgressWork,
			NextAction:     arguments.NextAction,
			Blockers:       arguments.Blockers,
			Artifacts:      arguments.ArtifactReferences,
		})
		return outcome.Run, err
	case "accept_handoff":
		var arguments struct {
			HandoffID string `json:"handoff_id"`
		}
		if err := strictArguments(call.Arguments, &arguments); err != nil {
			return nil, err
		}
		outcome, err := s.commander.Execute(ctx, daemon.CommandAcceptHandoff, orchestrator.AcceptHandoff{
			RunID: s.runID, ThreadID: call.Meta.ThreadID, HandoffID: arguments.HandoffID,
		})
		return outcome.Run, err
	case "resolve_handoff":
		var arguments struct {
			HandoffID  string                         `json:"handoff_id"`
			Resolution orchestrator.HandoffResolution `json:"resolution"`
		}
		if err := strictArguments(call.Arguments, &arguments); err != nil {
			return nil, err
		}
		outcome, err := s.commander.Execute(ctx, daemon.CommandResolveHandoff, orchestrator.ResolveHandoff{
			RunID: s.runID, ThreadID: call.Meta.ThreadID, HandoffID: arguments.HandoffID, Resolution: arguments.Resolution,
		})
		return outcome.Run, err
	case "get_run_state":
		if len(call.Arguments) > 0 && string(call.Arguments) != "{}" && string(call.Arguments) != "null" {
			return nil, errors.New("get_run_state accepts no arguments")
		}
		return s.commander.State(ctx, s.runID)
	default:
		return nil, fmt.Errorf("unknown tool %q", call.Name)
	}
}

func strictArguments(raw json.RawMessage, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode tool arguments: %w", err)
	}
	return nil
}

func toolResult(value any, isError bool) map[string]any {
	encoded, _ := json.Marshal(value)
	return map[string]any{
		"content": []map[string]string{{"type": "text", "text": string(encoded)}},
		"isError": isError,
	}
}

func tools() []map[string]any {
	stringArray := map[string]any{"type": "array", "items": map[string]string{"type": "string"}}
	return []map[string]any{
		{
			"name":        "report_progress",
			"description": "Persist semantic progress when a task state changes.",
			"inputSchema": map[string]any{
				"type":     "object",
				"required": []string{"status", "completed_work", "in_progress_work", "next_action", "blockers", "artifact_references"},
				"properties": map[string]any{
					"status":         map[string]any{"type": "string", "enum": []string{"active", "blocked", "completed"}},
					"completed_work": stringArray, "in_progress_work": stringArray,
					"next_action": map[string]string{"type": "string"}, "blockers": stringArray,
					"artifact_references": stringArray,
				},
			},
		},
		{
			"name": "accept_handoff", "description": "Accept ownership of a validated handoff.",
			"inputSchema": objectSchema([]string{"handoff_id"}, map[string]any{
				"handoff_id": map[string]string{"type": "string"},
			}),
		},
		{
			"name": "resolve_handoff", "description": "Resolve an accepted handoff after replanning.",
			"inputSchema": objectSchema([]string{"handoff_id", "resolution"}, map[string]any{
				"handoff_id": map[string]string{"type": "string"},
				"resolution": map[string]any{"type": "string", "enum": []string{"continued_by_owner", "replanned", "completed"}},
			}),
		},
		{
			"name": "get_run_state", "description": "Read the current managed run and agent tree state.",
			"inputSchema": objectSchema([]string{}, map[string]any{}),
		},
	}
}

func objectSchema(required []string, properties map[string]any) map[string]any {
	return map[string]any{"type": "object", "required": required, "properties": properties, "additionalProperties": false}
}
