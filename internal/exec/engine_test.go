package exec

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
)

type recordingObserver struct {
	mu       sync.Mutex
	logs     []recordedLog
	progress []int
}

type recordedLog struct {
	stream LogStream
	line   string
}

func (o *recordingObserver) Log(stream LogStream, line string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.logs = append(o.logs, recordedLog{stream: stream, line: line})
}

func (o *recordingObserver) Progress(percent int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.progress = append(o.progress, percent)
}

func TestCLIEngineStubPipelineUsesSeparateInvocationDirectories(t *testing.T) {
	root := t.TempDir()
	convertDir := filepath.Join(root, "convert")
	transcribeDir := filepath.Join(root, "transcribe")
	input := filepath.Join(root, "input.mp4")
	wav := filepath.Join(convertDir, "audio.wav")
	prefix := filepath.Join(transcribeDir, "out")
	if err := os.WriteFile(input, []byte("not media"), 0o600); err != nil {
		t.Fatal(err)
	}

	engine := newStubCLIEngine(t)
	observer := &recordingObserver{}
	env := append(os.Environ(), "STUB_PROGRESS_INTERVAL=10ms")
	if err := engine.ConvertToWAV(context.Background(), ConvertRequest{
		InputPath: input, OutputPath: wav, Env: env,
	}, observer); err != nil {
		t.Fatalf("ConvertToWAV: %v", err)
	}
	result, err := engine.Transcribe(context.Background(), TranscribeRequest{
		ModelPath:     filepath.Join(root, "model.bin"),
		InputPath:     wav,
		OutputPrefix:  prefix,
		OutputFormats: []string{"txt", "srt"},
		UseVAD:        true,
		VADModelPath:  filepath.Join(root, "vad.bin"),
		Env:           env,
	}, observer)
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
	if !result.VADApplied {
		t.Fatal("VAD was not applied despite the VAD-capable stub")
	}

	convertInvocation := readInvocation(t, convertDir)
	if want := []string{"-i", input, "-ar", "16000", "-ac", "1", "-y", wav}; !reflect.DeepEqual(convertInvocation.Argv, want) {
		t.Errorf("ffmpeg argv = %#v, want %#v", convertInvocation.Argv, want)
	}
	transcribeInvocation := readInvocation(t, transcribeDir)
	wantWhisper := []string{
		"-m", filepath.Join(root, "model.bin"), "-f", wav, "-of", prefix, "-l", "ja",
		"-pp", "-otxt", "-osrt", "--vad", "--vad-model", filepath.Join(root, "vad.bin"),
	}
	if !reflect.DeepEqual(transcribeInvocation.Argv, wantWhisper) {
		t.Errorf("whisper argv = %#v, want %#v", transcribeInvocation.Argv, wantWhisper)
	}
	if _, err := os.Stat(prefix + ".txt"); err != nil {
		t.Errorf("txt output: %v", err)
	}
	if _, err := os.Stat(prefix + ".srt"); err != nil {
		t.Errorf("srt output: %v", err)
	}
	if _, err := os.Stat(prefix + ".vtt"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("unexpected vtt output stat error = %v", err)
	}

	observer.mu.Lock()
	defer observer.mu.Unlock()
	if !reflect.DeepEqual(observer.progress, []int{25, 50, 75, 100}) {
		t.Errorf("progress = %v, want [25 50 75 100]", observer.progress)
	}
	if len(observer.logs) < 5 {
		t.Errorf("log callback count = %d, want ffmpeg plus whisper logs", len(observer.logs))
	}
}

func TestCLIEngineVADDetectionCachesByPathAndInvalidatesOnPathChange(t *testing.T) {
	root := t.TempDir()
	first := filepath.Join(root, "first-whisper")
	second := filepath.Join(root, "second-whisper")
	writeFeatureStub(t, first, true)
	writeFeatureStub(t, second, false)

	current := first
	engine := NewCLIEngine(CLIEngineConfig{Resolve: func() (Discovery, error) {
		return Discovery{WhisperCLI: ToolStatus{Name: "whisper-cli", Path: current, Found: true}}, nil
	}})
	features, err := engine.DetectWhisperFeatures(context.Background(), WhisperFeatureRequest{})
	if err != nil || !features.VADSupported {
		t.Fatalf("first DetectWhisperFeatures = %+v, %v; want VAD supported", features, err)
	}
	features, err = engine.DetectWhisperFeatures(context.Background(), WhisperFeatureRequest{})
	if err != nil || !features.VADSupported {
		t.Fatalf("cached DetectWhisperFeatures = %+v, %v; want VAD supported", features, err)
	}
	if got := readLines(t, first+".calls"); len(got) != 1 {
		t.Fatalf("first help probes = %d, want 1", len(got))
	}

	current = second
	features, err = engine.DetectWhisperFeatures(context.Background(), WhisperFeatureRequest{})
	if err != nil || features.VADSupported {
		t.Fatalf("path-changed DetectWhisperFeatures = %+v, %v; want VAD unsupported", features, err)
	}
	if got := readLines(t, second+".calls"); len(got) != 1 {
		t.Fatalf("second help probes = %d, want 1", len(got))
	}
}

func TestCLIEngineVADHelpFailureRetries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "flaky-whisper")
	writeFlakyFeatureStub(t, path)
	engine := NewCLIEngine(CLIEngineConfig{Resolve: func() (Discovery, error) {
		return Discovery{WhisperCLI: ToolStatus{Name: "whisper-cli", Path: path, Found: true}}, nil
	}})
	if _, err := engine.DetectWhisperFeatures(context.Background(), WhisperFeatureRequest{}); err == nil {
		t.Fatal("first help probe succeeded, want failure")
	}
	features, err := engine.DetectWhisperFeatures(context.Background(), WhisperFeatureRequest{})
	if err != nil || !features.VADSupported {
		t.Fatalf("second help probe = %+v, %v; want successful VAD detection", features, err)
	}
	if got := readLines(t, path+".calls"); len(got) != 2 {
		t.Fatalf("help probes = %d, want 2 after failed probe retry", len(got))
	}
}

func TestCLIEngineVADUnsupportedRunsWithoutVAD(t *testing.T) {
	dir := t.TempDir()
	prefix := filepath.Join(dir, "out")
	engine := newStubCLIEngine(t)
	result, err := engine.Transcribe(context.Background(), TranscribeRequest{
		ModelPath:     "model.bin",
		InputPath:     "input.wav",
		OutputPrefix:  prefix,
		OutputFormats: []string{"vtt"},
		UseVAD:        true,
		Env:           append(os.Environ(), "STUB_NO_VAD=1", "STUB_PROGRESS_INTERVAL=10ms"),
	}, nil)
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
	if result.VADApplied {
		t.Fatal("VAD applied even though --help did not advertise it")
	}
	invocation := readInvocation(t, dir)
	if contains(invocation.Argv, "--vad") || contains(invocation.Argv, "--vad-model") {
		t.Errorf("unsupported VAD flags were passed: %v", invocation.Argv)
	}
}

func TestCLIEngineMissingToolsAndInvalidRequests(t *testing.T) {
	missing := NewCLIEngine(CLIEngineConfig{Resolve: func() (Discovery, error) { return Discovery{}, nil }})
	if err := missing.ConvertToWAV(context.Background(), ConvertRequest{InputPath: "input", OutputPath: "out"}, nil); !errors.Is(err, ErrFFmpegNotFound) {
		t.Errorf("ConvertToWAV missing tool error = %v", err)
	}
	if _, err := missing.DetectWhisperFeatures(context.Background(), WhisperFeatureRequest{}); !errors.Is(err, ErrWhisperCLINotFound) {
		t.Errorf("DetectWhisperFeatures missing tool error = %v", err)
	}
	if _, err := missing.Transcribe(context.Background(), TranscribeRequest{
		ModelPath: "model", InputPath: "input", OutputPrefix: "out", OutputFormats: []string{"txt"},
	}, nil); !errors.Is(err, ErrWhisperCLINotFound) {
		t.Errorf("Transcribe missing tool error = %v", err)
	}

	valid := newStubCLIEngine(t)
	if err := valid.ConvertToWAV(context.Background(), ConvertRequest{OutputPath: "out"}, nil); !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("invalid convert error = %v", err)
	}
	if _, err := valid.Transcribe(context.Background(), TranscribeRequest{
		ModelPath: "model", InputPath: "input", OutputPrefix: "out", OutputFormats: []string{"json"},
	}, nil); !errors.Is(err, ErrUnsupportedOutputFormat) || !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("invalid format error = %v", err)
	}
}

func TestLineWriterBoundsHugeUnterminatedOutput(t *testing.T) {
	observer := &recordingObserver{}
	writer := newLineWriter(LogStreamStderr, observer, true)
	huge := append([]byte(strings.Repeat("x", maxEngineLineBytes*16)), 0xff, 0x80)
	if written, err := writer.Write(huge); err != nil || written != len(huge) {
		t.Fatalf("Write = %d, %v; want %d, nil", written, err, len(huge))
	}
	writer.flush()

	observer.mu.Lock()
	defer observer.mu.Unlock()
	if len(observer.logs) != 1 {
		t.Fatalf("log count = %d, want 1 bounded logical line", len(observer.logs))
	}
	line := observer.logs[0].line
	if len(line) > maxEngineLineBytes {
		t.Errorf("line length = %d, exceeds bound %d", len(line), maxEngineLineBytes)
	}
	if !utf8.ValidString(line) {
		t.Errorf("line is not valid UTF-8: %q", line)
	}
}

func TestLineWriterParsesAndClampsObservedWhisperProgress(t *testing.T) {
	observer := &recordingObserver{}
	writer := newLineWriter(LogStreamStderr, observer, true)
	for _, line := range []string{
		"whisper_print_progress_callback: progress = 0%\n",
		"whisper_print_progress_callback: progress = 42%\n",
		"whisper_print_progress_callback: progress = 272%\n",
		"whisper_print_progress_callback: progress = 1000%\n",
		"whisper_print_progress_callback: progress = 999999999999999999999999%\n",
		"whisper_print_progress_callback: progress = 0000%\n",
		"progress = invalid%\n",
	} {
		if _, err := writer.Write([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	observer.mu.Lock()
	defer observer.mu.Unlock()
	if !reflect.DeepEqual(observer.progress, []int{0, 42, 100, 100, 100, 0}) {
		t.Fatalf("progress = %v, want [0 42 100 100 100 0]", observer.progress)
	}
}

func TestCLIEngineFeatureDetectionConcurrent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "whisper")
	writeFeatureStub(t, path, true)
	engine := NewCLIEngine(CLIEngineConfig{Resolve: func() (Discovery, error) {
		return Discovery{WhisperCLI: ToolStatus{Name: "whisper-cli", Path: path, Found: true}}, nil
	}})

	var group sync.WaitGroup
	errs := make(chan error, 16)
	for range 16 {
		group.Add(1)
		go func() {
			defer group.Done()
			features, err := engine.DetectWhisperFeatures(context.Background(), WhisperFeatureRequest{})
			if err != nil {
				errs <- err
				return
			}
			if !features.VADSupported {
				errs <- errors.New("VAD unexpectedly unsupported")
			}
		}()
	}
	group.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if got := readLines(t, path+".calls"); len(got) != 1 {
		t.Fatalf("concurrent help probes = %d, want one cached probe", len(got))
	}
}

func newStubCLIEngine(t *testing.T) *CLIEngine {
	t.Helper()
	return NewCLIEngine(CLIEngineConfig{Resolve: func() (Discovery, error) {
		return Discovery{
			WhisperCLI: ToolStatus{Name: "whisper-cli", Path: stubPath(t, "whisper-cli"), Found: true},
			FFmpeg:     ToolStatus{Name: "ffmpeg", Path: stubPath(t, "ffmpeg"), Found: true},
		}, nil
	}})
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func writeFeatureStub(t *testing.T, path string, vad bool) {
	t.Helper()
	help := "  --vad                          enable VAD\\n  --vad-model FNAME              VAD model\\n"
	if !vad {
		help = "  --model FNAME                  model\\n"
	}
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" >>\"" + path + ".calls\"\n"
	script += "if [ \"$1\" = \"--help\" ]; then\nprintf '" + help + "'\nexit 0\nfi\nexit 1\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

func writeFlakyFeatureStub(t *testing.T, path string) {
	t.Helper()
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" >>\"" + path + ".calls\"\n"
	script += "count=$(wc -l <\"" + path + ".calls\")\nif [ \"$count\" -eq 1 ]; then exit 1; fi\n"
	script += "printf '%s\\n' '  --vad enabled' '  --vad-model FNAME'\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return strings.Fields(strings.TrimSpace(string(data)))
}
