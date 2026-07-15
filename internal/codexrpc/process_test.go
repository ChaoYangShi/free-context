package codexrpc_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ChaoYangShi/free-context/internal/codexrpc"
)

func TestProcessIgnoresNotificationsAndReturnsResponse(t *testing.T) {
	notifications := make(chan string, 1)
	process, _, err := codexrpc.Launch(context.Background(), codexrpc.ProcessOptions{
		Binary:       os.Args[0],
		Args:         []string{"-test.run=TestAppServerHelper"},
		Env:          []string{"FREE_CONTEXT_APP_SERVER_HELPER=1"},
		Version:      "0.144.4",
		Notification: func(message json.RawMessage) { notifications <- string(message) },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer process.Close()
	response, err := process.Call(context.Background(), []byte(`{"jsonrpc":"2.0","id":7,"method":"ping","params":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(response) != `{"jsonrpc":"2.0","id":7,"result":{"ok":true}}` {
		t.Fatalf("unexpected response %s", response)
	}
	select {
	case notification := <-notifications:
		if !strings.Contains(notification, "thread/started") {
			t.Fatalf("unexpected notification %s", notification)
		}
	case <-time.After(time.Second):
		t.Fatal("notification was not delivered")
	}
}

func TestAppServerHelper(t *testing.T) {
	if os.Getenv("FREE_CONTEXT_APP_SERVER_HELPER") != "1" {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() {
		os.Exit(2)
	}
	fmt.Println(`{"jsonrpc":"2.0","method":"thread/started","params":{}}`)
	fmt.Println(`{"jsonrpc":"2.0","id":7,"result":{"ok":true}}`)
}
