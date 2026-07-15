package codexrpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"

	"github.com/gorilla/websocket"
)

type SocketTransport struct {
	connection    *websocket.Conn
	notification  NotificationHandler
	notifications chan json.RawMessage
	writeMu       sync.Mutex
	pendingMu     sync.Mutex
	pending       map[string]chan callResult
	done          chan struct{}
	closeOnce     sync.Once
	readErr       error
}

func DialUnix(ctx context.Context, socketPath, version string, notification NotificationHandler) (*SocketTransport, *Client, error) {
	dialer := websocket.Dialer{
		NetDialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var unixDialer net.Dialer
			return unixDialer.DialContext(ctx, "unix", socketPath)
		},
	}
	connection, response, err := dialer.DialContext(ctx, "ws://localhost/rpc", http.Header{})
	if err != nil {
		if response != nil {
			return nil, nil, fmt.Errorf("connect app-server websocket: %w (HTTP %s)", err, response.Status)
		}
		return nil, nil, fmt.Errorf("connect app-server websocket: %w", err)
	}
	transport := &SocketTransport{
		connection:    connection,
		notification:  notification,
		notifications: make(chan json.RawMessage, 256),
		pending:       make(map[string]chan callResult),
		done:          make(chan struct{}),
	}
	if notification != nil {
		go transport.dispatchNotifications()
	}
	go transport.readLoop()
	return transport, New(transport, version), nil
}

func (t *SocketTransport) Call(ctx context.Context, request []byte) ([]byte, error) {
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
	t.pendingMu.Lock()
	if _, exists := t.pending[key]; exists {
		t.pendingMu.Unlock()
		return nil, fmt.Errorf("app-server request id %s is already pending", key)
	}
	t.pending[key] = result
	t.pendingMu.Unlock()
	if err := t.write(request); err != nil {
		t.removePending(key)
		return nil, err
	}
	select {
	case <-ctx.Done():
		t.removePending(key)
		return nil, ctx.Err()
	case response := <-result:
		return response.message, response.err
	case <-t.done:
		t.removePending(key)
		return nil, fmt.Errorf("app-server websocket closed: %w", t.readErr)
	}
}

func (t *SocketTransport) Close() error {
	var closeErr error
	t.closeOnce.Do(func() {
		closeErr = t.connection.Close()
		close(t.done)
		t.failPending(errors.New("app-server websocket closed"))
	})
	return closeErr
}

func (t *SocketTransport) readLoop() {
	for {
		messageType, message, err := t.connection.ReadMessage()
		if err != nil {
			t.readErr = err
			_ = t.Close()
			return
		}
		if messageType != websocket.TextMessage {
			continue
		}
		var envelope struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := json.Unmarshal(message, &envelope); err != nil {
			t.failPending(fmt.Errorf("decode app-server message: %w", err))
			continue
		}
		if envelope.Method != "" {
			if len(envelope.ID) != 0 && string(envelope.ID) != "null" {
				t.rejectServerRequest(envelope.ID)
			} else if t.notification != nil {
				t.notifications <- json.RawMessage(append([]byte(nil), message...))
			}
			continue
		}
		key := string(envelope.ID)
		t.pendingMu.Lock()
		pending := t.pending[key]
		delete(t.pending, key)
		t.pendingMu.Unlock()
		if pending != nil {
			pending <- callResult{message: append([]byte(nil), message...)}
		}
	}
}

func (t *SocketTransport) dispatchNotifications() {
	for {
		select {
		case message := <-t.notifications:
			t.notification(message)
		case <-t.done:
			return
		}
	}
}

func (t *SocketTransport) rejectServerRequest(id json.RawMessage) {
	response, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error":   map[string]any{"code": -32601, "message": "free-context does not handle app-server requests"},
	})
	_ = t.write(response)
}

func (t *SocketTransport) write(message []byte) error {
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	if err := t.connection.WriteMessage(websocket.TextMessage, message); err != nil {
		return fmt.Errorf("write app-server websocket message: %w", err)
	}
	return nil
}

func (t *SocketTransport) removePending(key string) {
	t.pendingMu.Lock()
	delete(t.pending, key)
	t.pendingMu.Unlock()
}

func (t *SocketTransport) failPending(err error) {
	t.pendingMu.Lock()
	pending := t.pending
	t.pending = make(map[string]chan callResult)
	t.pendingMu.Unlock()
	for _, result := range pending {
		result <- callResult{err: err}
	}
}
