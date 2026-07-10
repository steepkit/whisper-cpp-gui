package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/steepkit/whisper-cpp-gui/internal/job"
)

const sseHeartbeatInterval = 15 * time.Second

type jobAPIErrorResponse struct {
	ErrorCode string `json:"error_code"`
}

type jobOutputsResponse struct {
	ID           string        `json:"id"`
	Status       job.Status    `json:"status"`
	Outputs      []string      `json:"outputs"`
	RecoveryPath string        `json:"recovery_path,omitempty"`
	ErrorCode    job.ErrorCode `json:"error_code,omitempty"`
}

type jobSnapshotResponse struct {
	ID             string            `json:"id"`
	Preset         string            `json:"preset,omitempty"`
	Filename       string            `json:"filename,omitempty"`
	Status         job.Status        `json:"status"`
	Phase          job.Phase         `json:"phase,omitempty"`
	Progress       *int              `json:"progress"`
	Logs           []job.LogEntry    `json:"logs"`
	StderrTail     string            `json:"stderr_tail,omitempty"`
	Outputs        []string          `json:"outputs"`
	RecoveryPath   string            `json:"recovery_path,omitempty"`
	ErrorCode      job.ErrorCode     `json:"error_code,omitempty"`
	Warnings       []job.WarningCode `json:"warnings"`
	QueuedAt       time.Time         `json:"queued_at"`
	StartedAt      *time.Time        `json:"started_at"`
	FinishedAt     *time.Time        `json:"finished_at"`
	CleanupPending bool              `json:"cleanup_pending,omitempty"`
}

type jobEventResponse struct {
	Type      job.EventType `json:"type"`
	JobID     string        `json:"job_id"`
	Status    job.Status    `json:"status,omitempty"`
	Phase     job.Phase     `json:"phase,omitempty"`
	Progress  *int          `json:"progress,omitempty"`
	Log       *job.LogEntry `json:"log,omitempty"`
	ErrorCode job.ErrorCode `json:"error_code,omitempty"`
}

func (s *Server) handleJobEvents(w http.ResponseWriter, r *http.Request) {
	if s.jobs == nil {
		writeJobAPIError(w, http.StatusServiceUnavailable, "manager_unavailable")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJobAPIError(w, http.StatusInternalServerError, "streaming_unsupported")
		return
	}

	snapshot, events, unsubscribe, err := s.jobs.Subscribe(r.PathValue("id"))
	if err != nil {
		writeJobManagerError(w, err)
		return
	}
	defer unsubscribe()

	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("X-Accel-Buffering", "no")
	if err := writeSSEEvent(w, flusher, "snapshot", newJobSnapshotResponse(snapshot)); err != nil {
		return
	}
	if terminalJobStatus(snapshot.Status) && !snapshot.CleanupPending {
		return
	}
	heartbeat := time.NewTicker(sseHeartbeatInterval)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			if _, err := fmt.Fprint(w, ": keep-alive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case event, open := <-events:
			if !open {
				snapshot, err := s.jobs.Snapshot(r.PathValue("id"))
				if err == nil {
					_ = writeSSEEvent(w, flusher, "snapshot", newJobSnapshotResponse(snapshot))
				}
				return
			}
			if event.Type == job.EventState && terminalJobStatus(event.Status) {
				snapshot, err := s.jobs.Snapshot(event.JobID)
				if err != nil || writeSSEEvent(w, flusher, "snapshot", newJobSnapshotResponse(snapshot)) != nil {
					return
				}
				return
			}
			if err := writeSSEEvent(w, flusher, string(event.Type), newJobEventResponse(event)); err != nil {
				return
			}
		}
	}
}

func newJobSnapshotResponse(snapshot job.Snapshot) jobSnapshotResponse {
	logs := append([]job.LogEntry(nil), snapshot.Logs...)
	if logs == nil {
		logs = []job.LogEntry{}
	}
	outputs := append([]string(nil), snapshot.OutputFiles...)
	if outputs == nil {
		outputs = []string{}
	}
	warnings := make([]job.WarningCode, 0, len(snapshot.Warnings))
	for _, warning := range snapshot.Warnings {
		warnings = append(warnings, warning.Code)
	}
	var progress *int
	if snapshot.ProgressKnown {
		value := snapshot.Progress
		progress = &value
	}
	return jobSnapshotResponse{
		ID:             snapshot.ID,
		Preset:         snapshot.Preset,
		Filename:       snapshot.OriginalFilename,
		Status:         snapshot.Status,
		Phase:          snapshot.Phase,
		Progress:       progress,
		Logs:           logs,
		StderrTail:     snapshot.StderrTail,
		Outputs:        outputs,
		RecoveryPath:   snapshot.RecoveryPath,
		ErrorCode:      snapshot.ErrorCode,
		Warnings:       warnings,
		QueuedAt:       snapshot.QueuedAt,
		StartedAt:      optionalTime(snapshot.StartedAt),
		FinishedAt:     optionalTime(snapshot.FinishedAt),
		CleanupPending: snapshot.CleanupPending,
	}
}

func newJobEventResponse(event job.Event) jobEventResponse {
	response := jobEventResponse{
		Type:      event.Type,
		JobID:     event.JobID,
		Status:    event.Status,
		Phase:     event.Phase,
		Log:       event.Log,
		ErrorCode: event.ErrorCode,
	}
	if event.Type == job.EventProgress {
		value := event.Progress
		response.Progress = &value
	}
	return response
}

func optionalTime(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	copy := value
	return &copy
}

func terminalJobStatus(status job.Status) bool {
	switch status {
	case job.StatusDone, job.StatusFailed, job.StatusCancelled:
		return true
	default:
		return false
	}
}

func writeSSEEvent(w http.ResponseWriter, flusher http.Flusher, name string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode SSE %s event: %w", name, err)
	}
	if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, data); err != nil {
		return fmt.Errorf("write SSE %s event: %w", name, err)
	}
	flusher.Flush()
	return nil
}

func (s *Server) handleJobCancel(w http.ResponseWriter, r *http.Request) {
	if s.jobs == nil {
		writeJobAPIError(w, http.StatusServiceUnavailable, "manager_unavailable")
		return
	}
	if err := s.jobs.Cancel(r.PathValue("id")); err != nil {
		writeJobManagerError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleJobOutputs(w http.ResponseWriter, r *http.Request) {
	if s.jobs == nil {
		writeJobAPIError(w, http.StatusServiceUnavailable, "manager_unavailable")
		return
	}
	snapshot, err := s.jobs.Snapshot(r.PathValue("id"))
	if err != nil {
		writeJobManagerError(w, err)
		return
	}
	outputs := append([]string(nil), snapshot.OutputFiles...)
	if outputs == nil {
		outputs = []string{}
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(jobOutputsResponse{
		ID:           snapshot.ID,
		Status:       snapshot.Status,
		Outputs:      outputs,
		RecoveryPath: snapshot.RecoveryPath,
		ErrorCode:    snapshot.ErrorCode,
	}); err != nil {
		s.logger.Error("encoding job outputs response", "error", err)
	}
}

func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	if s.jobs == nil {
		writeJobAPIError(w, http.StatusServiceUnavailable, "manager_unavailable")
		return
	}
	snapshot, err := s.jobs.Snapshot(r.PathValue("id"))
	if err != nil {
		writeJobManagerError(w, err)
		return
	}
	requested := r.PathValue("file")
	file, info, err := openRecordedOutput(snapshot, requested)
	if err != nil {
		writeJobAPIError(w, http.StatusNotFound, "output_not_found")
		return
	}
	defer file.Close()

	if contentType := outputContentType(filepath.Ext(requested)); contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	disposition := mime.FormatMediaType("attachment", map[string]string{"filename": requested})
	w.Header().Set("Content-Disposition", disposition)
	http.ServeContent(w, r, requested, info.ModTime(), file)
}

func openRecordedOutput(snapshot job.Snapshot, requested string) (*os.File, fs.FileInfo, error) {
	if snapshot.Status != job.StatusDone || snapshot.OutputDir == "" ||
		requested == "" || filepath.Base(requested) != requested ||
		strings.ContainsAny(requested, `/\\`) || !slices.Contains(snapshot.OutputFiles, requested) {
		return nil, nil, fs.ErrNotExist
	}
	root := filepath.Clean(snapshot.OutputDir)
	if !filepath.IsAbs(root) {
		return nil, nil, fs.ErrNotExist
	}
	rootInfo, err := os.Lstat(root)
	if err != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return nil, nil, fs.ErrNotExist
	}
	candidate := filepath.Clean(filepath.Join(root, requested))
	relative, err := filepath.Rel(root, candidate)
	if err != nil || relative != requested || relative == "." || relative == ".." ||
		filepath.IsAbs(relative) || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return nil, nil, fs.ErrNotExist
	}
	pathInfo, err := os.Lstat(candidate)
	if err != nil || !pathInfo.Mode().IsRegular() || pathInfo.Mode()&os.ModeSymlink != 0 {
		return nil, nil, fs.ErrNotExist
	}
	file, err := os.Open(candidate)
	if err != nil {
		return nil, nil, fs.ErrNotExist
	}
	openedInfo, err := file.Stat()
	if err != nil || !openedInfo.Mode().IsRegular() || !os.SameFile(pathInfo, openedInfo) {
		_ = file.Close()
		return nil, nil, fs.ErrNotExist
	}
	return file, openedInfo, nil
}

func outputContentType(extension string) string {
	switch extension {
	case ".txt":
		return "text/plain; charset=utf-8"
	case ".srt":
		return "application/x-subrip; charset=utf-8"
	case ".vtt":
		return "text/vtt; charset=utf-8"
	default:
		return ""
	}
}

func writeJobManagerError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, job.ErrNotFound):
		writeJobAPIError(w, http.StatusNotFound, "job_not_found")
	case errors.Is(err, job.ErrNotCancelable):
		writeJobAPIError(w, http.StatusConflict, "job_not_cancelable")
	case errors.Is(err, job.ErrSubscriberLimit):
		writeJobAPIError(w, http.StatusTooManyRequests, "subscriber_limit")
	case errors.Is(err, job.ErrManagerClosed):
		writeJobAPIError(w, http.StatusServiceUnavailable, "manager_unavailable")
	default:
		writeJobAPIError(w, http.StatusInternalServerError, "internal_error")
	}
}

func writeJobAPIError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(jobAPIErrorResponse{ErrorCode: code})
}
