package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const TransitionTimeout = 30 * time.Minute

var (
	ErrUnsupportedCommand = errors.New("unsupported orchestration command")
	ErrWorkspaceActive    = errors.New("workspace already has an active run")
)

type Repository interface {
	Create(context.Context, Run) error
	Save(context.Context, Run) error
	Load(context.Context, string) (Run, error)
	List(context.Context) ([]Run, error)
	Delete(context.Context, string) error
	SaveHandoff(context.Context, Handoff) error
}

type Engine struct {
	repository Repository
	now        func() time.Time
	newID      func() string
}

func New(repository Repository, now func() time.Time, newID func() string) *Engine {
	return &Engine{repository: repository, now: now, newID: newID}
}

func (e *Engine) Execute(ctx context.Context, command any) (Outcome, error) {
	switch command := command.(type) {
	case StartRun:
		return e.startRun(ctx, command)
	case RegisterThread:
		return e.registerThread(ctx, command)
	case ReportProgress:
		return e.reportProgress(ctx, command)
	case BeginCompaction:
		return e.beginCompaction(ctx, command)
	case RecordHandoff:
		return e.recordHandoff(ctx, command)
	case ThreadStopped:
		return e.threadStopped(ctx, command)
	case AcceptHandoff:
		return e.acceptHandoff(ctx, command)
	case ResolveHandoff:
		return e.resolveHandoff(ctx, command)
	case TreeQuiesced:
		return e.treeQuiesced(ctx, command)
	case RegisterReplacementRoot:
		return e.registerReplacementRoot(ctx, command)
	case CompleteRootTransfer:
		return e.completeRootTransfer(ctx, command)
	case TurnEnded:
		return e.turnEnded(ctx, command)
	case ThreadTurnStarted:
		return e.threadTurnStarted(ctx, command)
	case UpdateThreadMetadata:
		return e.updateThreadMetadata(ctx, command)
	case BlockRun:
		return e.blockRun(ctx, command)
	case StopRun:
		return e.stopRun(ctx, command)
	case RegisterAppServer:
		return e.registerAppServer(ctx, command)
	case RecoverRun:
		return e.recoverRun(ctx, command)
	case ToolStarted:
		return e.toolStarted(ctx, command)
	case ToolEnded:
		return e.toolEnded(ctx, command)
	default:
		return Outcome{}, fmt.Errorf("%w: %T", ErrUnsupportedCommand, command)
	}
}

func (e *Engine) toolStarted(ctx context.Context, command ToolStarted) (Outcome, error) {
	run, thread, err := e.activeThread(ctx, command.RunID, command.ThreadID)
	if err != nil {
		return Outcome{}, err
	}
	if strings.TrimSpace(command.ToolUseID) == "" {
		return Outcome{}, errors.New("tool use id is required")
	}
	for _, id := range thread.InFlightToolIDs {
		if id == command.ToolUseID {
			return Outcome{}, fmt.Errorf("tool use %s is already in flight", id)
		}
	}
	thread.InFlightToolIDs = append(thread.InFlightToolIDs, command.ToolUseID)
	run.Threads[thread.ID] = thread
	return e.save(ctx, run, nil)
}

func (e *Engine) toolEnded(ctx context.Context, command ToolEnded) (Outcome, error) {
	run, err := e.repository.Load(ctx, command.RunID)
	if err != nil {
		return Outcome{}, fmt.Errorf("load run: %w", err)
	}
	thread, exists := run.Threads[command.ThreadID]
	if !exists {
		return Outcome{}, fmt.Errorf("thread %s is not registered", command.ThreadID)
	}
	found := false
	remaining := make([]string, 0, len(thread.InFlightToolIDs))
	for _, id := range thread.InFlightToolIDs {
		if id == command.ToolUseID {
			found = true
			continue
		}
		remaining = append(remaining, id)
	}
	if !found {
		return Outcome{}, fmt.Errorf("tool use %s is not in flight", command.ToolUseID)
	}
	thread.InFlightToolIDs = remaining
	run.Threads[thread.ID] = thread
	effects := []Effect{}
	if run.Status == RunTransitioning && run.Transition.Phase == PhaseQuiescing && !hasInFlightTools(run) {
		effects = append(effects, Effect{Kind: EffectQuiesceTree, ThreadID: run.Transition.OldRootID})
	}
	return e.save(ctx, run, effects)
}

func hasInFlightTools(run Run) bool {
	for _, thread := range run.Threads {
		if len(thread.InFlightToolIDs) != 0 {
			return true
		}
	}
	return false
}

func (e *Engine) registerAppServer(ctx context.Context, command RegisterAppServer) (Outcome, error) {
	run, err := e.repository.Load(ctx, command.RunID)
	if err != nil {
		return Outcome{}, fmt.Errorf("load run: %w", err)
	}
	if command.PID <= 0 || !filepath.IsAbs(command.Socket) {
		return Outcome{}, errors.New("app-server pid and absolute socket path are required")
	}
	run.AppServerPID = command.PID
	run.AppServerSocket = filepath.Clean(command.Socket)
	return e.save(ctx, run, nil)
}

func (e *Engine) recoverRun(ctx context.Context, command RecoverRun) (Outcome, error) {
	run, err := e.repository.Load(ctx, command.RunID)
	if err != nil {
		return Outcome{}, fmt.Errorf("load run: %w", err)
	}
	root, exists := run.Threads[run.RootThreadID]
	if !exists || root.Role != RoleRoot || strings.TrimSpace(root.TranscriptPath) == "" || strings.TrimSpace(root.Model) == "" {
		return Outcome{}, errors.New("run cannot recover without a registered root transcript and model")
	}
	now := e.now().UTC()
	for id, record := range run.Handoffs {
		record.Status = HandoffResolved
		record.Resolution = ResolutionReplanned
		record.ResolvedAt = &now
		run.Handoffs[id] = record
	}
	workerIDs := make([]string, 0)
	for id, thread := range run.Threads {
		if id == run.RootThreadID {
			thread.Status = ThreadCheckpointing
			run.Threads[id] = thread
			continue
		}
		if thread.Role == RoleRoot {
			thread.Status = ThreadRetired
			run.Threads[id] = thread
			continue
		}
		if thread.Status == ThreadCompleted || thread.Status == ThreadRetired || thread.Status == ThreadBlocked || thread.Status == ThreadFailed {
			continue
		}
		if strings.TrimSpace(thread.TranscriptPath) == "" || strings.TrimSpace(thread.Model) == "" {
			return Outcome{}, fmt.Errorf("worker %s cannot recover without transcript and model", id)
		}
		thread.Status = ThreadCheckpointing
		run.Threads[id] = thread
		workerIDs = append(workerIDs, id)
	}
	sort.Strings(workerIDs)
	run.Status = RunTransitioning
	run.Recovering = true
	run.BlockedReason = ""
	run.Transition = Transition{Phase: PhaseGenerating, OldRootID: run.RootThreadID, StartedAt: now, Deadline: now.Add(TransitionTimeout)}
	effects := make([]Effect, 0, len(workerIDs)+1)
	for _, id := range workerIDs {
		effects = append(effects, Effect{Kind: EffectGenerateHandoff, ThreadID: id})
	}
	effects = append(effects, Effect{Kind: EffectGenerateHandoff, ThreadID: run.RootThreadID})
	return e.save(ctx, run, effects)
}

func (e *Engine) updateThreadMetadata(ctx context.Context, command UpdateThreadMetadata) (Outcome, error) {
	run, err := e.repository.Load(ctx, command.RunID)
	if err != nil {
		return Outcome{}, fmt.Errorf("load run: %w", err)
	}
	thread, exists := run.Threads[command.ThreadID]
	if !exists {
		return Outcome{}, fmt.Errorf("thread %s is not registered", command.ThreadID)
	}
	if strings.TrimSpace(command.Model) == "" {
		return Outcome{}, errors.New("model is required")
	}
	thread.Model = command.Model
	run.Threads[thread.ID] = thread
	return e.save(ctx, run, nil)
}

func (e *Engine) threadTurnStarted(ctx context.Context, command ThreadTurnStarted) (Outcome, error) {
	run, err := e.repository.Load(ctx, command.RunID)
	if err != nil {
		return Outcome{}, fmt.Errorf("load run: %w", err)
	}
	thread, exists := run.Threads[command.ThreadID]
	if !exists {
		return Outcome{}, fmt.Errorf("thread %s is not registered", command.ThreadID)
	}
	if command.TurnID == "" {
		return Outcome{}, errors.New("turn id is required")
	}
	thread.CurrentTurnID = command.TurnID
	run.Threads[thread.ID] = thread
	return e.save(ctx, run, nil)
}

func (e *Engine) blockRun(ctx context.Context, command BlockRun) (Outcome, error) {
	run, err := e.repository.Load(ctx, command.RunID)
	if err != nil {
		return Outcome{}, fmt.Errorf("load run: %w", err)
	}
	if strings.TrimSpace(command.Reason) == "" {
		return Outcome{}, errors.New("block reason is required")
	}
	return e.block(ctx, run, command.Reason)
}

func (e *Engine) stopRun(ctx context.Context, command StopRun) (Outcome, error) {
	run, err := e.repository.Load(ctx, command.RunID)
	if err != nil {
		return Outcome{}, fmt.Errorf("load run: %w", err)
	}
	if run.Status == RunComplete || run.Status == RunStopped {
		return Outcome{}, fmt.Errorf("run %s is already terminal", run.ID)
	}
	for id, thread := range run.Threads {
		if thread.Status == ThreadActive || thread.Status == ThreadCheckpointing || thread.Status == ThreadAwaitingParent || thread.Status == ThreadValidating {
			thread.Status = ThreadRetired
			run.Threads[id] = thread
		}
	}
	run.Status = RunStopped
	run.BlockedReason = "stopped by user"
	return e.save(ctx, run, []Effect{{Kind: EffectStopAppServer}})
}

func (e *Engine) beginCompaction(ctx context.Context, command BeginCompaction) (Outcome, error) {
	run, thread, err := e.activeThread(ctx, command.RunID, command.ThreadID)
	if err != nil {
		return Outcome{}, err
	}
	if command.Trigger != "auto" && command.Trigger != "manual" {
		return Outcome{}, fmt.Errorf("unknown compaction trigger %q", command.Trigger)
	}
	if thread.Role == RoleRoot {
		now := e.now().UTC()
		thread.Status = ThreadCheckpointing
		thread.CurrentTurnID = command.TurnID
		run.Threads[thread.ID] = thread
		run.Status = RunTransitioning
		run.Transition = Transition{
			Phase:     PhaseQuiescing,
			OldRootID: thread.ID,
			StartedAt: now,
			Deadline:  now.Add(TransitionTimeout),
		}
		return e.save(ctx, run, []Effect{{Kind: EffectQuiesceTree, ThreadID: thread.ID}})
	}
	thread.Status = ThreadCheckpointing
	thread.CurrentTurnID = command.TurnID
	thread.TransitionDeadline = e.now().UTC().Add(TransitionTimeout)
	run.Threads[thread.ID] = thread
	return e.save(ctx, run, []Effect{{
		Kind:     EffectGenerateHandoff,
		ThreadID: thread.ID,
	}})
}

func (e *Engine) treeQuiesced(ctx context.Context, command TreeQuiesced) (Outcome, error) {
	run, err := e.repository.Load(ctx, command.RunID)
	if err != nil {
		return Outcome{}, fmt.Errorf("load run: %w", err)
	}
	if run.Status != RunTransitioning || run.Transition.Phase != PhaseQuiescing {
		return Outcome{}, errors.New("run is not quiescing")
	}
	workerIDs := make([]string, 0)
	for id, thread := range run.Threads {
		if id == run.Transition.OldRootID || thread.Status == ThreadRetired || thread.Status == ThreadCompleted || thread.Status == ThreadBlocked || thread.Status == ThreadFailed {
			continue
		}
		thread.Status = ThreadCheckpointing
		run.Threads[id] = thread
		workerIDs = append(workerIDs, id)
	}
	sort.Strings(workerIDs)
	effects := make([]Effect, 0, len(workerIDs)+1)
	for _, id := range workerIDs {
		effects = append(effects, Effect{Kind: EffectGenerateHandoff, ThreadID: id})
	}
	effects = append(effects, Effect{Kind: EffectGenerateHandoff, ThreadID: run.Transition.OldRootID})
	run.Transition.Phase = PhaseGenerating
	run.Transition.Deadline = e.now().UTC().Add(TransitionTimeout)
	return e.save(ctx, run, effects)
}

func (e *Engine) recordHandoff(ctx context.Context, command RecordHandoff) (Outcome, error) {
	run, err := e.repository.Load(ctx, command.RunID)
	if err != nil {
		return Outcome{}, fmt.Errorf("load run: %w", err)
	}
	handoff := command.Handoff
	if err := validateHandoff(run, handoff); err != nil {
		return Outcome{}, err
	}
	if _, exists := run.Handoffs[handoff.ID]; exists {
		return Outcome{}, fmt.Errorf("handoff %s already exists", handoff.ID)
	}
	thread, exists := run.Threads[handoff.SourceThreadID]
	if !exists {
		return Outcome{}, fmt.Errorf("source thread %s is not registered", handoff.SourceThreadID)
	}
	if thread.Status != ThreadCheckpointing {
		return Outcome{}, fmt.Errorf("source thread %s is not checkpointing", thread.ID)
	}
	if handoff.Scope == HandoffTree {
		if run.Status != RunTransitioning || run.Transition.Phase != PhaseGenerating || thread.ID != run.Transition.OldRootID {
			return Outcome{}, errors.New("tree handoff does not match the active root transition")
		}
		for _, childID := range handoff.ChildHandoffIDs {
			child, exists := run.Handoffs[childID]
			if !exists || child.Status != HandoffReady {
				return Outcome{}, fmt.Errorf("child handoff %s is not ready", childID)
			}
		}
	}
	if err := e.repository.SaveHandoff(ctx, handoff); err != nil {
		return Outcome{}, fmt.Errorf("persist handoff: %w", err)
	}
	run.Handoffs[handoff.ID] = HandoffRecord{
		ID:             handoff.ID,
		SourceThreadID: handoff.SourceThreadID,
		Status:         HandoffReady,
	}
	thread.Status = ThreadAwaitingParent
	thread.TransitionDeadline = e.now().UTC().Add(TransitionTimeout)
	run.Threads[thread.ID] = thread

	effects := []Effect{{Kind: EffectStopThread, ThreadID: thread.ID}}
	if handoff.Scope == HandoffTree {
		run.Transition.Phase = PhaseAwaitingRoot
		run.Transition.Deadline = e.now().UTC().Add(TransitionTimeout)
		run.Transition.TreeHandoffID = handoff.ID
		effects = []Effect{{Kind: EffectStartRootReadOnly, ThreadID: thread.ID, HandoffID: handoff.ID}}
	} else if thread.Role == RoleWorker && run.Status == RunActive {
		effects = append(effects, Effect{
			Kind:      EffectSteerParent,
			ThreadID:  thread.ParentThreadID,
			HandoffID: handoff.ID,
		})
	}
	return e.save(ctx, run, effects)
}

func (e *Engine) threadStopped(ctx context.Context, command ThreadStopped) (Outcome, error) {
	run, err := e.repository.Load(ctx, command.RunID)
	if err != nil {
		return Outcome{}, fmt.Errorf("load run: %w", err)
	}
	thread, exists := run.Threads[command.ThreadID]
	if !exists {
		return Outcome{}, fmt.Errorf("thread %s is not registered", command.ThreadID)
	}
	if thread.Status != ThreadAwaitingParent && thread.Status != ThreadCheckpointing {
		return Outcome{}, fmt.Errorf("thread %s cannot retire from %s", command.ThreadID, thread.Status)
	}
	thread.Status = ThreadRetired
	run.Threads[thread.ID] = thread
	return e.save(ctx, run, nil)
}

func (e *Engine) registerReplacementRoot(ctx context.Context, command RegisterReplacementRoot) (Outcome, error) {
	run, err := e.repository.Load(ctx, command.RunID)
	if err != nil {
		return Outcome{}, fmt.Errorf("load run: %w", err)
	}
	if run.Status != RunTransitioning || run.Transition.Phase != PhaseAwaitingRoot {
		return Outcome{}, errors.New("run is not awaiting a replacement root")
	}
	if command.ThreadID == "" || command.ThreadID == run.Transition.OldRootID {
		return Outcome{}, errors.New("replacement root id is invalid")
	}
	if run.Transition.NewRootID != "" {
		return Outcome{}, errors.New("replacement root is already registered")
	}
	run.Threads[command.ThreadID] = Thread{
		ID:               command.ThreadID,
		Role:             RoleRoot,
		AssignedTask:     run.Objective,
		Model:            command.Model,
		TranscriptPath:   command.TranscriptPath,
		CurrentTurnID:    command.TurnID,
		Status:           ThreadValidating,
		InFlightToolIDs:  []string{},
		AcceptedHandoffs: []string{},
		Progress: Progress{
			Status:         ProgressActive,
			CompletedWork:  []string{},
			InProgressWork: []string{"validating tree handoff"},
			NextAction:     "accept the tree handoff",
			Blockers:       []string{},
			Artifacts:      []string{},
			UpdatedAt:      e.now().UTC(),
		},
	}
	run.Transition.NewRootID = command.ThreadID
	return e.save(ctx, run, nil)
}

func (e *Engine) acceptHandoff(ctx context.Context, command AcceptHandoff) (Outcome, error) {
	run, err := e.repository.Load(ctx, command.RunID)
	if err != nil {
		return Outcome{}, fmt.Errorf("load run: %w", err)
	}
	thread, exists := run.Threads[command.ThreadID]
	if !exists {
		return Outcome{}, fmt.Errorf("thread %s is not registered", command.ThreadID)
	}
	record, exists := run.Handoffs[command.HandoffID]
	if !exists {
		return Outcome{}, fmt.Errorf("handoff %s does not exist", command.HandoffID)
	}
	if record.Status != HandoffReady {
		return Outcome{}, fmt.Errorf("handoff %s is %s", command.HandoffID, record.Status)
	}
	source := run.Threads[record.SourceThreadID]
	if record.ID == run.Transition.TreeHandoffID {
		if run.Status != RunTransitioning || run.Transition.Phase != PhaseAwaitingRoot || run.Transition.NewRootID != thread.ID || thread.Status != ThreadValidating {
			return Outcome{}, errors.New("tree handoff must be accepted by the validating replacement root")
		}
	} else {
		if run.Status != RunActive || thread.Status != ThreadActive {
			return Outcome{}, errors.New("handoff owner is not active")
		}
		if source.Role == RoleWorker && source.ParentThreadID != thread.ID {
			return Outcome{}, errors.New("worker handoff must be accepted by its parent")
		}
	}
	now := e.now().UTC()
	record.OwnerThreadID = thread.ID
	record.Status = HandoffAccepted
	record.AcceptedAt = &now
	run.Handoffs[record.ID] = record
	source.TransitionDeadline = now.Add(TransitionTimeout)
	run.Threads[source.ID] = source
	thread.AcceptedHandoffs = append(thread.AcceptedHandoffs, record.ID)
	run.Threads[thread.ID] = thread
	if record.ID == run.Transition.TreeHandoffID {
		run.Transition.Phase = PhaseRetiringOldTree
		run.Transition.Deadline = now.Add(TransitionTimeout)
		return e.save(ctx, run, []Effect{{
			Kind:      EffectRetireTree,
			ThreadID:  run.Transition.OldRootID,
			HandoffID: record.ID,
		}})
	}
	return e.save(ctx, run, nil)
}

func (e *Engine) completeRootTransfer(ctx context.Context, command CompleteRootTransfer) (Outcome, error) {
	run, err := e.repository.Load(ctx, command.RunID)
	if err != nil {
		return Outcome{}, fmt.Errorf("load run: %w", err)
	}
	if run.Status != RunTransitioning || run.Transition.Phase != PhaseRetiringOldTree {
		return Outcome{}, errors.New("run is not retiring its old tree")
	}
	newRootID := run.Transition.NewRootID
	newRoot, exists := run.Threads[newRootID]
	if !exists || newRoot.Status != ThreadValidating {
		return Outcome{}, errors.New("replacement root is not validating")
	}
	for id, thread := range run.Threads {
		if id == newRootID {
			continue
		}
		thread.Status = ThreadRetired
		thread.TransitionDeadline = time.Time{}
		run.Threads[id] = thread
	}
	newRoot.Status = ThreadActive
	run.Threads[newRootID] = newRoot
	run.RootThreadID = newRootID
	run.Status = RunActive
	run.Recovering = false
	run.Transition = Transition{}
	return e.save(ctx, run, []Effect{{Kind: EffectGrantSandbox, ThreadID: newRootID}})
}

func (e *Engine) turnEnded(ctx context.Context, command TurnEnded) (Outcome, error) {
	run, err := e.repository.Load(ctx, command.RunID)
	if err != nil {
		return Outcome{}, fmt.Errorf("load run: %w", err)
	}
	thread, exists := run.Threads[command.ThreadID]
	if !exists {
		return Outcome{}, fmt.Errorf("thread %s is not registered", command.ThreadID)
	}
	if thread.Status != ThreadActive {
		return Outcome{}, fmt.Errorf("thread %s is not active", command.ThreadID)
	}
	if thread.Role == RoleWorker {
		switch thread.Progress.Status {
		case ProgressCompleted:
			thread.Status = ThreadCompleted
		case ProgressBlocked:
			thread.Status = ThreadBlocked
		default:
			thread.Status = ThreadFailed
		}
		run.Threads[thread.ID] = thread
		return e.save(ctx, run, nil)
	}

	switch thread.Progress.Status {
	case ProgressActive:
		if strings.TrimSpace(thread.Progress.NextAction) == "" {
			return e.block(ctx, run, "root turn ended without next_action")
		}
		return e.save(ctx, run, []Effect{{Kind: EffectStartNextTurn, ThreadID: thread.ID, Prompt: thread.Progress.NextAction}})
	case ProgressBlocked:
		reason := strings.Join(thread.Progress.Blockers, "; ")
		if reason == "" {
			reason = "root reported blocked without a reason"
		}
		return e.block(ctx, run, reason)
	case ProgressCompleted:
		for id, candidate := range run.Threads {
			if id == thread.ID {
				continue
			}
			if candidate.Status == ThreadActive || candidate.Status == ThreadCheckpointing || candidate.Status == ThreadAwaitingParent || candidate.Status == ThreadValidating {
				return e.block(ctx, run, "root reported completion while agent threads are active")
			}
		}
		for _, handoff := range run.Handoffs {
			if handoff.Status != HandoffResolved {
				return e.block(ctx, run, "root reported completion with unresolved handoffs")
			}
		}
		run.Status = RunComplete
		return e.save(ctx, run, []Effect{{Kind: EffectStopAppServer, ThreadID: thread.ID}})
	default:
		return e.block(ctx, run, "root turn ended without a valid progress status")
	}
}

func (e *Engine) block(ctx context.Context, run Run, reason string) (Outcome, error) {
	run.Status = RunBlocked
	run.BlockedReason = reason
	return e.save(ctx, run, []Effect{{Kind: EffectBlockTree, ThreadID: run.RootThreadID}})
}

func (e *Engine) resolveHandoff(ctx context.Context, command ResolveHandoff) (Outcome, error) {
	run, err := e.repository.Load(ctx, command.RunID)
	if err != nil {
		return Outcome{}, fmt.Errorf("load run: %w", err)
	}
	record, exists := run.Handoffs[command.HandoffID]
	if !exists {
		return Outcome{}, fmt.Errorf("handoff %s does not exist", command.HandoffID)
	}
	if record.Status != HandoffAccepted || record.OwnerThreadID != command.ThreadID {
		return Outcome{}, errors.New("handoff can only be resolved by its owner")
	}
	if command.Resolution != ResolutionContinued && command.Resolution != ResolutionReplanned && command.Resolution != ResolutionCompleted {
		return Outcome{}, fmt.Errorf("unknown handoff resolution %q", command.Resolution)
	}
	now := e.now().UTC()
	record.Status = HandoffResolved
	record.Resolution = command.Resolution
	record.ResolvedAt = &now
	run.Handoffs[record.ID] = record
	source := run.Threads[record.SourceThreadID]
	source.TransitionDeadline = time.Time{}
	run.Threads[source.ID] = source
	return e.save(ctx, run, nil)
}

func (e *Engine) activeThread(ctx context.Context, runID, threadID string) (Run, Thread, error) {
	run, err := e.repository.Load(ctx, runID)
	if err != nil {
		return Run{}, Thread{}, fmt.Errorf("load run: %w", err)
	}
	if run.Status != RunActive {
		return Run{}, Thread{}, fmt.Errorf("run %s is not active", run.ID)
	}
	thread, exists := run.Threads[threadID]
	if !exists {
		return Run{}, Thread{}, fmt.Errorf("thread %s is not registered", threadID)
	}
	if thread.Status != ThreadActive {
		return Run{}, Thread{}, fmt.Errorf("thread %s is not active", threadID)
	}
	return run, thread, nil
}

func validateHandoff(run Run, handoff Handoff) error {
	if handoff.ID == "" || handoff.RunID == "" || handoff.SourceThreadID == "" {
		return errors.New("handoff identity fields are required")
	}
	if handoff.RunID != run.ID {
		return errors.New("handoff run does not match")
	}
	if handoff.WorkspacePath != run.WorkspacePath {
		return errors.New("handoff workspace does not match")
	}
	if handoff.Objective != run.Objective {
		return errors.New("handoff objective does not match")
	}
	if handoff.Scope != HandoffAgent && handoff.Scope != HandoffTree {
		return errors.New("handoff scope is invalid")
	}
	if handoff.CreatedAt.IsZero() || handoff.SourceSessionID == "" || handoff.SourceTurnID == "" || handoff.AssignedTask == "" || handoff.Model == "" {
		return errors.New("handoff source fields are required")
	}
	if len(handoff.CompletionCriteria) == 0 || handoff.Constraints == nil || handoff.Decisions == nil || handoff.CompletedWork == nil || handoff.InProgressWork == nil || handoff.Blockers == nil || handoff.ArtifactReferences == nil || handoff.SuggestedSkills == nil || handoff.ChildHandoffIDs == nil {
		return errors.New("all handoff arrays are required")
	}
	return nil
}

func (e *Engine) registerThread(ctx context.Context, command RegisterThread) (Outcome, error) {
	run, err := e.repository.Load(ctx, command.RunID)
	if err != nil {
		return Outcome{}, fmt.Errorf("load run: %w", err)
	}
	if command.ThreadID == "" {
		return Outcome{}, errors.New("thread id is required")
	}
	if _, exists := run.Threads[command.ThreadID]; exists {
		return Outcome{}, fmt.Errorf("thread %s is already registered", command.ThreadID)
	}
	if command.Role == RoleRoot {
		if command.ParentThreadID != "" {
			return Outcome{}, errors.New("root thread cannot have a parent")
		}
		if run.RootThreadID != "" {
			return Outcome{}, errors.New("run already has a root thread")
		}
		run.RootThreadID = command.ThreadID
		run.Status = RunActive
	} else if command.Role == RoleWorker {
		parent, exists := run.Threads[command.ParentThreadID]
		if !exists {
			return Outcome{}, fmt.Errorf("parent thread %s is not registered", command.ParentThreadID)
		}
		if parent.Status != ThreadActive {
			return Outcome{}, fmt.Errorf("parent thread %s is not active", command.ParentThreadID)
		}
	} else {
		return Outcome{}, fmt.Errorf("unknown thread role %q", command.Role)
	}
	run.Threads[command.ThreadID] = Thread{
		ID:               command.ThreadID,
		ParentThreadID:   command.ParentThreadID,
		Role:             command.Role,
		AssignedTask:     command.AssignedTask,
		Model:            command.Model,
		TranscriptPath:   command.TranscriptPath,
		CurrentTurnID:    command.TurnID,
		Status:           ThreadActive,
		InFlightToolIDs:  []string{},
		AcceptedHandoffs: []string{},
		Progress: Progress{
			Status:         ProgressActive,
			CompletedWork:  []string{},
			InProgressWork: []string{},
			Blockers:       []string{},
			Artifacts:      []string{},
			UpdatedAt:      e.now().UTC(),
		},
	}
	return e.save(ctx, run, nil)
}

func (e *Engine) reportProgress(ctx context.Context, command ReportProgress) (Outcome, error) {
	run, err := e.repository.Load(ctx, command.RunID)
	if err != nil {
		return Outcome{}, fmt.Errorf("load run: %w", err)
	}
	thread, exists := run.Threads[command.ThreadID]
	if !exists {
		return Outcome{}, fmt.Errorf("thread %s is not registered", command.ThreadID)
	}
	if thread.Status != ThreadActive {
		return Outcome{}, fmt.Errorf("thread %s is not active", command.ThreadID)
	}
	if command.Status == ProgressActive && strings.TrimSpace(command.NextAction) == "" {
		return Outcome{}, errors.New("active progress requires next_action")
	}
	if command.Status == ProgressBlocked && len(command.Blockers) == 0 {
		return Outcome{}, errors.New("blocked progress requires blockers")
	}
	if command.Status == ProgressCompleted && (len(command.InProgressWork) != 0 || command.NextAction != "" || len(command.Blockers) != 0) {
		return Outcome{}, errors.New("completed progress cannot contain active work, next_action, or blockers")
	}
	thread.Progress = Progress{
		Status:         command.Status,
		CompletedWork:  append([]string(nil), command.CompletedWork...),
		InProgressWork: append([]string(nil), command.InProgressWork...),
		NextAction:     command.NextAction,
		Blockers:       append([]string(nil), command.Blockers...),
		Artifacts:      append([]string(nil), command.Artifacts...),
		UpdatedAt:      e.now().UTC(),
	}
	run.Threads[command.ThreadID] = thread
	return e.save(ctx, run, nil)
}

func (e *Engine) save(ctx context.Context, run Run, effects []Effect) (Outcome, error) {
	run.UpdatedAt = e.now().UTC()
	run.Revision++
	if err := e.repository.Save(ctx, run); err != nil {
		return Outcome{}, fmt.Errorf("persist run: %w", err)
	}
	return Outcome{Run: run, Effects: effects}, nil
}

func (e *Engine) startRun(ctx context.Context, command StartRun) (Outcome, error) {
	workspace, err := canonicalPath(command.WorkspacePath)
	if err != nil {
		return Outcome{}, fmt.Errorf("canonical workspace: %w", err)
	}
	if strings.TrimSpace(command.Objective) == "" {
		return Outcome{}, errors.New("objective is required")
	}
	if len(command.CompletionCriteria) == 0 {
		return Outcome{}, errors.New("at least one completion criterion is required")
	}
	for _, criterion := range command.CompletionCriteria {
		if strings.TrimSpace(criterion) == "" {
			return Outcome{}, errors.New("completion criteria cannot be blank")
		}
	}
	existingRuns, err := e.repository.List(ctx)
	if err != nil {
		return Outcome{}, fmt.Errorf("list runs: %w", err)
	}
	for _, existing := range existingRuns {
		if existing.WorkspacePath == workspace && runOccupiesWorkspace(existing.Status) {
			return Outcome{}, fmt.Errorf("%w: %s", ErrWorkspaceActive, existing.ID)
		}
	}

	now := e.now().UTC()
	run := Run{
		ID:                 e.newID(),
		CreatedAt:          now,
		UpdatedAt:          now,
		WorkspacePath:      workspace,
		Objective:          command.Objective,
		CompletionCriteria: append([]string(nil), command.CompletionCriteria...),
		Sandbox:            command.Sandbox,
		Status:             RunStarting,
		Threads:            make(map[string]Thread),
		Handoffs:           make(map[string]HandoffRecord),
		Revision:           1,
	}
	if err := e.repository.Create(ctx, run); err != nil {
		return Outcome{}, fmt.Errorf("persist run: %w", err)
	}

	return Outcome{
		Run:     run,
		Effects: []Effect{{Kind: EffectStartAppServer}},
	}, nil
}

func runOccupiesWorkspace(status RunStatus) bool {
	return status == RunStarting || status == RunActive || status == RunTransitioning
}

func canonicalPath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	return filepath.Clean(resolved), nil
}
