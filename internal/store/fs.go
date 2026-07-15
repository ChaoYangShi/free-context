package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ChaoYangShi/free-context/internal/orchestrator"
)

var (
	ErrAlreadyExists    = errors.New("state already exists")
	ErrNotFound         = errors.New("state not found")
	ErrRevisionConflict = errors.New("run revision conflict")
)

type FS struct {
	root string
}

func NewFS(root string) *FS {
	return &FS{root: root}
}

func (s *FS) Create(ctx context.Context, run orchestrator.Run) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validID(run.ID); err != nil {
		return err
	}
	directory := s.runDirectory(run.ID)
	if err := os.MkdirAll(filepath.Join(directory, "handoffs"), 0o700); err != nil {
		return fmt.Errorf("create run directory: %w", err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return fmt.Errorf("secure run directory: %w", err)
	}
	return writeImmutableJSON(filepath.Join(directory, "run.json"), run)
}

func (s *FS) Save(ctx context.Context, run orchestrator.Run) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	current, err := s.Load(ctx, run.ID)
	if err != nil {
		return err
	}
	if run.Revision != current.Revision+1 {
		return fmt.Errorf("%w: current=%d next=%d", ErrRevisionConflict, current.Revision, run.Revision)
	}
	return replaceJSON(filepath.Join(s.runDirectory(run.ID), "run.json"), run)
}

func (s *FS) Load(ctx context.Context, id string) (orchestrator.Run, error) {
	if err := ctx.Err(); err != nil {
		return orchestrator.Run{}, err
	}
	if err := validID(id); err != nil {
		return orchestrator.Run{}, err
	}
	var run orchestrator.Run
	if err := readJSON(filepath.Join(s.runDirectory(id), "run.json"), &run); err != nil {
		return orchestrator.Run{}, err
	}
	return run, nil
}

func (s *FS) List(ctx context.Context) ([]orchestrator.Run, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(filepath.Join(s.root, "runs"))
	if errors.Is(err, fs.ErrNotExist) {
		return []orchestrator.Run{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list runs: %w", err)
	}
	runs := make([]orchestrator.Run, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		run, err := s.Load(ctx, entry.Name())
		if err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].CreatedAt.Before(runs[j].CreatedAt) })
	return runs, nil
}

func (s *FS) Delete(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validID(id); err != nil {
		return err
	}
	if err := os.RemoveAll(s.runDirectory(id)); err != nil {
		return fmt.Errorf("delete run: %w", err)
	}
	return nil
}

func (s *FS) SaveHandoff(ctx context.Context, handoff orchestrator.Handoff) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validID(handoff.RunID); err != nil {
		return err
	}
	if err := validID(handoff.ID); err != nil {
		return err
	}
	path := filepath.Join(s.runDirectory(handoff.RunID), "handoffs", handoff.ID+".json")
	return writeImmutableJSON(path, handoff)
}

func (s *FS) LoadHandoff(ctx context.Context, runID, handoffID string) (orchestrator.Handoff, error) {
	if err := ctx.Err(); err != nil {
		return orchestrator.Handoff{}, err
	}
	if err := validID(runID); err != nil {
		return orchestrator.Handoff{}, err
	}
	if err := validID(handoffID); err != nil {
		return orchestrator.Handoff{}, err
	}
	var handoff orchestrator.Handoff
	path := filepath.Join(s.runDirectory(runID), "handoffs", handoffID+".json")
	if err := readJSON(path, &handoff); err != nil {
		return orchestrator.Handoff{}, err
	}
	return handoff, nil
}

func (s *FS) runDirectory(id string) string {
	return filepath.Join(s.root, "runs", id)
}

func validID(id string) error {
	if id == "" || id == "." || id == ".." || strings.ContainsAny(id, `/\\`) {
		return fmt.Errorf("invalid state id %q", id)
	}
	return nil
}

func readJSON(path string, target any) error {
	file, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("open state: %w", err)
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode state: %w", err)
	}
	return nil
}

func replaceJSON(path string, value any) error {
	temporary, err := writeTemporaryJSON(filepath.Dir(path), value)
	if err != nil {
		return err
	}
	defer os.Remove(temporary)
	if err := os.Rename(temporary, path); err != nil {
		return fmt.Errorf("commit state: %w", err)
	}
	return syncDirectory(filepath.Dir(path))
}

func writeImmutableJSON(path string, value any) error {
	temporary, err := writeTemporaryJSON(filepath.Dir(path), value)
	if err != nil {
		return err
	}
	defer os.Remove(temporary)
	if err := os.Link(temporary, path); errors.Is(err, fs.ErrExist) {
		return ErrAlreadyExists
	} else if err != nil {
		return fmt.Errorf("commit immutable state: %w", err)
	}
	return syncDirectory(filepath.Dir(path))
}

func writeTemporaryJSON(directory string, value any) (string, error) {
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", fmt.Errorf("create state directory: %w", err)
	}
	file, err := os.CreateTemp(directory, ".pending-*.json")
	if err != nil {
		return "", fmt.Errorf("create temporary state: %w", err)
	}
	path := file.Name()
	committed := false
	defer func() {
		if !committed {
			file.Close()
			os.Remove(path)
		}
	}()
	if err := file.Chmod(0o600); err != nil {
		return "", fmt.Errorf("secure temporary state: %w", err)
	}
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return "", fmt.Errorf("encode state: %w", err)
	}
	if err := file.Sync(); err != nil {
		return "", fmt.Errorf("sync state: %w", err)
	}
	if err := file.Close(); err != nil {
		return "", fmt.Errorf("close state: %w", err)
	}
	committed = true
	return path, nil
}

func syncDirectory(directory string) error {
	handle, err := os.Open(directory)
	if err != nil {
		return fmt.Errorf("open state directory: %w", err)
	}
	defer handle.Close()
	if err := handle.Sync(); err != nil {
		return fmt.Errorf("sync state directory: %w", err)
	}
	return nil
}
