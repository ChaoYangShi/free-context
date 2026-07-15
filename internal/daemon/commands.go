package daemon

import (
	"encoding/json"
	"fmt"

	"github.com/ChaoYangShi/free-context/internal/orchestrator"
)

type CommandKind string

const (
	CommandRegisterThread          CommandKind = "register_thread"
	CommandReportProgress          CommandKind = "report_progress"
	CommandBeginCompaction         CommandKind = "begin_compaction"
	CommandRecordHandoff           CommandKind = "record_handoff"
	CommandThreadStopped           CommandKind = "thread_stopped"
	CommandAcceptHandoff           CommandKind = "accept_handoff"
	CommandResolveHandoff          CommandKind = "resolve_handoff"
	CommandTreeQuiesced            CommandKind = "tree_quiesced"
	CommandRegisterReplacementRoot CommandKind = "register_replacement_root"
	CommandCompleteRootTransfer    CommandKind = "complete_root_transfer"
	CommandTurnEnded               CommandKind = "turn_ended"
	CommandFinalizeCompletion      CommandKind = "finalize_reported_completion"
	CommandBlockRun                CommandKind = "block_run"
	CommandThreadTurnStarted       CommandKind = "thread_turn_started"
	CommandStopRun                 CommandKind = "stop_run"
	CommandResumeRun               CommandKind = "resume_run"
	CommandUpdateThreadMetadata    CommandKind = "update_thread_metadata"
	CommandRegisterAppServer       CommandKind = "register_app_server"
	CommandRecoverRun              CommandKind = "recover_run"
	CommandToolStarted             CommandKind = "tool_started"
	CommandToolEnded               CommandKind = "tool_ended"
)

type commandEnvelope struct {
	Kind    CommandKind     `json:"kind"`
	Command json.RawMessage `json:"command"`
}

func decodeCommand(envelope commandEnvelope) (any, error) {
	var target any
	switch envelope.Kind {
	case CommandRegisterThread:
		target = &orchestrator.RegisterThread{}
	case CommandReportProgress:
		target = &orchestrator.ReportProgress{}
	case CommandBeginCompaction:
		target = &orchestrator.BeginCompaction{}
	case CommandRecordHandoff:
		target = &orchestrator.RecordHandoff{}
	case CommandThreadStopped:
		target = &orchestrator.ThreadStopped{}
	case CommandAcceptHandoff:
		target = &orchestrator.AcceptHandoff{}
	case CommandResolveHandoff:
		target = &orchestrator.ResolveHandoff{}
	case CommandTreeQuiesced:
		target = &orchestrator.TreeQuiesced{}
	case CommandRegisterReplacementRoot:
		target = &orchestrator.RegisterReplacementRoot{}
	case CommandCompleteRootTransfer:
		target = &orchestrator.CompleteRootTransfer{}
	case CommandTurnEnded:
		target = &orchestrator.TurnEnded{}
	case CommandFinalizeCompletion:
		target = &orchestrator.FinalizeReportedCompletion{}
	case CommandBlockRun:
		target = &orchestrator.BlockRun{}
	case CommandThreadTurnStarted:
		target = &orchestrator.ThreadTurnStarted{}
	case CommandStopRun:
		target = &orchestrator.StopRun{}
	case CommandResumeRun:
		target = &orchestrator.ResumeRun{}
	case CommandUpdateThreadMetadata:
		target = &orchestrator.UpdateThreadMetadata{}
	case CommandRegisterAppServer:
		target = &orchestrator.RegisterAppServer{}
	case CommandRecoverRun:
		target = &orchestrator.RecoverRun{}
	case CommandToolStarted:
		target = &orchestrator.ToolStarted{}
	case CommandToolEnded:
		target = &orchestrator.ToolEnded{}
	default:
		return nil, fmt.Errorf("unknown command kind %q", envelope.Kind)
	}
	if err := json.Unmarshal(envelope.Command, target); err != nil {
		return nil, fmt.Errorf("decode %s command: %w", envelope.Kind, err)
	}
	switch command := target.(type) {
	case *orchestrator.RegisterThread:
		return *command, nil
	case *orchestrator.ReportProgress:
		return *command, nil
	case *orchestrator.BeginCompaction:
		return *command, nil
	case *orchestrator.RecordHandoff:
		return *command, nil
	case *orchestrator.ThreadStopped:
		return *command, nil
	case *orchestrator.AcceptHandoff:
		return *command, nil
	case *orchestrator.ResolveHandoff:
		return *command, nil
	case *orchestrator.TreeQuiesced:
		return *command, nil
	case *orchestrator.RegisterReplacementRoot:
		return *command, nil
	case *orchestrator.CompleteRootTransfer:
		return *command, nil
	case *orchestrator.TurnEnded:
		return *command, nil
	case *orchestrator.FinalizeReportedCompletion:
		return *command, nil
	case *orchestrator.BlockRun:
		return *command, nil
	case *orchestrator.ThreadTurnStarted:
		return *command, nil
	case *orchestrator.StopRun:
		return *command, nil
	case *orchestrator.ResumeRun:
		return *command, nil
	case *orchestrator.UpdateThreadMetadata:
		return *command, nil
	case *orchestrator.RegisterAppServer:
		return *command, nil
	case *orchestrator.RecoverRun:
		return *command, nil
	case *orchestrator.ToolStarted:
		return *command, nil
	case *orchestrator.ToolEnded:
		return *command, nil
	default:
		panic("unreachable command type")
	}
}
