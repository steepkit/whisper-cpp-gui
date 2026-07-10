package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/steepkit/whisper-cpp-gui/internal/job"
)

type jobAPIRunnerFunc func(context.Context, job.RunRequest, job.Observer) (job.RunResult, error)

func (f jobAPIRunnerFunc) Run(ctx context.Context, request job.RunRequest, observer job.Observer) (job.RunResult, error) {
	return f(ctx, request, observer)
}

func TestJobEventsTerminalSnapshotAndAuthentication(t *testing.T) {
	outputDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(outputDir, "transcript.txt"), []byte("finished\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	manager, root := newJobAPIManager(t, jobAPIRunnerFunc(func(_ context.Context, _ job.RunRequest, _ job.Observer) (job.RunResult, error) {
		return job.RunResult{FinalOutputDir: outputDir, OutputFiles: []string{"transcript.txt"}}, nil
	}), job.Config{})
	id := "job-sse-terminal"
	submitJobAPIRequest(t, manager, root, id, []string{"txt"})
	want := waitForServerJob(t, manager, id)

	srv := newJobAPIServer(t, manager, 8123)
	host := "127.0.0.1:8123"
	recorder := doRequest(srv, http.MethodGet, "/api/jobs/"+id+"/events?token="+testToken, host, "", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("SSE status = %d: %s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Content-Type"); got != "text/event-stream; charset=utf-8" {
		t.Fatalf("Content-Type = %q", got)
	}
	name, payload := parseSSEFrame(t, bufio.NewReader(recorder.Body))
	if name != "snapshot" {
		t.Fatalf("first event = %q, want snapshot", name)
	}
	var snapshot job.Snapshot
	if err := json.Unmarshal(payload, &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.ID != id || snapshot.Status != job.StatusDone || len(snapshot.OutputFiles) != 1 || snapshot.OutputFiles[0] != "transcript.txt" {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if snapshot.OutputDir != "" || strings.Contains(string(payload), outputDir) {
		t.Fatalf("SSE exposed private output directory: %s", payload)
	}
	if !snapshot.FinishedAt.Equal(want.FinishedAt) {
		t.Fatalf("snapshot finished_at = %v, want %v", snapshot.FinishedAt, want.FinishedAt)
	}

	for _, target := range []string{
		"/api/jobs/" + id + "/events",
		"/api/jobs/" + id + "/events?token=wrong",
	} {
		if got := doRequest(srv, http.MethodGet, target, host, "", "").Code; got != http.StatusUnauthorized {
			t.Errorf("%s status = %d, want 401", target, got)
		}
	}
	if got := doRequest(srv, http.MethodGet, "/api/jobs/missing/events?token="+testToken, host, "", "").Code; got != http.StatusNotFound {
		t.Fatalf("missing SSE status = %d, want 404", got)
	}
}

func TestJobEventsStreamsEventsAndBoundsSubscribers(t *testing.T) {
	started := make(chan struct{})
	emit := make(chan struct{})
	release := make(chan struct{})
	manager, root := newJobAPIManager(t, jobAPIRunnerFunc(func(ctx context.Context, _ job.RunRequest, observer job.Observer) (job.RunResult, error) {
		close(started)
		select {
		case <-emit:
		case <-ctx.Done():
			return job.RunResult{}, context.Cause(ctx)
		}
		observer.Phase(job.PhaseTranscribing)
		observer.Progress(42)
		observer.Log(job.LogStreamStderr, "progress line")
		select {
		case <-release:
			return job.RunResult{}, nil
		case <-ctx.Done():
			return job.RunResult{}, context.Cause(ctx)
		}
	}), job.Config{SubscriberLimit: 1})
	id := "job-sse-live"
	submitJobAPIRequest(t, manager, root, id, nil)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("runner did not start")
	}

	srv := newJobAPIServer(t, manager, 0)
	baseURL, err := srv.Start()
	if err != nil {
		t.Fatal(err)
	}
	requestURL := baseURL + "/api/jobs/" + id + "/events?token=" + testToken
	response, err := http.Get(requestURL)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	reader := bufio.NewReader(response.Body)
	name, _ := parseSSEFrame(t, reader)
	if name != "snapshot" {
		t.Fatalf("first event = %q, want snapshot", name)
	}

	second, err := http.Get(requestURL)
	if err != nil {
		t.Fatal(err)
	}
	if second.StatusCode != http.StatusTooManyRequests {
		body, _ := io.ReadAll(second.Body)
		_ = second.Body.Close()
		t.Fatalf("second subscriber status = %d: %s", second.StatusCode, body)
	}
	_ = second.Body.Close()
	_ = response.Body.Close()

	var replacement *http.Response
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		replacement, err = http.Get(requestURL)
		if err != nil {
			t.Fatal(err)
		}
		if replacement.StatusCode == http.StatusOK {
			break
		}
		_ = replacement.Body.Close()
		replacement = nil
		time.Sleep(time.Millisecond)
	}
	if replacement == nil {
		t.Fatal("subscriber slot was not released after abrupt disconnect")
	}
	response = replacement
	reader = bufio.NewReader(response.Body)
	name, _ = parseSSEFrame(t, reader)
	if name != "snapshot" {
		t.Fatalf("reconnect first event = %q, want snapshot", name)
	}

	close(emit)
	wantNames := []string{"state", "progress", "log"}
	for _, wantName := range wantNames {
		name, payload := parseSSEFrame(t, reader)
		if name != wantName {
			t.Fatalf("event = %q, want %q (%s)", name, wantName, payload)
		}
	}
	close(release)
	name, payload := parseSSEFrame(t, reader)
	if name != "snapshot" {
		t.Fatalf("terminal event = %q, want snapshot (%s)", name, payload)
	}
	var terminal job.Snapshot
	if err := json.Unmarshal(payload, &terminal); err != nil {
		t.Fatal(err)
	}
	if terminal.Status != job.StatusDone || terminal.CleanupPending {
		t.Fatalf("terminal snapshot = %+v", terminal)
	}
	_ = response.Body.Close()
	waitForJobTerminalSnapshot(t, manager, id)
}

func TestJobEventsOverflowEndsWithFreshSnapshot(t *testing.T) {
	started := make(chan struct{})
	emit := make(chan struct{})
	emitted := make(chan struct{})
	releaseRunner := make(chan struct{})
	manager, root := newJobAPIManager(t, jobAPIRunnerFunc(func(ctx context.Context, _ job.RunRequest, observer job.Observer) (job.RunResult, error) {
		close(started)
		select {
		case <-emit:
		case <-ctx.Done():
			return job.RunResult{}, context.Cause(ctx)
		}
		for progress := 1; progress <= 100; progress++ {
			observer.Progress(progress)
		}
		close(emitted)
		select {
		case <-releaseRunner:
			return job.RunResult{}, nil
		case <-ctx.Done():
			return job.RunResult{}, context.Cause(ctx)
		}
	}), job.Config{SubscriberCapacity: 1})
	id := "job-sse-overflow"
	submitJobAPIRequest(t, manager, root, id, nil)
	<-started

	srv := newJobAPIServer(t, manager, 8123)
	writer := newBlockingSSEWriter()
	request := httptest.NewRequest(http.MethodGet, "/api/jobs/"+id+"/events?token="+testToken, nil)
	request.Host = "127.0.0.1:8123"
	done := make(chan struct{})
	go func() {
		defer close(done)
		srv.Handler().ServeHTTP(writer, request)
	}()
	select {
	case <-writer.firstWrite:
	case <-time.After(5 * time.Second):
		t.Fatal("initial SSE snapshot was not written")
	}

	close(emit)
	select {
	case <-writer.secondWrite:
	case <-time.After(5 * time.Second):
		t.Fatal("incremental SSE event was not written")
	}
	select {
	case <-emitted:
	case <-time.After(5 * time.Second):
		t.Fatal("runner did not finish emitting overflow events")
	}
	close(writer.releaseSecond)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("overflowed SSE handler did not return")
	}

	body := writer.bodyString()
	lastSnapshot := strings.LastIndex(body, "event: snapshot\n")
	if lastSnapshot <= 0 {
		t.Fatalf("fresh overflow snapshot missing: %s", body)
	}
	if !strings.Contains(body[lastSnapshot:], `"progress":100`) {
		t.Fatalf("overflow snapshot is stale: %s", body[lastSnapshot:])
	}
	close(releaseRunner)
	waitForJobTerminalSnapshot(t, manager, id)
}

func TestJobCancelAndOutputsAPI(t *testing.T) {
	started := make(chan struct{})
	manager, root := newJobAPIManager(t, jobAPIRunnerFunc(func(ctx context.Context, _ job.RunRequest, _ job.Observer) (job.RunResult, error) {
		close(started)
		<-ctx.Done()
		return job.RunResult{}, context.Cause(ctx)
	}), job.Config{})
	id := "job-cancel-api"
	submitJobAPIRequest(t, manager, root, id, nil)
	<-started
	srv := newJobAPIServer(t, manager, 8123)
	host := "127.0.0.1:8123"

	if got := doRequest(srv, http.MethodPost, "/api/jobs/"+id+"/cancel", host, "", testToken).Code; got != http.StatusNoContent {
		t.Fatalf("cancel status = %d, want 204", got)
	}
	snapshot := waitForServerJob(t, manager, id)
	if snapshot.Status != job.StatusCancelled {
		t.Fatalf("cancelled snapshot = %+v", snapshot)
	}
	if got := doRequest(srv, http.MethodPost, "/api/jobs/"+id+"/cancel", host, "", testToken).Code; got != http.StatusConflict {
		t.Fatalf("terminal cancel status = %d, want 409", got)
	}

	recorder := doRequest(srv, http.MethodGet, "/api/jobs/"+id+"/outputs", host, "", testToken)
	if recorder.Code != http.StatusOK {
		t.Fatalf("outputs status = %d: %s", recorder.Code, recorder.Body.String())
	}
	var outputs jobOutputsResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &outputs); err != nil {
		t.Fatal(err)
	}
	if outputs.ID != id || outputs.Status != job.StatusCancelled || outputs.Outputs == nil || len(outputs.Outputs) != 0 {
		t.Fatalf("outputs response = %+v", outputs)
	}
	if got := doRequest(srv, http.MethodGet, "/api/jobs/missing/outputs", host, "", testToken).Code; got != http.StatusNotFound {
		t.Fatalf("missing outputs status = %d, want 404", got)
	}
}

func TestDownloadAllowsOnlyRecordedRegularOutput(t *testing.T) {
	outputDir := t.TempDir()
	recorded := filepath.Join(outputDir, "transcript.txt")
	if err := os.WriteFile(recorded, []byte("allowed output\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	manager, root := newJobAPIManager(t, jobAPIRunnerFunc(func(_ context.Context, _ job.RunRequest, _ job.Observer) (job.RunResult, error) {
		return job.RunResult{FinalOutputDir: outputDir, OutputFiles: []string{"transcript.txt"}}, nil
	}), job.Config{})
	id := "job-download-api"
	submitJobAPIRequest(t, manager, root, id, []string{"txt"})
	waitForServerJob(t, manager, id)
	srv := newJobAPIServer(t, manager, 8123)
	host := "127.0.0.1:8123"

	recorder := doRequest(srv, http.MethodGet, "/api/download/"+id+"/transcript.txt", host, "", testToken)
	if recorder.Code != http.StatusOK || recorder.Body.String() != "allowed output\n" {
		t.Fatalf("download = %d, %q", recorder.Code, recorder.Body.String())
	}
	if disposition := recorder.Header().Get("Content-Disposition"); !strings.Contains(disposition, "attachment") || !strings.Contains(disposition, "transcript.txt") {
		t.Fatalf("Content-Disposition = %q", disposition)
	}
	if got := recorder.Header().Get("Content-Type"); got != "text/plain; charset=utf-8" {
		t.Fatalf("Content-Type = %q", got)
	}

	cases := []struct {
		name   string
		target string
		token  string
	}{
		{name: "unrecorded", target: "/api/download/" + id + "/outside.txt", token: testToken},
		{name: "missing job", target: "/api/download/missing/transcript.txt", token: testToken},
		{name: "query token rejected", target: "/api/download/" + id + "/transcript.txt?token=" + testToken},
		{name: "encoded traversal", target: "/api/download/" + id + "/..%2Foutside.txt", token: testToken},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			recorder := doRequest(srv, http.MethodGet, test.target, host, "", test.token)
			if recorder.Code >= 200 && recorder.Code < 300 {
				t.Fatalf("unexpected success: %d, %q", recorder.Code, recorder.Body.String())
			}
			if strings.Contains(recorder.Body.String(), "secret") {
				t.Fatal("response exposed outside content")
			}
		})
	}

	if err := os.Remove(recorded); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, recorded); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	recorder = doRequest(srv, http.MethodGet, "/api/download/"+id+"/transcript.txt", host, "", testToken)
	if recorder.Code != http.StatusNotFound || strings.Contains(recorder.Body.String(), "secret") {
		t.Fatalf("symlink download = %d, %q", recorder.Code, recorder.Body.String())
	}
}

func newJobAPIManager(t *testing.T, runner job.Runner, config job.Config) (*job.Manager, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "jobs")
	config.TempRoot = root
	manager, err := job.NewManager(runner, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)
	return manager, root
}

func newJobAPIServer(t *testing.T, manager *job.Manager, port int) *Server {
	t.Helper()
	srv, err := New(Config{
		Port:   port,
		Token:  testToken,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Jobs:   manager,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return srv
}

func submitJobAPIRequest(t *testing.T, manager *job.Manager, root, id string, formats []string) {
	t.Helper()
	workDir, err := job.CreateWorkDir(root, id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Submit(job.RunRequest{ID: id, WorkDir: workDir, OutputFormats: formats}); err != nil {
		t.Fatal(err)
	}
}

func waitForJobTerminalSnapshot(t *testing.T, manager *job.Manager, id string) job.Snapshot {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		snapshot, err := manager.Snapshot(id)
		if err == nil && (snapshot.Status == job.StatusDone || snapshot.Status == job.StatusFailed || snapshot.Status == job.StatusCancelled) && !snapshot.CleanupPending {
			return snapshot
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for terminal job %s", id)
	return job.Snapshot{}
}

func parseSSEFrame(t *testing.T, reader *bufio.Reader) (string, []byte) {
	t.Helper()
	var name string
	var data []byte
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read SSE frame: %v", err)
		}
		line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		if line == "" {
			if name == "" || data == nil {
				t.Fatalf("incomplete SSE frame: event=%q data=%q", name, data)
			}
			return name, data
		}
		if strings.HasPrefix(line, "event: ") {
			name = strings.TrimPrefix(line, "event: ")
		}
		if strings.HasPrefix(line, "data: ") {
			data = append(data, strings.TrimPrefix(line, "data: ")...)
		}
	}
}

type blockingSSEWriter struct {
	mu            sync.Mutex
	header        http.Header
	body          bytes.Buffer
	writes        int
	status        int
	firstWrite    chan struct{}
	secondWrite   chan struct{}
	releaseSecond chan struct{}
}

func newBlockingSSEWriter() *blockingSSEWriter {
	return &blockingSSEWriter{
		header:        make(http.Header),
		firstWrite:    make(chan struct{}),
		secondWrite:   make(chan struct{}),
		releaseSecond: make(chan struct{}),
	}
}

func (w *blockingSSEWriter) Header() http.Header {
	return w.header
}

func (w *blockingSSEWriter) WriteHeader(status int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.status == 0 {
		w.status = status
	}
}

func (w *blockingSSEWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	w.writes++
	writeNumber := w.writes
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if writeNumber == 1 {
		close(w.firstWrite)
	}
	if writeNumber == 2 {
		close(w.secondWrite)
		w.mu.Unlock()
		<-w.releaseSecond
		w.mu.Lock()
	}
	defer w.mu.Unlock()
	return w.body.Write(data)
}

func (w *blockingSSEWriter) Flush() {}

func (w *blockingSSEWriter) bodyString() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.body.String()
}

func TestOutputContentType(t *testing.T) {
	for extension, want := range map[string]string{
		".txt": "text/plain; charset=utf-8",
		".srt": "application/x-subrip; charset=utf-8",
		".vtt": "text/vtt; charset=utf-8",
		".bin": "",
	} {
		t.Run(fmt.Sprintf("extension_%s", strings.TrimPrefix(extension, ".")), func(t *testing.T) {
			if got := outputContentType(extension); got != want {
				t.Fatalf("outputContentType(%q) = %q, want %q", extension, got, want)
			}
		})
	}
}

func TestJobWireResponsesKeepUnknownProgressAndInternalErrorsDistinct(t *testing.T) {
	queuedAt := time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC)
	snapshot := job.Snapshot{
		RunRequest: job.RunRequest{ID: "job-wire", Preset: "ja_fast", OriginalFilename: "lecture.wav"},
		Status:     job.StatusQueued,
		Error:      "/private/path must not be exposed",
		Warnings: []job.Warning{{
			Code:    job.WarningCodeCleanupFailed,
			Message: "/private/cleanup path must not be exposed",
		}},
		QueuedAt: queuedAt,
	}
	unknown, err := json.Marshal(newJobSnapshotResponse(snapshot))
	if err != nil {
		t.Fatal(err)
	}
	wantFragments := []string{
		`"progress":null`,
		`"logs":[]`,
		`"outputs":[]`,
		`"warnings":["cleanup_failed"]`,
		`"started_at":null`,
		`"finished_at":null`,
	}
	for _, fragment := range wantFragments {
		if !strings.Contains(string(unknown), fragment) {
			t.Errorf("wire snapshot missing %s: %s", fragment, unknown)
		}
	}
	if strings.Contains(string(unknown), "/private/") || strings.Contains(string(unknown), `"error"`) {
		t.Fatalf("wire snapshot exposed an internal error: %s", unknown)
	}

	snapshot.ProgressKnown = true
	snapshot.Progress = 0
	known, err := json.Marshal(newJobSnapshotResponse(snapshot))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(known), `"progress":0`) {
		t.Fatalf("known zero progress was not preserved: %s", known)
	}
	event, err := json.Marshal(newJobEventResponse(job.Event{Type: job.EventProgress, JobID: "job-wire", Progress: 0}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(event), `"progress":0`) {
		t.Fatalf("zero progress event was omitted: %s", event)
	}
}
