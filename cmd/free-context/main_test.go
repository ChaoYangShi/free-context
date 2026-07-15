package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestRunHelp(t *testing.T) {
	command := exec.Command(os.Args[0], "-test.run=TestCLIProcess")
	command.Env = append(os.Environ(),
		"FREE_CONTEXT_TEST_CLI=1",
		"XDG_STATE_HOME=relative-path-that-must-not-be-resolved",
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("free-context --help: %v\n%s", err, output)
	}

	for _, expected := range []string{
		"Usage:\n  free-context <command> [arguments]",
		"free-context run",
		"free-context daemon start|stop|status",
		"free-context --version",
	} {
		if !strings.Contains(string(output), expected) {
			t.Errorf("help output does not contain %q:\n%s", expected, output)
		}
	}
}

func TestCLIProcess(t *testing.T) {
	if os.Getenv("FREE_CONTEXT_TEST_CLI") != "1" {
		return
	}
	os.Args = []string{"free-context", "--help"}
	main()
}
