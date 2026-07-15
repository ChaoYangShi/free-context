package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/ChaoYangShi/free-context/internal/appserver"
	"github.com/ChaoYangShi/free-context/internal/codexrpc"
	"github.com/ChaoYangShi/free-context/internal/handoff"
	"github.com/ChaoYangShi/free-context/internal/orchestrator"
)

type AppServers interface {
	Start(context.Context, orchestrator.Run) (appserver.Runtime, error)
	Get(string) (appserver.Runtime, error)
	Stop(string) error
}

type HandoffGenerator interface {
	Generate(context.Context, handoff.GenerateInput) (orchestrator.Handoff, error)
}

type Controller struct {
	Engine     *orchestrator.Engine
	Repository orchestrator.Repository
	AppServers AppServers
	Handoffs   HandoffGenerator

	mu sync.Mutex
}

func NewController(engine *orchestrator.Engine, repository orchestrator.Repository, servers AppServers, handoffs HandoffGenerator) *Controller {
	return &Controller{Engine: engine, Repository: repository, AppServers: servers, Handoffs: handoffs}
}

func (c *Controller) Recover(ctx context.Context, runID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	run, err := c.Repository.Load(ctx, runID)
	if err != nil {
		return err
	}
	runtime, err := c.AppServers.Start(ctx, run)
	if err == nil {
		_, err = c.Engine.Execute(ctx, orchestrator.RegisterAppServer{RunID: runID, PID: runtime.PID(), Socket: strings.TrimPrefix(runtime.Endpoint(), "unix://")})
	}
	if err == nil {
		var outcome orchestrator.Outcome
		outcome, err = c.Engine.Execute(ctx, orchestrator.RecoverRun{RunID: runID})
		if err == nil {
			err = c.apply(ctx, outcome)
		}
	}
	if err == nil {
		return nil
	}
	_ = c.AppServers.Stop(runID)
	_, _ = c.Engine.Execute(ctx, orchestrator.BlockRun{RunID: runID, Reason: "automatic recovery failed: " + err.Error()})
	return err
}

func (c *Controller) CheckTimeouts(ctx context.Context, now time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	runs, err := c.Repository.List(ctx)
	if err != nil {
		return err
	}
	for _, run := range runs {
		if run.Status == orchestrator.RunTransitioning && !run.Transition.Deadline.IsZero() && !now.Before(run.Transition.Deadline) {
			outcome, err := c.Engine.Execute(ctx, orchestrator.BlockRun{RunID: run.ID, Reason: "root transition exceeded its 30 minute deadline"})
			if err == nil {
				err = c.apply(ctx, outcome)
			}
			if err != nil {
				return err
			}
			continue
		}
		if run.Status != orchestrator.RunActive {
			continue
		}
		for _, thread := range run.Threads {
			if thread.TransitionDeadline.IsZero() || now.Before(thread.TransitionDeadline) {
				continue
			}
			outcome, err := c.Engine.Execute(ctx, orchestrator.BlockRun{RunID: run.ID, Reason: fmt.Sprintf("thread %s handoff exceeded its 30 minute deadline", thread.ID)})
			if err == nil {
				err = c.apply(ctx, outcome)
			}
			if err != nil {
				return err
			}
			break
		}
	}
	return nil
}

func (c *Controller) HandleAppServerExit(ctx context.Context, runID string, exitErr error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	run, err := c.Repository.Load(ctx, runID)
	if err != nil || run.Status == orchestrator.RunComplete || run.Status == orchestrator.RunStopped || run.Status == orchestrator.RunBlocked {
		return
	}
	reason := "managed app-server exited unexpectedly"
	if exitErr != nil {
		reason += ": " + exitErr.Error()
	}
	if outcome, err := c.Engine.Execute(ctx, orchestrator.BlockRun{RunID: runID, Reason: reason}); err == nil {
		_ = c.apply(ctx, outcome)
	}
}

func (c *Controller) Execute(ctx context.Context, command any) (orchestrator.Outcome, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	outcome, err := c.Engine.Execute(ctx, command)
	if err != nil {
		return orchestrator.Outcome{}, err
	}
	if err := c.apply(ctx, outcome); err != nil {
		if outcome.Run.Status != orchestrator.RunComplete {
			if _, blockErr := c.Engine.Execute(ctx, orchestrator.BlockRun{RunID: outcome.Run.ID, Reason: err.Error()}); blockErr == nil {
				_ = c.applyCurrentRunBlock(ctx, outcome.Run.ID)
			}
		}
		return orchestrator.Outcome{}, err
	}
	if outcome.Run.Status == orchestrator.RunComplete {
		return orchestrator.Outcome{Run: outcome.Run}, nil
	}
	current, err := c.Repository.Load(ctx, outcome.Run.ID)
	if err != nil {
		return orchestrator.Outcome{}, err
	}
	return orchestrator.Outcome{Run: current}, nil
}

func (c *Controller) apply(ctx context.Context, outcome orchestrator.Outcome) error {
	for _, effect := range outcome.Effects {
		if err := c.applyEffect(ctx, outcome.Run.ID, effect); err != nil {
			return err
		}
	}
	return nil
}

func (c *Controller) applyEffect(ctx context.Context, runID string, effect orchestrator.Effect) error {
	run, err := c.Repository.Load(ctx, runID)
	if err != nil {
		return err
	}
	switch effect.Kind {
	case orchestrator.EffectStartAppServer:
		if c.AppServers == nil {
			return errors.New("app-server manager is required")
		}
		_, err := c.AppServers.Start(ctx, run)
		if err != nil {
			return err
		}
		runtime, err := c.AppServers.Get(runID)
		if err != nil {
			return err
		}
		_, err = c.transition(ctx, orchestrator.RegisterAppServer{RunID: runID, PID: runtime.PID(), Socket: strings.TrimPrefix(runtime.Endpoint(), "unix://")})
		if err != nil {
			return err
		}
		return nil
	case orchestrator.EffectGenerateHandoff:
		if c.Handoffs == nil {
			return errors.New("handoff generator is required")
		}
		thread, exists := run.Threads[effect.ThreadID]
		if !exists {
			return fmt.Errorf("thread %s is not registered", effect.ThreadID)
		}
		scope := orchestrator.HandoffAgent
		childIDs := []string{}
		if thread.Role == orchestrator.RoleRoot {
			scope = orchestrator.HandoffTree
			for id, record := range run.Handoffs {
				if record.SourceThreadID != thread.ID && record.Status == orchestrator.HandoffReady {
					childIDs = append(childIDs, id)
				}
			}
		}
		generated, err := c.Handoffs.Generate(ctx, handoff.GenerateInput{Run: run, Thread: thread, Scope: scope, ChildHandoffIDs: childIDs})
		if err != nil {
			return err
		}
		_, err = c.transition(ctx, orchestrator.RecordHandoff{RunID: runID, Handoff: generated})
		return err
	case orchestrator.EffectStopThread:
		if run.Recovering {
			_, err := c.transition(ctx, orchestrator.ThreadStopped{RunID: runID, ThreadID: effect.ThreadID})
			return err
		}
		session, err := c.AppServers.Get(runID)
		if err != nil {
			return err
		}
		thread := run.Threads[effect.ThreadID]
		if thread.CurrentTurnID != "" {
			if err := session.Interrupt(ctx, thread.ID, thread.CurrentTurnID); err != nil {
				return err
			}
		}
		if err := session.ArchiveThread(ctx, thread.ID); err != nil {
			return err
		}
		_, err = c.transition(ctx, orchestrator.ThreadStopped{RunID: runID, ThreadID: thread.ID})
		return err
	case orchestrator.EffectSteerParent:
		session, err := c.AppServers.Get(runID)
		if err != nil {
			return err
		}
		parent := run.Threads[effect.ThreadID]
		return session.SteerTurn(ctx, effect.ThreadID, parent.CurrentTurnID, fmt.Sprintf("Free Context handoff %s is ready. Read it with get_run_state, call accept_handoff with handoff_id=%s, then call resolve_handoff after deciding whether to continue, replan, or complete.", effect.HandoffID, effect.HandoffID))
	case orchestrator.EffectQuiesceTree:
		for _, thread := range run.Threads {
			if len(thread.InFlightToolIDs) != 0 {
				return nil
			}
		}
		session, err := c.AppServers.Get(runID)
		if err != nil {
			return err
		}
		for _, thread := range run.Threads {
			if thread.Status == orchestrator.ThreadActive && thread.CurrentTurnID != "" {
				if err := session.Interrupt(ctx, thread.ID, thread.CurrentTurnID); err != nil {
					return err
				}
			}
		}
		_, err = c.transition(ctx, orchestrator.TreeQuiesced{RunID: runID})
		return err
	case orchestrator.EffectStartRootReadOnly:
		session, err := c.AppServers.Get(runID)
		if err != nil {
			return err
		}
		oldRoot := run.Threads[run.Transition.OldRootID]
		thread, err := session.StartThread(ctx, codexrpc.StartThreadInput{WorkspacePath: run.WorkspacePath, Model: oldRoot.Model, Sandbox: "read-only"})
		if err != nil {
			return err
		}
		_, err = c.transition(ctx, orchestrator.RegisterReplacementRoot{RunID: runID, ThreadID: thread.ID, Model: oldRoot.Model, TranscriptPath: stringValue(thread.Path)})
		if err != nil {
			return err
		}
		turn, err := session.StartTurn(ctx, thread.ID, fmt.Sprintf("A previous Free Context tree handoff %s is ready. Inspect the workspace and handoff references with get_run_state. You must explicitly call accept_handoff with handoff_id=%s before doing any work. Then replan unfinished work and report_progress with a concrete next_action.", effect.HandoffID, effect.HandoffID), run.WorkspacePath, oldRoot.Model, true)
		if err != nil {
			return err
		}
		_, err = c.transition(ctx, orchestrator.ThreadTurnStarted{RunID: runID, ThreadID: thread.ID, TurnID: turn.ID})
		return err
	case orchestrator.EffectRetireTree:
		session, err := c.AppServers.Get(runID)
		if err != nil {
			return err
		}
		for id, thread := range run.Threads {
			if id == run.Transition.NewRootID || thread.Status == orchestrator.ThreadRetired {
				continue
			}
			if run.Recovering {
				continue
			}
			if thread.CurrentTurnID != "" {
				if err := session.Interrupt(ctx, id, thread.CurrentTurnID); err != nil {
					return err
				}
			}
			if err := session.ArchiveThread(ctx, id); err != nil {
				return err
			}
		}
		_, err = c.transition(ctx, orchestrator.CompleteRootTransfer{RunID: runID})
		return err
	case orchestrator.EffectGrantSandbox:
		session, err := c.AppServers.Get(runID)
		if err != nil {
			return err
		}
		thread := run.Threads[effect.ThreadID]
		return session.UpdateThreadSettings(ctx, thread.ID, thread.Model, run.Sandbox)
	case orchestrator.EffectStartNextTurn:
		session, err := c.AppServers.Get(runID)
		if err != nil {
			return err
		}
		thread := run.Threads[effect.ThreadID]
		turn, err := session.StartTurn(ctx, thread.ID, effect.Prompt, run.WorkspacePath, thread.Model, false)
		if err != nil {
			return err
		}
		_, err = c.transition(ctx, orchestrator.ThreadTurnStarted{RunID: runID, ThreadID: thread.ID, TurnID: turn.ID})
		return err
	case orchestrator.EffectBlockTree:
		if c.AppServers == nil {
			return nil
		}
		session, err := c.AppServers.Get(runID)
		if err != nil {
			return err
		}
		for _, thread := range run.Threads {
			if thread.Status == orchestrator.ThreadActive && thread.CurrentTurnID != "" {
				if err := session.Interrupt(ctx, thread.ID, thread.CurrentTurnID); err != nil {
					return err
				}
			}
		}
		return nil
	case orchestrator.EffectStopAppServer:
		return c.AppServers.Stop(runID)
	case orchestrator.EffectDeleteRun:
		return c.Repository.Delete(ctx, runID)
	default:
		return fmt.Errorf("unsupported effect %q", effect.Kind)
	}
}

func (c *Controller) transition(ctx context.Context, command any) (orchestrator.Outcome, error) {
	outcome, err := c.Engine.Execute(ctx, command)
	if err != nil {
		return orchestrator.Outcome{}, err
	}
	if err := c.apply(ctx, outcome); err != nil {
		return orchestrator.Outcome{}, err
	}
	return outcome, nil
}

func (c *Controller) applyCurrentRunBlock(ctx context.Context, runID string) error {
	run, err := c.Repository.Load(ctx, runID)
	if err != nil {
		return err
	}
	for _, effect := range []orchestrator.Effect{{Kind: orchestrator.EffectBlockTree, ThreadID: run.RootThreadID}} {
		if err := c.applyEffect(ctx, run.ID, effect); err != nil {
			return err
		}
	}
	return nil
}

func (c *Controller) HandleNotification(runID string, message json.RawMessage) {
	var envelope struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(message, &envelope); err != nil {
		return
	}
	ctx := context.Background()
	switch envelope.Method {
	case "thread/started":
		var params struct {
			Thread codexrpc.Thread `json:"thread"`
		}
		if json.Unmarshal(envelope.Params, &params) != nil || params.Thread.ID == "" {
			return
		}
		if session, err := c.AppServers.Get(runID); err == nil {
			if resumed, err := session.ResumeThread(ctx, params.Thread.ID); err == nil {
				params.Thread = resumed
			}
		}
		run, err := c.Repository.Load(ctx, runID)
		if err != nil {
			return
		}
		model := params.Thread.Model
		if root, exists := run.Threads[run.RootThreadID]; model == "" && exists {
			model = root.Model
		}
		role := orchestrator.RoleWorker
		assignedTask := params.Thread.Preview
		if params.Thread.ParentThreadID == nil && run.RootThreadID == "" && run.Status == orchestrator.RunStarting {
			role = orchestrator.RoleRoot
			assignedTask = run.Objective
		}
		if role == orchestrator.RoleWorker && params.Thread.ParentThreadID == nil {
			return
		}
		_, _ = c.Execute(ctx, orchestrator.RegisterThread{RunID: runID, ThreadID: params.Thread.ID, ParentThreadID: stringValue(params.Thread.ParentThreadID), Role: role, AssignedTask: assignedTask, Model: model, TranscriptPath: stringValue(params.Thread.Path)})
	case "turn/started":
		var params struct {
			ThreadID string        `json:"threadId"`
			Turn     codexrpc.Turn `json:"turn"`
		}
		if json.Unmarshal(envelope.Params, &params) == nil && params.ThreadID != "" {
			_, _ = c.Execute(ctx, orchestrator.ThreadTurnStarted{RunID: runID, ThreadID: params.ThreadID, TurnID: params.Turn.ID})
		}
	case "thread/settings/updated":
		var params struct {
			ThreadID       string `json:"threadId"`
			ThreadSettings struct {
				Model string `json:"model"`
			} `json:"threadSettings"`
		}
		if json.Unmarshal(envelope.Params, &params) == nil && params.ThreadID != "" && params.ThreadSettings.Model != "" {
			_, _ = c.Execute(ctx, orchestrator.UpdateThreadMetadata{RunID: runID, ThreadID: params.ThreadID, Model: params.ThreadSettings.Model})
		}
	case "thread/tokenUsage/updated":
		var params struct {
			ThreadID   string `json:"threadId"`
			TurnID     string `json:"turnId"`
			TokenUsage struct {
				Total struct {
					TotalTokens           int `json:"totalTokens"`
					InputTokens           int `json:"inputTokens"`
					CachedInputTokens     int `json:"cachedInputTokens"`
					OutputTokens          int `json:"outputTokens"`
					ReasoningOutputTokens int `json:"reasoningOutputTokens"`
				} `json:"total"`
				Last struct {
					TotalTokens int `json:"totalTokens"`
				} `json:"last"`
				ModelContextWindow int `json:"modelContextWindow"`
			} `json:"tokenUsage"`
		}
		if json.Unmarshal(envelope.Params, &params) == nil && params.ThreadID != "" {
			_, _ = c.Execute(ctx, orchestrator.RecordTokenCapacity{
				RunID:    runID,
				ThreadID: params.ThreadID,
				Snapshot: orchestrator.TokenCapacitySnapshot{
					TurnID:                params.TurnID,
					TotalTokens:           params.TokenUsage.Total.TotalTokens,
					InputTokens:           params.TokenUsage.Total.InputTokens,
					CachedInputTokens:     params.TokenUsage.Total.CachedInputTokens,
					OutputTokens:          params.TokenUsage.Total.OutputTokens,
					ReasoningOutputTokens: params.TokenUsage.Total.ReasoningOutputTokens,
					LastTotalTokens:       params.TokenUsage.Last.TotalTokens,
					ModelContextWindow:    params.TokenUsage.ModelContextWindow,
				},
			})
		}
	case "item/started":
		var params struct {
			ThreadID string `json:"threadId"`
			Item     struct {
				ID   string `json:"id"`
				Type string `json:"type"`
			} `json:"item"`
		}
		if json.Unmarshal(envelope.Params, &params) == nil && params.ThreadID != "" && trackedItemType(params.Item.Type) {
			_, _ = c.Execute(ctx, orchestrator.ToolStarted{RunID: runID, ThreadID: params.ThreadID, ToolUseID: params.Item.ID})
		}
	case "item/completed":
		var params struct {
			ThreadID string `json:"threadId"`
			Item     struct {
				ID   string `json:"id"`
				Type string `json:"type"`
			} `json:"item"`
		}
		if json.Unmarshal(envelope.Params, &params) == nil && params.ThreadID != "" && trackedItemType(params.Item.Type) {
			_, _ = c.Execute(ctx, orchestrator.ToolEnded{RunID: runID, ThreadID: params.ThreadID, ToolUseID: params.Item.ID})
		}
	case "turn/completed":
		var params struct {
			ThreadID string `json:"threadId"`
		}
		if json.Unmarshal(envelope.Params, &params) == nil && params.ThreadID != "" {
			_, _ = c.Execute(ctx, orchestrator.TurnEnded{RunID: runID, ThreadID: params.ThreadID})
		}
	case "thread/status/changed":
		var params struct {
			Status struct {
				Type        string   `json:"type"`
				ActiveFlags []string `json:"activeFlags"`
			} `json:"status"`
		}
		if json.Unmarshal(envelope.Params, &params) == nil {
			for _, flag := range params.Status.ActiveFlags {
				if flag == "waitingOnUserInput" {
					_, _ = c.Execute(ctx, orchestrator.BlockRun{RunID: runID, Reason: "Codex requested user input while running in background"})
					return
				}
			}
		}
	}
}

func trackedItemType(itemType string) bool {
	switch itemType {
	case "commandExecution", "fileChange", "mcpToolCall", "dynamicToolCall", "collabAgentToolCall", "imageView", "webSearch", "sleep":
		return true
	default:
		return false
	}
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

var _ Executor = (*Controller)(nil)
