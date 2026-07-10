package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/steepkit/whisper-cpp-gui/internal/model"
)

// ModelService is the HTTP-free model manager contract consumed by Server.
type ModelService interface {
	Snapshots() ([]model.Snapshot, error)
	Snapshot(name string) (model.Snapshot, error)
	Download(name string) (model.Snapshot, error)
	Subscribe(name string) (model.Snapshot, <-chan model.Event, func(), error)
	Delete(name string) error
	Close()
}

type modelsResponse struct {
	Models []modelSnapshotResponse `json:"models"`
}

type modelDescriptorResponse struct {
	Name     string `json:"name"`
	Filename string `json:"filename"`
	Size     int64  `json:"size"`
	SHA256   string `json:"sha256"`
}

type modelSnapshotResponse struct {
	Model           modelDescriptorResponse `json:"model"`
	State           model.State             `json:"state"`
	BytesDownloaded int64                   `json:"bytes_downloaded"`
	ErrorCode       model.ErrorCode         `json:"error_code,omitempty"`
}

type modelEventResponse struct {
	Type            model.EventType `json:"type"`
	Name            string          `json:"name"`
	State           model.State     `json:"state,omitempty"`
	BytesDownloaded int64           `json:"bytes_downloaded,omitempty"`
	ErrorCode       model.ErrorCode `json:"error_code,omitempty"`
}

type modelAPIErrorResponse struct {
	ErrorCode string `json:"error_code"`
}

func (s *Server) handleModels(w http.ResponseWriter, _ *http.Request) {
	if s.models == nil {
		writeModelAPIError(w, http.StatusServiceUnavailable, "manager_unavailable")
		return
	}
	snapshots, err := s.models.Snapshots()
	if err != nil {
		writeModelManagerError(w, err)
		return
	}
	models := make([]modelSnapshotResponse, 0, len(snapshots))
	for _, snapshot := range snapshots {
		models = append(models, newModelSnapshotResponse(snapshot))
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(modelsResponse{Models: models}); err != nil {
		s.logger.Error("encoding model list response", "error", err)
	}
}

func (s *Server) handleModelDownload(w http.ResponseWriter, r *http.Request) {
	if s.models == nil {
		writeModelAPIError(w, http.StatusServiceUnavailable, "manager_unavailable")
		return
	}
	snapshot, err := s.models.Download(r.PathValue("name"))
	if err != nil && !errors.Is(err, model.ErrAlreadyDownloaded) {
		writeModelManagerError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	if err := json.NewEncoder(w).Encode(newModelSnapshotResponse(snapshot)); err != nil {
		s.logger.Error("encoding model download response", "error", err)
	}
}

func (s *Server) handleModelEvents(w http.ResponseWriter, r *http.Request) {
	if s.models == nil {
		writeModelAPIError(w, http.StatusServiceUnavailable, "manager_unavailable")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeModelAPIError(w, http.StatusInternalServerError, "streaming_unsupported")
		return
	}
	snapshot, events, unsubscribe, err := s.models.Subscribe(r.PathValue("name"))
	if err != nil {
		writeModelManagerError(w, err)
		return
	}
	defer unsubscribe()
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("X-Accel-Buffering", "no")
	if err := writeSSEEvent(w, flusher, "snapshot", newModelSnapshotResponse(snapshot)); err != nil {
		return
	}
	if snapshot.State != model.StateDownloading {
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
				snapshot, err := s.models.Snapshot(r.PathValue("name"))
				if err == nil {
					_ = writeSSEEvent(w, flusher, "snapshot", newModelSnapshotResponse(snapshot))
				}
				return
			}
			if event.Type == model.EventState && event.State != model.StateDownloading {
				snapshot, err := s.models.Snapshot(event.Name)
				if err == nil {
					_ = writeSSEEvent(w, flusher, "snapshot", newModelSnapshotResponse(snapshot))
				}
				return
			}
			if err := writeSSEEvent(w, flusher, string(event.Type), newModelEventResponse(event)); err != nil {
				return
			}
		}
	}
}

func (s *Server) handleModelDelete(w http.ResponseWriter, r *http.Request) {
	if s.models == nil {
		writeModelAPIError(w, http.StatusServiceUnavailable, "manager_unavailable")
		return
	}
	if err := s.models.Delete(r.PathValue("name")); err != nil {
		writeModelManagerError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func newModelSnapshotResponse(snapshot model.Snapshot) modelSnapshotResponse {
	return modelSnapshotResponse{
		Model: modelDescriptorResponse{
			Name:     snapshot.Model.Name,
			Filename: snapshot.Model.Filename,
			Size:     snapshot.Model.Size,
			SHA256:   snapshot.Model.SHA256,
		},
		State:           snapshot.State,
		BytesDownloaded: snapshot.BytesDownloaded,
		ErrorCode:       snapshot.ErrorCode,
	}
}

func newModelEventResponse(event model.Event) modelEventResponse {
	return modelEventResponse{
		Type:            event.Type,
		Name:            event.Name,
		State:           event.State,
		BytesDownloaded: event.BytesDownloaded,
		ErrorCode:       event.ErrorCode,
	}
}

func writeModelManagerError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, model.ErrInvalidModelName), errors.Is(err, model.ErrUnknownModel):
		writeModelAPIError(w, http.StatusNotFound, "model_not_found")
	case errors.Is(err, model.ErrDownloadInProgress), errors.Is(err, model.ErrModelBusy):
		writeModelAPIError(w, http.StatusConflict, "download_in_progress")
	case errors.Is(err, model.ErrModelInUse):
		writeModelAPIError(w, http.StatusConflict, "model_in_use")
	case errors.Is(err, model.ErrSubscriberLimit):
		writeModelAPIError(w, http.StatusTooManyRequests, "subscriber_limit")
	case errors.Is(err, model.ErrManagerClosed):
		writeModelAPIError(w, http.StatusServiceUnavailable, "manager_unavailable")
	default:
		writeModelAPIError(w, http.StatusInternalServerError, "internal_error")
	}
}

func writeModelAPIError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(modelAPIErrorResponse{ErrorCode: code})
}
