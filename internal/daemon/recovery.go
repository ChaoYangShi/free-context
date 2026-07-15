package daemon

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ChaoYangShi/free-context/internal/orchestrator"
)

func RecoverPersistedRuns(ctx context.Context, controller *Controller, engine *orchestrator.Engine, repository orchestrator.Repository) error {
	runs, err := repository.List(ctx)
	if err != nil {
		return err
	}
	for _, run := range runs {
		if run.Status != orchestrator.RunStarting && run.Status != orchestrator.RunActive && run.Status != orchestrator.RunTransitioning {
			continue
		}
		if run.RootThreadID == "" {
			_, _ = engine.Execute(ctx, orchestrator.BlockRun{RunID: run.ID, Reason: "daemon restarted before a root thread was registered"})
			continue
		}
		if err := stopOwnedAppServer(ctx, run); err != nil {
			_, _ = engine.Execute(ctx, orchestrator.BlockRun{RunID: run.ID, Reason: "automatic recovery blocked: " + err.Error()})
			continue
		}
		_ = controller.Recover(ctx, run.ID)
	}
	return nil
}

func stopOwnedAppServer(ctx context.Context, run orchestrator.Run) error {
	if run.AppServerPID <= 0 || !filepath.IsAbs(run.AppServerSocket) {
		return errors.New("prior app-server ownership metadata is incomplete")
	}
	processPath := filepath.Join("/proc", strconv.Itoa(run.AppServerPID))
	commandLine, err := os.ReadFile(filepath.Join(processPath, "cmdline"))
	if errors.Is(err, os.ErrNotExist) {
		if socketAccepting(run.AppServerSocket) {
			return errors.New("prior app-server socket is active but its recorded process is gone")
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect prior app-server: %w", err)
	}
	command := strings.ReplaceAll(string(commandLine), "\x00", " ")
	if !strings.Contains(command, "app-server") || !strings.Contains(command, run.AppServerSocket) {
		return errors.New("recorded pid does not own the recorded app-server socket")
	}
	if err := syscall.Kill(-run.AppServerPID, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("stop prior app-server: %w", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(processPath); errors.Is(err, os.ErrNotExist) {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
	if _, err := os.Stat(processPath); err == nil {
		if err := syscall.Kill(-run.AppServerPID, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			return fmt.Errorf("kill prior app-server: %w", err)
		}
	}
	if socketAccepting(run.AppServerSocket) {
		return errors.New("prior app-server socket remained active after its process group was stopped")
	}
	return nil
}

func socketAccepting(path string) bool {
	connection, err := net.DialTimeout("unix", path, 100*time.Millisecond)
	if err != nil {
		return false
	}
	_ = connection.Close()
	return true
}
