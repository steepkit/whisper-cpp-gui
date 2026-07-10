package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	appconfig "github.com/steepkit/whisper-cpp-gui/config"
	wexec "github.com/steepkit/whisper-cpp-gui/internal/exec"
	"github.com/steepkit/whisper-cpp-gui/internal/job"
	"github.com/steepkit/whisper-cpp-gui/internal/model"
)

func TestUploadStubPipelineEndToEnd(t *testing.T) {
	tempRoot := filepath.Join(t.TempDir(), "jobs")
	home := t.TempDir()
	if err := os.Mkdir(filepath.Join(home, "Downloads"), 0o755); err != nil {
		t.Fatal(err)
	}
	modelDir := t.TempDir()
	whisperModel := filepath.Join(modelDir, "whisper.bin")
	vadModel := filepath.Join(modelDir, "vad.bin")
	for _, path := range []string{whisperModel, vadModel} {
		if err := os.WriteFile(path, []byte("stub model"), 0o600); err != nil {
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

	var evidenceMu sync.Mutex
	evidence := make(map[string][]string)
	manager, err := job.NewManager(pipeline, job.Config{
		TempRoot: tempRoot,
		Cleanup: func(workDir string) error {
			for _, phase := range []string{"convert", "transcribe"} {
				path := filepath.Join(workDir, phase, "invocation.json")
				data, readErr := os.ReadFile(path)
				if readErr != nil {
					return readErr
				}
				var invocation struct {
					Argv []string `json:"argv"`
				}
				if err := json.Unmarshal(data, &invocation); err != nil {
					return err
				}
				evidenceMu.Lock()
				evidence[phase] = invocation.Argv
				evidenceMu.Unlock()
			}
			return os.RemoveAll(workDir)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	presets, err := appconfig.Load()
	if err != nil {
		manager.Close()
		t.Fatal(err)
	}
	srv, err := New(Config{
		Port:     8123,
		Token:    testToken,
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Jobs:     manager,
		Presets:  presets,
		TempRoot: tempRoot,
		ModelPathResolver: func(name string) (string, error) {
			switch name {
			case model.ModelLargeV3TurboQ50, model.ModelLargeV3Q50:
				return whisperModel, nil
			case model.ModelSileroVAD:
				return vadModel, nil
			default:
				return "", model.ErrUnknownModel
			}
		},
		VADLogicalName: model.ModelSileroVAD,
		IDGenerator:    func() (string, error) { return "job-http-e2e", nil },
	})
	if err != nil {
		manager.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	body, contentType := makeMultipart(t,
		multipartPart{name: "preset", value: []byte("ja_fast")},
		multipartPart{name: "file", filename: "研究会.m4a", value: []byte("stub media")},
	)
	recorder := uploadRequest(srv, bytes.NewReader(body), contentType)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("upload status = %d: %s", recorder.Code, recorder.Body.String())
	}
	var response uploadResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	snapshot := waitForServerJob(t, manager, response.ID)
	if snapshot.Status != job.StatusDone || snapshot.Phase != "" {
		t.Fatalf("terminal snapshot = %+v", snapshot)
	}
	if filepath.Base(snapshot.OutputDir) != "研究会" || !reflect.DeepEqual(snapshot.OutputFiles, []string{"transcript.txt", "transcript.srt", "transcript.vtt"}) {
		t.Fatalf("published metadata = %q, %v", snapshot.OutputDir, snapshot.OutputFiles)
	}
	for _, name := range snapshot.OutputFiles {
		if _, err := os.Stat(filepath.Join(snapshot.OutputDir, name)); err != nil {
			t.Errorf("published file %s: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(tempRoot, response.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("work directory remains: %v", err)
	}

	evidenceMu.Lock()
	defer evidenceMu.Unlock()
	if len(evidence["convert"]) == 0 || len(evidence["transcribe"]) == 0 {
		t.Fatalf("missing invocation evidence: %v", evidence)
	}
	if !containsServerArg(evidence["transcribe"], "--vad") || !containsServerArg(evidence["transcribe"], vadModel) {
		t.Fatalf("whisper invocation missing VAD arguments: %v", evidence["transcribe"])
	}
}

func TestUploadStubFailuresExposeStderrAndCleanWorkDir(t *testing.T) {
	tests := []struct {
		name      string
		failPhase string
		wantCode  job.ErrorCode
	}{
		{name: "ffmpeg conversion", failPhase: "convert", wantCode: job.ErrorCodeConversionFailed},
		{name: "whisper transcription", failPhase: "transcribe", wantCode: job.ErrorCodeTranscriptionFailed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tempRoot := filepath.Join(t.TempDir(), "jobs")
			home := t.TempDir()
			modelDir := t.TempDir()
			whisperModel := filepath.Join(modelDir, "whisper.bin")
			vadModel := filepath.Join(modelDir, "vad.bin")
			for _, path := range []string{whisperModel, vadModel} {
				if err := os.WriteFile(path, []byte("stub model"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			cliEngine := wexec.NewCLIEngine(wexec.CLIEngineConfig{Resolve: func() (wexec.Discovery, error) {
				return wexec.Discovery{
					WhisperCLI: wexec.ToolStatus{Name: "whisper-cli", Path: serverStubPath(t, "whisper-cli"), Found: true},
					FFmpeg:     wexec.ToolStatus{Name: "ffmpeg", Path: serverStubPath(t, "ffmpeg"), Found: true},
				}, nil
			}})
			pipeline, err := job.NewPipeline(job.PipelineConfig{
				Engine:    phaseFailEngine{Engine: cliEngine, phase: test.failPhase},
				Publisher: job.NewPublisher(job.PublisherConfig{HomeDir: func() (string, error) { return home, nil }}),
				Env:       append(os.Environ(), "STUB_FAIL=0", "STUB_PROGRESS_INTERVAL=10ms"),
			})
			if err != nil {
				t.Fatal(err)
			}
			manager, err := job.NewManager(pipeline, job.Config{TempRoot: tempRoot})
			if err != nil {
				t.Fatal(err)
			}
			presets, err := appconfig.Load()
			if err != nil {
				manager.Close()
				t.Fatal(err)
			}
			id := "job-stub-failure-" + test.failPhase
			srv, err := New(Config{
				Port:     8123,
				Token:    testToken,
				Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
				Jobs:     manager,
				Presets:  presets,
				TempRoot: tempRoot,
				ModelPathResolver: func(name string) (string, error) {
					switch name {
					case model.ModelLargeV3TurboQ50, model.ModelLargeV3Q50:
						return whisperModel, nil
					case model.ModelSileroVAD:
						return vadModel, nil
					default:
						return "", model.ErrUnknownModel
					}
				},
				VADLogicalName: model.ModelSileroVAD,
				IDGenerator:    func() (string, error) { return id, nil },
			})
			if err != nil {
				manager.Close()
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = srv.Close() })

			body, contentType := makeMultipart(t,
				multipartPart{name: "preset", value: []byte("ja_fast")},
				multipartPart{name: "file", filename: "failure.wav", value: []byte("stub media")},
			)
			recorder := uploadRequest(srv, bytes.NewReader(body), contentType)
			if recorder.Code != http.StatusAccepted {
				t.Fatalf("upload status = %d: %s", recorder.Code, recorder.Body.String())
			}
			snapshot := waitForServerJob(t, manager, id)
			if snapshot.Status != job.StatusFailed || snapshot.ErrorCode != test.wantCode {
				t.Fatalf("failure snapshot = %+v", snapshot)
			}
			if !strings.Contains(snapshot.StderrTail, "forced failure") {
				t.Fatalf("stderr tail = %q", snapshot.StderrTail)
			}
			if _, err := os.Stat(filepath.Join(tempRoot, id)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("failed work directory remains: %v", err)
			}
		})
	}
}

type phaseFailEngine struct {
	wexec.Engine
	phase string
}

func (e phaseFailEngine) ConvertToWAV(ctx context.Context, request wexec.ConvertRequest, observer wexec.Observer) error {
	if e.phase == "convert" {
		request.Env = append(request.Env, "STUB_FAIL=1")
	}
	return e.Engine.ConvertToWAV(ctx, request, observer)
}

func (e phaseFailEngine) Transcribe(ctx context.Context, request wexec.TranscribeRequest, observer wexec.Observer) (wexec.TranscriptionResult, error) {
	if e.phase == "transcribe" {
		request.Env = append(request.Env, "STUB_FAIL=1")
	}
	return e.Engine.Transcribe(ctx, request, observer)
}

func waitForServerJob(t *testing.T, manager *job.Manager, id string) job.Snapshot {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		snapshot, events, unsubscribe, err := manager.Subscribe(id)
		if err != nil {
			t.Fatalf("Subscribe: %v", err)
		}
		if (snapshot.Status == job.StatusDone || snapshot.Status == job.StatusFailed || snapshot.Status == job.StatusCancelled) && !snapshot.CleanupPending {
			unsubscribe()
			return snapshot
		}
		select {
		case _, ok := <-events:
			unsubscribe()
			if !ok {
				continue
			}
		case <-deadline.C:
			unsubscribe()
			t.Fatalf("timed out waiting for job %s", id)
		}
	}
}

func serverStubPath(t *testing.T, name string) string {
	t.Helper()
	path, err := filepath.Abs(filepath.Join("..", "..", "testdata", "stubs", name))
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func containsServerArg(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
