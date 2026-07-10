package server

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/steepkit/whisper-cpp-gui/internal/job"
	"github.com/steepkit/whisper-cpp-gui/internal/model"
)

type trackingModelLease struct {
	path   string
	closed chan struct{}
	once   sync.Once
}

func newTrackingModelLease(path string) *trackingModelLease {
	return &trackingModelLease{path: path, closed: make(chan struct{})}
}

func (l *trackingModelLease) Path() string {
	return l.path
}

func (l *trackingModelLease) Close() error {
	l.once.Do(func() { close(l.closed) })
	return nil
}

func (l *trackingModelLease) isClosed() bool {
	select {
	case <-l.closed:
		return true
	default:
		return false
	}
}

func TestUploadFailsClosedWhenRequiredModelCannotBeLeased(t *testing.T) {
	invalidPathLease := newTrackingModelLease("")
	tests := []struct {
		name       string
		acquire    ModelAcquireFunc
		wantStatus int
		wantCode   string
		wantClosed *trackingModelLease
	}{
		{
			name: "missing model",
			acquire: func(string) (ModelLease, error) {
				return nil, model.ErrModelUnavailable
			},
			wantStatus: http.StatusConflict,
			wantCode:   "model_missing",
		},
		{
			name: "nil lease",
			acquire: func(string) (ModelLease, error) {
				return nil, nil
			},
			wantStatus: http.StatusInternalServerError,
			wantCode:   "internal_error",
		},
		{
			name: "empty path",
			acquire: func(string) (ModelLease, error) {
				return invalidPathLease, nil
			},
			wantStatus: http.StatusInternalServerError,
			wantCode:   "internal_error",
			wantClosed: invalidPathLease,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "jobs")
			started := make(chan struct{}, 1)
			manager, err := job.NewManager(uploadRunnerFunc(func(context.Context, job.RunRequest, job.Observer) (job.RunResult, error) {
				started <- struct{}{}
				return job.RunResult{}, nil
			}), job.Config{TempRoot: root})
			if err != nil {
				t.Fatal(err)
			}
			srv, err := New(Config{
				Port:         8123,
				Token:        testToken,
				Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
				Jobs:         manager,
				TempRoot:     root,
				AcquireModel: test.acquire,
				IDGenerator:  func() (string, error) { return "job-model-required", nil },
			})
			if err != nil {
				manager.Close()
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = srv.Close() })

			body, contentType := modelLeaseUploadBody(t)
			recorder := uploadRequest(srv, bytes.NewReader(body), contentType)
			if recorder.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d: %s", recorder.Code, test.wantStatus, recorder.Body.String())
			}
			assertModelAPIError(t, recorder.Body.String(), test.wantCode)
			if entries := rootEntries(t, root); len(entries) != 0 {
				t.Fatalf("failed upload retained %d work directories", len(entries))
			}
			select {
			case <-started:
				t.Fatal("job runner started without a valid model lease")
			default:
			}
			if test.wantClosed != nil && !test.wantClosed.isClosed() {
				t.Fatal("invalid model lease was not released")
			}
		})
	}
}

func TestModelLeaseBlocksDeleteWhileJobIsQueuedAndRunning(t *testing.T) {
	firstStarted := make(chan struct{})
	secondStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	releaseSecond := make(chan struct{})
	runner := uploadRunnerFunc(func(ctx context.Context, request job.RunRequest, _ job.Observer) (job.RunResult, error) {
		switch request.ID {
		case "blocker":
			close(firstStarted)
			select {
			case <-releaseFirst:
				return job.RunResult{}, nil
			case <-ctx.Done():
				return job.RunResult{}, context.Cause(ctx)
			}
		case "leased-job":
			close(secondStarted)
			select {
			case <-releaseSecond:
				return job.RunResult{}, nil
			case <-ctx.Done():
				return job.RunResult{}, context.Cause(ctx)
			}
		default:
			return job.RunResult{}, nil
		}
	})
	manager, root := newJobAPIManager(t, runner, job.Config{QueueCapacity: 4})
	submitJobAPIRequest(t, manager, root, "blocker", nil)
	select {
	case <-firstStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("blocking job did not start")
	}

	lease := newTrackingModelLease("/models/ggml-large-v3-turbo-q5_0.bin")
	service := &fakeModelService{deleteFunc: func(string) error {
		if lease.isClosed() {
			return nil
		}
		return model.ErrModelInUse
	}}
	var acquiredName string
	srv, err := New(Config{
		Port:     8123,
		Token:    testToken,
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Jobs:     manager,
		Models:   service,
		TempRoot: root,
		AcquireModel: func(name string) (ModelLease, error) {
			acquiredName = name
			return lease, nil
		},
		IDGenerator: func() (string, error) { return "leased-job", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	body, contentType := modelLeaseUploadBody(t)
	recorder := uploadRequest(srv, bytes.NewReader(body), contentType)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("upload status = %d: %s", recorder.Code, recorder.Body.String())
	}
	if acquiredName != model.ModelLargeV3TurboQ50 {
		t.Fatalf("acquired model = %q", acquiredName)
	}
	snapshot, err := manager.Snapshot("leased-job")
	if err != nil || snapshot.Status != job.StatusQueued {
		t.Fatalf("queued snapshot = %+v, err = %v", snapshot, err)
	}
	assertModelDeleteStatus(t, srv, http.StatusConflict)
	if lease.isClosed() {
		t.Fatal("lease released while job was queued")
	}

	close(releaseFirst)
	select {
	case <-secondStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("leased job did not start")
	}
	assertModelDeleteStatus(t, srv, http.StatusConflict)
	if lease.isClosed() {
		t.Fatal("lease released while job was running")
	}

	close(releaseSecond)
	waitForJobTerminalSnapshot(t, manager, "leased-job")
	select {
	case <-lease.closed:
	case <-time.After(2 * time.Second):
		t.Fatal("lease was not released after job reached a terminal state")
	}
	assertModelDeleteStatus(t, srv, http.StatusNoContent)
}

func TestServerCloseReleasesModelLeaseBeforeClosingService(t *testing.T) {
	runnerStarted := make(chan struct{})
	runner := uploadRunnerFunc(func(ctx context.Context, _ job.RunRequest, _ job.Observer) (job.RunResult, error) {
		close(runnerStarted)
		<-ctx.Done()
		return job.RunResult{}, context.Cause(ctx)
	})
	manager, root := newJobAPIManager(t, runner, job.Config{})
	lease := newTrackingModelLease("/models/ggml-large-v3-turbo-q5_0.bin")
	serviceClosedAfterLease := make(chan bool, 1)
	service := &fakeModelService{closeFunc: func() {
		serviceClosedAfterLease <- lease.isClosed()
	}}
	srv, err := New(Config{
		Port:     8123,
		Token:    testToken,
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Jobs:     manager,
		Models:   service,
		TempRoot: root,
		AcquireModel: func(string) (ModelLease, error) {
			return lease, nil
		},
		IDGenerator: func() (string, error) { return "job-close-lease", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	body, contentType := modelLeaseUploadBody(t)
	recorder := uploadRequest(srv, bytes.NewReader(body), contentType)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("upload status = %d: %s", recorder.Code, recorder.Body.String())
	}
	select {
	case <-runnerStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("runner did not start")
	}

	closeDone := make(chan error, 1)
	go func() { closeDone <- srv.Close() }()
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Server.Close deadlocked with an active model lease")
	}
	select {
	case released := <-serviceClosedAfterLease:
		if !released {
			t.Fatal("model service closed before the active lease was released")
		}
	default:
		t.Fatal("model service was not closed")
	}
	service.mu.Lock()
	closeCount := service.closeCount
	service.mu.Unlock()
	if closeCount != 1 {
		t.Fatalf("model service Close calls = %d, want 1", closeCount)
	}
}

func modelLeaseUploadBody(t *testing.T) ([]byte, string) {
	t.Helper()
	return makeMultipart(t,
		multipartPart{name: "preset", value: []byte("ja_fast")},
		multipartPart{name: "options", value: []byte(`{"vad":false,"outputs":["txt"]}`)},
		multipartPart{name: "file", filename: "lecture.wav", value: []byte("audio")},
	)
}

func assertModelDeleteStatus(t *testing.T, srv *Server, want int) {
	t.Helper()
	result := modelAPIRequest(srv, http.MethodDelete, "/api/models/"+model.ModelLargeV3TurboQ50, false)
	if result.status != want {
		t.Fatalf("delete status = %d, want %d: %s", result.status, want, result.body)
	}
}

var _ ModelLease = (*trackingModelLease)(nil)
