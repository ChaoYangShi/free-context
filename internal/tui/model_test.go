package tui

import (
	"strings"
	"testing"

	"github.com/ChaoYangShi/free-context/internal/daemon"
	"github.com/ChaoYangShi/free-context/internal/orchestrator"
)

func TestModelViewShowsCurrentRootAndNonRetiredWorkers(t *testing.T) {
	t.Parallel()

	model := Model{
		width: 120,
		runs:  []orchestrator.Run{{ID: "run-1"}},
		state: daemon.RunState{Run: orchestrator.Run{
			ID:           "run-1",
			RootThreadID: "root-2",
			Threads: map[string]orchestrator.Thread{
				"root-1":          {ID: "root-1", Role: orchestrator.RoleRoot, Status: orchestrator.ThreadRetired},
				"root-2":          {ID: "root-2", Role: orchestrator.RoleRoot, Status: orchestrator.ThreadActive},
				"worker-active":   {ID: "worker-active", Role: orchestrator.RoleWorker, Status: orchestrator.ThreadActive},
				"worker-blocked":  {ID: "worker-blocked", Role: orchestrator.RoleWorker, Status: orchestrator.ThreadBlocked},
				"worker-retired":  {ID: "worker-retired", Role: orchestrator.RoleWorker, Status: orchestrator.ThreadRetired},
				"worker-complete": {ID: "worker-complete", Role: orchestrator.RoleWorker, Status: orchestrator.ThreadCompleted},
			},
		}},
	}

	view := model.View()
	for _, expected := range []string{"root-2", "worker-active", "worker-blocked", "worker-complete"} {
		if !strings.Contains(view, expected) {
			t.Fatalf("view does not contain %q:\n%s", expected, view)
		}
	}
	for _, excluded := range []string{"root-1", "worker-retired"} {
		if strings.Contains(view, excluded) {
			t.Fatalf("view contains retired thread %q:\n%s", excluded, view)
		}
	}
}

func TestModelViewShowsCapacityPressureAndUnknownWindow(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name     string
		snapshot *orchestrator.TokenCapacitySnapshot
		want     string
	}{
		{name: "no snapshot", snapshot: nil, want: "unknown"},
		{name: "unknown window", snapshot: &orchestrator.TokenCapacitySnapshot{TotalTokens: 164000}, want: "164k / unknown  unknown"},
		{name: "normal", snapshot: &orchestrator.TokenCapacitySnapshot{TotalTokens: 690, ModelContextWindow: 1000}, want: "69%"},
		{name: "warning", snapshot: &orchestrator.TokenCapacitySnapshot{TotalTokens: 700, ModelContextWindow: 1000}, want: "70%"},
		{name: "critical", snapshot: &orchestrator.TokenCapacitySnapshot{TotalTokens: 900, ModelContextWindow: 1000}, want: "90%"},
	} {
		t.Run(test.name, func(t *testing.T) {
			model := modelWithRootSnapshot(test.snapshot)
			view := model.View()
			if !strings.Contains(view, test.want) {
				t.Fatalf("view does not contain %q:\n%s", test.want, view)
			}
		})
	}
}

func TestModelRefreshKeepsSelectedRunAndStateTogether(t *testing.T) {
	t.Parallel()

	model := Model{
		selectedRunIndex: 1,
		runs:             []orchestrator.Run{{ID: "run-old"}, {ID: "run-selected"}},
		state:            daemon.RunState{Run: orchestrator.Run{ID: "run-selected"}},
	}
	updated, _ := model.Update(fetchMsg{
		runs:  []orchestrator.Run{{ID: "run-fallback"}, {ID: "run-other"}},
		state: daemon.RunState{Run: orchestrator.Run{ID: "run-fallback"}},
	})
	model = updated.(Model)

	if got := model.selectedRunID(); got != "run-fallback" {
		t.Fatalf("selected run = %q, want fetched state run", got)
	}
	if model.state.Run.ID != "run-fallback" {
		t.Fatalf("state run = %q, want run-fallback", model.state.Run.ID)
	}
}

func modelWithRootSnapshot(snapshot *orchestrator.TokenCapacitySnapshot) Model {
	return Model{
		width: 120,
		runs:  []orchestrator.Run{{ID: "run-1"}},
		state: daemon.RunState{Run: orchestrator.Run{
			ID:           "run-1",
			RootThreadID: "root-1",
			Threads: map[string]orchestrator.Thread{
				"root-1": {ID: "root-1", Role: orchestrator.RoleRoot, Status: orchestrator.ThreadActive, TokenCapacity: snapshot},
			},
		}},
	}
}
