package job

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	wexec "github.com/steepkit/whisper-cpp-gui/internal/exec"
)

type pipelineObserver struct {
	mu       sync.Mutex
	phases   []Phase
	logs     []LogEntry
	progress []int
}

func (o *pipelineObserver) Log(stream LogStream, line string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.logs = append(o.logs, LogEntry{Stream: stream, Line: line})
}

func (o *pipelineObserver) Progress(percent int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.progress = append(o.progress, percent)
}

func (o *pipelineObserver) Phase(phase Phase) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.phases = append(o.phases, phase)
}

func TestPipelineStubEndToEnd(t *testing.T) {
	request := pipelineRequestFixture(t, "job-e2e", true)
	home := t.TempDir()
	if err := os.Mkdir(filepath.Join(home, "Downloads"), 0o755); err != nil {
		t.Fatal(err)
	}
	engine := wexec.NewCLIEngine(wexec.CLIEngineConfig{Resolve: func() (wexec.Discovery, error) {
		return wexec.Discovery{
			WhisperCLI: wexec.ToolStatus{Name: "whisper-cli", Path: pipelineStubPath(t, "whisper-cli"), Found: true},
			FFmpeg:     wexec.ToolStatus{Name: "ffmpeg", Path: pipelineStubPath(t, "ffmpeg"), Found: true},
		}, nil
	}})
	pipeline, err := NewPipeline(PipelineConfig{
		Engine:    engine,
		Publisher: NewPublisher(PublisherConfig{HomeDir: func() (string, error) { return home, nil }}),
		Env:       append(os.Environ(), "STUB_PROGRESS_INTERVAL=10ms"),
	})
	if err != nil {
		t.Fatal(err)
	}
	observer := &pipelineObserver{}
	result, err := pipeline.Run(context.Background(), request, observer)
	if err != nil {
		t.Fatalf("Pipeline.Run: %v", err)
	}
	if filepath.Base(result.FinalOutputDir) != "講義 01" {
		t.Fatalf("final output dir = %q", result.FinalOutputDir)
	}
	if !reflect.DeepEqual(result.OutputFiles, []string{"transcript.txt", "transcript.srt", "transcript.vtt"}) {
		t.Fatalf("output files = %v", result.OutputFiles)
	}
	for _, name := range result.OutputFiles {
		if _, err := os.Stat(filepath.Join(result.FinalOutputDir, name)); err != nil {
			t.Errorf("published output %s: %v", name, err)
		}
	}
	observer.mu.Lock()
	if !reflect.DeepEqual(observer.phases, []Phase{PhaseConverting, PhaseTranscribing, PhaseMoving}) {
		t.Errorf("phases = %v", observer.phases)
	}
	if !reflect.DeepEqual(observer.progress, []int{25, 50, 75, 100}) {
		t.Errorf("progress = %v", observer.progress)
	}
	observer.mu.Unlock()

	ffmpegArgs := readPipelineInvocation(t, filepath.Join(request.WorkDir, "convert"))
	wav := filepath.Join(request.WorkDir, "convert", "audio.wav")
	if want := []string{"-i", request.InputPath, "-ar", "16000", "-ac", "1", "-y", wav}; !reflect.DeepEqual(ffmpegArgs, want) {
		t.Errorf("ffmpeg args = %v, want %v", ffmpegArgs, want)
	}
	whisperArgs := readPipelineInvocation(t, filepath.Join(request.WorkDir, "transcribe"))
	prefix := filepath.Join(request.WorkDir, "transcribe", "out")
	wantWhisper := []string{
		"-m", request.ModelPath, "-f", wav, "-of", prefix, "-l", "ja",
		"-pp", "-otxt", "-osrt", "-ovtt", "--vad", "--vad-model", request.VADModelPath,
	}
	if !reflect.DeepEqual(whisperArgs, wantWhisper) {
		t.Errorf("whisper args = %v, want %v", whisperArgs, wantWhisper)
	}
}

func TestPipelineVADUnsupportedDoesNotRequireModel(t *testing.T) {
	request := pipelineRequestFixture(t, "job-no-vad", false)
	request.UseVAD = true
	request.VADModelPath = filepath.Join(request.WorkDir, "missing-vad.bin")
	home := t.TempDir()
	engine := wexec.NewCLIEngine(wexec.CLIEngineConfig{Resolve: func() (wexec.Discovery, error) {
		return wexec.Discovery{
			WhisperCLI: wexec.ToolStatus{Name: "whisper-cli", Path: pipelineStubPath(t, "whisper-cli"), Found: true},
			FFmpeg:     wexec.ToolStatus{Name: "ffmpeg", Path: pipelineStubPath(t, "ffmpeg"), Found: true},
		}, nil
	}})
	pipeline, err := NewPipeline(PipelineConfig{
		Engine:    engine,
		Publisher: NewPublisher(PublisherConfig{HomeDir: func() (string, error) { return home, nil }}),
		Env:       append(os.Environ(), "STUB_NO_VAD=1", "STUB_PROGRESS_INTERVAL=10ms"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pipeline.Run(context.Background(), request, &pipelineObserver{}); err != nil {
		t.Fatalf("Pipeline.Run without CLI VAD support: %v", err)
	}
	args := readPipelineInvocation(t, filepath.Join(request.WorkDir, "transcribe"))
	for _, arg := range args {
		if arg == "--vad" || arg == "--vad-model" {
			t.Fatalf("unsupported VAD argument present: %v", args)
		}
	}
}

func TestPipelineWithoutVADSkipsFeatureDetection(t *testing.T) {
	request := pipelineRequestFixture(t, "job-vad-disabled", false)
	engine := &fakePipelineEngine{detectErr: errors.New("help unavailable")}
	pipeline, err := NewPipeline(PipelineConfig{
		Engine:    engine,
		Publisher: NewPublisher(PublisherConfig{HomeDir: func() (string, error) { return t.TempDir(), nil }}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pipeline.Run(context.Background(), request, &pipelineObserver{}); err != nil {
		t.Fatalf("Pipeline.Run without VAD: %v", err)
	}
	if engine.detectCalls != 0 {
		t.Fatalf("feature detection calls = %d, want 0", engine.detectCalls)
	}
}

func TestPipelineFailureCodesAndRecovery(t *testing.T) {
	now := time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name         string
		configure    func(*fakePipelineEngine, *RunRequest)
		publisher    func(t *testing.T) *Publisher
		wantCode     ErrorCode
		wantPreserve bool
	}{
		{
			name: "missing model",
			configure: func(_ *fakePipelineEngine, request *RunRequest) {
				request.ModelPath = filepath.Join(request.WorkDir, "missing.bin")
			},
			wantCode: ErrorCodeModelMissing,
		},
		{
			name: "missing supported VAD model",
			configure: func(engine *fakePipelineEngine, request *RunRequest) {
				engine.features.VADSupported = true
				request.UseVAD = true
				request.VADModelPath = filepath.Join(request.WorkDir, "missing-vad.bin")
			},
			wantCode: ErrorCodeVADMissing,
		},
		{
			name: "conversion failure",
			configure: func(engine *fakePipelineEngine, _ *RunRequest) {
				engine.convertErr = errors.New("convert failed")
			},
			wantCode: ErrorCodeConversionFailed,
		},
		{
			name: "transcription failure",
			configure: func(engine *fakePipelineEngine, _ *RunRequest) {
				engine.transcribeErr = errors.New("transcribe failed")
			},
			wantCode: ErrorCodeTranscriptionFailed,
		},
		{
			name:      "publish failure",
			configure: func(_ *fakePipelineEngine, _ *RunRequest) {},
			publisher: func(t *testing.T) *Publisher {
				return NewPublisher(PublisherConfig{
					HomeDir: func() (string, error) { return t.TempDir(), nil },
					CopyFile: func(context.Context, string, string) error {
						return errors.New("publish failed")
					},
				})
			},
			wantCode:     ErrorCodeOutputPublishFailed,
			wantPreserve: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := pipelineRequestFixture(t, "job-failure", true)
			engine := &fakePipelineEngine{}
			test.configure(engine, &request)
			publisher := NewPublisher(PublisherConfig{HomeDir: func() (string, error) { return t.TempDir(), nil }})
			if test.publisher != nil {
				publisher = test.publisher(t)
			}
			pipeline, err := NewPipeline(PipelineConfig{Engine: engine, Publisher: publisher, Now: func() time.Time { return now }})
			if err != nil {
				t.Fatal(err)
			}
			_, runErr := pipeline.Run(context.Background(), request, &pipelineObserver{})
			var coded *RunError
			if !errors.As(runErr, &coded) || coded.Code != test.wantCode || coded.PreserveWorkDir != test.wantPreserve {
				t.Fatalf("Run error = %#v, want code=%s preserve=%t", runErr, test.wantCode, test.wantPreserve)
			}
			markerPath := filepath.Join(request.WorkDir, RecoveryMarkerName)
			_, markerErr := os.Stat(markerPath)
			if test.wantPreserve && markerErr != nil {
				t.Fatalf("recovery marker missing: %v", markerErr)
			}
			if !test.wantPreserve && !errors.Is(markerErr, os.ErrNotExist) {
				t.Fatalf("unexpected recovery marker: %v", markerErr)
			}
		})
	}
}

type fakePipelineEngine struct {
	features      wexec.WhisperFeatures
	detectErr     error
	detectCalls   int
	convertErr    error
	transcribeErr error
}

func (e *fakePipelineEngine) DetectWhisperFeatures(context.Context, wexec.WhisperFeatureRequest) (wexec.WhisperFeatures, error) {
	e.detectCalls++
	return e.features, e.detectErr
}

func (e *fakePipelineEngine) ConvertToWAV(_ context.Context, request wexec.ConvertRequest, observer wexec.Observer) error {
	if e.convertErr != nil {
		return e.convertErr
	}
	if observer != nil {
		observer.Log(wexec.LogStreamStderr, "converted")
	}
	return os.WriteFile(request.OutputPath, []byte("wav"), 0o600)
}

func (e *fakePipelineEngine) Transcribe(_ context.Context, request wexec.TranscribeRequest, observer wexec.Observer) (wexec.TranscriptionResult, error) {
	if e.transcribeErr != nil {
		return wexec.TranscriptionResult{}, e.transcribeErr
	}
	for _, format := range request.OutputFormats {
		if err := os.WriteFile(request.OutputPrefix+"."+format, []byte(format), 0o600); err != nil {
			return wexec.TranscriptionResult{}, err
		}
	}
	if observer != nil {
		observer.Progress(100)
	}
	return wexec.TranscriptionResult{VADApplied: request.UseVAD && e.features.VADSupported}, nil
}

func pipelineRequestFixture(t *testing.T, id string, withVADModel bool) RunRequest {
	t.Helper()
	root := t.TempDir()
	workDir, err := CreateWorkDir(root, id)
	if err != nil {
		t.Fatal(err)
	}
	input := filepath.Join(workDir, "input")
	model := filepath.Join(workDir, "model.bin")
	if err := os.WriteFile(input, []byte("input"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(model, []byte("model"), 0o600); err != nil {
		t.Fatal(err)
	}
	request := RunRequest{
		ID:               id,
		WorkDir:          workDir,
		InputPath:        input,
		OriginalFilename: "講義 01.mp3",
		ModelPath:        model,
		UseVAD:           withVADModel,
		OutputFormats:    []string{"txt", "srt", "vtt"},
	}
	if withVADModel {
		request.VADModelPath = filepath.Join(workDir, "vad.bin")
		if err := os.WriteFile(request.VADModelPath, []byte("vad"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return request
}

func pipelineStubPath(t *testing.T, name string) string {
	t.Helper()
	path, err := filepath.Abs(filepath.Join("..", "..", "testdata", "stubs", name))
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func readPipelineInvocation(t *testing.T, directory string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(directory, "invocation.json"))
	if err != nil {
		t.Fatal(err)
	}
	var invocation struct {
		Argv []string `json:"argv"`
	}
	if err := json.Unmarshal(data, &invocation); err != nil {
		t.Fatal(err)
	}
	return invocation.Argv
}
