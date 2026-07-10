package job

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	wexec "github.com/steepkit/whisper-cpp-gui/internal/exec"
)

type PipelineConfig struct {
	Engine    wexec.Engine
	Publisher *Publisher
	Env       []string
	Now       func() time.Time
}

// Pipeline orchestrates conversion, transcription, and output publishing.
// CLI arguments and process lifecycle remain owned by exec.Engine.
type Pipeline struct {
	engine    wexec.Engine
	publisher *Publisher
	env       []string
	now       func() time.Time
}

func NewPipeline(config PipelineConfig) (*Pipeline, error) {
	if config.Engine == nil {
		return nil, fmt.Errorf("pipeline engine is required")
	}
	if config.Publisher == nil {
		return nil, fmt.Errorf("pipeline publisher is required")
	}
	now := config.Now
	if now == nil {
		now = time.Now
	}
	var env []string
	if config.Env != nil {
		env = append([]string{}, config.Env...)
	}
	return &Pipeline{
		engine:    config.Engine,
		publisher: config.Publisher,
		env:       env,
		now:       now,
	}, nil
}

func (p *Pipeline) Run(ctx context.Context, request RunRequest, observer Observer) (RunResult, error) {
	if p == nil || p.engine == nil || p.publisher == nil {
		return RunResult{}, &RunError{Code: ErrorCodeRunFailed, Err: errors.New("pipeline is not configured")}
	}
	if err := validatePipelineRequest(request); err != nil {
		return RunResult{}, &RunError{Code: ErrorCodeRunFailed, Err: err}
	}
	if err := requireRegularFile(request.ModelPath); err != nil {
		return RunResult{}, &RunError{Code: ErrorCodeModelMissing, Err: fmt.Errorf("validate Whisper model: %w", err)}
	}

	if request.UseVAD {
		features, err := p.engine.DetectWhisperFeatures(ctx, wexec.WhisperFeatureRequest{Env: p.env})
		if err != nil {
			return RunResult{}, &RunError{Code: ErrorCodeTranscriptionFailed, Err: err}
		}
		if features.VADSupported {
			if err := requireRegularFile(request.VADModelPath); err != nil {
				return RunResult{}, &RunError{Code: ErrorCodeVADMissing, Err: fmt.Errorf("validate VAD model: %w", err)}
			}
		}
	}

	convertDir := filepath.Join(request.WorkDir, "convert")
	transcribeDir := filepath.Join(request.WorkDir, "transcribe")
	if err := createPipelineDirectory(convertDir); err != nil {
		return RunResult{}, &RunError{Code: ErrorCodeConversionFailed, Err: err}
	}
	if err := createPipelineDirectory(transcribeDir); err != nil {
		return RunResult{}, &RunError{Code: ErrorCodeTranscriptionFailed, Err: err}
	}

	wavPath := filepath.Join(convertDir, "audio.wav")
	if observer != nil {
		observer.Phase(PhaseConverting)
	}
	if err := p.engine.ConvertToWAV(ctx, wexec.ConvertRequest{
		InputPath:  request.InputPath,
		OutputPath: wavPath,
		Dir:        request.WorkDir,
		Env:        p.env,
	}, engineObserver{observer: observer}); err != nil {
		return RunResult{}, &RunError{Code: ErrorCodeConversionFailed, Err: err}
	}

	outputPrefix := filepath.Join(transcribeDir, "out")
	if observer != nil {
		observer.Phase(PhaseTranscribing)
	}
	if _, err := p.engine.Transcribe(ctx, wexec.TranscribeRequest{
		ModelPath:     request.ModelPath,
		InputPath:     wavPath,
		OutputPrefix:  outputPrefix,
		OutputFormats: append([]string(nil), request.OutputFormats...),
		UseVAD:        request.UseVAD,
		VADModelPath:  request.VADModelPath,
		Dir:           request.WorkDir,
		Env:           p.env,
	}, engineObserver{observer: observer}); err != nil {
		return RunResult{}, &RunError{Code: ErrorCodeTranscriptionFailed, Err: err}
	}

	if observer != nil {
		observer.Phase(PhaseMoving)
	}
	result, err := p.publisher.Publish(ctx, PublishRequest{
		JobID:            request.ID,
		WorkDir:          request.WorkDir,
		OutputPrefix:     outputPrefix,
		OriginalFilename: request.OriginalFilename,
		Formats:          request.OutputFormats,
	})
	if err == nil {
		return result, nil
	}
	if ctx.Err() != nil {
		return RunResult{}, ctx.Err()
	}
	publishErr := fmt.Errorf("publish transcription outputs: %w", err)
	if markerErr := MarkRecovery(request.WorkDir, request.ID, p.now()); markerErr != nil {
		return RunResult{}, &RunError{
			Code: ErrorCodeOutputPublishFailed,
			Err:  errors.Join(publishErr, fmt.Errorf("mark output recovery: %w", markerErr)),
		}
	}
	return RunResult{}, &RunError{
		Code:            ErrorCodeOutputPublishFailed,
		Err:             publishErr,
		PreserveWorkDir: true,
	}
}

type engineObserver struct {
	observer Observer
}

func (o engineObserver) Log(stream wexec.LogStream, line string) {
	if o.observer == nil {
		return
	}
	jobStream := LogStreamStdout
	if stream == wexec.LogStreamStderr {
		jobStream = LogStreamStderr
	}
	o.observer.Log(jobStream, line)
}

func (o engineObserver) Progress(percent int) {
	if o.observer != nil {
		o.observer.Progress(percent)
	}
}

func validatePipelineRequest(request RunRequest) error {
	if request.ID == "" || request.WorkDir == "" || request.InputPath == "" || request.ModelPath == "" {
		return fmt.Errorf("pipeline request paths and ID are required")
	}
	workDir, err := filepath.Abs(request.WorkDir)
	if err != nil {
		return fmt.Errorf("resolve pipeline work directory: %w", err)
	}
	if err := requireRealDirectory(workDir); err != nil {
		return fmt.Errorf("validate pipeline work directory: %w", err)
	}
	inputPath, err := filepath.Abs(request.InputPath)
	if err != nil {
		return fmt.Errorf("resolve pipeline input: %w", err)
	}
	if err := requirePathWithin(workDir, inputPath); err != nil {
		return fmt.Errorf("validate pipeline input: %w", err)
	}
	if err := requireRegularFile(inputPath); err != nil {
		return fmt.Errorf("validate pipeline input: %w", err)
	}
	if _, err := validateOutputFormats(request.OutputFormats); err != nil {
		return err
	}
	return nil
}

func createPipelineDirectory(path string) error {
	if err := os.Mkdir(path, 0o700); err != nil {
		return fmt.Errorf("create pipeline directory %s: %w", filepath.Base(path), err)
	}
	return nil
}
