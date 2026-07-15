package tui

import (
	"fmt"
	"sort"

	"github.com/ChaoYangShi/free-context/internal/orchestrator"
)

type Focus int

const (
	FocusRuns Focus = iota
	FocusThreads
)

type Pressure string

const (
	PressureUnknown  Pressure = "unknown"
	PressureNormal   Pressure = "normal"
	PressureWarning  Pressure = "warning"
	PressureCritical Pressure = "critical"
)

type CapacityView struct {
	SnapshotKnown bool
	WindowKnown   bool
	Used          int
	Window        int
	Remaining     int
	Percentage    int
	Pressure      Pressure
}

type ThreadRow struct {
	ID       string
	Role     orchestrator.ThreadRole
	Status   orchestrator.ThreadStatus
	Capacity CapacityView
	Thread   orchestrator.Thread
}

func sortedRuns(runs []orchestrator.Run) []orchestrator.Run {
	sorted := append([]orchestrator.Run(nil), runs...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].CreatedAt.Equal(sorted[j].CreatedAt) {
			return sorted[i].ID < sorted[j].ID
		}
		return sorted[i].CreatedAt.Before(sorted[j].CreatedAt)
	})
	return sorted
}

func activeTreeRows(run orchestrator.Run) []ThreadRow {
	rows := make([]ThreadRow, 0, len(run.Threads))
	if root, exists := run.Threads[run.RootThreadID]; exists {
		rows = append(rows, threadRow(root))
	}
	workerIDs := make([]string, 0)
	for id, thread := range run.Threads {
		if id == run.RootThreadID || thread.Role != orchestrator.RoleWorker || thread.Status == orchestrator.ThreadRetired {
			continue
		}
		workerIDs = append(workerIDs, id)
	}
	sort.Strings(workerIDs)
	for _, id := range workerIDs {
		rows = append(rows, threadRow(run.Threads[id]))
	}
	return rows
}

func threadRow(thread orchestrator.Thread) ThreadRow {
	return ThreadRow{
		ID:       thread.ID,
		Role:     thread.Role,
		Status:   thread.Status,
		Capacity: capacityView(thread.TokenCapacity),
		Thread:   thread,
	}
}

func capacityView(snapshot *orchestrator.TokenCapacitySnapshot) CapacityView {
	if snapshot == nil {
		return CapacityView{Pressure: PressureUnknown}
	}
	if snapshot.ModelContextWindow <= 0 {
		return CapacityView{
			SnapshotKnown: true,
			Used:          snapshot.TotalTokens,
			Pressure:      PressureUnknown,
		}
	}
	remaining := snapshot.ModelContextWindow - snapshot.TotalTokens
	if remaining < 0 {
		remaining = 0
	}
	percentage := snapshot.TotalTokens * 100 / snapshot.ModelContextWindow
	pressure := PressureNormal
	if percentage >= 90 {
		pressure = PressureCritical
	} else if percentage >= 70 {
		pressure = PressureWarning
	}
	return CapacityView{
		SnapshotKnown: true,
		WindowKnown:   true,
		Used:          snapshot.TotalTokens,
		Window:        snapshot.ModelContextWindow,
		Remaining:     remaining,
		Percentage:    percentage,
		Pressure:      pressure,
	}
}

func formatCapacity(capacity CapacityView) string {
	if !capacity.SnapshotKnown {
		return "unknown"
	}
	if !capacity.WindowKnown {
		return fmt.Sprintf("%s / unknown  unknown", compactNumber(capacity.Used))
	}
	return fmt.Sprintf("%d%%  %s / %s  %s", capacity.Percentage, compactNumber(capacity.Used), compactNumber(capacity.Window), capacity.Pressure)
}

func formatCapacityDetail(capacity CapacityView) string {
	if !capacity.SnapshotKnown {
		return "unknown"
	}
	if !capacity.WindowKnown {
		return fmt.Sprintf("%d / unknown, unknown remaining, unknown, unknown", capacity.Used)
	}
	return fmt.Sprintf("%d / %d, %d remaining, %d%%, %s", capacity.Used, capacity.Window, capacity.Remaining, capacity.Percentage, capacity.Pressure)
}

func compactNumber(value int) string {
	if value >= 1000 && value%1000 == 0 {
		return fmt.Sprintf("%dk", value/1000)
	}
	return fmt.Sprintf("%d", value)
}
