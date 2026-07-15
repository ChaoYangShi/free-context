package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/ChaoYangShi/free-context/internal/orchestrator"
)

func Serve(ctx context.Context, socket string, handler http.Handler) error {
	return ServeReady(ctx, socket, handler, nil)
}

func ServeReady(ctx context.Context, socket string, handler http.Handler, ready chan<- struct{}) error {
	if err := os.MkdirAll(filepath.Dir(socket), 0o700); err != nil {
		return fmt.Errorf("create socket directory: %w", err)
	}
	if err := removeStaleSocket(socket); err != nil {
		return err
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		return fmt.Errorf("listen daemon socket: %w", err)
	}
	if err := os.Chmod(socket, 0o600); err != nil {
		listener.Close()
		os.Remove(socket)
		return fmt.Errorf("secure daemon socket: %w", err)
	}
	if ready != nil {
		close(ready)
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		_ = server.Shutdown(context.Background())
	}()
	err = server.Serve(listener)
	_ = listener.Close()
	_ = os.Remove(socket)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func removeStaleSocket(socket string) error {
	info, err := os.Lstat(socket)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect daemon socket: %w", err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("daemon socket path is not a socket")
	}
	probe, err := net.DialTimeout("unix", socket, 100*time.Millisecond)
	if err == nil {
		probe.Close()
		return fmt.Errorf("daemon socket is already active")
	}
	return os.Remove(socket)
}

type Client struct {
	socket string
	http   *http.Client
}

func NewClient(socket string) *Client {
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var dialer net.Dialer
		return dialer.DialContext(ctx, "unix", socket)
	}}
	return &Client{socket: socket, http: &http.Client{Transport: transport}}
}

func (c *Client) Ping(ctx context.Context) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://free-context/ping", nil)
	if err != nil {
		return err
	}
	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		return fmt.Errorf("unexpected ping status %d", response.StatusCode)
	}
	return nil
}

type StartRunInput struct {
	WorkspacePath      string
	Objective          string
	CompletionCriteria []string
	Sandbox            string
}

func (c *Client) StartRun(ctx context.Context, input StartRunInput) (orchestrator.Outcome, error) {
	payload := struct {
		WorkspacePath      string   `json:"workspace_path"`
		Objective          string   `json:"objective"`
		CompletionCriteria []string `json:"completion_criteria"`
		Sandbox            string   `json:"sandbox"`
	}{input.WorkspacePath, input.Objective, input.CompletionCriteria, input.Sandbox}
	var outcome orchestrator.Outcome
	if err := c.request(ctx, http.MethodPost, "/v1/runs", payload, http.StatusCreated, &outcome); err != nil {
		return orchestrator.Outcome{}, err
	}
	return outcome, nil
}

func (c *Client) Run(ctx context.Context, id string) (orchestrator.Run, error) {
	var run orchestrator.Run
	if err := c.request(ctx, http.MethodGet, "/v1/runs/"+id, nil, http.StatusOK, &run); err != nil {
		return orchestrator.Run{}, err
	}
	return run, nil
}

func (c *Client) State(ctx context.Context, id string) (RunState, error) {
	var state RunState
	if err := c.request(ctx, http.MethodGet, "/v1/states/"+id, nil, http.StatusOK, &state); err != nil {
		return RunState{}, err
	}
	return state, nil
}

func (c *Client) List(ctx context.Context) ([]orchestrator.Run, error) {
	var runs []orchestrator.Run
	if err := c.request(ctx, http.MethodGet, "/v1/runs", nil, http.StatusOK, &runs); err != nil {
		return nil, err
	}
	return runs, nil
}

func (c *Client) Delete(ctx context.Context, id string) error {
	return c.request(ctx, http.MethodDelete, "/v1/runs/"+id, nil, http.StatusNoContent, nil)
}

func (c *Client) Execute(ctx context.Context, kind CommandKind, command any) (orchestrator.Outcome, error) {
	payload := struct {
		Kind    CommandKind `json:"kind"`
		Command any         `json:"command"`
	}{kind, command}
	var outcome orchestrator.Outcome
	if err := c.request(ctx, http.MethodPost, "/v1/commands", payload, http.StatusOK, &outcome); err != nil {
		return orchestrator.Outcome{}, err
	}
	return outcome, nil
}

func (c *Client) request(ctx context.Context, method, path string, input any, wantStatus int, output any) error {
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, "http://free-context"+path, body)
	if err != nil {
		return err
	}
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != wantStatus {
		data, _ := io.ReadAll(io.LimitReader(response.Body, 16<<10))
		return fmt.Errorf("daemon returned %d: %s", response.StatusCode, bytes.TrimSpace(data))
	}
	if output == nil {
		return nil
	}
	return json.NewDecoder(response.Body).Decode(output)
}
