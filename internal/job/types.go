package job

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const (
	DefaultQueueCapacity       = 4
	DefaultRunTimeout          = 12 * time.Hour
	DefaultLogCapacityBytes    = 512 * 1024
	DefaultLogLineBytes        = 8 * 1024
	DefaultStderrCapacityBytes = 64 * 1024
	DefaultTerminalCapacity    = 100
	DefaultTerminalTTL         = 24 * time.Hour
	DefaultSubscriberCapacity  = 64
	DefaultSubscriberLimit     = 16
)

var (
	ErrQueueFull         = errors.New("job queue is full")
	ErrNotFound          = errors.New("job not found")
	ErrDuplicateID       = errors.New("job ID already exists")
	ErrInvalidRequest    = errors.New("invalid job request")
	ErrInvalidTransition = errors.New("invalid job status transition")
	ErrNotCancelable     = errors.New("job is not cancelable")
	ErrManagerClosed     = errors.New("job manager is closed")
	ErrSubscriberLimit   = errors.New("job subscriber limit reached")
)

// Status is the externally visible state of a job.
type Status string

const (
	StatusQueued    Status = "queued"
	StatusRunning   Status = "running"
	StatusDone      Status = "done"
	StatusFailed    Status = "failed"
	StatusCancelled Status = "cancelled"
)

// ErrorCode is a stable machine-readable failure reason.
type ErrorCode string

const (
	ErrorCodeRunFailed           ErrorCode = "run_failed"
	ErrorCodeTimeout             ErrorCode = "timeout"
	ErrorCodeManagerClosed       ErrorCode = "manager_closed"
	ErrorCodeModelMissing        ErrorCode = "model_missing"
	ErrorCodeVADMissing          ErrorCode = "vad_model_missing"
	ErrorCodeConversionFailed    ErrorCode = "conversion_failed"
	ErrorCodeTranscriptionFailed ErrorCode = "transcription_failed"
	ErrorCodeOutputPublishFailed ErrorCode = "output_publish_failed"
)

// Phase identifies the current step while a job is running. Terminal and
// queued jobs have an empty phase so the five-state machine remains stable.
type Phase string

const (
	PhaseConverting   Phase = "converting"
	PhaseTranscribing Phase = "transcribing"
	PhaseMoving       Phase = "moving"
)

// WarningCode is a stable machine-readable non-fatal warning reason.
type WarningCode string

const WarningCodeCleanupFailed WarningCode = "cleanup_failed"

// LogStream identifies the source of a log line.
type LogStream string

const (
	LogStreamStdout LogStream = "stdout"
	LogStreamStderr LogStream = "stderr"
)

// EventType identifies a best-effort subscription event.
type EventType string

const (
	EventState    EventType = "state"
	EventProgress EventType = "progress"
	EventLog      EventType = "log"
)

// RunRequest contains the paths and preset a Runner needs. If ID is empty,
// Manager assigns one. If WorkDir is empty, Manager derives it from TempRoot.
type RunRequest struct {
	ID               string   `json:"id"`
	InputPath        string   `json:"-"`
	OutputDir        string   `json:"-"`
	Preset           string   `json:"preset,omitempty"`
	WorkDir          string   `json:"-"`
	OriginalFilename string   `json:"filename,omitempty"`
	ModelPath        string   `json:"-"`
	VADModelPath     string   `json:"-"`
	UseVAD           bool     `json:"-"`
	OutputFormats    []string `json:"-"`
}

// RunResult is committed to the job record only after Runner returns nil.
type RunResult struct {
	FinalOutputDir string
	OutputFiles    []string
}

// Runner owns process execution and process-group cancellation. Implementations
// must return after ctx is cancelled.
type Runner interface {
	Run(ctx context.Context, request RunRequest, observer Observer) (RunResult, error)
}

// Observer receives best-effort output from a running job. Calls made after Run
// returns are ignored.
type Observer interface {
	Log(stream LogStream, line string)
	Progress(percent int)
	Phase(phase Phase)
}

// RunError lets a Runner expose a stable failure code without coupling Manager
// to a concrete execution implementation.
type RunError struct {
	Code            ErrorCode
	Err             error
	PreserveWorkDir bool
}

func (e *RunError) Error() string {
	if e == nil {
		return ""
	}
	if e.Err != nil {
		return e.Err.Error()
	}
	return string(e.Code)
}

func (e *RunError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// LogEntry is one retained, bounded log line.
type LogEntry struct {
	Stream LogStream `json:"stream"`
	Line   string    `json:"line"`
}

// Warning records a non-fatal problem without changing the terminal status.
type Warning struct {
	Code    WarningCode `json:"code"`
	Message string      `json:"message,omitempty"`
}

// Snapshot is the authoritative point-in-time representation of a job.
type Snapshot struct {
	RunRequest
	Status         Status     `json:"status"`
	Phase          Phase      `json:"phase,omitempty"`
	Progress       int        `json:"progress"`
	ProgressKnown  bool       `json:"-"`
	Logs           []LogEntry `json:"logs,omitempty"`
	StderrTail     string     `json:"stderr_tail,omitempty"`
	OutputFiles    []string   `json:"outputs,omitempty"`
	RecoveryPath   string     `json:"recovery_path,omitempty"`
	ErrorCode      ErrorCode  `json:"error_code,omitempty"`
	Error          string     `json:"error,omitempty"`
	Warnings       []Warning  `json:"warnings,omitempty"`
	QueuedAt       time.Time  `json:"queued_at"`
	StartedAt      time.Time  `json:"started_at,omitempty"`
	FinishedAt     time.Time  `json:"finished_at,omitempty"`
	CleanupPending bool       `json:"cleanup_pending,omitempty"`
}

// Event is a non-blocking notification. Snapshot remains authoritative after
// overflow, reconnect, or any other event loss.
type Event struct {
	Type      EventType `json:"type"`
	JobID     string    `json:"job_id"`
	Status    Status    `json:"status,omitempty"`
	Phase     Phase     `json:"phase,omitempty"`
	Progress  int       `json:"progress,omitempty"`
	Log       *LogEntry `json:"log,omitempty"`
	ErrorCode ErrorCode `json:"error_code,omitempty"`
}

// Timer is the subset of time.Timer required by Manager.
type Timer interface {
	Stop() bool
}

// Clock supplies timestamps and timeout callbacks. Tests can inject a manual
// clock without relying on wall-clock sleeps.
type Clock interface {
	Now() time.Time
	AfterFunc(delay time.Duration, callback func()) Timer
}

// CleanupFunc removes a job work directory after every terminal path.
type CleanupFunc func(workDir string) error

// Config contains bounded resource limits and test seams for Manager.
// Zero-valued limits select their documented defaults.
type Config struct {
	QueueCapacity       int
	RunTimeout          time.Duration
	LogCapacityBytes    int
	LogLineBytes        int
	StderrCapacityBytes int
	TerminalCapacity    int
	TerminalTTL         time.Duration
	SubscriberCapacity  int
	SubscriberLimit     int
	TempRoot            string
	Cleanup             CleanupFunc
	Clock               Clock
	IDGenerator         func() (string, error)
}

type realClock struct{}

func (realClock) Now() time.Time {
	return time.Now()
}

func (realClock) AfterFunc(delay time.Duration, callback func()) Timer {
	return time.AfterFunc(delay, callback)
}

// NewID returns a cryptographically random job identifier.
func NewID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate job ID: %w", err)
	}
	return "job-" + hex.EncodeToString(value[:]), nil
}

// DefaultTempRoot returns the per-user cache path used for recoverable job
// work. A user-owned parent avoids predictable-name squatting in a shared
// system temporary directory.
func DefaultTempRoot() (string, error) {
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("resolve user cache directory: %w", err)
	}
	if !filepath.IsAbs(cacheDir) {
		return "", fmt.Errorf("user cache directory is not absolute")
	}
	return filepath.Join(cacheDir, "whisper-cpp-gui", "jobs"), nil
}

func isTerminal(status Status) bool {
	switch status {
	case StatusDone, StatusFailed, StatusCancelled:
		return true
	default:
		return false
	}
}

func validTransition(from, to Status) bool {
	switch from {
	case StatusQueued:
		return to == StatusRunning || to == StatusCancelled
	case StatusRunning:
		return to == StatusDone || to == StatusFailed || to == StatusCancelled
	default:
		return false
	}
}
