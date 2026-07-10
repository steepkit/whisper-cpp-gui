package server

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/steepkit/whisper-cpp-gui/internal/model"
)

type fakeModelService struct {
	snapshotsFunc func() ([]model.Snapshot, error)
	snapshotFunc  func(string) (model.Snapshot, error)
	downloadFunc  func(string) (model.Snapshot, error)
	subscribeFunc func(string) (model.Snapshot, <-chan model.Event, func(), error)
	deleteFunc    func(string) error
	closeFunc     func()

	mu         sync.Mutex
	closeCount int
}

func (f *fakeModelService) Snapshots() ([]model.Snapshot, error) {
	if f.snapshotsFunc == nil {
		return nil, nil
	}
	return f.snapshotsFunc()
}

func (f *fakeModelService) Snapshot(name string) (model.Snapshot, error) {
	if f.snapshotFunc == nil {
		return model.Snapshot{}, model.ErrUnknownModel
	}
	return f.snapshotFunc(name)
}

func (f *fakeModelService) Download(name string) (model.Snapshot, error) {
	if f.downloadFunc == nil {
		return model.Snapshot{}, model.ErrUnknownModel
	}
	return f.downloadFunc(name)
}

func (f *fakeModelService) Subscribe(name string) (model.Snapshot, <-chan model.Event, func(), error) {
	if f.subscribeFunc == nil {
		return model.Snapshot{}, nil, nil, model.ErrUnknownModel
	}
	return f.subscribeFunc(name)
}

func (f *fakeModelService) Delete(name string) error {
	if f.deleteFunc == nil {
		return nil
	}
	return f.deleteFunc(name)
}

func (f *fakeModelService) Close() {
	f.mu.Lock()
	f.closeCount++
	f.mu.Unlock()
	if f.closeFunc != nil {
		f.closeFunc()
	}
}

func newModelAPIServer(t *testing.T, service ModelService) *Server {
	t.Helper()
	s, err := New(Config{
		Port:   8123,
		Token:  testToken,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Models: service,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return s
}

func modelAPIRequest(s *Server, method, target string, queryAuth bool) *responseResult {
	headerToken := testToken
	if queryAuth {
		headerToken = ""
		target += "?token=" + testToken
	}
	recorder := doRequest(s, method, target, "127.0.0.1:8123", "", headerToken)
	return &responseResult{status: recorder.Code, body: recorder.Body.String(), header: recorder.Header()}
}

type responseResult struct {
	status int
	body   string
	header http.Header
}

func TestModelListReturnsPathFreeManifestEnvelope(t *testing.T) {
	want := model.Snapshot{
		Model: model.Descriptor{
			Name:     model.ModelLargeV3TurboQ50,
			Filename: "ggml-large-v3-turbo-q5_0.bin",
			Size:     574041195,
			SHA256:   strings.Repeat("a", 64),
		},
		State:           model.StateDownloading,
		BytesDownloaded: 1234,
	}
	service := &fakeModelService{snapshotsFunc: func() ([]model.Snapshot, error) {
		return []model.Snapshot{want}, nil
	}}
	result := modelAPIRequest(newModelAPIServer(t, service), http.MethodGet, "/api/models", false)
	if result.status != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", result.status, result.body)
	}
	if got := result.header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q", got)
	}
	var response modelsResponse
	if err := json.Unmarshal([]byte(result.body), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Models) != 1 || response.Models[0] != newModelSnapshotResponse(want) {
		t.Fatalf("response = %+v, want %+v", response, want)
	}
	for _, forbidden := range []string{"source_url", "local_path", "https://", "/tmp/"} {
		if strings.Contains(result.body, forbidden) {
			t.Errorf("model response exposed %q: %s", forbidden, result.body)
		}
	}
}

func TestModelDownloadStatusMapping(t *testing.T) {
	descriptor := model.Descriptor{Name: model.ModelLargeV3Q50}
	tests := []struct {
		name       string
		err        error
		state      model.State
		wantStatus int
		wantCode   string
	}{
		{name: "started", state: model.StateDownloading, wantStatus: http.StatusAccepted},
		{name: "already valid is idempotent", state: model.StateDownloaded, err: model.ErrAlreadyDownloaded, wantStatus: http.StatusAccepted},
		{name: "same process download", err: model.ErrDownloadInProgress, wantStatus: http.StatusConflict, wantCode: "download_in_progress"},
		{name: "other process download", err: model.ErrModelBusy, wantStatus: http.StatusConflict, wantCode: "download_in_progress"},
		{name: "unknown model", err: model.ErrUnknownModel, wantStatus: http.StatusNotFound, wantCode: "model_not_found"},
		{name: "closed manager", err: model.ErrManagerClosed, wantStatus: http.StatusServiceUnavailable, wantCode: "manager_unavailable"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var gotName string
			service := &fakeModelService{downloadFunc: func(name string) (model.Snapshot, error) {
				gotName = name
				return model.Snapshot{Model: descriptor, State: test.state}, test.err
			}}
			result := modelAPIRequest(newModelAPIServer(t, service), http.MethodPost, "/api/models/"+model.ModelLargeV3Q50+"/download", false)
			if result.status != test.wantStatus {
				t.Fatalf("status = %d, want %d: %s", result.status, test.wantStatus, result.body)
			}
			if gotName != model.ModelLargeV3Q50 {
				t.Fatalf("Download name = %q", gotName)
			}
			if test.wantCode != "" {
				assertModelAPIError(t, result.body, test.wantCode)
				return
			}
			var response modelSnapshotResponse
			if err := json.Unmarshal([]byte(result.body), &response); err != nil {
				t.Fatal(err)
			}
			if response.Model.Name != descriptor.Name || response.State != test.state {
				t.Fatalf("response = %+v", response)
			}
		})
	}
}

func TestModelDeleteIsIdempotentAndRejectsBusyModels(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{name: "missing or downloaded", wantStatus: http.StatusNoContent},
		{name: "leased by job", err: model.ErrModelInUse, wantStatus: http.StatusConflict, wantCode: "model_in_use"},
		{name: "download active", err: model.ErrDownloadInProgress, wantStatus: http.StatusConflict, wantCode: "download_in_progress"},
		{name: "unknown model", err: model.ErrUnknownModel, wantStatus: http.StatusNotFound, wantCode: "model_not_found"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &fakeModelService{deleteFunc: func(name string) error {
				if name != model.ModelLargeV3Q50 {
					t.Fatalf("Delete name = %q", name)
				}
				return test.err
			}}
			result := modelAPIRequest(newModelAPIServer(t, service), http.MethodDelete, "/api/models/"+model.ModelLargeV3Q50, false)
			if result.status != test.wantStatus {
				t.Fatalf("status = %d, want %d: %s", result.status, test.wantStatus, result.body)
			}
			if test.wantCode != "" {
				assertModelAPIError(t, result.body, test.wantCode)
			}
		})
	}
}

func TestModelEventsSendSnapshotProgressAndTerminalSnapshot(t *testing.T) {
	name := model.ModelLargeV3Q50
	descriptor := model.Descriptor{Name: name, Filename: "model.bin", Size: 100, SHA256: strings.Repeat("b", 64)}
	events := make(chan model.Event, 2)
	events <- model.Event{Type: model.EventProgress, Name: name, BytesDownloaded: 40}
	events <- model.Event{Type: model.EventState, Name: name, State: model.StateDownloaded}
	close(events)
	unsubscribed := 0
	service := &fakeModelService{
		snapshotFunc: func(got string) (model.Snapshot, error) {
			if got != name {
				return model.Snapshot{}, fmt.Errorf("unexpected model %q", got)
			}
			return model.Snapshot{Model: descriptor, State: model.StateDownloaded, BytesDownloaded: 100}, nil
		},
		subscribeFunc: func(got string) (model.Snapshot, <-chan model.Event, func(), error) {
			if got != name {
				return model.Snapshot{}, nil, nil, fmt.Errorf("unexpected model %q", got)
			}
			return model.Snapshot{Model: descriptor, State: model.StateDownloading, BytesDownloaded: 10}, events, func() { unsubscribed++ }, nil
		},
	}
	result := modelAPIRequest(newModelAPIServer(t, service), http.MethodGet, "/api/models/"+name+"/events", true)
	if result.status != http.StatusOK {
		t.Fatalf("status = %d: %s", result.status, result.body)
	}
	if got := result.header.Get("Content-Type"); got != "text/event-stream; charset=utf-8" {
		t.Fatalf("Content-Type = %q", got)
	}
	reader := bufio.NewReader(strings.NewReader(result.body))
	eventName, payload := parseSSEFrame(t, reader)
	if eventName != "snapshot" {
		t.Fatalf("first event = %q", eventName)
	}
	var initial modelSnapshotResponse
	if err := json.Unmarshal(payload, &initial); err != nil {
		t.Fatal(err)
	}
	if initial.State != model.StateDownloading || initial.BytesDownloaded != 10 {
		t.Fatalf("initial snapshot = %+v", initial)
	}
	eventName, payload = parseSSEFrame(t, reader)
	if eventName != string(model.EventProgress) {
		t.Fatalf("second event = %q", eventName)
	}
	var progress modelEventResponse
	if err := json.Unmarshal(payload, &progress); err != nil {
		t.Fatal(err)
	}
	if progress.BytesDownloaded != 40 || progress.Name != name {
		t.Fatalf("progress = %+v", progress)
	}
	eventName, payload = parseSSEFrame(t, reader)
	if eventName != "snapshot" {
		t.Fatalf("terminal event = %q", eventName)
	}
	var terminal modelSnapshotResponse
	if err := json.Unmarshal(payload, &terminal); err != nil {
		t.Fatal(err)
	}
	if terminal.State != model.StateDownloaded || terminal.BytesDownloaded != 100 {
		t.Fatalf("terminal snapshot = %+v", terminal)
	}
	if unsubscribed != 1 {
		t.Fatalf("unsubscribe calls = %d, want 1", unsubscribed)
	}
}

func TestModelEventsReturnTerminalSnapshotAndMapSubscriberLimit(t *testing.T) {
	name := model.ModelSileroVAD
	descriptor := model.Descriptor{Name: name}
	t.Run("terminal", func(t *testing.T) {
		events := make(chan model.Event)
		close(events)
		service := &fakeModelService{subscribeFunc: func(string) (model.Snapshot, <-chan model.Event, func(), error) {
			return model.Snapshot{Model: descriptor, State: model.StateMissing}, events, func() {}, nil
		}}
		result := modelAPIRequest(newModelAPIServer(t, service), http.MethodGet, "/api/models/"+name+"/events", true)
		if result.status != http.StatusOK || strings.Count(result.body, "event: snapshot") != 1 {
			t.Fatalf("terminal SSE response = %d %q", result.status, result.body)
		}
	})
	t.Run("subscriber limit", func(t *testing.T) {
		service := &fakeModelService{subscribeFunc: func(string) (model.Snapshot, <-chan model.Event, func(), error) {
			return model.Snapshot{}, nil, nil, model.ErrSubscriberLimit
		}}
		result := modelAPIRequest(newModelAPIServer(t, service), http.MethodGet, "/api/models/"+name+"/events", true)
		if result.status != http.StatusTooManyRequests {
			t.Fatalf("status = %d, want 429: %s", result.status, result.body)
		}
		assertModelAPIError(t, result.body, "subscriber_limit")
	})
}

func assertModelAPIError(t *testing.T, body, want string) {
	t.Helper()
	var response modelAPIErrorResponse
	if err := json.Unmarshal([]byte(body), &response); err != nil {
		t.Fatalf("decode error response: %v: %q", err, body)
	}
	if response.ErrorCode != want {
		t.Fatalf("error_code = %q, want %q", response.ErrorCode, want)
	}
}

var _ ModelService = (*fakeModelService)(nil)
