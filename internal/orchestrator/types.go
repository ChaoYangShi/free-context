package orchestrator

import "time"

type RunStatus string

const (
	RunStarting      RunStatus = "starting"
	RunActive        RunStatus = "active"
	RunTransitioning RunStatus = "transitioning"
	RunBlocked       RunStatus = "blocked"
	RunComplete      RunStatus = "completed"
	RunStopped       RunStatus = "stopped"
)

type ThreadRole string

const (
	RoleRoot   ThreadRole = "root"
	RoleWorker ThreadRole = "worker"
)

type ThreadStatus string

const (
	ThreadActive         ThreadStatus = "active"
	ThreadCheckpointing  ThreadStatus = "checkpointing"
	ThreadAwaitingParent ThreadStatus = "awaiting_parent"
	ThreadValidating     ThreadStatus = "validating"
	ThreadRetired        ThreadStatus = "retired"
	ThreadCompleted      ThreadStatus = "completed"
	ThreadBlocked        ThreadStatus = "blocked"
	ThreadFailed         ThreadStatus = "failed"
)

type TransitionPhase string

const (
	PhaseQuiescing       TransitionPhase = "quiescing"
	PhaseGenerating      TransitionPhase = "generating_handoffs"
	PhaseAwaitingRoot    TransitionPhase = "awaiting_root"
	PhaseRetiringOldTree TransitionPhase = "retiring_old_tree"
)

type Transition struct {
	Phase         TransitionPhase `json:"phase"`
	OldRootID     string          `json:"old_root_id"`
	NewRootID     string          `json:"new_root_id"`
	TreeHandoffID string          `json:"tree_handoff_id"`
	StartedAt     time.Time       `json:"started_at"`
	Deadline      time.Time       `json:"deadline"`
}

type ProgressStatus string

const (
	ProgressActive    ProgressStatus = "active"
	ProgressBlocked   ProgressStatus = "blocked"
	ProgressCompleted ProgressStatus = "completed"
)

type Run struct {
	ID                 string                   `json:"run_id"`
	CreatedAt          time.Time                `json:"created_at"`
	UpdatedAt          time.Time                `json:"updated_at"`
	WorkspacePath      string                   `json:"workspace_path"`
	Objective          string                   `json:"objective"`
	CompletionCriteria []string                 `json:"completion_criteria"`
	Sandbox            string                   `json:"sandbox"`
	AppServerPID       int                      `json:"app_server_pid"`
	AppServerSocket    string                   `json:"app_server_socket"`
	Recovering         bool                     `json:"recovering"`
	Status             RunStatus                `json:"status"`
	RootThreadID       string                   `json:"root_thread_id"`
	Threads            map[string]Thread        `json:"threads"`
	Handoffs           map[string]HandoffRecord `json:"handoffs"`
	BlockedReason      string                   `json:"blocked_reason"`
	Transition         Transition               `json:"transition"`
	Revision           uint64                   `json:"revision"`
}

type Thread struct {
	ID                 string                 `json:"thread_id"`
	ParentThreadID     string                 `json:"parent_thread_id"`
	Role               ThreadRole             `json:"role"`
	AssignedTask       string                 `json:"assigned_task"`
	Model              string                 `json:"model"`
	TranscriptPath     string                 `json:"transcript_path"`
	CurrentTurnID      string                 `json:"current_turn_id"`
	Status             ThreadStatus           `json:"status"`
	TransitionDeadline time.Time              `json:"transition_deadline"`
	InFlightToolIDs    []string               `json:"in_flight_tool_ids"`
	Progress           Progress               `json:"progress"`
	AcceptedHandoffs   []string               `json:"accepted_handoffs"`
	TokenCapacity      *TokenCapacitySnapshot `json:"token_capacity,omitempty"`
}

type Progress struct {
	Status         ProgressStatus `json:"status"`
	CompletedWork  []string       `json:"completed_work"`
	InProgressWork []string       `json:"in_progress_work"`
	NextAction     string         `json:"next_action"`
	Blockers       []string       `json:"blockers"`
	Artifacts      []string       `json:"artifact_references"`
	UpdatedAt      time.Time      `json:"updated_at"`
}

type TokenCapacitySnapshot struct {
	TurnID                string    `json:"turn_id"`
	TotalTokens           int       `json:"total_tokens"`
	InputTokens           int       `json:"input_tokens"`
	CachedInputTokens     int       `json:"cached_input_tokens"`
	OutputTokens          int       `json:"output_tokens"`
	ReasoningOutputTokens int       `json:"reasoning_output_tokens"`
	LastTotalTokens       int       `json:"last_total_tokens"`
	ModelContextWindow    int       `json:"model_context_window"`
	ObservedAt            time.Time `json:"observed_at"`
}

type HandoffScope string

const (
	HandoffAgent HandoffScope = "agent"
	HandoffTree  HandoffScope = "tree"
)

type Decision struct {
	Decision string `json:"decision"`
	Reason   string `json:"reason"`
}

type CompletedWork struct {
	Result   string   `json:"result"`
	Evidence []string `json:"evidence"`
}

type Handoff struct {
	ID                 string          `json:"handoff_id"`
	RunID              string          `json:"run_id"`
	CreatedAt          time.Time       `json:"created_at"`
	Scope              HandoffScope    `json:"scope"`
	SourceSessionID    string          `json:"source_session_id"`
	SourceTurnID       string          `json:"source_turn_id"`
	SourceThreadID     string          `json:"source_thread_id"`
	ParentThreadID     *string         `json:"parent_thread_id"`
	WorkspacePath      string          `json:"workspace_path"`
	AssignedTask       string          `json:"assigned_task"`
	Model              string          `json:"model"`
	Objective          string          `json:"objective"`
	CompletionCriteria []string        `json:"completion_criteria"`
	Constraints        []string        `json:"constraints"`
	Decisions          []Decision      `json:"decisions"`
	CompletedWork      []CompletedWork `json:"completed_work"`
	InProgressWork     []string        `json:"in_progress_work"`
	NextAction         string          `json:"next_action"`
	Blockers           []string        `json:"blockers"`
	ArtifactReferences []string        `json:"artifact_references"`
	SuggestedSkills    []string        `json:"suggested_skills"`
	ChildHandoffIDs    []string        `json:"child_handoff_ids"`
}

type HandoffStatus string

const (
	HandoffReady    HandoffStatus = "ready"
	HandoffAccepted HandoffStatus = "accepted"
	HandoffResolved HandoffStatus = "resolved"
)

type HandoffResolution string

const (
	ResolutionContinued HandoffResolution = "continued_by_owner"
	ResolutionReplanned HandoffResolution = "replanned"
	ResolutionCompleted HandoffResolution = "completed"
)

type HandoffRecord struct {
	ID             string            `json:"handoff_id"`
	SourceThreadID string            `json:"source_thread_id"`
	OwnerThreadID  string            `json:"owner_thread_id"`
	Status         HandoffStatus     `json:"status"`
	Resolution     HandoffResolution `json:"resolution"`
	AcceptedAt     *time.Time        `json:"accepted_at"`
	ResolvedAt     *time.Time        `json:"resolved_at"`
}

type EffectKind string

const (
	EffectStartAppServer    EffectKind = "start_app_server"
	EffectGenerateHandoff   EffectKind = "generate_handoff"
	EffectStopThread        EffectKind = "stop_thread"
	EffectSteerParent       EffectKind = "steer_parent"
	EffectQuiesceTree       EffectKind = "quiesce_tree"
	EffectStartRootReadOnly EffectKind = "start_root_read_only"
	EffectRetireTree        EffectKind = "retire_tree"
	EffectGrantSandbox      EffectKind = "grant_sandbox"
	EffectStartNextTurn     EffectKind = "start_next_turn"
	EffectBlockTree         EffectKind = "block_tree"
	EffectStopAppServer     EffectKind = "stop_app_server"
	EffectDeleteRun         EffectKind = "delete_run"
)

type Effect struct {
	Kind      EffectKind `json:"kind"`
	ThreadID  string     `json:"thread_id,omitempty"`
	HandoffID string     `json:"handoff_id,omitempty"`
	Prompt    string     `json:"prompt,omitempty"`
}

type Outcome struct {
	Run     Run      `json:"run"`
	Effects []Effect `json:"effects"`
}

type StartRun struct {
	WorkspacePath      string
	Objective          string
	CompletionCriteria []string
	Sandbox            string
}

type RegisterThread struct {
	RunID          string
	ThreadID       string
	ParentThreadID string
	Role           ThreadRole
	AssignedTask   string
	Model          string
	TranscriptPath string
	TurnID         string
}

type ReportProgress struct {
	RunID          string
	ThreadID       string
	Status         ProgressStatus
	CompletedWork  []string
	InProgressWork []string
	NextAction     string
	Blockers       []string
	Artifacts      []string
}

type BeginCompaction struct {
	RunID    string
	ThreadID string
	TurnID   string
	Trigger  string
}

type RecordHandoff struct {
	RunID   string
	Handoff Handoff
}

type ThreadStopped struct {
	RunID    string
	ThreadID string
}

type AcceptHandoff struct {
	RunID     string
	ThreadID  string
	HandoffID string
}

type ResolveHandoff struct {
	RunID      string
	ThreadID   string
	HandoffID  string
	Resolution HandoffResolution
}

type TreeQuiesced struct {
	RunID string
}

type RegisterReplacementRoot struct {
	RunID          string
	ThreadID       string
	Model          string
	TranscriptPath string
	TurnID         string
}

type CompleteRootTransfer struct {
	RunID string
}

type TurnEnded struct {
	RunID    string
	ThreadID string
}

type FinalizeReportedCompletion struct {
	RunID string
}

type ThreadTurnStarted struct {
	RunID    string
	ThreadID string
	TurnID   string
}

type UpdateThreadMetadata struct {
	RunID    string
	ThreadID string
	Model    string
}

type RecordTokenCapacity struct {
	RunID    string
	ThreadID string
	Snapshot TokenCapacitySnapshot
}

type BlockRun struct {
	RunID  string
	Reason string
}

type StopRun struct {
	RunID string
}

type ResumeRun struct {
	RunID string
}

type RegisterAppServer struct {
	RunID  string
	PID    int
	Socket string
}

type RecoverRun struct {
	RunID string
}

type ToolStarted struct {
	RunID     string
	ThreadID  string
	ToolUseID string
}

type ToolEnded struct {
	RunID     string
	ThreadID  string
	ToolUseID string
}
