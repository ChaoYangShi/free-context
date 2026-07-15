package daemon

import (
	"context"
	"net"
	"path/filepath"
	"testing"

	"github.com/ChaoYangShi/free-context/internal/orchestrator"
)

func TestStopOwnedAppServerAcceptsGoneProcessAndInactiveSocket(t *testing.T) {
	err := stopOwnedAppServer(context.Background(), orchestrator.Run{
		AppServerPID:    99999999,
		AppServerSocket: filepath.Join(t.TempDir(), "gone.sock"),
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestStopOwnedAppServerBlocksWhenSocketIsActiveButPIDIsGone(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "active.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	err = stopOwnedAppServer(context.Background(), orchestrator.Run{AppServerPID: 99999999, AppServerSocket: socket})
	if err == nil {
		t.Fatal("expected uncertain ownership to block recovery")
	}
}
