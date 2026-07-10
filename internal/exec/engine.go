package exec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"
)

const (
	maxEngineLineBytes = 8 << 10
	maxHelpBytes       = 64 << 10
)

var (
	// ErrInvalidRequest reports a missing or inconsistent Engine request.
	ErrInvalidRequest = errors.New("invalid exec request")
	// ErrFFmpegNotFound reports that no executable ffmpeg was discovered.
	ErrFFmpegNotFound = errors.New("ffmpeg not found")
	// ErrWhisperCLINotFound reports that no executable whisper-cli was discovered.
	ErrWhisperCLINotFound = errors.New("whisper-cli not found")
	// ErrUnsupportedOutputFormat reports an output format outside the CLI allowlist.
	ErrUnsupportedOutputFormat = errors.New("unsupported output format")
)

// LogStream identifies a child-process output stream.
type LogStream string

const (
	LogStreamStdout LogStream = "stdout"
	LogStreamStderr LogStream = "stderr"
)

// Observer receives best-effort logs and progress while an Engine method runs.
// Implementations must be safe for concurrent calls from stdout and stderr.
type Observer interface {
	Log(stream LogStream, line string)
	Progress(percent int)
}

// ObserverFuncs adapts callbacks into an Observer. Nil callbacks are ignored.
type ObserverFuncs struct {
	OnLog      func(stream LogStream, line string)
	OnProgress func(percent int)
}

func (f ObserverFuncs) Log(stream LogStream, line string) {
	if f.OnLog != nil {
		f.OnLog(stream, line)
	}
}

func (f ObserverFuncs) Progress(percent int) {
	if f.OnProgress != nil {
		f.OnProgress(percent)
	}
}

// ConvertRequest describes a single ffmpeg conversion. A nil Env inherits the
// current process environment; a non-nil Env is passed to the child as-is.
type ConvertRequest struct {
	InputPath  string
	OutputPath string
	Dir        string
	Env        []string
}

// WhisperFeatureRequest controls feature detection. Env follows the same
// inheritance rule as ConvertRequest so tests can use the repository stubs.
type WhisperFeatureRequest struct {
	Env []string
}

// WhisperFeatures records capabilities discovered from whisper-cli --help.
type WhisperFeatures struct {
	VADSupported bool
}

// TranscribeRequest describes one whisper-cli invocation. OutputFormats must
// contain one or more values from txt, srt, and vtt. whisper-cli is always
// asked to use Japanese; callers cannot override the language.
type TranscribeRequest struct {
	ModelPath     string
	InputPath     string
	OutputPrefix  string
	OutputFormats []string
	UseVAD        bool
	VADModelPath  string
	Dir           string
	Env           []string
}

// TranscriptionResult records which optional CLI behavior was actually used.
type TranscriptionResult struct {
	VADApplied bool
}

// Engine hides CLI invocation details from the orchestration layer.
type Engine interface {
	ConvertToWAV(ctx context.Context, request ConvertRequest, observer Observer) error
	DetectWhisperFeatures(ctx context.Context, request WhisperFeatureRequest) (WhisperFeatures, error)
	Transcribe(ctx context.Context, request TranscribeRequest, observer Observer) (TranscriptionResult, error)
}

// DiscoveryResolver dynamically resolves CLI paths. It is called for every
// operation so tools installed after application startup are discovered.
type DiscoveryResolver func() (Discovery, error)

// CLIEngineConfig configures CLIEngine. A nil Resolve uses the current user
// config, PATH, and GOOS each time an operation starts.
type CLIEngineConfig struct {
	Resolve DiscoveryResolver
	Runner  *ProcessRunner
}

// CLIEngine implements Engine using direct child processes.
type CLIEngine struct {
	resolve DiscoveryResolver
	runner  *ProcessRunner

	featuresMu   sync.Mutex
	featuresPath string
	features     WhisperFeatures
	featuresOK   bool
}

// NewCLIEngine constructs a CLI-backed Engine. The zero config is suitable for
// production; configuration is re-read by its resolver on every operation.
func NewCLIEngine(config CLIEngineConfig) *CLIEngine {
	resolver := config.Resolve
	if resolver == nil {
		resolver = discoverCurrentTools
	}
	runner := config.Runner
	if runner == nil {
		runner = NewProcessRunner()
	}
	return &CLIEngine{resolve: resolver, runner: runner}
}

func discoverCurrentTools() (Discovery, error) {
	configPath, err := UserConfigPath()
	if err != nil {
		return Discovery{}, fmt.Errorf("locating user config: %w", err)
	}
	config, err := LoadUserConfig(configPath)
	if err != nil {
		return Discovery{}, fmt.Errorf("loading user config: %w", err)
	}
	return Discover(config, os.Getenv("PATH"), runtime.GOOS), nil
}

// ConvertToWAV converts input to a 16kHz mono WAV using the provisional M1-4
// ffmpeg contract. The argument order is intentionally exact until M0-3
// validates it against the packaged binary.
func (e *CLIEngine) ConvertToWAV(ctx context.Context, request ConvertRequest, observer Observer) error {
	if err := validateConvertRequest(request); err != nil {
		return err
	}
	discovery, err := e.discover()
	if err != nil {
		return err
	}
	if !discovery.FFmpeg.Found {
		return fmt.Errorf("converting to WAV: %w", ErrFFmpegNotFound)
	}

	return e.run(ctx, CommandSpec{
		Path: discovery.FFmpeg.Path,
		Args: []string{"-i", request.InputPath, "-ar", "16000", "-ac", "1", "-y", request.OutputPath},
		Dir:  request.Dir,
		Env:  request.Env,
	}, observer)
}

// DetectWhisperFeatures runs a bounded whisper-cli --help probe. Successful
// probes are cached by resolved binary path; failed probes are deliberately not
// cached so a repaired or transiently unavailable CLI is retried.
func (e *CLIEngine) DetectWhisperFeatures(ctx context.Context, request WhisperFeatureRequest) (WhisperFeatures, error) {
	discovery, err := e.discover()
	if err != nil {
		return WhisperFeatures{}, err
	}
	if !discovery.WhisperCLI.Found {
		return WhisperFeatures{}, fmt.Errorf("detecting whisper-cli features: %w", ErrWhisperCLINotFound)
	}
	return e.detectWhisperFeaturesAtPath(ctx, discovery.WhisperCLI.Path, request.Env)
}

func (e *CLIEngine) detectWhisperFeaturesAtPath(ctx context.Context, whisperPath string, env []string) (WhisperFeatures, error) {
	e.featuresMu.Lock()
	defer e.featuresMu.Unlock()
	if e.featuresOK && e.featuresPath == whisperPath {
		return e.features, nil
	}

	help := &boundedBuffer{limit: maxHelpBytes}
	err := e.runner.Run(ctx, CommandSpec{
		Path:   whisperPath,
		Args:   []string{"--help"},
		Env:    env,
		Stdout: help,
		Stderr: help,
	})
	if err != nil {
		return WhisperFeatures{}, fmt.Errorf("detecting whisper-cli features for %q: %w", whisperPath, err)
	}

	features := WhisperFeatures{
		VADSupported: vadFlagPattern.Match(help.Bytes()) && vadModelFlagPattern.Match(help.Bytes()),
	}
	e.featuresPath = whisperPath
	e.features = features
	e.featuresOK = true
	return features, nil
}

// Transcribe invokes whisper-cli with allowlisted outputs. If VAD is requested,
// an available VAD-capable CLI receives both required VAD arguments; a CLI
// without VAD support runs without them.
func (e *CLIEngine) Transcribe(ctx context.Context, request TranscribeRequest, observer Observer) (TranscriptionResult, error) {
	if err := validateTranscribeRequest(request); err != nil {
		return TranscriptionResult{}, err
	}
	discovery, err := e.discover()
	if err != nil {
		return TranscriptionResult{}, err
	}
	if !discovery.WhisperCLI.Found {
		return TranscriptionResult{}, fmt.Errorf("transcribing: %w", ErrWhisperCLINotFound)
	}

	result := TranscriptionResult{}
	args := []string{"-m", request.ModelPath, "-f", request.InputPath, "-of", request.OutputPrefix, "-l", "ja", "-pp"}
	for _, format := range request.OutputFormats {
		args = append(args, "-o"+format)
	}
	if request.UseVAD {
		features, err := e.detectWhisperFeaturesAtPath(ctx, discovery.WhisperCLI.Path, request.Env)
		if err != nil {
			return TranscriptionResult{}, err
		}
		if features.VADSupported {
			if request.VADModelPath == "" {
				return TranscriptionResult{}, fmt.Errorf("%w: VAD model path is required when VAD is supported", ErrInvalidRequest)
			}
			args = append(args, "--vad", "--vad-model", request.VADModelPath)
			result.VADApplied = true
		}
	}

	err = e.run(ctx, CommandSpec{
		Path: discovery.WhisperCLI.Path,
		Args: args,
		Dir:  request.Dir,
		Env:  request.Env,
	}, observer)
	if err != nil {
		return TranscriptionResult{}, err
	}
	return result, nil
}

func (e *CLIEngine) discover() (Discovery, error) {
	if e == nil || e.resolve == nil || e.runner == nil {
		return Discovery{}, fmt.Errorf("%w: CLI engine is not initialized", ErrInvalidRequest)
	}
	discovery, err := e.resolve()
	if err != nil {
		return Discovery{}, fmt.Errorf("discovering CLI tools: %w", err)
	}
	return discovery, nil
}

func (e *CLIEngine) run(ctx context.Context, spec CommandSpec, observer Observer) error {
	var stdout, stderr *lineWriter
	if observer != nil {
		stdout = newLineWriter(LogStreamStdout, observer, false)
		stderr = newLineWriter(LogStreamStderr, observer, true)
		spec.Stdout = stdout
		spec.Stderr = stderr
	}
	err := e.runner.Run(ctx, spec)
	if stdout != nil {
		stdout.flush()
		stderr.flush()
	}
	if err != nil {
		return fmt.Errorf("running %q: %w", spec.Path, err)
	}
	return nil
}

func validateConvertRequest(request ConvertRequest) error {
	if request.InputPath == "" {
		return fmt.Errorf("%w: input path is required", ErrInvalidRequest)
	}
	if request.OutputPath == "" {
		return fmt.Errorf("%w: output path is required", ErrInvalidRequest)
	}
	return nil
}

func validateTranscribeRequest(request TranscribeRequest) error {
	for _, field := range []struct {
		name  string
		value string
	}{
		{"model path", request.ModelPath},
		{"input path", request.InputPath},
		{"output prefix", request.OutputPrefix},
	} {
		if field.value == "" {
			return fmt.Errorf("%w: %s is required", ErrInvalidRequest, field.name)
		}
	}
	if len(request.OutputFormats) == 0 {
		return fmt.Errorf("%w: at least one output format is required", ErrInvalidRequest)
	}
	seen := make(map[string]struct{}, len(request.OutputFormats))
	for _, format := range request.OutputFormats {
		switch format {
		case "txt", "srt", "vtt":
		default:
			return fmt.Errorf("%w: %w %q", ErrInvalidRequest, ErrUnsupportedOutputFormat, format)
		}
		if _, duplicate := seen[format]; duplicate {
			return fmt.Errorf("%w: duplicate output format %q", ErrInvalidRequest, format)
		}
		seen[format] = struct{}{}
	}
	return nil
}

var (
	progressPattern     = regexp.MustCompile(`(?i)\bprogress\s*=\s*([0-9]+)\s*%`)
	vadFlagPattern      = regexp.MustCompile(`(?:^|[\s,])--vad(?:$|[\s,])`)
	vadModelFlagPattern = regexp.MustCompile(`(?:^|[\s,])--vad-model(?:$|[\s,])`)
)

type lineWriter struct {
	stream         LogStream
	observer       Observer
	parseProgress  bool
	line           []byte
	discardingLine bool
}

func newLineWriter(stream LogStream, observer Observer, parseProgress bool) *lineWriter {
	return &lineWriter{
		stream:        stream,
		observer:      observer,
		parseProgress: parseProgress,
		line:          make([]byte, 0, maxEngineLineBytes),
	}
}

func (w *lineWriter) Write(data []byte) (int, error) {
	for _, value := range data {
		if value == '\n' {
			w.flush()
			continue
		}
		if w.discardingLine {
			continue
		}
		w.line = append(w.line, value)
		if len(w.line) == maxEngineLineBytes {
			w.flush()
			w.discardingLine = true
		}
	}
	return len(data), nil
}

func (w *lineWriter) flush() {
	if len(w.line) == 0 {
		w.discardingLine = false
		return
	}
	line := boundedValidUTF8(w.line, maxEngineLineBytes)
	w.observer.Log(w.stream, line)
	if w.parseProgress {
		if match := progressPattern.FindStringSubmatch(line); len(match) == 2 {
			digits := strings.TrimLeft(match[1], "0")
			if digits == "" {
				digits = "0"
			}
			if len(digits) > 3 {
				w.observer.Progress(100)
			} else if percent, err := strconv.Atoi(digits); err == nil {
				w.observer.Progress(min(percent, 100))
			}
		}
	}
	w.line = w.line[:0]
	w.discardingLine = false
}

func boundedValidUTF8(value []byte, limit int) string {
	value = bytes.TrimSuffix(value, []byte{'\r'})
	valid := bytes.ToValidUTF8(value, []byte(string(utf8.RuneError)))
	if len(valid) <= limit {
		return string(valid)
	}
	valid = valid[:limit]
	for len(valid) > 0 && !utf8.Valid(valid) {
		valid = valid[:len(valid)-1]
	}
	return string(valid)
}

// boundedBuffer retains at most limit bytes even when a child writes an
// unterminated stream. It is safe for cmd stdout/stderr concurrent writes.
type boundedBuffer struct {
	mu    sync.Mutex
	limit int
	buf   []byte
}

func (b *boundedBuffer) Write(value []byte) (int, error) {
	originalLength := len(value)
	b.mu.Lock()
	defer b.mu.Unlock()
	remaining := b.limit - len(b.buf)
	if remaining > 0 {
		if len(value) > remaining {
			value = value[:remaining]
		}
		b.buf = append(b.buf, value...)
	}
	return originalLength, nil
}

func (b *boundedBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.buf...)
}

var _ io.Writer = (*lineWriter)(nil)
