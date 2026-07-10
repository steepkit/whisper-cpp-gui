//go:build linux || darwin

package exec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	osexec "os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const helperModeEnv = "WHISPER_GUI_PROCESS_HELPER"

func TestProcessRunnerDefaultGrace(t *testing.T) {
	if got := NewProcessRunner().gracePeriod(); got != 5*time.Second {
		t.Fatalf("default grace = %s, want 5s", got)
	}
}

func TestProcessRunnerCancelledBeforeStart(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "started")
	wantCause := errors.New("cancelled before dispatch")
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(wantCause)

	err := NewProcessRunner().Run(ctx, helperSpec(t, "term", marker))
	if !errors.Is(err, wantCause) {
		t.Fatalf("Run error = %v, want cancellation cause", err)
	}
	if _, statErr := os.Stat(marker); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("process started despite pre-cancelled context: stat error %v", statErr)
	}
}

func TestProcessRunnerNormalExit(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	spec := helperSpec(t, "record", "")
	spec.Args = append(spec.Args, "payload")
	spec.Dir = dir
	spec.Env = append(spec.Env, "HELPER_VALUE=from-spec")
	spec.Stdout = &stdout
	spec.Stderr = &stderr

	if err := NewProcessRunner().Run(context.Background(), spec); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if want := "stdout:from-spec:" + dir + ":payload"; stdout.String() != want {
		t.Errorf("stdout = %q, want %q", stdout.String(), want)
	}
	if stderr.String() != "stderr:payload" {
		t.Errorf("stderr = %q, want %q", stderr.String(), "stderr:payload")
	}
}

func TestProcessRunnerNonzeroExit(t *testing.T) {
	err := NewProcessRunner().Run(context.Background(), helperSpec(t, "exit", ""))
	if err == nil {
		t.Fatal("Run succeeded, want non-zero exit error")
	}
	var exitErr *osexec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 23 {
		t.Fatalf("Run error = %v, want exit code 23", err)
	}
}

func TestProcessRunnerCancellationTerminatesGroup(t *testing.T) {
	dir := t.TempDir()
	ready := filepath.Join(dir, "ready")
	termObserved := filepath.Join(dir, "term-observed")
	spec := helperSpec(t, "group-parent", ready)
	spec.Env = append(spec.Env,
		"HELPER_TERM_MARKER="+termObserved,
		"HELPER_CHILD_READY="+filepath.Join(dir, "child-ready"),
	)

	runner := newProcessRunner(time.Second)
	ctx, cancel := context.WithCancelCause(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- runner.Run(ctx, spec) }()

	readyData := waitForFile(t, ready, 2*time.Second)
	fields := strings.Fields(readyData)
	if len(fields) != 2 {
		t.Fatalf("ready data = %q, want parent and child PIDs", readyData)
	}
	childPID, err := strconv.Atoi(fields[1])
	if err != nil {
		t.Fatalf("parse child PID %q: %v", fields[1], err)
	}

	wantCause := errors.New("user cancellation")
	started := time.Now()
	cancel(wantCause)
	runErr := receiveError(t, errCh, 2*time.Second)
	if !errors.Is(runErr, wantCause) {
		t.Fatalf("Run error = %v, want cancellation cause", runErr)
	}
	if elapsed := time.Since(started); elapsed >= time.Second {
		t.Fatalf("TERM-responsive group used full grace: %s", elapsed)
	}
	if _, err := os.Stat(termObserved); err != nil {
		t.Fatalf("parent did not observe SIGTERM: %v", err)
	}
	waitForProcessGone(t, childPID, time.Second)
}

func TestProcessRunnerCancellationKillsTermIgnoringProcess(t *testing.T) {
	ready := filepath.Join(t.TempDir(), "ready")
	runner := newProcessRunner(100 * time.Millisecond)
	ctx, cancel := context.WithCancelCause(context.Background())
	spec := helperSpec(t, "ignore-term", ready)
	errCh := make(chan error, 1)
	go func() { errCh <- runner.Run(ctx, spec) }()

	pidText := strings.TrimSpace(waitForFile(t, ready, 2*time.Second))
	pid, err := strconv.Atoi(pidText)
	if err != nil {
		t.Fatalf("parse helper PID %q: %v", pidText, err)
	}
	wantCause := errors.New("test timeout")
	started := time.Now()
	cancel(wantCause)
	runErr := receiveError(t, errCh, 2*time.Second)
	elapsed := time.Since(started)
	if !errors.Is(runErr, wantCause) {
		t.Fatalf("Run error = %v, want cancellation cause", runErr)
	}
	if elapsed < 80*time.Millisecond || elapsed > time.Second {
		t.Fatalf("SIGKILL fallback elapsed = %s, want approximately 100ms", elapsed)
	}
	waitForProcessGone(t, pid, time.Second)
}

func TestProcessRunnerKillsTermIgnoringWhisperStubGroup(t *testing.T) {
	dir := t.TempDir()
	prefix := filepath.Join(dir, "result")
	childPIDFile := filepath.Join(dir, "sleep.pid")
	spec := CommandSpec{
		Path: stubPath(t, "whisper-cli"),
		Args: []string{"-f", "input.wav", "-of", prefix, "-pp"},
		Env: append(os.Environ(),
			"STUB_IGNORE_TERM=1",
			"STUB_PROGRESS_INTERVAL=60s",
			"STUB_CHILD_PID_FILE="+childPIDFile,
		),
	}
	runner := newProcessRunner(150 * time.Millisecond)
	ctx, cancel := context.WithCancelCause(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- runner.Run(ctx, spec) }()

	waitForFile(t, filepath.Join(dir, "invocation.json"), 2*time.Second)
	childPIDText := strings.TrimSpace(waitForFile(t, childPIDFile, 2*time.Second))
	childPID, err := strconv.Atoi(childPIDText)
	if err != nil {
		t.Fatalf("parse stub child PID %q: %v", childPIDText, err)
	}
	wantCause := errors.New("cancel whisper stub")
	started := time.Now()
	cancel(wantCause)
	time.Sleep(40 * time.Millisecond)
	if !processExists(childPID) {
		t.Fatal("stub sleep child exited on SIGTERM; it must ignore TERM")
	}
	runErr := receiveError(t, errCh, 2*time.Second)
	elapsed := time.Since(started)
	if !errors.Is(runErr, wantCause) {
		t.Fatalf("Run error = %v, want cancellation cause", runErr)
	}
	if elapsed < 120*time.Millisecond || elapsed > time.Second {
		t.Fatalf("stub SIGKILL fallback elapsed = %s, want approximately 150ms", elapsed)
	}
	waitForProcessGone(t, childPID, time.Second)

	inv := readInvocation(t, dir)
	if inv.Env["STUB_IGNORE_TERM"] != "1" {
		t.Errorf("invocation STUB_IGNORE_TERM = %q, want 1", inv.Env["STUB_IGNORE_TERM"])
	}
}

func helperSpec(t *testing.T, mode, readyPath string) CommandSpec {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("resolving test executable: %v", err)
	}
	return CommandSpec{
		Path: executable,
		Args: []string{"-test.run=^TestProcessRunnerHelper$"},
		Env: []string{
			helperModeEnv + "=" + mode,
			"HELPER_READY=" + readyPath,
			"GORACE=atexit_sleep_ms=0",
			"GOCOVERDIR=" + t.TempDir(),
		},
	}
}

func waitForFile(t *testing.T, path string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil && len(data) > 0 {
			return string(data)
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("reading %s: %v", path, err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
	return ""
}

func receiveError(t *testing.T, errCh <-chan error, timeout time.Duration) error {
	t.Helper()
	select {
	case err := <-errCh:
		return err
	case <-time.After(timeout):
		t.Fatal("timed out waiting for ProcessRunner")
		return nil
	}
}

func waitForProcessGone(t *testing.T, pid int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !processExists(pid) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("process %d remains after ProcessRunner returned", pid)
}

func processExists(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// TestProcessRunnerHelper is re-executed by the tests above as a direct child;
// it is not a shell wrapper.
func TestProcessRunnerHelper(t *testing.T) {
	mode := os.Getenv(helperModeEnv)
	if mode == "" {
		return
	}

	switch mode {
	case "record":
		cwd, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		payload := os.Args[len(os.Args)-1]
		fmt.Fprintf(os.Stdout, "stdout:%s:%s:%s", os.Getenv("HELPER_VALUE"), cwd, payload)
		fmt.Fprintf(os.Stderr, "stderr:%s", payload)
	case "exit":
		os.Exit(23)
	case "term":
		termCh := make(chan os.Signal, 1)
		signal.Notify(termCh, syscall.SIGTERM)
		writeHelperFile(t, os.Getenv("HELPER_READY"), strconv.Itoa(os.Getpid()))
		<-termCh
	case "ignore-term":
		signal.Ignore(syscall.SIGTERM)
		writeHelperFile(t, os.Getenv("HELPER_READY"), strconv.Itoa(os.Getpid()))
		for {
			time.Sleep(time.Hour)
		}
	case "group-parent":
		runGroupParentHelper(t)
	case "group-child":
		termCh := make(chan os.Signal, 1)
		signal.Notify(termCh, syscall.SIGTERM)
		writeHelperFile(t, os.Getenv("HELPER_CHILD_READY"), strconv.Itoa(os.Getpid()))
		<-termCh
	default:
		t.Fatalf("unknown helper mode %q", mode)
	}
	os.Exit(0)
}

func runGroupParentHelper(t *testing.T) {
	termCh := make(chan os.Signal, 1)
	signal.Notify(termCh, syscall.SIGTERM)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	child := osexec.Command(executable, "-test.run=^TestProcessRunnerHelper$")
	child.Env = []string{
		helperModeEnv + "=group-child",
		"HELPER_CHILD_READY=" + os.Getenv("HELPER_CHILD_READY"),
		"GORACE=atexit_sleep_ms=0",
	}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	waitForHelperFile(os.Getenv("HELPER_CHILD_READY"), 2*time.Second)
	writeHelperFile(t, os.Getenv("HELPER_READY"), fmt.Sprintf("%d %d", os.Getpid(), child.Process.Pid))
	<-termCh
	if err := child.Wait(); err != nil {
		t.Fatalf("waiting for helper child: %v", err)
	}
	writeHelperFile(t, os.Getenv("HELPER_TERM_MARKER"), "term")
}

func writeHelperFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func waitForHelperFile(path string, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	panic("timed out waiting for helper file " + path)
}
