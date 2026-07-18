package daemon_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/ChaoYangShi/free-context/internal/codexconfig"
	"github.com/ChaoYangShi/free-context/internal/daemon"
	"github.com/ChaoYangShi/free-context/internal/orchestrator"
	"github.com/ChaoYangShi/free-context/internal/store"
)

func TestHandlerCreatesListsAndReadsRun(t *testing.T) {
	t.Parallel()

	repository := store.NewFS(t.TempDir())
	engine := orchestrator.New(repository, func() time.Time {
		return time.Date(2026, 7, 14, 8, 0, 0, 0, time.UTC)
	}, func() string { return "run-1" })
	handler := daemon.NewHandler(engine, repository)
	workspace := t.TempDir()
	body := []byte(`{"workspace_path":"` + filepath.ToSlash(workspace) + `","objective":"migrate","completion_criteria":["done"],"sandbox":"workspace-write"}`)
	request := httptest.NewRequest(http.MethodPost, "/v1/runs", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	recording := httptest.NewRecorder()
	handler.ServeHTTP(recording, request)
	if recording.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", recording.Code, recording.Body.String())
	}

	var created orchestrator.Outcome
	if err := json.Unmarshal(recording.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if created.Run.ID != "run-1" || created.Run.WorkspacePath != workspace {
		t.Fatalf("created = %#v", created.Run)
	}
	if created.Run.Sandbox != codexconfig.DangerFullAccessSandbox {
		t.Fatalf("sandbox = %q, want %q", created.Run.Sandbox, codexconfig.DangerFullAccessSandbox)
	}

	list := httptest.NewRecorder()
	handler.ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/v1/runs", nil))
	if list.Code != http.StatusOK {
		t.Fatalf("list status = %d", list.Code)
	}
	var runs []orchestrator.Run
	if err := json.Unmarshal(list.Body.Bytes(), &runs); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	if len(runs) != 1 || runs[0].ID != "run-1" {
		t.Fatalf("runs = %#v", runs)
	}

	get := httptest.NewRecorder()
	handler.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/v1/runs/run-1", nil))
	if get.Code != http.StatusOK {
		t.Fatalf("get status = %d", get.Code)
	}
	var loaded orchestrator.Run
	if err := json.Unmarshal(get.Body.Bytes(), &loaded); err != nil {
		t.Fatalf("decode get response: %v", err)
	}
	if loaded.Objective != "migrate" {
		t.Fatalf("loaded = %#v", loaded)
	}
	handoff := orchestrator.Handoff{ID: "handoff-1", RunID: "run-1", Objective: "migrate"}
	if err := repository.SaveHandoff(t.Context(), handoff); err != nil {
		t.Fatal(err)
	}
	loaded.Handoffs[handoff.ID] = orchestrator.HandoffRecord{ID: handoff.ID, Status: orchestrator.HandoffReady}
	loaded.Revision++
	if err := repository.Save(t.Context(), loaded); err != nil {
		t.Fatal(err)
	}
	stateResponse := httptest.NewRecorder()
	handler.ServeHTTP(stateResponse, httptest.NewRequest(http.MethodGet, "/v1/states/run-1", nil))
	var state daemon.RunState
	if err := json.Unmarshal(stateResponse.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if len(state.Handoffs) != 1 || state.Handoffs[0].ID != handoff.ID {
		t.Fatalf("state omitted immutable handoff: %#v", state)
	}

	remove := httptest.NewRecorder()
	handler.ServeHTTP(remove, httptest.NewRequest(http.MethodDelete, "/v1/runs/run-1", nil))
	if remove.Code != http.StatusConflict {
		t.Fatalf("delete active run status = %d, want conflict", remove.Code)
	}
}
