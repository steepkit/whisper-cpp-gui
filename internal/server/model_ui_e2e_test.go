package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	appconfig "github.com/steepkit/whisper-cpp-gui/config"
	wexec "github.com/steepkit/whisper-cpp-gui/internal/exec"
	"github.com/steepkit/whisper-cpp-gui/internal/job"
	"github.com/steepkit/whisper-cpp-gui/internal/model"
)

type modelUIService struct {
	mu        sync.Mutex
	order     []string
	snapshots map[string]model.Snapshot
	paths     map[string]string
	leases    map[string]int
	closed    bool
}

func newModelUIService(directory string) *modelUIService {
	service := &modelUIService{
		snapshots: make(map[string]model.Snapshot),
		paths:     make(map[string]string),
		leases:    make(map[string]int),
	}
	for _, descriptor := range model.Manifest() {
		service.order = append(service.order, descriptor.Name)
		service.snapshots[descriptor.Name] = model.Snapshot{Model: descriptor, State: model.StateMissing}
		service.paths[descriptor.Name] = filepath.Join(directory, descriptor.Filename)
	}
	return service
}

func (s *modelUIService) Snapshots() ([]model.Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, model.ErrManagerClosed
	}
	result := make([]model.Snapshot, 0, len(s.order))
	for _, name := range s.order {
		result = append(result, s.snapshots[name])
	}
	return result, nil
}

func (s *modelUIService) Snapshot(name string) (model.Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return model.Snapshot{}, model.ErrManagerClosed
	}
	snapshot, ok := s.snapshots[name]
	if !ok {
		return model.Snapshot{}, model.ErrUnknownModel
	}
	return snapshot, nil
}

func (s *modelUIService) Download(name string) (model.Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return model.Snapshot{}, model.ErrManagerClosed
	}
	snapshot, ok := s.snapshots[name]
	if !ok {
		return model.Snapshot{}, model.ErrUnknownModel
	}
	switch snapshot.State {
	case model.StateDownloaded:
		return snapshot, model.ErrAlreadyDownloaded
	case model.StateDownloading:
		return model.Snapshot{}, model.ErrDownloadInProgress
	}
	snapshot.State = model.StateDownloading
	snapshot.BytesDownloaded = 0
	snapshot.ErrorCode = model.ErrorCodeNone
	s.snapshots[name] = snapshot
	return snapshot, nil
}

func (s *modelUIService) Subscribe(name string) (model.Snapshot, <-chan model.Event, func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return model.Snapshot{}, nil, nil, model.ErrManagerClosed
	}
	initial, ok := s.snapshots[name]
	if !ok {
		return model.Snapshot{}, nil, nil, model.ErrUnknownModel
	}
	events := make(chan model.Event, 2)
	if initial.State != model.StateDownloading {
		close(events)
		return initial, events, func() {}, nil
	}
	progress := initial.Model.Size / 2
	events <- model.Event{Type: model.EventProgress, Name: name, State: model.StateDownloading, BytesDownloaded: progress}
	events <- model.Event{Type: model.EventState, Name: name, State: model.StateDownloaded, BytesDownloaded: initial.Model.Size}
	close(events)
	terminal := initial
	terminal.State = model.StateDownloaded
	terminal.BytesDownloaded = initial.Model.Size
	terminal.ErrorCode = model.ErrorCodeNone
	s.snapshots[name] = terminal
	return initial, events, func() {}, nil
}

func (s *modelUIService) Delete(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return model.ErrManagerClosed
	}
	snapshot, ok := s.snapshots[name]
	if !ok {
		return model.ErrUnknownModel
	}
	if s.leases[name] > 0 {
		return model.ErrModelInUse
	}
	snapshot.State = model.StateMissing
	snapshot.BytesDownloaded = 0
	snapshot.ErrorCode = model.ErrorCodeNone
	s.snapshots[name] = snapshot
	return nil
}

func (s *modelUIService) Acquire(name string) (ModelLease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, model.ErrManagerClosed
	}
	snapshot, ok := s.snapshots[name]
	if !ok {
		return nil, model.ErrUnknownModel
	}
	if snapshot.State != model.StateDownloaded {
		return nil, model.ErrModelUnavailable
	}
	s.leases[name]++
	return &modelUILease{service: s, name: name, path: s.paths[name]}, nil
}

func (s *modelUIService) Close() {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
}

type modelUILease struct {
	service *modelUIService
	name    string
	path    string
	once    sync.Once
}

func (l *modelUILease) Path() string {
	return l.path
}

func (l *modelUILease) Close() error {
	l.once.Do(func() {
		l.service.mu.Lock()
		if l.service.leases[l.name] > 0 {
			l.service.leases[l.name]--
		}
		l.service.mu.Unlock()
	})
	return nil
}

type modelUIPipelineGate struct {
	runner      job.Runner
	started     chan struct{}
	release     chan struct{}
	startedOnce sync.Once
	releaseOnce sync.Once
}

func newModelUIPipelineGate(runner job.Runner) *modelUIPipelineGate {
	return &modelUIPipelineGate{
		runner:  runner,
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (g *modelUIPipelineGate) Run(ctx context.Context, request job.RunRequest, observer job.Observer) (job.RunResult, error) {
	g.startedOnce.Do(func() { close(g.started) })
	select {
	case <-g.release:
		return g.runner.Run(ctx, request, observer)
	case <-ctx.Done():
		return job.RunResult{}, context.Cause(ctx)
	}
}

func (g *modelUIPipelineGate) Release() {
	g.releaseOnce.Do(func() { close(g.release) })
}

func TestModelLifecycleAPIAndStubPipelineEndToEnd(t *testing.T) {
	tempRoot := filepath.Join(t.TempDir(), "jobs")
	home := t.TempDir()
	if err := os.Mkdir(filepath.Join(home, "Downloads"), 0o755); err != nil {
		t.Fatal(err)
	}
	modelDirectory := t.TempDir()
	models := newModelUIService(modelDirectory)
	for _, name := range []string{model.ModelLargeV3TurboQ50, model.ModelSileroVAD} {
		if err := os.WriteFile(models.paths[name], []byte("stub model"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	engine := wexec.NewCLIEngine(wexec.CLIEngineConfig{Resolve: func() (wexec.Discovery, error) {
		return wexec.Discovery{
			WhisperCLI: wexec.ToolStatus{Name: "whisper-cli", Path: serverStubPath(t, "whisper-cli"), Found: true},
			FFmpeg:     wexec.ToolStatus{Name: "ffmpeg", Path: serverStubPath(t, "ffmpeg"), Found: true},
		}, nil
	}})
	pipeline, err := job.NewPipeline(job.PipelineConfig{
		Engine: engine,
		Publisher: job.NewPublisher(job.PublisherConfig{
			HomeDir: func() (string, error) { return home, nil },
		}),
		Env: append(os.Environ(), "STUB_PROGRESS_INTERVAL=10ms"),
	})
	if err != nil {
		t.Fatal(err)
	}
	gate := newModelUIPipelineGate(pipeline)
	defer gate.Release()
	jobs, err := job.NewManager(gate, job.Config{TempRoot: tempRoot})
	if err != nil {
		t.Fatal(err)
	}
	presets, err := appconfig.Load()
	if err != nil {
		jobs.Close()
		t.Fatal(err)
	}
	srv, err := New(Config{
		Port:         8123,
		Token:        testToken,
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		Jobs:         jobs,
		Models:       models,
		AcquireModel: models.Acquire,
		Presets:      presets,
		TempRoot:     tempRoot,
		IDGenerator:  func() (string, error) { return "job-model-ui-e2e", nil },
	})
	if err != nil {
		jobs.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	initial := listModelsThroughAPI(t, srv)
	assertModelState(t, initial, model.ModelLargeV3TurboQ50, model.StateMissing)
	assertModelState(t, initial, model.ModelSileroVAD, model.StateMissing)

	body, contentType := modelUIUploadBody(t)
	missing := uploadRequest(srv, bytes.NewReader(body), contentType)
	if missing.Code != http.StatusConflict {
		t.Fatalf("upload without model status = %d: %s", missing.Code, missing.Body.String())
	}
	assertModelAPIError(t, missing.Body.String(), "model_missing")
	if entries := rootEntries(t, tempRoot); len(entries) != 0 {
		t.Fatalf("missing-model upload retained %d work directories", len(entries))
	}

	for _, name := range []string{model.ModelLargeV3TurboQ50, model.ModelSileroVAD} {
		downloadModelThroughAPI(t, srv, name)
	}
	downloaded := listModelsThroughAPI(t, srv)
	assertModelState(t, downloaded, model.ModelLargeV3TurboQ50, model.StateDownloaded)
	assertModelState(t, downloaded, model.ModelSileroVAD, model.StateDownloaded)

	body, contentType = modelUIUploadBody(t)
	accepted := uploadRequest(srv, bytes.NewReader(body), contentType)
	if accepted.Code != http.StatusAccepted {
		t.Fatalf("upload after model download status = %d: %s", accepted.Code, accepted.Body.String())
	}
	var upload uploadResponse
	if err := json.Unmarshal(accepted.Body.Bytes(), &upload); err != nil {
		t.Fatal(err)
	}
	select {
	case <-gate.started:
	case <-time.After(5 * time.Second):
		t.Fatal("stub pipeline did not start")
	}
	whileRunning := doRequest(srv, http.MethodDelete, "/api/models/"+model.ModelLargeV3TurboQ50, "127.0.0.1:8123", "", testToken)
	if whileRunning.Code != http.StatusConflict {
		t.Fatalf("delete leased model status = %d, want 409: %s", whileRunning.Code, whileRunning.Body.String())
	}
	assertModelAPIError(t, whileRunning.Body.String(), "model_in_use")
	gate.Release()
	snapshot := waitForServerJob(t, jobs, upload.ID)
	if snapshot.Status != job.StatusDone || !containsString(snapshot.OutputFiles, "transcript.txt") {
		t.Fatalf("terminal job snapshot = %+v", snapshot)
	}

	deleteModelAfterLease(t, srv, model.ModelLargeV3TurboQ50)
	afterDelete := listModelsThroughAPI(t, srv)
	assertModelState(t, afterDelete, model.ModelLargeV3TurboQ50, model.StateMissing)
}

func modelUIUploadBody(t *testing.T) ([]byte, string) {
	t.Helper()
	return makeMultipart(t,
		multipartPart{name: "preset", value: []byte("ja_fast")},
		multipartPart{name: "file", filename: "model-ui.wav", value: []byte("stub media")},
	)
}

func listModelsThroughAPI(t *testing.T, srv *Server) []modelSnapshotResponse {
	t.Helper()
	recorder := doRequest(srv, http.MethodGet, "/api/models", "127.0.0.1:8123", "", testToken)
	if recorder.Code != http.StatusOK {
		t.Fatalf("model list status = %d: %s", recorder.Code, recorder.Body.String())
	}
	var response modelsResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	return response.Models
}

func downloadModelThroughAPI(t *testing.T, srv *Server, name string) {
	t.Helper()
	path := "/api/models/" + name
	started := doRequest(srv, http.MethodPost, path+"/download", "127.0.0.1:8123", "", testToken)
	if started.Code != http.StatusAccepted {
		t.Fatalf("start model %s status = %d: %s", name, started.Code, started.Body.String())
	}
	events := doRequest(srv, http.MethodGet, path+"/events?token="+testToken, "127.0.0.1:8123", "", "")
	if events.Code != http.StatusOK {
		t.Fatalf("model %s events status = %d: %s", name, events.Code, events.Body.String())
	}
	reader := bufio.NewReader(events.Body)
	firstName, _ := parseSSEFrame(t, reader)
	progressName, _ := parseSSEFrame(t, reader)
	terminalName, terminalPayload := parseSSEFrame(t, reader)
	if firstName != "snapshot" || progressName != "progress" || terminalName != "snapshot" {
		t.Fatalf("model %s SSE order = %q, %q, %q", name, firstName, progressName, terminalName)
	}
	var terminal modelSnapshotResponse
	if err := json.Unmarshal(terminalPayload, &terminal); err != nil {
		t.Fatal(err)
	}
	if terminal.Model.Name != name || terminal.State != model.StateDownloaded || terminal.BytesDownloaded != terminal.Model.Size {
		t.Fatalf("model %s terminal snapshot = %+v", name, terminal)
	}
}

func deleteModelAfterLease(t *testing.T, srv *Server, name string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		recorder := doRequest(srv, http.MethodDelete, "/api/models/"+name, "127.0.0.1:8123", "", testToken)
		if recorder.Code == http.StatusNoContent {
			return
		}
		if recorder.Code != http.StatusConflict || time.Now().After(deadline) {
			t.Fatalf("delete model %s status = %d: %s", name, recorder.Code, recorder.Body.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func assertModelState(t *testing.T, snapshots []modelSnapshotResponse, name string, want model.State) {
	t.Helper()
	for _, snapshot := range snapshots {
		if snapshot.Model.Name == name {
			if snapshot.State != want {
				t.Fatalf("model %s state = %q, want %q", name, snapshot.State, want)
			}
			return
		}
	}
	t.Fatalf("model list is missing %s", name)
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

var _ ModelService = (*modelUIService)(nil)
var _ ModelLease = (*modelUILease)(nil)
