package main

import (
	"errors"
	"net"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/steepkit/whisper-cpp-gui/internal/job"
)

// TestRunNoBrowserSuppressesLaunch is the acceptance test for --no-browser:
// the injected launcher must never be called.
func TestRunNoBrowserSuppressesLaunch(t *testing.T) {
	setTestUserCache(t)
	var out strings.Builder
	calls := 0
	app, err := run([]string{"--no-browser", "--port", "0"}, &out, func(target string) error {
		calls++
		return nil
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	defer app.Close()
	if calls != 0 {
		t.Errorf("browser launcher called %d times with --no-browser, want 0", calls)
	}
	if !strings.Contains(out.String(), "http://127.0.0.1:") {
		t.Errorf("startup output must print the URL, got: %q", out.String())
	}
	token := assertPrivateBootstrap(t, app)
	if !strings.Contains(out.String(), "Bootstrap file: "+app.bootstrapPath) {
		t.Errorf("--no-browser output must include the private bootstrap file, got: %q", out.String())
	}
	if strings.Contains(out.String(), token) || strings.Contains(out.String(), "#token=") {
		t.Errorf("--no-browser stdout exposed the token: %q", out.String())
	}
}

// TestRunOpensBrowserByDefault covers the default path with an injected
// fake launcher (no real browser is started).
func TestRunOpensBrowserByDefault(t *testing.T) {
	setTestUserCache(t)
	var out strings.Builder
	var gotTarget string
	app, err := run([]string{"--port", "0"}, &out, func(target string) error {
		gotTarget = target
		return nil
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	defer app.Close()
	if gotTarget == "" {
		t.Fatal("browser launcher was not called on default settings")
	}
	if gotTarget != app.bootstrapPath {
		t.Fatalf("browser target = %q, want private bootstrap file %q", gotTarget, app.bootstrapPath)
	}
	token := assertPrivateBootstrap(t, app)
	if strings.Contains(gotTarget, token) || strings.Contains(gotTarget, "#token=") {
		t.Fatalf("browser launcher target exposed the token: %q", gotTarget)
	}
	if strings.Contains(out.String(), token) || strings.Contains(out.String(), "#token=") || strings.Contains(out.String(), app.bootstrapPath) {
		t.Errorf("normal startup stdout must not expose the token: %q", out.String())
	}
}

func TestRunPrintsBootstrapWhenBrowserLaunchFails(t *testing.T) {
	setTestUserCache(t)
	var out strings.Builder
	app, err := run([]string{"--port", "0"}, &out, func(string) error {
		return errors.New("launcher unavailable")
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	defer app.Close()
	token := assertPrivateBootstrap(t, app)
	if !strings.Contains(out.String(), "Bootstrap file: "+app.bootstrapPath) {
		t.Errorf("launch failure must print the private bootstrap file, got: %q", out.String())
	}
	if strings.Contains(out.String(), token) || strings.Contains(out.String(), "#token=") {
		t.Errorf("launch failure stdout exposed the token: %q", out.String())
	}
}

func TestApplicationCloseRemovesBootstrap(t *testing.T) {
	setTestUserCache(t)
	var out strings.Builder
	app, err := run([]string{"--no-browser", "--port", "0"}, &out, func(string) error { return nil })
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	dir := app.bootstrapDir
	if err := app.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("bootstrap directory remains after Close: %v", err)
	}
}

func assertPrivateBootstrap(t *testing.T, app *application) string {
	t.Helper()
	dirInfo, err := os.Stat(app.bootstrapDir)
	if err != nil {
		t.Fatalf("stat bootstrap directory: %v", err)
	}
	if got := dirInfo.Mode().Perm(); got != 0o700 {
		t.Errorf("bootstrap directory mode = %o, want 700", got)
	}
	fileInfo, err := os.Stat(app.bootstrapPath)
	if err != nil {
		t.Fatalf("stat bootstrap file: %v", err)
	}
	if got := fileInfo.Mode().Perm(); got != 0o600 {
		t.Errorf("bootstrap file mode = %o, want 600", got)
	}
	data, err := os.ReadFile(app.bootstrapPath)
	if err != nil {
		t.Fatalf("read bootstrap file: %v", err)
	}
	const marker = "#token="
	index := strings.Index(string(data), marker)
	if index < 0 || len(data) < index+len(marker)+64 {
		t.Fatalf("bootstrap file does not contain a 256-bit fragment token: %q", data)
	}
	return string(data[index+len(marker) : index+len(marker)+64])
}

func TestRunLauncherReportsNonZeroExit(t *testing.T) {
	if os.Getenv("GO_WANT_LAUNCHER_HELPER") == "1" {
		os.Exit(7)
	}
	cmd := osexec.Command(os.Args[0], "-test.run=TestRunLauncherReportsNonZeroExit")
	cmd.Env = append(os.Environ(), "GO_WANT_LAUNCHER_HELPER=1")
	if err := runLauncher(cmd); err == nil {
		t.Fatal("runLauncher accepted a non-zero launcher exit")
	}
}

// TestRunVersion prints the version and starts nothing.
func TestRunVersion(t *testing.T) {
	var out strings.Builder
	app, err := run([]string{"--version"}, &out, func(string) error {
		t.Fatal("browser must not open for --version")
		return nil
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if app != nil {
		app.Close()
		t.Fatal("run --version must not start a server")
	}
	if !strings.Contains(out.String(), version) {
		t.Errorf("version output %q must contain %q", out.String(), version)
	}
}

// TestRunOccupiedPortFails asserts --port on a busy port errors out
// instead of silently rebinding elsewhere.
func TestRunOccupiedPortFails(t *testing.T) {
	setTestUserCache(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserving port: %v", err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	var out strings.Builder
	app, err := run([]string{"--no-browser", "--port", strconv.Itoa(port)}, &out, func(string) error { return nil })
	if err == nil {
		app.Close()
		t.Fatalf("run on occupied port %d succeeded, want error", port)
	}
}

func TestRunCleansOnlyStaleJobWork(t *testing.T) {
	setTestUserCache(t)
	root, err := job.PrepareTempRoot("")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	createWork := func(id string, age time.Duration) string {
		t.Helper()
		dir, err := job.CreateWorkDir(root, id)
		if err != nil {
			t.Fatal(err)
		}
		stamp := now.Add(-age)
		if err := os.Chtimes(dir, stamp, stamp); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	stale := createWork("job-startup-stale", job.DefaultStaleWorkTTL+time.Hour)
	fresh := createWork("job-startup-fresh", job.DefaultStaleWorkTTL-time.Hour)
	recovery := createWork("job-startup-recovery", job.DefaultStaleWorkTTL+time.Hour)
	if err := job.MarkRecovery(recovery, "job-startup-recovery", now.Add(-job.DefaultRecoveryTTL+time.Hour)); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	app, err := run([]string{"--no-browser", "--port", "0"}, &out, func(string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	if _, err := os.Stat(stale); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale work remains: %v", err)
	}
	for label, path := range map[string]string{"fresh work": fresh, "recovery work": recovery} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("%s was removed: %v", label, err)
		}
	}
}

func setTestUserCache(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
}
