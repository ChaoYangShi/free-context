package codexrpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

var (
	ErrUnsupportedVersion  = errors.New("Codex CLI version is below the supported minimum")
	ErrUnsupportedPlatform = errors.New("Codex app-server must run on Linux")
)

type Transport interface {
	Call(context.Context, []byte) ([]byte, error)
}

type Client struct {
	transport Transport
	version   string
	mu        sync.Mutex
	nextID    uint64
}

func New(transport Transport, version string) *Client {
	return &Client{transport: transport, version: version}
}

func (c *Client) Initialize(ctx context.Context) error {
	if !supportedVersion(c.version) {
		return fmt.Errorf("%w: %s", ErrUnsupportedVersion, c.version)
	}
	var response struct {
		PlatformOS string `json:"platformOs"`
	}
	if err := c.call(ctx, "initialize", map[string]any{
		"clientInfo":   map[string]string{"name": "free-context", "version": "0.1.0"},
		"capabilities": map[string]any{"experimentalApi": true},
	}, &response); err != nil {
		return err
	}
	if response.PlatformOS != "linux" {
		return fmt.Errorf("%w: %s", ErrUnsupportedPlatform, response.PlatformOS)
	}
	return nil
}

type StartThreadInput struct {
	WorkspacePath string
	Model         string
	Sandbox       string
	Ephemeral     bool
	Config        map[string]any
}

type Thread struct {
	ID             string  `json:"id"`
	SessionID      string  `json:"sessionId"`
	ParentThreadID *string `json:"parentThreadId"`
	Path           *string `json:"path"`
	Preview        string  `json:"preview"`
	Model          string  `json:"-"`
}

type Turn struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

func (c *Client) StartThread(ctx context.Context, input StartThreadInput) (Thread, error) {
	if filepath.IsAbs(input.WorkspacePath) == false {
		return Thread{}, errors.New("workspace path must be absolute")
	}
	params := map[string]any{
		"cwd":            input.WorkspacePath,
		"model":          input.Model,
		"sandbox":        input.Sandbox,
		"approvalPolicy": "never",
		"ephemeral":      input.Ephemeral,
		"threadSource":   "free-context",
	}
	if input.Config != nil {
		params["config"] = input.Config
	}
	var response struct {
		Thread Thread `json:"thread"`
		Model  string `json:"model"`
	}
	if err := c.call(ctx, "thread/start", params, &response); err != nil {
		return Thread{}, err
	}
	if response.Thread.ID == "" {
		return Thread{}, errors.New("thread/start returned no thread id")
	}
	response.Thread.Model = response.Model
	return response.Thread, nil
}

func (c *Client) ResumeThread(ctx context.Context, threadID string) (Thread, error) {
	if threadID == "" {
		return Thread{}, errors.New("thread id is required")
	}
	var response struct {
		Thread Thread `json:"thread"`
		Model  string `json:"model"`
	}
	if err := c.call(ctx, "thread/resume", map[string]any{"threadId": threadID, "excludeTurns": true}, &response); err != nil {
		return Thread{}, err
	}
	response.Thread.Model = response.Model
	return response.Thread, nil
}

func (c *Client) StartTurn(ctx context.Context, threadID, prompt, workspace, model string, readOnly bool) (Turn, error) {
	policy := map[string]any{"type": "workspaceWrite"}
	if readOnly {
		policy = map[string]any{"type": "readOnly"}
	}
	var response struct {
		Turn Turn `json:"turn"`
	}
	if err := c.call(ctx, "turn/start", map[string]any{
		"threadId":       threadID,
		"input":          []map[string]any{{"type": "text", "text": prompt}},
		"cwd":            workspace,
		"model":          model,
		"approvalPolicy": "never",
		"sandboxPolicy":  policy,
	}, &response); err != nil {
		return Turn{}, err
	}
	if response.Turn.ID == "" {
		return Turn{}, errors.New("turn/start returned no turn id")
	}
	return response.Turn, nil
}

func (c *Client) SteerTurn(ctx context.Context, threadID, turnID, prompt string) error {
	if threadID == "" || turnID == "" {
		return errors.New("thread id and expected turn id are required for steering")
	}
	return c.call(ctx, "turn/steer", map[string]any{
		"threadId":       threadID,
		"expectedTurnId": turnID,
		"input":          []map[string]any{{"type": "text", "text": prompt}},
	}, nil)
}

func (c *Client) Interrupt(ctx context.Context, threadID, turnID string) error {
	if threadID == "" || turnID == "" {
		return errors.New("thread id and turn id are required for interruption")
	}
	params := map[string]any{"threadId": threadID, "turnId": turnID}
	return c.call(ctx, "turn/interrupt", params, nil)
}

func (c *Client) UpdateThreadSettings(ctx context.Context, threadID, model, sandbox string) error {
	params := map[string]any{"threadId": threadID}
	if model != "" {
		params["model"] = model
	}
	if sandbox != "" {
		params["sandboxPolicy"] = sandboxPolicy(sandbox)
	}
	return c.call(ctx, "thread/settings/update", params, nil)
}

func (c *Client) ArchiveThread(ctx context.Context, threadID string) error {
	return c.call(ctx, "thread/archive", map[string]any{"threadId": threadID}, nil)
}

func (c *Client) call(ctx context.Context, method string, params any, result any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nextID++
	request := map[string]any{"jsonrpc": "2.0", "id": c.nextID, "method": method, "params": params}
	encoded, err := json.Marshal(request)
	if err != nil {
		return err
	}
	responseBytes, err := c.transport.Call(ctx, encoded)
	if err != nil {
		return err
	}
	var envelope struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(responseBytes, &envelope); err != nil {
		return fmt.Errorf("decode app-server response: %w", err)
	}
	if envelope.Error != nil {
		return fmt.Errorf("app-server %s failed (%d): %s", method, envelope.Error.Code, envelope.Error.Message)
	}
	if result == nil {
		return nil
	}
	if err := json.Unmarshal(envelope.Result, result); err != nil {
		return fmt.Errorf("decode app-server %s result: %w", method, err)
	}
	return nil
}

func supportedVersion(version string) bool {
	version = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(version), "codex-cli "))
	parts := strings.Split(version, ".")
	if len(parts) < 3 {
		return false
	}
	major, majorErr := strconv.Atoi(parts[0])
	minor, minorErr := strconv.Atoi(parts[1])
	patch, patchErr := strconv.Atoi(parts[2])
	if majorErr != nil || minorErr != nil || patchErr != nil {
		return false
	}
	if major != 0 {
		return major > 0
	}
	if minor != 144 {
		return minor > 144
	}
	return patch >= 4
}

func sandboxPolicy(sandbox string) map[string]any {
	switch sandbox {
	case "read-only":
		return map[string]any{"type": "readOnly"}
	case "danger-full-access":
		return map[string]any{"type": "dangerFullAccess"}
	default:
		return map[string]any{"type": "workspaceWrite"}
	}
}
