package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"

	"github.com/ChaoYangShi/free-context/internal/orchestrator"
	"github.com/ChaoYangShi/free-context/internal/store"
)

type Handler struct {
	executor   Executor
	repository orchestrator.Repository
}

type Executor interface {
	Execute(context.Context, any) (orchestrator.Outcome, error)
}

type HandoffLoader interface {
	LoadHandoff(context.Context, string, string) (orchestrator.Handoff, error)
}

type RunState struct {
	Run      orchestrator.Run       `json:"run"`
	Handoffs []orchestrator.Handoff `json:"handoffs"`
}

func NewHandler(executor Executor, repository orchestrator.Repository) http.Handler {
	return &Handler{executor: executor, repository: repository}
}

type startRunRequest struct {
	WorkspacePath      string   `json:"workspace_path"`
	Objective          string   `json:"objective"`
	CompletionCriteria []string `json:"completion_criteria"`
	Sandbox            string   `json:"sandbox"`
}

func (h *Handler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	if request.URL.Path == "/ping" {
		response.WriteHeader(http.StatusNoContent)
		return
	}
	if request.URL.Path == "/v1/commands" {
		h.command(response, request)
		return
	}
	if request.URL.Path == "/v1/runs" {
		h.runs(response, request)
		return
	}
	if strings.HasPrefix(request.URL.Path, "/v1/states/") {
		h.state(response, request, strings.TrimPrefix(request.URL.Path, "/v1/states/"))
		return
	}
	if strings.HasPrefix(request.URL.Path, "/v1/runs/") {
		h.run(response, request, strings.TrimPrefix(request.URL.Path, "/v1/runs/"))
		return
	}
	writeError(response, http.StatusNotFound, "route not found")
}

func (h *Handler) state(response http.ResponseWriter, request *http.Request, id string) {
	if request.Method != http.MethodGet || id == "" || strings.Contains(id, "/") {
		writeError(response, http.StatusNotFound, "state not found")
		return
	}
	run, err := h.repository.Load(request.Context(), id)
	if err != nil {
		writeDomainError(response, err)
		return
	}
	loader, ok := h.repository.(HandoffLoader)
	if !ok {
		writeError(response, http.StatusInternalServerError, "handoff repository is unavailable")
		return
	}
	ids := make([]string, 0, len(run.Handoffs))
	for handoffID := range run.Handoffs {
		ids = append(ids, handoffID)
	}
	sort.Strings(ids)
	handoffs := make([]orchestrator.Handoff, 0, len(ids))
	for _, handoffID := range ids {
		handoff, err := loader.LoadHandoff(request.Context(), id, handoffID)
		if err != nil {
			writeDomainError(response, err)
			return
		}
		handoffs = append(handoffs, handoff)
	}
	writeJSON(response, http.StatusOK, RunState{Run: run, Handoffs: handoffs})
}

func (h *Handler) command(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		response.Header().Set("Allow", "POST")
		writeError(response, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var envelope commandEnvelope
	if err := decodeJSON(request, &envelope); err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	command, err := decodeCommand(envelope)
	if err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	outcome, err := h.executor.Execute(request.Context(), command)
	if err != nil {
		writeDomainError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, outcome)
}

func (h *Handler) runs(response http.ResponseWriter, request *http.Request) {
	switch request.Method {
	case http.MethodPost:
		var input startRunRequest
		if err := decodeJSON(request, &input); err != nil {
			writeError(response, http.StatusBadRequest, err.Error())
			return
		}
		outcome, err := h.executor.Execute(request.Context(), orchestrator.StartRun{
			WorkspacePath:      input.WorkspacePath,
			Objective:          input.Objective,
			CompletionCriteria: input.CompletionCriteria,
			Sandbox:            input.Sandbox,
		})
		if err != nil {
			writeDomainError(response, err)
			return
		}
		writeJSON(response, http.StatusCreated, outcome)
	case http.MethodGet:
		runs, err := h.repository.List(request.Context())
		if err != nil {
			writeDomainError(response, err)
			return
		}
		writeJSON(response, http.StatusOK, runs)
	default:
		response.Header().Set("Allow", "GET, POST")
		writeError(response, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *Handler) run(response http.ResponseWriter, request *http.Request, id string) {
	if id == "" || strings.Contains(id, "/") {
		writeError(response, http.StatusNotFound, "run not found")
		return
	}
	switch request.Method {
	case http.MethodGet:
		run, err := h.repository.Load(request.Context(), id)
		if err != nil {
			writeDomainError(response, err)
			return
		}
		writeJSON(response, http.StatusOK, run)
	case http.MethodDelete:
		run, err := h.repository.Load(request.Context(), id)
		if err != nil {
			writeDomainError(response, err)
			return
		}
		if run.Status != orchestrator.RunComplete && run.Status != orchestrator.RunStopped {
			writeError(response, http.StatusConflict, "run must be completed or stopped before deletion")
			return
		}
		if err := h.repository.Delete(request.Context(), id); err != nil {
			writeDomainError(response, err)
			return
		}
		response.WriteHeader(http.StatusNoContent)
	default:
		response.Header().Set("Allow", "GET, DELETE")
		writeError(response, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func decodeJSON(request *http.Request, target any) error {
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	return nil
}

func writeJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}

func writeError(response http.ResponseWriter, status int, message string) {
	writeJSON(response, status, map[string]string{"error": message})
}

func writeDomainError(response http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	if errors.Is(err, orchestrator.ErrWorkspaceActive) {
		status = http.StatusConflict
	} else if errors.Is(err, store.ErrNotFound) {
		status = http.StatusNotFound
	} else if errors.Is(err, store.ErrAlreadyExists) {
		status = http.StatusConflict
	} else if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		status = http.StatusRequestTimeout
	} else if strings.Contains(err.Error(), "required") || strings.Contains(err.Error(), "invalid") {
		status = http.StatusBadRequest
	}
	writeError(response, status, err.Error())
}
