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
	var completedCleanupError error
	for _, run := range runs {
		if run.Status == orchestrator.RunComplete {
			if err := stopOwnedAppServer(ctx, run); err != nil {
				completedCleanupError = errors.Join(completedCleanupError, fmt.Errorf("stop completed run %s app-server: %w", run.ID, err))
				continue
			}
			if err := repository.Delete(ctx, run.ID); err != nil {
				completedCleanupError = errors.Join(completedCleanupError, fmt.Errorf("delete completed run %s: %w", run.ID, err))
			}
			continue
		}
		if !runRecoverableOnDaemonStart(run) {
			continue
		}
		if run.RootThreadID == "" {
			_, _ = engine.Execute(ctx, orchestrator.BlockRun{RunID: run.ID, Reason: "daemon restarted before a root thread was registered"})
			continue
		}
		_ = controller.Recover(ctx, run.ID)
	}
	return completedCleanupError
}

func runRecoverableOnDaemonStart(run orchestrator.Run) bool {
	if run.Status == orchestrator.RunStarting || run.Status == orchestrator.RunActive || run.Status == orchestrator.RunTransitioning {
		return true
	}
	if run.Status != orchestrator.RunBlocked {
		return false
	}
	return strings.HasPrefix(run.BlockedReason, "automatic recovery failed:") ||
		strings.HasPrefix(run.BlockedReason, "automatic recovery blocked:")
}

func refreshRecoverableThreadMetadata(ctx context.Context, engine *orchestrator.Engine, repository orchestrator.Repository, run orchestrator.Run) error {
	for _, thread := range run.Threads {
		if invalidTokenCapacity(thread.TokenCapacity) {
			if _, err := engine.Execute(ctx, orchestrator.ClearTokenCapacity{
				RunID:    run.ID,
				ThreadID: thread.ID,
			}); err != nil {
				return err
			}
		}
		if strings.TrimSpace(thread.CurrentTurnID) == "" {
			turnID, ok, err := latestTranscriptTurnID(thread.TranscriptPath)
			if err != nil {
				return err
			}
			if ok {
				if _, err := engine.Execute(ctx, orchestrator.ThreadTurnStarted{
					RunID:    run.ID,
					ThreadID: thread.ID,
					TurnID:   turnID,
				}); err != nil {
					return err
				}
			}
		}
		if strings.TrimSpace(thread.Model) != "" {
			continue
		}
		model, ok, err := latestTranscriptModel(thread.TranscriptPath)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		if _, err := engine.Execute(ctx, orchestrator.UpdateThreadMetadata{
			RunID:    run.ID,
			ThreadID: thread.ID,
			Model:    model,
		}); err != nil {
			return err
		}
	}
	return nil
}

func invalidTokenCapacity(snapshot *orchestrator.TokenCapacitySnapshot) bool {
	return snapshot != nil && snapshot.ModelContextWindow > 0 && snapshot.TotalTokens > snapshot.ModelContextWindow
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
		killDeadline := time.Now().Add(time.Second)
		for time.Now().Before(killDeadline) {
			if _, err := os.Stat(processPath); errors.Is(err, os.ErrNotExist) {
				break
			}
			time.Sleep(10 * time.Millisecond)
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
