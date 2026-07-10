package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/steepkit/whisper-cpp-gui/internal/job"
)

type uploadRunnerFunc func(context.Context, job.RunRequest, job.Observer) (job.RunResult, error)

func (f uploadRunnerFunc) Run(ctx context.Context, request job.RunRequest, observer job.Observer) (job.RunResult, error) {
	return f(ctx, request, observer)
}

type uploadHarness struct {
	server   *Server
	manager  *job.Manager
	root     string
	captured <-chan job.RunRequest
}

func newUploadHarness(t *testing.T, id string, queueCapacity int, writer func(string) (io.WriteCloser, error)) uploadHarness {
	t.Helper()
	root := filepath.Join(t.TempDir(), "jobs")
	captured := make(chan job.RunRequest, 1)
	manager, err := job.NewManager(uploadRunnerFunc(func(ctx context.Context, request job.RunRequest, _ job.Observer) (job.RunResult, error) {
		captured <- request
		<-ctx.Done()
		return job.RunResult{}, ctx.Err()
	}), job.Config{TempRoot: root, QueueCapacity: queueCapacity})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	s, err := New(Config{
		Port:     8123,
		Token:    testToken,
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Jobs:     manager,
		TempRoot: root,
		ModelPathResolver: func(name string) (string, error) {
			return "/models/" + name + ".bin", nil
		},
		IDGenerator:      func() (string, error) { return id, nil },
		UploadFileWriter: writer,
	})
	if err != nil {
		manager.Close()
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return uploadHarness{server: s, manager: manager, root: root, captured: captured}
}

type multipartPart struct {
	name     string
	filename string
	value    []byte
}

func makeMultipart(t *testing.T, parts ...multipartPart) ([]byte, string) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for _, part := range parts {
		var (
			field io.Writer
			err   error
		)
		if part.filename == "" {
			field, err = writer.CreateFormField(part.name)
		} else {
			field, err = writer.CreateFormFile(part.name, part.filename)
		}
		if err != nil {
			t.Fatalf("create multipart part: %v", err)
		}
		if _, err := field.Write(part.value); err != nil {
			t.Fatalf("write multipart part: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart: %v", err)
	}
	return body.Bytes(), writer.FormDataContentType()
}

func uploadRequest(s *Server, body io.Reader, contentType string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/jobs", body)
	req.Host = "127.0.0.1:8123"
	req.Header.Set("X-Auth-Token", testToken)
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

func rootEntries(t *testing.T, root string) []os.DirEntry {
	t.Helper()
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatalf("ReadDir(%s): %v", root, err)
	}
	return entries
}

func TestUploadRejectedBeforeBodyRead(t *testing.T) {
	tests := []struct {
		name   string
		host   string
		origin string
		token  string
		want   int
	}{
		{name: "missing token", host: "127.0.0.1:8123", want: http.StatusUnauthorized},
		{name: "bad host", host: "evil.example", token: testToken, want: http.StatusForbidden},
		{name: "bad origin", host: "127.0.0.1:8123", origin: "http://evil.example", token: testToken, want: http.StatusForbidden},
	}
	s := newTestServer(t, 8123)
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body := &readTracker{Reader: strings.NewReader("unread")}
			req := httptest.NewRequest(http.MethodPost, "/api/jobs", body)
			req.Host = test.host
			req.Header.Set("Content-Type", "multipart/form-data; boundary=unused")
			if test.origin != "" {
				req.Header.Set("Origin", test.origin)
			}
			if test.token != "" {
				req.Header.Set("X-Auth-Token", test.token)
			}
			rec := httptest.NewRecorder()
			s.Handler().ServeHTTP(rec, req)
			if rec.Code != test.want {
				t.Fatalf("status = %d, want %d", rec.Code, test.want)
			}
			if body.reads != 0 {
				t.Fatalf("rejected request read %d bytes", body.reads)
			}
		})
	}
}

func TestUploadAcceptsBothFieldOrdersAndSubmitsFixedPaths(t *testing.T) {
	for _, fileFirst := range []bool{false, true} {
		t.Run(fmt.Sprintf("file first %t", fileFirst), func(t *testing.T) {
			h := newUploadHarness(t, "job-upload", 1, nil)
			preset := multipartPart{name: "preset", value: []byte("ja_fast")}
			file := multipartPart{name: "file", filename: "lecture.mp3", value: []byte("audio")}
			parts := []multipartPart{preset, file}
			if fileFirst {
				parts = []multipartPart{file, preset}
			}
			body, contentType := makeMultipart(t, parts...)
			rec := uploadRequest(h.server, bytes.NewReader(body), contentType)
			if rec.Code != http.StatusAccepted {
				t.Fatalf("status = %d, want 202: %s", rec.Code, rec.Body.String())
			}
			var response uploadResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if response.ID != "job-upload" || response.Status != job.StatusQueued {
				t.Fatalf("response = %#v", response)
			}
			request := <-h.captured
			wantDir := filepath.Join(h.root, "job-upload")
			if request.WorkDir != wantDir || request.InputPath != filepath.Join(wantDir, uploadInputName) {
				t.Fatalf("request paths = %q, %q", request.WorkDir, request.InputPath)
			}
			if request.Preset != "ja_fast" || request.OriginalFilename != "lecture.mp3" || request.ModelPath != "/models/large-v3-turbo-q5_0.bin" || request.VADModelPath != "/models/silero-vad.bin" || !request.UseVAD {
				t.Fatalf("request = %#v", request)
			}
			if strings.Join(request.OutputFormats, ",") != "txt,srt,vtt" {
				t.Fatalf("default output formats = %v", request.OutputFormats)
			}
			if got, err := os.ReadFile(request.InputPath); err != nil || string(got) != "audio" {
				t.Fatalf("saved input = %q, %v", got, err)
			}
			if _, err := os.Stat(filepath.Join(wantDir, uploadPartialName)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("partial remains: %v", err)
			}
		})
	}
}

func TestUploadOptionsOverridePresetDefaults(t *testing.T) {
	h := newUploadHarness(t, "job-options", 1, nil)
	body, contentType := makeMultipart(t,
		multipartPart{name: "options", value: []byte(`{"vad":false,"outputs":["txt","vtt"]}`)},
		multipartPart{name: "file", filename: "lecture.wav", value: []byte("audio")},
		multipartPart{name: "preset", value: []byte("ja_fast")},
	)
	rec := uploadRequest(h.server, bytes.NewReader(body), contentType)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202: %s", rec.Code, rec.Body.String())
	}
	request := <-h.captured
	if request.UseVAD || request.VADModelPath != "" {
		t.Fatalf("VAD override was not applied: %#v", request)
	}
	if strings.Join(request.OutputFormats, ",") != "txt,vtt" {
		t.Fatalf("output override = %v, want txt,vtt", request.OutputFormats)
	}
}

func TestUploadValidationAndFilenameSanitization(t *testing.T) {
	tests := []struct {
		name  string
		parts []multipartPart
	}{
		{name: "missing file", parts: []multipartPart{{name: "preset", value: []byte("ja_fast")}}},
		{name: "missing preset", parts: []multipartPart{{name: "file", filename: "a.mp3", value: []byte("x")}}},
		{name: "duplicate preset", parts: []multipartPart{{name: "preset", value: []byte("ja_fast")}, {name: "preset", value: []byte("ja_fast")}, {name: "file", filename: "a.mp3", value: []byte("x")}}},
		{name: "duplicate file", parts: []multipartPart{{name: "preset", value: []byte("ja_fast")}, {name: "file", filename: "a.mp3", value: []byte("x")}, {name: "file", filename: "b.mp3", value: []byte("x")}}},
		{name: "duplicate options", parts: []multipartPart{{name: "preset", value: []byte("ja_fast")}, {name: "options", value: []byte(`{"vad":true,"outputs":["txt"]}`)}, {name: "options", value: []byte(`{"vad":false,"outputs":["srt"]}`)}, {name: "file", filename: "a.mp3", value: []byte("x")}}},
		{name: "options as file", parts: []multipartPart{{name: "preset", value: []byte("ja_fast")}, {name: "options", filename: "options.json", value: []byte(`{"vad":true,"outputs":["txt"]}`)}, {name: "file", filename: "a.mp3", value: []byte("x")}}},
		{name: "malformed options", parts: []multipartPart{{name: "preset", value: []byte("ja_fast")}, {name: "options", value: []byte(`{"vad":`)}, {name: "file", filename: "a.mp3", value: []byte("x")}}},
		{name: "options trailing value", parts: []multipartPart{{name: "preset", value: []byte("ja_fast")}, {name: "options", value: []byte(`{"vad":true,"outputs":["txt"]} {}`)}, {name: "file", filename: "a.mp3", value: []byte("x")}}},
		{name: "options unknown field", parts: []multipartPart{{name: "preset", value: []byte("ja_fast")}, {name: "options", value: []byte(`{"vad":true,"outputs":["txt"],"model":"other"}`)}, {name: "file", filename: "a.mp3", value: []byte("x")}}},
		{name: "options duplicate vad key", parts: []multipartPart{{name: "preset", value: []byte("ja_fast")}, {name: "options", value: []byte(`{"vad":true,"vad":false,"outputs":["txt"]}`)}, {name: "file", filename: "a.mp3", value: []byte("x")}}},
		{name: "options duplicate outputs key", parts: []multipartPart{{name: "preset", value: []byte("ja_fast")}, {name: "options", value: []byte(`{"vad":true,"outputs":["txt"],"outputs":["srt"]}`)}, {name: "file", filename: "a.mp3", value: []byte("x")}}},
		{name: "options null vad", parts: []multipartPart{{name: "preset", value: []byte("ja_fast")}, {name: "options", value: []byte(`{"vad":null,"outputs":["txt"]}`)}, {name: "file", filename: "a.mp3", value: []byte("x")}}},
		{name: "options wrong output type", parts: []multipartPart{{name: "preset", value: []byte("ja_fast")}, {name: "options", value: []byte(`{"vad":true,"outputs":"txt"}`)}, {name: "file", filename: "a.mp3", value: []byte("x")}}},
		{name: "options missing vad", parts: []multipartPart{{name: "preset", value: []byte("ja_fast")}, {name: "options", value: []byte(`{"outputs":["txt"]}`)}, {name: "file", filename: "a.mp3", value: []byte("x")}}},
		{name: "options missing outputs", parts: []multipartPart{{name: "preset", value: []byte("ja_fast")}, {name: "options", value: []byte(`{"vad":true}`)}, {name: "file", filename: "a.mp3", value: []byte("x")}}},
		{name: "options empty outputs", parts: []multipartPart{{name: "preset", value: []byte("ja_fast")}, {name: "options", value: []byte(`{"vad":true,"outputs":[]}`)}, {name: "file", filename: "a.mp3", value: []byte("x")}}},
		{name: "options duplicate output", parts: []multipartPart{{name: "preset", value: []byte("ja_fast")}, {name: "options", value: []byte(`{"vad":true,"outputs":["txt","txt"]}`)}, {name: "file", filename: "a.mp3", value: []byte("x")}}},
		{name: "options unsupported output", parts: []multipartPart{{name: "preset", value: []byte("ja_fast")}, {name: "options", value: []byte(`{"vad":true,"outputs":["json"]}`)}, {name: "file", filename: "a.mp3", value: []byte("x")}}},
		{name: "options too large", parts: []multipartPart{{name: "preset", value: []byte("ja_fast")}, {name: "options", value: bytes.Repeat([]byte(" "), maxOptionsBytes+1)}, {name: "file", filename: "a.mp3", value: []byte("x")}}},
		{name: "unknown field", parts: []multipartPart{{name: "preset", value: []byte("ja_fast")}, {name: "other", value: []byte("x")}, {name: "file", filename: "a.mp3", value: []byte("x")}}},
		{name: "invalid preset", parts: []multipartPart{{name: "file", filename: "a.mp3", value: []byte("x")}, {name: "preset", value: []byte("missing")}}},
		{name: "invalid filename", parts: []multipartPart{{name: "preset", value: []byte("ja_fast")}, {name: "file", filename: strings.Repeat("a", maxFilenameBytes+1), value: []byte("x")}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h := newUploadHarness(t, "job-invalid", 1, nil)
			body, contentType := makeMultipart(t, test.parts...)
			rec := uploadRequest(h.server, bytes.NewReader(body), contentType)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
			}
			if len(rootEntries(t, h.root)) != 0 {
				t.Fatalf("invalid upload left work directories")
			}
		})
	}

	h := newUploadHarness(t, "job-hostile", 1, nil)
	body, contentType := makeMultipart(t,
		multipartPart{name: "preset", value: []byte("ja_fast")},
		multipartPart{name: "file", filename: "../../lecture\\escape.mp3", value: []byte("x")},
	)
	rec := uploadRequest(h.server, bytes.NewReader(body), contentType)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("hostile filename status = %d: %s", rec.Code, rec.Body.String())
	}
	request := <-h.captured
	if request.OriginalFilename != "escape.mp3" || strings.ContainsAny(request.OriginalFilename, "/\\") {
		t.Fatalf("unsafe filename metadata = %q", request.OriginalFilename)
	}
}

func TestSanitizeUploadFilenameRejectsInvalidMetadata(t *testing.T) {
	for _, filename := range []string{
		"",
		".",
		"..",
		"line\nbreak.wav",
		"tab\tname.wav",
		"bad\x00name.wav",
		string([]byte{'b', 'a', 'd', 0xff}),
		strings.Repeat("a", maxFilenameBytes+1),
	} {
		if _, err := sanitizeUploadFilename(filename); !errors.Is(err, errInvalidUpload) {
			t.Errorf("sanitizeUploadFilename(%q) error = %v", filename, err)
		}
	}
	if got, err := sanitizeUploadFilename(`..\folder/lecture.wav`); err != nil || got != "lecture.wav" {
		t.Fatalf("safe basename = %q, %v", got, err)
	}
}

func TestUploadRejectsBodyLimitsAndTruncation(t *testing.T) {
	t.Run("streaming overflow", func(t *testing.T) {
		h := newUploadHarness(t, "job-overflow", 1, nil)
		h.server.uploadBodyLimit = 64
		body, contentType := makeMultipart(t,
			multipartPart{name: "preset", value: []byte("ja_fast")},
			multipartPart{name: "file", filename: "a.mp3", value: bytes.Repeat([]byte("x"), 256)},
		)
		rec := uploadRequest(h.server, readerOnly{Reader: bytes.NewReader(body)}, contentType)
		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("status = %d, want 413: %s", rec.Code, rec.Body.String())
		}
		if len(rootEntries(t, h.root)) != 0 {
			t.Fatal("overflow left work directories")
		}
	})

	t.Run("declared content length", func(t *testing.T) {
		h := newUploadHarness(t, "job-length", 1, nil)
		h.server.uploadBodyLimit = 64
		body, contentType := makeMultipart(t, multipartPart{name: "file", filename: "a.mp3", value: []byte("x")})
		req := httptest.NewRequest(http.MethodPost, "/api/jobs", bytes.NewReader(body))
		req.Host = "127.0.0.1:8123"
		req.Header.Set("X-Auth-Token", testToken)
		req.Header.Set("Content-Type", contentType)
		req.ContentLength = 65
		rec := httptest.NewRecorder()
		h.server.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("status = %d, want 413", rec.Code)
		}
		if len(rootEntries(t, h.root)) != 0 {
			t.Fatal("early Content-Length rejection created a work directory")
		}
	})

	t.Run("truncated multipart", func(t *testing.T) {
		h := newUploadHarness(t, "job-truncated", 1, nil)
		body := "--cut\r\nContent-Disposition: form-data; name=\"file\"; filename=\"a.mp3\"\r\n\r\npartial"
		rec := uploadRequest(h.server, strings.NewReader(body), "multipart/form-data; boundary=cut")
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
		}
		if len(rootEntries(t, h.root)) != 0 {
			t.Fatal("truncated upload left work directories")
		}
	})

	t.Run("malformed multipart header", func(t *testing.T) {
		h := newUploadHarness(t, "job-malformed", 1, nil)
		body := "--bad\r\nnot-a-header\r\n\r\nbody\r\n--bad--\r\n"
		rec := uploadRequest(h.server, strings.NewReader(body), "multipart/form-data; boundary=bad")
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
		}
		if len(rootEntries(t, h.root)) != 0 {
			t.Fatal("malformed upload left work directories")
		}
	})
}

func TestUploadQueueFullAndStorageCleanup(t *testing.T) {
	t.Run("queue full", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "jobs")
		started := make(chan string, 1)
		manager, err := job.NewManager(uploadRunnerFunc(func(ctx context.Context, request job.RunRequest, _ job.Observer) (job.RunResult, error) {
			started <- request.ID
			<-ctx.Done()
			return job.RunResult{}, ctx.Err()
		}), job.Config{TempRoot: root, QueueCapacity: 1})
		if err != nil {
			t.Fatal(err)
		}
		s, err := New(Config{Port: 8123, Token: testToken, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Jobs: manager, TempRoot: root, ModelPathResolver: func(name string) (string, error) { return name, nil }, IDGenerator: func() (string, error) { return "job-full", nil }})
		if err != nil {
			manager.Close()
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		if _, err := manager.Submit(job.RunRequest{ID: "job-running"}); err != nil {
			t.Fatalf("submit running: %v", err)
		}
		<-started
		if _, err := manager.Submit(job.RunRequest{ID: "job-queued"}); err != nil {
			t.Fatalf("submit queued: %v", err)
		}
		body, contentType := makeMultipart(t, multipartPart{name: "preset", value: []byte("ja_fast")}, multipartPart{name: "file", filename: "a.mp3", value: []byte("x")})
		rec := uploadRequest(s, bytes.NewReader(body), contentType)
		if rec.Code != http.StatusTooManyRequests {
			t.Fatalf("status = %d, want 429: %s", rec.Code, rec.Body.String())
		}
		if _, err := os.Stat(filepath.Join(root, "job-full")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("queue-full upload remains: %v", err)
		}
	})

	t.Run("injected ENOSPC", func(t *testing.T) {
		h := newUploadHarness(t, "job-nospace", 1, func(string) (io.WriteCloser, error) {
			return nil, syscall.ENOSPC
		})
		body, contentType := makeMultipart(t, multipartPart{name: "preset", value: []byte("ja_fast")}, multipartPart{name: "file", filename: "a.mp3", value: []byte("x")})
		rec := uploadRequest(h.server, bytes.NewReader(body), contentType)
		if rec.Code != http.StatusInsufficientStorage {
			t.Fatalf("status = %d, want 507: %s", rec.Code, rec.Body.String())
		}
		if len(rootEntries(t, h.root)) != 0 {
			t.Fatal("ENOSPC upload left work directories")
		}
	})
}

func TestConcurrentUploadIsRejectedBeforeBodyRead(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	h := newUploadHarness(t, "job-serial-upload", 1, func(path string) (io.WriteCloser, error) {
		file, err := openUploadFile(path)
		if err != nil {
			return nil, err
		}
		return &blockingUploadWriter{
			WriteCloser: file,
			beforeWrite: func() {
				once.Do(func() { close(entered) })
				<-release
			},
		}, nil
	})
	firstBody, firstContentType := makeMultipart(t,
		multipartPart{name: "preset", value: []byte("ja_fast")},
		multipartPart{name: "file", filename: "first.wav", value: []byte("audio")},
	)
	firstResult := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		firstResult <- uploadRequest(h.server, bytes.NewReader(firstBody), firstContentType)
	}()
	<-entered

	secondBody := &readTracker{Reader: strings.NewReader("unread")}
	second := uploadRequest(h.server, secondBody, "multipart/form-data; boundary=unused")
	if second.Code != http.StatusTooManyRequests {
		t.Fatalf("second upload status = %d, want 429: %s", second.Code, second.Body.String())
	}
	if secondBody.reads != 0 {
		t.Fatalf("busy upload read %d bytes", secondBody.reads)
	}
	var response uploadErrorResponse
	if err := json.Unmarshal(second.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.ErrorCode != "upload_busy" {
		t.Fatalf("busy upload error code = %q", response.ErrorCode)
	}

	close(release)
	if first := <-firstResult; first.Code != http.StatusAccepted {
		t.Fatalf("first upload status = %d: %s", first.Code, first.Body.String())
	}
}

func TestUploadStreamsHundredMegabytesWithoutLargeAllocation(t *testing.T) {
	h := newUploadHarness(t, "job-large", 1, nil)
	const size = int64(100 << 20)
	boundary := "streaming-boundary"
	header := "--" + boundary + "\r\nContent-Disposition: form-data; name=\"preset\"\r\n\r\nja_fast\r\n" +
		"--" + boundary + "\r\nContent-Disposition: form-data; name=\"file\"; filename=\"large.wav\"\r\nContent-Type: audio/wav\r\n\r\n"
	tail := "\r\n--" + boundary + "--\r\n"
	body := io.MultiReader(strings.NewReader(header), io.LimitReader(zeroReader{}, size), strings.NewReader(tail))

	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	rec := uploadRequest(h.server, readerOnly{Reader: body}, "multipart/form-data; boundary="+boundary)
	runtime.GC()
	runtime.ReadMemStats(&after)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202: %s", rec.Code, rec.Body.String())
	}
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 16<<20 {
		t.Fatalf("streaming %d bytes allocated %d bytes, want <= %d", size, allocated, 16<<20)
	}
	request := <-h.captured
	info, err := os.Stat(request.InputPath)
	if err != nil || info.Size() != size {
		t.Fatalf("saved large input = %v, %v", info, err)
	}
}

func TestUploadReadTimeoutReleasesPermitAndCleansWorkDir(t *testing.T) {
	entered := make(chan struct{})
	var once sync.Once
	h := newUploadHarness(t, "job-slow-upload", 1, func(path string) (io.WriteCloser, error) {
		file, err := openUploadFile(path)
		if err != nil {
			return nil, err
		}
		return &blockingUploadWriter{
			WriteCloser: file,
			beforeWrite: func() {
				once.Do(func() { close(entered) })
			},
		}, nil
	})
	const timeout = 500 * time.Millisecond
	h.server.uploadReadTimeout = timeout
	h.server.port = 0
	if _, err := h.server.Start(); err != nil {
		t.Fatal(err)
	}
	if got := h.server.httpServer.ReadTimeout; got != timeout {
		t.Fatalf("http ReadTimeout = %s, want %s", got, timeout)
	}

	conn, err := net.Dial("tcp", h.server.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	boundary := "slow-boundary"
	bodyPrefix := "--" + boundary + "\r\nContent-Disposition: form-data; name=\"preset\"\r\n\r\nja_fast\r\n" +
		"--" + boundary + "\r\nContent-Disposition: form-data; name=\"file\"; filename=\"slow.wav\"\r\n\r\npartial"
	request := fmt.Sprintf("POST /api/jobs HTTP/1.1\r\nHost: 127.0.0.1:%d\r\nX-Auth-Token: %s\r\nContent-Type: multipart/form-data; boundary=%s\r\nContent-Length: %d\r\n\r\n%s",
		h.server.Port(), testToken, boundary, len(bodyPrefix)+(1<<20), bodyPrefix)
	if _, err := io.WriteString(conn, request); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("slow upload handler did not start writing")
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		entries, readErr := os.ReadDir(h.root)
		if readErr == nil && len(entries) == 0 && len(h.server.uploadPermit) == 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("slow upload retained permit or work directory: permit=%d entries=%d", len(h.server.uploadPermit), len(rootEntries(t, h.root)))
}

func TestServerCloseClosesOwnedManagerOnce(t *testing.T) {
	root := filepath.Join(t.TempDir(), "jobs")
	manager, err := job.NewManager(uploadRunnerFunc(func(context.Context, job.RunRequest, job.Observer) (job.RunResult, error) {
		return job.RunResult{}, nil
	}), job.Config{TempRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(Config{Token: testToken, Jobs: manager, TempRoot: root})
	if err != nil {
		manager.Close()
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if _, err := manager.Submit(job.RunRequest{ID: "after-close"}); !errors.Is(err, job.ErrManagerClosed) {
		t.Fatalf("Submit after Server.Close = %v, want ErrManagerClosed", err)
	}
	if _, err := s.Start(); err == nil {
		t.Fatal("Start after Server.Close succeeded")
	}
}

func TestServerCloseAbortsActiveUploadAndWaitsForCleanup(t *testing.T) {
	entered := make(chan struct{})
	var once sync.Once
	h := newUploadHarness(t, "job-close-upload", 1, func(path string) (io.WriteCloser, error) {
		file, err := openUploadFile(path)
		if err != nil {
			return nil, err
		}
		return &blockingUploadWriter{
			WriteCloser: file,
			beforeWrite: func() {
				once.Do(func() { close(entered) })
			},
		}, nil
	})
	h.server.port = 0
	if _, err := h.server.Start(); err != nil {
		t.Fatal(err)
	}
	conn, err := net.Dial("tcp", h.server.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	boundary := "close-boundary"
	bodyPrefix := "--" + boundary + "\r\nContent-Disposition: form-data; name=\"preset\"\r\n\r\nja_fast\r\n" +
		"--" + boundary + "\r\nContent-Disposition: form-data; name=\"file\"; filename=\"active.wav\"\r\n\r\npartial"
	request := fmt.Sprintf("POST /api/jobs HTTP/1.1\r\nHost: 127.0.0.1:%d\r\nX-Auth-Token: %s\r\nContent-Type: multipart/form-data; boundary=%s\r\nContent-Length: %d\r\n\r\n%s",
		h.server.Port(), testToken, boundary, len(bodyPrefix)+(1<<20), bodyPrefix)
	if _, err := io.WriteString(conn, request); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("upload handler did not start writing")
	}

	closed := make(chan error, 1)
	go func() { closed <- h.server.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("Server.Close: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Server.Close did not abort the active upload")
	}
	if entries := rootEntries(t, h.root); len(entries) != 0 {
		t.Fatalf("active upload cleanup left %d work directories", len(entries))
	}
}

type readTracker struct {
	io.Reader
	reads int
}

func (r *readTracker) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.reads += n
	return n, err
}

type readerOnly struct{ io.Reader }

type blockingUploadWriter struct {
	io.WriteCloser
	beforeWrite func()
}

func (w *blockingUploadWriter) Write(value []byte) (int, error) {
	w.beforeWrite()
	return w.WriteCloser.Write(value)
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	for index := range p {
		p[index] = 0
	}
	return len(p), nil
}
