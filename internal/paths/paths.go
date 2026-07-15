package paths

import (
	"errors"
	"os"
	"path/filepath"
)

type Layout struct {
	StateRoot    string
	RuntimeRoot  string
	DaemonSocket string
	PIDFile      string
	LogFile      string
}

func Resolve() (Layout, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Layout{}, err
	}
	stateBase := os.Getenv("XDG_STATE_HOME")
	if stateBase == "" {
		stateBase = filepath.Join(home, ".local", "state")
	}
	stateRoot := filepath.Join(stateBase, "free-context")
	runtimeBase := os.Getenv("XDG_RUNTIME_DIR")
	if runtimeBase == "" {
		runtimeBase = filepath.Join(stateRoot, "runtime")
	}
	runtimeRoot := filepath.Join(runtimeBase, "free-context")
	if !filepath.IsAbs(stateRoot) || !filepath.IsAbs(runtimeRoot) {
		return Layout{}, errors.New("free-context state and runtime paths must be absolute")
	}
	return Layout{
		StateRoot:    filepath.Clean(stateRoot),
		RuntimeRoot:  filepath.Clean(runtimeRoot),
		DaemonSocket: filepath.Join(runtimeRoot, "daemon.sock"),
		PIDFile:      filepath.Join(stateRoot, "daemon.pid"),
		LogFile:      filepath.Join(stateRoot, "daemon.log"),
	}, nil
}
