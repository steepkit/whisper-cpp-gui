//go:build linux || darwin

package job

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	processexec "github.com/steepkit/whisper-cpp-gui/internal/exec"
)

func TestTimeoutTerminatesStubAndCleansWorkDir(t *testing.T) {
	clock := newManualClock()
	tempRoot := t.TempDir()
	childPIDFile := filepath.Join(t.TempDir(), "child.pid")
	stub, err := filepath.Abs(filepath.Join("..", "..", "testdata", "stubs", "whisper-cli"))
	if err != nil {
		t.Fatalf("resolve whisper stub: %v", err)
	}

	processRunner := processexec.NewProcessRunner()
	runner := runnerFunc(func(ctx context.Context, request RunRequest, _ Observer) error {
		return processRunner.Run(ctx, processexec.CommandSpec{
			Path: stub,
			Args: []string{
				"-f", "input.wav",
				"-of", filepath.Join(request.WorkDir, "result"),
				"-pp",
			},
			Env: append(os.Environ(),
				"STUB_PROGRESS_INTERVAL=60s",
				"STUB_CHILD_PID_FILE="+childPIDFile,
			),
		})
	})
	m := newTestManager(t, runner, Config{
		Clock:      clock,
		RunTimeout: time.Minute,
		TempRoot:   tempRoot,
	})

	submitted := submit(t, m, "stub-timeout", "")
	waitForTestFile(t, filepath.Join(submitted.WorkDir, "invocation.json"))
	childPIDText := strings.TrimSpace(waitForTestFile(t, childPIDFile))
	childPID, err := strconv.Atoi(childPIDText)
	if err != nil {
		t.Fatalf("parse stub child PID %q: %v", childPIDText, err)
	}

	clock.Advance(time.Minute)
	snapshot := waitTerminal(t, m, submitted.ID)
	if snapshot.Status != StatusFailed || snapshot.ErrorCode != ErrorCodeTimeout {
		t.Fatalf("terminal state = %s/%s, want failed/timeout", snapshot.Status, snapshot.ErrorCode)
	}
	if _, err := os.Stat(submitted.WorkDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("work directory remains after timeout cleanup: %v", err)
	}
	if err := syscall.Kill(childPID, 0); err == nil || errors.Is(err, syscall.EPERM) {
		t.Fatalf("stub child process %d remains after timeout", childPID)
	}
}

func waitForTestFile(t *testing.T, path string) string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil && len(data) > 0 {
			return string(data)
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("read %s: %v", path, err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
	return ""
}
