package model

import (
	"errors"
	"net/http"
	"time"
)

const (
	DefaultSubscriberCapacity  = 32
	DefaultSubscriberLimit     = 16
	DefaultPartialMaxAge       = 24 * time.Hour
	DefaultDownloadTimeout     = 12 * time.Hour
	DefaultDownloadIdleTimeout = 2 * time.Minute
	DefaultProgressStepBytes   = int64(1 << 20)
)

var (
	ErrManagerClosed               = errors.New("model manager is closed")
	ErrDownloadInProgress          = errors.New("model download is already in progress")
	ErrModelBusy                   = errors.New("model is busy in another process")
	ErrAlreadyDownloaded           = errors.New("model is already downloaded")
	ErrModelUnavailable            = errors.New("model is not downloaded and valid")
	ErrModelInUse                  = errors.New("model is in use")
	ErrSubscriberLimit             = errors.New("model subscriber limit reached")
	ErrCrossProcessLockUnsupported = errors.New("cross-process model locking is unsupported on this platform")
)

// State is the externally visible state of one manifest entry.
type State string

const (
	StateMissing     State = "missing"
	StateDownloading State = "downloading"
	StateDownloaded  State = "downloaded"
	StateInvalid     State = "invalid"
)

// ErrorCode is a stable, path-free download failure reason.
type ErrorCode string

const (
	ErrorCodeNone           ErrorCode = ""
	ErrorCodeCancelled      ErrorCode = "cancelled"
	ErrorCodeNetwork        ErrorCode = "network_error"
	ErrorCodeHTTPStatus     ErrorCode = "http_status"
	ErrorCodeContentLength  ErrorCode = "content_length"
	ErrorCodeSizeMismatch   ErrorCode = "size_mismatch"
	ErrorCodeChecksum       ErrorCode = "checksum_mismatch"
	ErrorCodeStorage        ErrorCode = "storage_error"
	ErrorCodeRedirectPolicy ErrorCode = "redirect_policy"
	ErrorCodeTimeout        ErrorCode = "timeout"
)

// Snapshot is authoritative after reconnect or event loss. It contains no
// source URL or local filesystem path.
type Snapshot struct {
	Model           Descriptor `json:"model"`
	State           State      `json:"state"`
	BytesDownloaded int64      `json:"bytes_downloaded"`
	ErrorCode       ErrorCode  `json:"error_code,omitempty"`
}

// EventType identifies a best-effort subscription event.
type EventType string

const (
	EventState    EventType = "state"
	EventProgress EventType = "progress"
)

// Event is a bounded, best-effort notification. Snapshot remains authoritative.
type Event struct {
	Type            EventType `json:"type"`
	Name            string    `json:"name"`
	State           State     `json:"state,omitempty"`
	BytesDownloaded int64     `json:"bytes_downloaded,omitempty"`
	ErrorCode       ErrorCode `json:"error_code,omitempty"`
}

// Config supplies bounded limits and test seams. Zero values select defaults.
type Config struct {
	Directory           string
	Client              *http.Client
	SubscriberCapacity  int
	SubscriberLimit     int
	DownloadTimeout     time.Duration
	DownloadIdleTimeout time.Duration
	ProgressStepBytes   int64
	manifestOverride    []manifestEntry
	now                 func() time.Time
	validateFile        func(string, Descriptor) State
}
