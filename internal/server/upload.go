package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"unicode"
	"unicode/utf8"

	"github.com/steepkit/whisper-cpp-gui/internal/job"
	"github.com/steepkit/whisper-cpp-gui/internal/model"
)

const (
	defaultUploadBodyLimit = int64(8 << 30)
	maxPresetBytes         = 256
	maxOptionsBytes        = 1024
	maxFilenameBytes       = 255
	uploadPartialName      = "upload.partial"
	uploadInputName        = "input"
)

var errInvalidUpload = errors.New("invalid upload")

var allowedUploadOutputs = map[string]struct{}{
	"txt": {},
	"srt": {},
	"vtt": {},
}

type uploadOptions struct {
	VAD     bool
	Outputs []string
}

type uploadResponse struct {
	ID     string     `json:"id"`
	Status job.Status `json:"status"`
}

type uploadErrorResponse struct {
	ErrorCode string `json:"error_code"`
}

func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	if !s.beginUpload() {
		writeUploadError(w, http.StatusServiceUnavailable, "server_closing")
		return
	}
	defer s.endUpload()
	if s.jobs == nil {
		writeUploadError(w, http.StatusInternalServerError, "internal_error")
		return
	}
	if r.ContentLength > s.uploadBodyLimit {
		writeUploadError(w, http.StatusRequestEntityTooLarge, "payload_too_large")
		return
	}
	select {
	case s.uploadPermit <- struct{}{}:
		defer func() { <-s.uploadPermit }()
	default:
		writeUploadError(w, http.StatusTooManyRequests, "upload_busy")
		return
	}

	// This wraps the entire body before MultipartReader parses boundaries or
	// headers, so the limit applies regardless of multipart field order.
	r.Body = http.MaxBytesReader(w, r.Body, s.uploadBodyLimit)
	reader, err := r.MultipartReader()
	if err != nil {
		writeUploadError(w, http.StatusBadRequest, "invalid_upload")
		return
	}

	var (
		presetID        string
		originalName    string
		workDir         string
		inputPath       string
		options         uploadOptions
		seenPreset      bool
		seenOptions     bool
		seenFile        bool
		submissionReady bool
	)
	defer func() {
		if workDir != "" && !submissionReady {
			_ = os.RemoveAll(workDir)
		}
	}()

	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			var maxBytes *http.MaxBytesError
			if errors.As(err, &maxBytes) {
				writeUploadError(w, http.StatusRequestEntityTooLarge, "payload_too_large")
			} else {
				writeUploadError(w, http.StatusBadRequest, "invalid_upload")
			}
			return
		}

		switch part.FormName() {
		case "preset":
			if seenPreset || part.FileName() != "" {
				_ = part.Close()
				writeUploadError(w, http.StatusBadRequest, "invalid_upload")
				return
			}
			presetID, err = readPresetPart(part)
			seenPreset = true
		case "file":
			if seenFile || part.FileName() == "" {
				_ = part.Close()
				writeUploadError(w, http.StatusBadRequest, "invalid_upload")
				return
			}
			originalName, err = sanitizeUploadFilename(part.FileName())
			if err == nil {
				workDir, inputPath, err = s.saveUploadFile(part)
			}
			if closeErr := part.Close(); err == nil && closeErr != nil {
				err = closeErr
			}
			seenFile = true
		case "options":
			if seenOptions || part.FileName() != "" {
				_ = part.Close()
				writeUploadError(w, http.StatusBadRequest, "invalid_upload")
				return
			}
			options, err = readOptionsPart(part)
			seenOptions = true
		default:
			_ = part.Close()
			writeUploadError(w, http.StatusBadRequest, "invalid_upload")
			return
		}
		if err != nil {
			writeUploadFailure(w, err)
			return
		}
	}

	if !seenPreset || !seenFile {
		writeUploadError(w, http.StatusBadRequest, "invalid_upload")
		return
	}
	preset, ok := s.presets.Lookup(presetID)
	if !ok {
		writeUploadError(w, http.StatusBadRequest, "invalid_upload")
		return
	}
	useVAD := preset.VAD
	outputFormats := preset.Outputs
	if seenOptions {
		useVAD = options.VAD
		outputFormats = append([]string(nil), options.Outputs...)
	}
	var leases []ModelLease
	defer func() { s.closeModelLeases(leases) }()
	modelPath, lease, err := s.resolveRequiredModel(preset.Model)
	if errors.Is(err, model.ErrModelUnavailable) {
		writeUploadError(w, http.StatusConflict, "model_missing")
		return
	}
	if err != nil {
		writeUploadError(w, http.StatusInternalServerError, "internal_error")
		return
	}
	if lease != nil {
		leases = append(leases, lease)
	}
	request := job.RunRequest{
		ID:               filepath.Base(workDir),
		WorkDir:          workDir,
		InputPath:        inputPath,
		OriginalFilename: originalName,
		Preset:           preset.ID,
		ModelPath:        modelPath,
		UseVAD:           useVAD,
		OutputFormats:    outputFormats,
	}
	if useVAD {
		request.VADModelPath, lease, err = s.resolveOptionalVADModel(s.vadLogicalName)
		if err != nil {
			writeUploadError(w, http.StatusInternalServerError, "internal_error")
			return
		}
		if lease != nil {
			leases = append(leases, lease)
		}
	}
	snapshot, err := s.jobs.Submit(request)
	if err != nil {
		if errors.Is(err, job.ErrQueueFull) {
			writeUploadError(w, http.StatusTooManyRequests, "queue_full")
			return
		}
		writeUploadError(w, http.StatusInternalServerError, "internal_error")
		return
	}
	s.retainModelLeases(snapshot.ID, leases)
	leases = nil
	submissionReady = true
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	if err := json.NewEncoder(w).Encode(uploadResponse{ID: snapshot.ID, Status: snapshot.Status}); err != nil {
		s.logger.Error("encoding upload response", "error", err)
	}
}

func (s *Server) resolveRequiredModel(name string) (string, ModelLease, error) {
	if s.acquireModel == nil {
		path, err := s.modelPathResolver(name)
		return path, nil, err
	}
	lease, err := s.acquireModel(name)
	if err != nil {
		return "", nil, err
	}
	if lease == nil {
		return "", nil, fmt.Errorf("acquire model %q returned an empty lease", name)
	}
	modelPath := lease.Path()
	if !filepath.IsAbs(modelPath) {
		return "", nil, errors.Join(
			fmt.Errorf("acquire model %q returned a non-absolute path", name),
			lease.Close(),
		)
	}
	return modelPath, lease, nil
}

func (s *Server) resolveOptionalVADModel(name string) (string, ModelLease, error) {
	path, lease, err := s.resolveRequiredModel(name)
	if !errors.Is(err, model.ErrModelUnavailable) {
		return path, lease, err
	}
	// A CLI without VAD support must still be allowed to run without the VAD
	// model. The pipeline performs feature detection before it touches this path.
	path, err = s.modelPathResolver(name)
	return path, nil, err
}

// saveUploadFile streams the sole file part into a private work directory.
func (s *Server) saveUploadFile(part io.Reader) (string, string, error) {
	id, err := s.idGenerator()
	if err != nil {
		return "", "", fmt.Errorf("generate upload ID: %w", err)
	}
	workDir, err := job.CreateWorkDir(s.tempRoot, id)
	if err != nil {
		return "", "", err
	}
	partialPath := filepath.Join(workDir, uploadPartialName)
	inputPath := filepath.Join(workDir, uploadInputName)
	file, err := s.uploadFileWriter(partialPath)
	if err != nil {
		_ = os.RemoveAll(workDir)
		return "", "", err
	}
	_, copyErr := io.Copy(file, part)
	closeErr := file.Close()
	if copyErr != nil {
		_ = os.RemoveAll(workDir)
		return "", "", copyErr
	}
	if closeErr != nil {
		_ = os.RemoveAll(workDir)
		return "", "", closeErr
	}
	if err := os.Rename(partialPath, inputPath); err != nil {
		_ = os.RemoveAll(workDir)
		return "", "", err
	}
	return workDir, inputPath, nil
}

func openUploadFile(path string) (io.WriteCloser, error) {
	return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
}

func readPresetPart(part io.ReadCloser) (string, error) {
	data, err := io.ReadAll(io.LimitReader(part, maxPresetBytes+1))
	closeErr := part.Close()
	if err != nil {
		return "", err
	}
	if closeErr != nil {
		return "", closeErr
	}
	if len(data) == 0 || len(data) > maxPresetBytes {
		return "", errInvalidUpload
	}
	return string(data), nil
}

func readOptionsPart(part io.ReadCloser) (uploadOptions, error) {
	data, err := io.ReadAll(io.LimitReader(part, maxOptionsBytes+1))
	closeErr := part.Close()
	if err != nil {
		return uploadOptions{}, err
	}
	if closeErr != nil {
		return uploadOptions{}, closeErr
	}
	if len(data) == 0 || len(data) > maxOptionsBytes {
		return uploadOptions{}, errInvalidUpload
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return uploadOptions{}, errInvalidUpload
	}
	var (
		result     uploadOptions
		seenFields = make(map[string]struct{}, 2)
	)
	for decoder.More() {
		token, err := decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok {
			return uploadOptions{}, errInvalidUpload
		}
		if _, duplicate := seenFields[key]; duplicate {
			return uploadOptions{}, errInvalidUpload
		}
		seenFields[key] = struct{}{}
		switch key {
		case "vad":
			var raw json.RawMessage
			if err := decoder.Decode(&raw); err != nil {
				return uploadOptions{}, errInvalidUpload
			}
			switch string(bytes.TrimSpace(raw)) {
			case "true":
				result.VAD = true
			case "false":
				result.VAD = false
			default:
				return uploadOptions{}, errInvalidUpload
			}
		case "outputs":
			if err := decoder.Decode(&result.Outputs); err != nil || result.Outputs == nil {
				return uploadOptions{}, errInvalidUpload
			}
		default:
			return uploadOptions{}, errInvalidUpload
		}
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') {
		return uploadOptions{}, errInvalidUpload
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return uploadOptions{}, errInvalidUpload
	}
	if _, ok := seenFields["vad"]; !ok {
		return uploadOptions{}, errInvalidUpload
	}
	if _, ok := seenFields["outputs"]; !ok || len(result.Outputs) == 0 || len(result.Outputs) > len(allowedUploadOutputs) {
		return uploadOptions{}, errInvalidUpload
	}
	seen := make(map[string]struct{}, len(result.Outputs))
	for _, output := range result.Outputs {
		if _, ok := allowedUploadOutputs[output]; !ok {
			return uploadOptions{}, errInvalidUpload
		}
		if _, duplicate := seen[output]; duplicate {
			return uploadOptions{}, errInvalidUpload
		}
		seen[output] = struct{}{}
	}
	result.Outputs = append([]string(nil), result.Outputs...)
	return result, nil
}

func sanitizeUploadFilename(filename string) (string, error) {
	filename = strings.ReplaceAll(filename, "\\", "/")
	filename = path.Base(filename)
	if filename == "" || filename == "." || filename == ".." ||
		!utf8.ValidString(filename) || len(filename) > maxFilenameBytes {
		return "", errInvalidUpload
	}
	for _, value := range filename {
		if unicode.IsControl(value) {
			return "", errInvalidUpload
		}
	}
	return filename, nil
}

func writeUploadFailure(w http.ResponseWriter, err error) {
	var maxBytes *http.MaxBytesError
	if errors.As(err, &maxBytes) {
		writeUploadError(w, http.StatusRequestEntityTooLarge, "payload_too_large")
		return
	}
	if isStorageFull(err) {
		writeUploadError(w, http.StatusInsufficientStorage, "insufficient_storage")
		return
	}
	if errors.Is(err, errInvalidUpload) || errors.Is(err, io.ErrUnexpectedEOF) {
		writeUploadError(w, http.StatusBadRequest, "invalid_upload")
		return
	}
	writeUploadError(w, http.StatusInternalServerError, "internal_error")
}

func isStorageFull(err error) bool {
	return errors.Is(err, syscall.ENOSPC) || errors.Is(err, syscall.EDQUOT)
}

func writeUploadError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(uploadErrorResponse{ErrorCode: code})
}
