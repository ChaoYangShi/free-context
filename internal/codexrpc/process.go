package codexrpc

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
)

type NotificationHandler func(json.RawMessage)

type ProcessOptions struct {
	Binary       string
	Args         []string
	Env          []string
	Version      string
	Notification NotificationHandler
}

type callResult struct {
	message []byte
	err     error
}

type Process struct {
	command       *exec.Cmd
	stdin         io.WriteCloser
	stderr        *bytes.Buffer
	notification  NotificationHandler
	notifications chan json.RawMessage
	writeMu       sync.Mutex
	pendingMu     sync.Mutex
	pending       map[string]chan callResult
	done          chan struct{}
	waitErr       error
	closeMu       sync.Once
}

func Launch(ctx context.Context, options ProcessOptions) (*Process, *Client, error) {
	if options.Binary == "" {
		options.Binary = "codex"
	}
	command := exec.Command(options.Binary, options.Args...)
	command.Env = append(os.Environ(), options.Env...)
	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, nil, fmt.Errorf("create app-server stdin: %w", err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		stdin.Close()
		return nil, nil, fmt.Errorf("create app-server stdout: %w", err)
	}
	stderr := &bytes.Buffer{}
	command.Stderr = stderr
	if err := command.Start(); err != nil {
		stdin.Close()
		return nil, nil, fmt.Errorf("start app-server: %w", err)
	}
	process := &Process{
		command:       command,
		stdin:         stdin,
		stderr:        stderr,
		notification:  options.Notification,
		notifications: make(chan json.RawMessage, 256),
		pending:       make(map[string]chan callResult),
		done:          make(chan struct{}),
	}
	if process.notification != nil {
		go func() {
			for {
				select {
				case message := <-process.notifications:
					process.notification(message)
				case <-process.done:
					return
				}
			}
		}()
	}
	go process.readLoop(stdout)
	go func() {
		process.waitErr = command.Wait()
		close(process.done)
		process.failPending(fmt.Errorf("app-server exited: %w", process.waitErr))
	}()
	go func() {
		select {
		case <-ctx.Done():
			_ = process.Close()
		case <-process.done:
		}
	}()
	client := New(process, options.Version)
	return process, client, nil
}

func (p *Process) Call(ctx context.Context, request []byte) ([]byte, error) {
	var envelope struct {
		ID json.RawMessage `json:"id"`
	}
	if err := json.Unmarshal(request, &envelope); err != nil {
		return nil, fmt.Errorf("decode outgoing app-server request: %w", err)
	}
	key := string(envelope.ID)
	if key == "" || key == "null" {
		return nil, errors.New("app-server request id is required")
	}
	result := make(chan callResult, 1)
	p.pendingMu.Lock()
	if _, exists := p.pending[key]; exists {
		p.pendingMu.Unlock()
		return nil, fmt.Errorf("app-server request id %s is already pending", key)
	}
	p.pending[key] = result
	p.pendingMu.Unlock()

	if err := p.write(request); err != nil {
		p.removePending(key)
		return nil, err
	}
	select {
	case <-ctx.Done():
		p.removePending(key)
		return nil, ctx.Err()
	case response := <-result:
		return response.message, response.err
	case <-p.done:
		p.removePending(key)
		return nil, fmt.Errorf("app-server exited: %w: %s", p.waitErr, p.Stderr())
	}
}

func (p *Process) Close() error {
	var closeErr error
	p.closeMu.Do(func() {
		_ = p.stdin.Close()
		if p.command.Process != nil {
			closeErr = p.command.Process.Kill()
			if errors.Is(closeErr, os.ErrProcessDone) {
				closeErr = nil
			}
		}
		<-p.done
	})
	return closeErr
}

func (p *Process) Wait() error {
	<-p.done
	if errors.Is(p.waitErr, os.ErrProcessDone) {
		return nil
	}
	return p.waitErr
}

func (p *Process) Stderr() string {
	if p.stderr == nil {
		return ""
	}
	return p.stderr.String()
}

func (p *Process) readLoop(stdout io.Reader) {
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64<<10), 16<<20)
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		var message struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := json.Unmarshal(line, &message); err != nil {
			p.failPending(fmt.Errorf("decode app-server message: %w", err))
			continue
		}
		if message.Method != "" {
			if len(message.ID) != 0 && string(message.ID) != "null" {
				p.rejectServerRequest(message.ID)
			} else if p.notification != nil {
				p.notifications <- json.RawMessage(line)
			}
			continue
		}
		key := string(message.ID)
		p.pendingMu.Lock()
		pending := p.pending[key]
		delete(p.pending, key)
		p.pendingMu.Unlock()
		if pending != nil {
			pending <- callResult{message: line}
		}
	}
	if err := scanner.Err(); err != nil {
		p.failPending(fmt.Errorf("read app-server stream: %w", err))
	} else {
		p.failPending(io.EOF)
	}
}

func (p *Process) rejectServerRequest(id json.RawMessage) {
	response := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error": map[string]any{
			"code":    -32601,
			"message": "free-context does not handle app-server requests",
		},
	}
	encoded, err := json.Marshal(response)
	if err == nil {
		_ = p.write(encoded)
	}
}

func (p *Process) write(message []byte) error {
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	if _, err := p.stdin.Write(append(message, '\n')); err != nil {
		return fmt.Errorf("write app-server message: %w", err)
	}
	return nil
}

func (p *Process) removePending(key string) {
	p.pendingMu.Lock()
	delete(p.pending, key)
	p.pendingMu.Unlock()
}

func (p *Process) failPending(err error) {
	p.pendingMu.Lock()
	pending := p.pending
	p.pending = make(map[string]chan callResult)
	p.pendingMu.Unlock()
	for _, result := range pending {
		result <- callResult{err: err}
	}
}
