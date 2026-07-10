package model

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestDownloadSuccessAndUnknownContentLength(t *testing.T) {
	content := []byte("small deterministic model")
	entry := tinyEntry(content)
	var requestCount int
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requestCount++
		if request.Host != "huggingface.co" {
			t.Errorf("request Host = %q, want huggingface.co", request.Host)
		}
		wantPath := "/ggml-org/whisper-vad/resolve/9ffd54a1e1ee413ddf265af9913beaf518d1639b/ggml-silero-v6.2.0.bin"
		if request.URL.Path != wantPath || request.URL.RawQuery != "" {
			t.Errorf("request target = %q?%s, want %q", request.URL.Path, request.URL.RawQuery, wantPath)
		}
		writer.(http.Flusher).Flush()
		_, _ = writer.Write(content)
	}))
	defer server.Close()

	manager := newTinyManager(t, entry, clientForServer(t, server))
	defer manager.Close()
	snapshot := downloadAndWait(t, manager, entry.descriptor.Name)
	if snapshot.State != StateDownloaded || snapshot.ErrorCode != ErrorCodeNone || snapshot.BytesDownloaded != int64(len(content)) {
		t.Fatalf("terminal snapshot = %+v", snapshot)
	}
	if requestCount != 1 {
		t.Fatalf("request count = %d, want 1", requestCount)
	}
	got, err := os.ReadFile(filepath.Join(manager.directory, entry.descriptor.Filename))
	if err != nil {
		t.Fatalf("read downloaded model: %v", err)
	}
	if string(got) != string(content) {
		t.Fatalf("downloaded content = %q, want %q", got, content)
	}
	assertNoOwnedPartials(t, manager.directory, manager.entries)
}

func TestDownloadRejectsChecksumStatusAndLengths(t *testing.T) {
	content := []byte("expected bytes")
	tests := []struct {
		name      string
		entry     manifestEntry
		handler   http.HandlerFunc
		wantCode  ErrorCode
		wantState State
	}{
		{
			name:  "checksum mismatch",
			entry: tinyEntry([]byte("different expected bytes")),
			handler: func(writer http.ResponseWriter, _ *http.Request) {
				_, _ = writer.Write(content)
			},
			wantCode:  ErrorCodeSizeMismatch,
			wantState: StateMissing,
		},
		{
			name:  "checksum mismatch exact size",
			entry: tinyEntry([]byte("EXPECTED BYTES")),
			handler: func(writer http.ResponseWriter, _ *http.Request) {
				_, _ = writer.Write(content)
			},
			wantCode:  ErrorCodeChecksum,
			wantState: StateMissing,
		},
		{
			name:  "status",
			entry: tinyEntry(content),
			handler: func(writer http.ResponseWriter, _ *http.Request) {
				writer.WriteHeader(http.StatusForbidden)
			},
			wantCode:  ErrorCodeHTTPStatus,
			wantState: StateMissing,
		},
		{
			name:  "content length precheck",
			entry: tinyEntry(content),
			handler: func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set("Content-Length", fmt.Sprint(len(content)+1))
				writer.WriteHeader(http.StatusOK)
			},
			wantCode:  ErrorCodeContentLength,
			wantState: StateMissing,
		},
		{
			name:  "unknown length hard limit",
			entry: tinyEntry(content),
			handler: func(writer http.ResponseWriter, _ *http.Request) {
				writer.(http.Flusher).Flush()
				_, _ = writer.Write(append(append([]byte(nil), content...), 'x'))
			},
			wantCode:  ErrorCodeSizeMismatch,
			wantState: StateMissing,
		},
		{
			name:  "declared length smaller than manifest",
			entry: tinyEntry(content),
			handler: func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set("Content-Length", fmt.Sprint(len(content)-1))
				_, _ = writer.Write(content[:len(content)-1])
			},
			wantCode:  ErrorCodeSizeMismatch,
			wantState: StateMissing,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewTLSServer(test.handler)
			defer server.Close()
			manager := newTinyManager(t, test.entry, clientForServer(t, server))
			defer manager.Close()
			snapshot := downloadAndWait(t, manager, test.entry.descriptor.Name)
			if snapshot.State != test.wantState || snapshot.ErrorCode != test.wantCode {
				t.Fatalf("terminal snapshot = %+v, want state/code %s/%s", snapshot, test.wantState, test.wantCode)
			}
			if _, err := os.Lstat(filepath.Join(manager.directory, test.entry.descriptor.Filename)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("final file exists after rejection: %v", err)
			}
			assertNoOwnedPartials(t, manager.directory, manager.entries)
		})
	}
}

func TestInterruptedDownloadRemovesOwnedPartial(t *testing.T) {
	content := []byte("expected complete body")
	entry := tinyEntry(content)
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Length", fmt.Sprint(len(content)))
		_, _ = writer.Write(content[:4])
	}))
	defer server.Close()
	manager := newTinyManager(t, entry, clientForServer(t, server))
	defer manager.Close()
	snapshot := downloadAndWait(t, manager, entry.descriptor.Name)
	if snapshot.ErrorCode != ErrorCodeNetwork || snapshot.State != StateMissing {
		t.Fatalf("terminal snapshot = %+v, want missing/network_error", snapshot)
	}
	assertNoOwnedPartials(t, manager.directory, manager.entries)
}

func TestDownloadTotalAndIdleTimeoutsCleanAndUnlock(t *testing.T) {
	entry := tinyEntry([]byte("never completed"))
	tests := []struct {
		name  string
		total time.Duration
		idle  time.Duration
	}{
		{name: "total", total: 100 * time.Millisecond, idle: 5 * time.Second},
		{name: "idle", total: 5 * time.Second, idle: 100 * time.Millisecond},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				writer.(http.Flusher).Flush()
				<-request.Context().Done()
			}))
			defer server.Close()
			directory := t.TempDir()
			manager, err := NewManager(Config{
				Directory:           directory,
				Client:              clientForServer(t, server),
				DownloadTimeout:     test.total,
				DownloadIdleTimeout: test.idle,
				manifestOverride:    []manifestEntry{entry},
			})
			if err != nil {
				t.Fatalf("NewManager: %v", err)
			}
			defer manager.Close()
			snapshot := downloadAndWait(t, manager, entry.descriptor.Name)
			if snapshot.State != StateMissing || snapshot.ErrorCode != ErrorCodeTimeout {
				t.Fatalf("terminal snapshot = %+v, want missing/timeout", snapshot)
			}
			assertNoOwnedPartials(t, directory, manager.entries)
			lock, err := tryModelLock(directory, entry.descriptor.Filename, true)
			if err != nil {
				t.Fatalf("model remained locked: %v", err)
			}
			if err := lock.close(); err != nil {
				t.Fatalf("closing probe lock: %v", err)
			}
		})
	}
}

func TestRedirectPolicyAndLimit(t *testing.T) {
	content := []byte("redirected model")
	entry := tinyEntry(content)
	tests := []struct {
		name      string
		initial   string
		wantState State
		wantCode  ErrorCode
	}{
		{name: "allowed host", initial: "https://cdn-lfs.huggingface.co/final?signature=not-logged", wantState: StateDownloaded},
		{name: "exactly five redirects", initial: "https://huggingface.co/allowed/1", wantState: StateDownloaded},
		{name: "downgrade", initial: "http://huggingface.co/final", wantState: StateMissing, wantCode: ErrorCodeRedirectPolicy},
		{name: "disallowed host", initial: "https://example.com/final", wantState: StateMissing, wantCode: ErrorCodeRedirectPolicy},
		{name: "userinfo", initial: "https://user@huggingface.co/final", wantState: StateMissing, wantCode: ErrorCodeRedirectPolicy},
		{name: "custom port", initial: "https://huggingface.co:444/final", wantState: StateMissing, wantCode: ErrorCodeRedirectPolicy},
		{name: "more than five", initial: "https://huggingface.co/redirect/1", wantState: StateMissing, wantCode: ErrorCodeRedirectPolicy},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				switch {
				case request.URL.Path == sourceURL(entry).Path:
					http.Redirect(writer, request, test.initial, http.StatusFound)
				case request.URL.Path == "/final":
					_, _ = writer.Write(content)
				case strings.HasPrefix(request.URL.Path, "/redirect/"):
					var step int
					if _, err := fmt.Sscanf(request.URL.Path, "/redirect/%d", &step); err != nil {
						t.Errorf("parse redirect step: %v", err)
					}
					http.Redirect(writer, request, fmt.Sprintf("https://huggingface.co/redirect/%d", step+1), http.StatusFound)
				case strings.HasPrefix(request.URL.Path, "/allowed/"):
					var step int
					if _, err := fmt.Sscanf(request.URL.Path, "/allowed/%d", &step); err != nil {
						t.Errorf("parse allowed redirect step: %v", err)
					}
					if step == 5 {
						_, _ = writer.Write(content)
						return
					}
					http.Redirect(writer, request, fmt.Sprintf("https://huggingface.co/allowed/%d", step+1), http.StatusFound)
				default:
					writer.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			manager := newTinyManager(t, entry, clientForServer(t, server))
			defer manager.Close()
			snapshot := downloadAndWait(t, manager, entry.descriptor.Name)
			if snapshot.State != test.wantState || snapshot.ErrorCode != test.wantCode {
				t.Fatalf("terminal snapshot = %+v, want state/code %s/%s", snapshot, test.wantState, test.wantCode)
			}
		})
	}
}

func TestDuplicateAndCrossManagerDownloadsAreExcluded(t *testing.T) {
	entry := tinyEntry([]byte("blocked body"))
	received := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.(http.Flusher).Flush()
		close(received)
		<-release
		_, _ = writer.Write([]byte("blocked body"))
	}))
	defer server.Close()
	directory := t.TempDir()
	client := clientForServer(t, server)
	first := newTinyManagerIn(t, directory, entry, client)
	defer first.Close()
	second := newTinyManagerIn(t, directory, entry, client)
	defer second.Close()
	if _, err := first.Download(entry.descriptor.Name); err != nil {
		t.Fatalf("first Download: %v", err)
	}
	<-received
	if _, err := first.Download(entry.descriptor.Name); !errors.Is(err, ErrDownloadInProgress) {
		t.Fatalf("duplicate Download error = %v, want ErrDownloadInProgress", err)
	}
	if _, err := second.Download(entry.descriptor.Name); !errors.Is(err, ErrModelBusy) {
		t.Fatalf("cross-manager Download error = %v, want ErrModelBusy", err)
	}
	close(release)
	waitForTerminal(t, first, entry.descriptor.Name)
}

func TestPartialNamesDoNotCollide(t *testing.T) {
	content := []byte("model")
	entry := tinyEntry(content)
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write(content)
	}))
	defer server.Close()
	directory := t.TempDir()
	collision := filepath.Join(directory, "."+entry.descriptor.Filename+".collision.partial")
	if err := os.WriteFile(collision, []byte("owned by another process"), 0o600); err != nil {
		t.Fatalf("create collision partial: %v", err)
	}
	manager := newTinyManagerIn(t, directory, entry, clientForServer(t, server))
	defer manager.Close()
	snapshot := downloadAndWait(t, manager, entry.descriptor.Name)
	if snapshot.State != StateDownloaded {
		t.Fatalf("terminal snapshot = %+v", snapshot)
	}
	got, err := os.ReadFile(collision)
	if err != nil || string(got) != "owned by another process" {
		t.Fatalf("collision partial changed: content=%q error=%v", got, err)
	}
}

func TestDownloadReplacesInvalidFinal(t *testing.T) {
	content := []byte("valid")
	entry := tinyEntry(content)
	directory := t.TempDir()
	finalPath := filepath.Join(directory, entry.descriptor.Filename)
	if err := os.WriteFile(finalPath, []byte("wrong"), 0o600); err != nil {
		t.Fatalf("write invalid final: %v", err)
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write(content)
	}))
	defer server.Close()
	manager := newTinyManagerIn(t, directory, entry, clientForServer(t, server))
	defer manager.Close()
	snapshot := downloadAndWait(t, manager, entry.descriptor.Name)
	if snapshot.State != StateDownloaded {
		t.Fatalf("terminal snapshot = %+v", snapshot)
	}
	got, err := os.ReadFile(finalPath)
	if err != nil || string(got) != string(content) {
		t.Fatalf("replacement content = %q, error=%v", got, err)
	}
}

func TestStartupCleanupAgeAndSymlinkPolicy(t *testing.T) {
	entry := tinyEntry([]byte("model"))
	directory := t.TempDir()
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	oldOwned := filepath.Join(directory, "."+entry.descriptor.Filename+".old.partial")
	recentOwned := filepath.Join(directory, "."+entry.descriptor.Filename+".recent.partial")
	unowned := filepath.Join(directory, ".other.old.partial")
	for _, path := range []string{oldOwned, recentOwned, unowned} {
		if err := os.WriteFile(path, []byte("partial"), 0o600); err != nil {
			t.Fatalf("write partial: %v", err)
		}
	}
	oldTime := now.Add(-25 * time.Hour)
	if err := os.Chtimes(oldOwned, oldTime, oldTime); err != nil {
		t.Fatalf("age owned partial: %v", err)
	}
	if err := os.Chtimes(unowned, oldTime, oldTime); err != nil {
		t.Fatalf("age unowned partial: %v", err)
	}
	var symlink string
	if runtime.GOOS != "windows" {
		target := filepath.Join(directory, "target")
		if err := os.WriteFile(target, []byte("target"), 0o600); err != nil {
			t.Fatalf("write symlink target: %v", err)
		}
		symlink = filepath.Join(directory, "."+entry.descriptor.Filename+".link.partial")
		if err := os.Symlink(target, symlink); err != nil {
			t.Fatalf("create partial symlink: %v", err)
		}
	}
	manager, err := NewManager(Config{Directory: directory, now: func() time.Time { return now }, manifestOverride: []manifestEntry{entry}})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	manager.Close()
	if _, err := os.Lstat(oldOwned); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old owned partial was not removed: %v", err)
	}
	for _, path := range []string{recentOwned, unowned, symlink} {
		if path == "" {
			continue
		}
		if _, err := os.Lstat(path); err != nil {
			t.Errorf("preserved path %q: %v", filepath.Base(path), err)
		}
	}
}

func TestStartupCleanupPreservesLockedActivePartial(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("cross-process locks are unsupported on Windows")
	}
	entry := tinyEntry([]byte("model"))
	directory := t.TempDir()
	partial := filepath.Join(directory, "."+entry.descriptor.Filename+".active.partial")
	if err := os.WriteFile(partial, []byte("partial"), 0o600); err != nil {
		t.Fatalf("write partial: %v", err)
	}
	oldTime := time.Now().Add(-25 * time.Hour)
	if err := os.Chtimes(partial, oldTime, oldTime); err != nil {
		t.Fatalf("age partial: %v", err)
	}
	lock, err := tryModelLock(directory, entry.descriptor.Filename, true)
	if err != nil {
		t.Fatalf("lock active model: %v", err)
	}
	manager, err := NewManager(Config{Directory: directory, manifestOverride: []manifestEntry{entry}})
	if err != nil {
		_ = lock.close()
		t.Fatalf("NewManager: %v", err)
	}
	manager.Close()
	if _, err := os.Lstat(partial); err != nil {
		t.Errorf("active partial was removed: %v", err)
	}
	if err := lock.close(); err != nil {
		t.Fatalf("close active lock: %v", err)
	}
}

func TestUnsafeDirectoryIsRepairedAndLockSymlinkIsRejected(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission and symlink policy")
	}
	entry := tinyEntry([]byte("model"))
	unsafeDirectory := t.TempDir()
	if err := os.Chmod(unsafeDirectory, 0o755); err != nil {
		t.Fatalf("chmod unsafe directory: %v", err)
	}
	repaired, err := NewManager(Config{Directory: unsafeDirectory, manifestOverride: []manifestEntry{entry}})
	if err != nil {
		t.Fatalf("NewManager repairing directory: %v", err)
	}
	repaired.Close()
	info, err := os.Lstat(unsafeDirectory)
	if err != nil {
		t.Fatalf("inspect repaired directory: %v", err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("repaired directory mode = %v", info.Mode().Perm())
	}

	directory := t.TempDir()
	manager := newTinyManagerIn(t, directory, entry, nil)
	defer manager.Close()
	target := filepath.Join(directory, "lock-target")
	if err := os.WriteFile(target, []byte("target"), 0o600); err != nil {
		t.Fatalf("write lock target: %v", err)
	}
	lockPath := filepath.Join(directory, "."+entry.descriptor.Filename+".lock")
	if err := os.Symlink(target, lockPath); err != nil {
		t.Fatalf("create lock symlink: %v", err)
	}
	if _, err := manager.Snapshot(entry.descriptor.Name); err == nil {
		t.Fatal("Snapshot silently accepted an unsafe lock path")
	}
}

func TestLeasesRevalidateAndBlockDelete(t *testing.T) {
	content := []byte("valid model")
	entry := tinyEntry(content)
	directory := t.TempDir()
	path := filepath.Join(directory, entry.descriptor.Filename)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("write model: %v", err)
	}
	first := newTinyManagerIn(t, directory, entry, nil)
	defer first.Close()
	second := newTinyManagerIn(t, directory, entry, nil)
	defer second.Close()
	lease, err := first.Acquire(entry.descriptor.Name)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if lease.Path() != path {
		t.Fatalf("lease path = %q, want %q", lease.Path(), path)
	}
	if err := first.Delete(entry.descriptor.Name); !errors.Is(err, ErrModelInUse) {
		t.Fatalf("local Delete error = %v, want ErrModelInUse", err)
	}
	if err := second.Delete(entry.descriptor.Name); !errors.Is(err, ErrModelInUse) {
		t.Fatalf("cross-manager Delete error = %v, want ErrModelInUse", err)
	}
	if err := lease.Close(); err != nil {
		t.Fatalf("close lease: %v", err)
	}
	if err := first.Delete(entry.descriptor.Name); err != nil {
		t.Fatalf("Delete after lease: %v", err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("model still exists after Delete: %v", err)
	}

	if err := os.WriteFile(path, []byte("bad content"), 0o600); err != nil {
		t.Fatalf("write invalid model: %v", err)
	}
	if _, err := first.Acquire(entry.descriptor.Name); !errors.Is(err, ErrModelUnavailable) {
		t.Fatalf("Acquire invalid model error = %v, want ErrModelUnavailable", err)
	}
	snapshot, err := first.Snapshot(entry.descriptor.Name)
	if err != nil || snapshot.State != StateInvalid {
		t.Fatalf("invalid snapshot = %+v, error=%v", snapshot, err)
	}
}

func TestFullValidationDoesNotBlockManagerState(t *testing.T) {
	entry := tinyEntry([]byte("model"))
	tests := []struct {
		name          string
		validated     State
		run           func(*Manager) error
		wantOperation error
	}{
		{
			name:      "download short-circuit",
			validated: StateDownloaded,
			run: func(manager *Manager) error {
				_, err := manager.Download(entry.descriptor.Name)
				return err
			},
			wantOperation: ErrAlreadyDownloaded,
		},
		{
			name:      "acquire revalidation",
			validated: StateInvalid,
			run: func(manager *Manager) error {
				_, err := manager.Acquire(entry.descriptor.Name)
				return err
			},
			wantOperation: ErrModelUnavailable,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			validationStarted := make(chan struct{})
			releaseValidation := make(chan struct{})
			manager, err := NewManager(Config{
				Directory:        t.TempDir(),
				manifestOverride: []manifestEntry{entry},
				validateFile: func(string, Descriptor) State {
					close(validationStarted)
					<-releaseValidation
					return test.validated
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			defer manager.Close()

			operationDone := make(chan error, 1)
			go func() { operationDone <- test.run(manager) }()
			select {
			case <-validationStarted:
			case <-time.After(5 * time.Second):
				t.Fatal("full validation did not start")
			}

			snapshotDone := make(chan error, 1)
			go func() {
				_, err := manager.Snapshot(entry.descriptor.Name)
				snapshotDone <- err
			}()
			var snapshotErr error
			responsive := false
			select {
			case snapshotErr = <-snapshotDone:
				responsive = true
			case <-time.After(time.Second):
			}
			close(releaseValidation)
			operationErr := <-operationDone
			if !responsive {
				t.Fatal("Snapshot blocked on a full model checksum validation")
			}
			if snapshotErr != nil {
				t.Fatalf("Snapshot during validation: %v", snapshotErr)
			}
			if !errors.Is(operationErr, test.wantOperation) {
				t.Fatalf("operation error = %v, want %v", operationErr, test.wantOperation)
			}
		})
	}
}

func TestCloseWaitsForFullValidation(t *testing.T) {
	entry := tinyEntry([]byte("model"))
	validationStarted := make(chan struct{})
	releaseValidation := make(chan struct{})
	manager, err := NewManager(Config{
		Directory:        t.TempDir(),
		manifestOverride: []manifestEntry{entry},
		validateFile: func(string, Descriptor) State {
			close(validationStarted)
			<-releaseValidation
			return StateInvalid
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	operationDone := make(chan error, 1)
	go func() {
		_, err := manager.Acquire(entry.descriptor.Name)
		operationDone <- err
	}()
	select {
	case <-validationStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("full validation did not start")
	}

	closeDone := make(chan struct{})
	go func() {
		manager.Close()
		close(closeDone)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		manager.mu.Lock()
		closed := manager.closed
		manager.mu.Unlock()
		if closed {
			break
		}
		if time.Now().After(deadline) {
			close(releaseValidation)
			t.Fatal("Close did not begin")
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case <-closeDone:
		close(releaseValidation)
		t.Fatal("Close returned while full validation was active")
	default:
	}

	close(releaseValidation)
	select {
	case <-closeDone:
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not finish after validation ended")
	}
	if err := <-operationDone; !errors.Is(err, ErrManagerClosed) {
		t.Fatalf("Acquire error = %v, want ErrManagerClosed", err)
	}
}

func TestSubscriberOverflowReconnectAndLimit(t *testing.T) {
	entry := tinyEntry([]byte("model"))
	manager, err := NewManager(Config{
		Directory:          t.TempDir(),
		SubscriberCapacity: 1,
		SubscriberLimit:    2,
		manifestOverride:   []manifestEntry{entry},
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	defer manager.Close()
	manager.mu.Lock()
	r := manager.records[entry.descriptor.Name]
	r.state = StateDownloading
	manager.mu.Unlock()

	initial, events, unsubscribe, err := manager.Subscribe(entry.descriptor.Name)
	if err != nil || initial.State != StateDownloading {
		t.Fatalf("Subscribe = %+v, error=%v", initial, err)
	}
	defer unsubscribe()
	_, _, unsubscribeSecond, err := manager.Subscribe(entry.descriptor.Name)
	if err != nil {
		t.Fatalf("second Subscribe: %v", err)
	}
	defer unsubscribeSecond()
	if _, _, _, err := manager.Subscribe(entry.descriptor.Name); !errors.Is(err, ErrSubscriberLimit) {
		t.Fatalf("third Subscribe error = %v, want ErrSubscriberLimit", err)
	}
	manager.progress(r, 1)
	manager.progress(r, 2)
	first, ok := <-events
	if !ok || first.BytesDownloaded != 1 {
		t.Fatalf("first event = %+v, open=%v", first, ok)
	}
	if _, ok := <-events; ok {
		t.Fatal("overflowed subscriber channel remained open")
	}
	manager.mu.Lock()
	r.state = StateMissing
	manager.closeSubscribersLocked(entry.descriptor.Name)
	manager.mu.Unlock()
	reconnected, terminalEvents, _, err := manager.Subscribe(entry.descriptor.Name)
	if err != nil || reconnected.State != StateMissing || reconnected.BytesDownloaded != 2 {
		t.Fatalf("reconnected snapshot = %+v, error=%v", reconnected, err)
	}
	if _, ok := <-terminalEvents; ok {
		t.Fatal("terminal reconnect channel remained open")
	}
}

func TestCloseCancelsDownloadWaitsAndCleansPartial(t *testing.T) {
	entry := tinyEntry([]byte("never completed"))
	started := make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.(http.Flusher).Flush()
		close(started)
		<-request.Context().Done()
	}))
	defer server.Close()
	manager := newTinyManager(t, entry, clientForServer(t, server))
	if _, err := manager.Download(entry.descriptor.Name); err != nil {
		t.Fatalf("Download: %v", err)
	}
	_, events, unsubscribe, err := manager.Subscribe(entry.descriptor.Name)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer unsubscribe()
	<-started
	done := make(chan struct{})
	go func() {
		manager.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Close did not cancel and wait for download")
	}
	for range events {
	}
	manager.mu.Lock()
	snapshot := snapshotOf(manager.records[entry.descriptor.Name])
	manager.mu.Unlock()
	if snapshot.State != StateMissing || snapshot.ErrorCode != ErrorCodeCancelled {
		t.Fatalf("snapshot after Close = %+v, want missing/cancelled", snapshot)
	}
	assertNoOwnedPartials(t, manager.directory, manager.entries)
	manager.Close()
}

func tinyEntry(content []byte) manifestEntry {
	sum := sha256.Sum256(content)
	return manifestEntry{
		descriptor: Descriptor{
			Name:     ModelSileroVAD,
			Filename: "ggml-silero-v6.2.0.bin",
			Size:     int64(len(content)),
			SHA256:   hex.EncodeToString(sum[:]),
		},
		repository: "ggml-org/whisper-vad",
		commit:     "9ffd54a1e1ee413ddf265af9913beaf518d1639b",
	}
}

func newTinyManager(t *testing.T, entry manifestEntry, client *http.Client) *Manager {
	t.Helper()
	return newTinyManagerIn(t, t.TempDir(), entry, client)
}

func newTinyManagerIn(t *testing.T, directory string, entry manifestEntry, client *http.Client) *Manager {
	t.Helper()
	manager, err := NewManager(Config{Directory: directory, Client: client, manifestOverride: []manifestEntry{entry}})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return manager
}

func clientForServer(t *testing.T, server *httptest.Server) *http.Client {
	t.Helper()
	transport, ok := server.Client().Transport.(*http.Transport)
	if !ok {
		t.Fatalf("httptest transport type = %T", server.Client().Transport)
	}
	transport = transport.Clone()
	transport.Proxy = nil
	transport.TLSClientConfig = transport.TLSClientConfig.Clone()
	// Requests intentionally retain the pinned production host while this test
	// transport dials the loopback TLS server.
	transport.TLSClientConfig.InsecureSkipVerify = true
	serverAddress := server.Listener.Addr().String()
	dialer := &net.Dialer{}
	transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return dialer.DialContext(ctx, network, serverAddress)
	}
	return &http.Client{Transport: transport}
}

func downloadAndWait(t *testing.T, manager *Manager, name string) Snapshot {
	t.Helper()
	if _, err := manager.Download(name); err != nil {
		t.Fatalf("Download: %v", err)
	}
	return waitForTerminal(t, manager, name)
}

func waitForTerminal(t *testing.T, manager *Manager, name string) Snapshot {
	t.Helper()
	snapshot, events, unsubscribe, err := manager.Subscribe(name)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer unsubscribe()
	for snapshot.State == StateDownloading {
		if _, ok := <-events; !ok {
			break
		}
		snapshot, err = manager.Snapshot(name)
		if err != nil {
			t.Fatalf("Snapshot: %v", err)
		}
	}
	if snapshot.State == StateDownloading {
		snapshot, err = manager.Snapshot(name)
		if err != nil {
			t.Fatalf("terminal Snapshot: %v", err)
		}
	}
	return snapshot
}

func assertNoOwnedPartials(t *testing.T, directory string, entries []manifestEntry) {
	t.Helper()
	directoryEntries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	var partials []string
	for _, entry := range directoryEntries {
		if ownedPartialName(entry.Name(), entries) {
			partials = append(partials, entry.Name())
		}
	}
	if len(partials) != 0 {
		t.Fatalf("owned partials remain: %s", strings.Join(partials, ", "))
	}
}

func TestLeaseCloseConcurrentIsIdempotent(t *testing.T) {
	content := []byte("valid")
	entry := tinyEntry(content)
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, entry.descriptor.Filename), content, 0o600); err != nil {
		t.Fatalf("write model: %v", err)
	}
	manager := newTinyManagerIn(t, directory, entry, nil)
	defer manager.Close()
	lease, err := manager.Acquire(entry.descriptor.Name)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	var wait sync.WaitGroup
	for range 8 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_ = lease.Close()
		}()
	}
	wait.Wait()
	if err := manager.Delete(entry.descriptor.Name); err != nil {
		t.Fatalf("Delete after concurrent Lease.Close: %v", err)
	}
}
