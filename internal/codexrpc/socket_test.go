package codexrpc_test

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ChaoYangShi/free-context/internal/codexrpc"
	"github.com/gorilla/websocket"
)

func TestUnixWebSocketTransportRoutesResponsesAndNotifications(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "app-server.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		connection, err := (&websocket.Upgrader{}).Upgrade(response, request, nil)
		if err != nil {
			return
		}
		defer connection.Close()
		_, message, err := connection.ReadMessage()
		if err != nil {
			return
		}
		var envelope struct {
			ID json.RawMessage `json:"id"`
		}
		if json.Unmarshal(message, &envelope) != nil {
			return
		}
		_ = connection.WriteJSON(map[string]any{"jsonrpc": "2.0", "method": "thread/started", "params": map[string]any{}})
		_ = connection.WriteJSON(map[string]any{"jsonrpc": "2.0", "id": envelope.ID, "result": map[string]any{"ok": true}})
	})}
	go server.Serve(listener)
	defer server.Close()

	notifications := make(chan string, 1)
	transport, _, err := codexrpc.DialUnix(context.Background(), socket, "0.144.4", func(message json.RawMessage) {
		notifications <- string(message)
	})
	if err != nil {
		t.Fatal(err)
	}
	defer transport.Close()
	response, err := transport.Call(context.Background(), []byte(`{"jsonrpc":"2.0","id":9,"method":"probe","params":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(response), `"id":9`) {
		t.Fatalf("unexpected response %s", response)
	}
	select {
	case message := <-notifications:
		if !strings.Contains(message, "thread/started") {
			t.Fatalf("unexpected notification %s", message)
		}
	case <-time.After(time.Second):
		t.Fatal("notification not delivered")
	}
}
