// Package server provides the HTTP layer: routing, token/Host/Origin
// validation middleware, SSE delivery, and static file serving.
//
// Layer rule: server may depend on job (and lower layers); job and exec
// must not import server or reference HTTP concepts.
package server

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/steepkit/whisper-cpp-gui/config"
	"github.com/steepkit/whisper-cpp-gui/internal/exec"
	"github.com/steepkit/whisper-cpp-gui/internal/job"
	"github.com/steepkit/whisper-cpp-gui/internal/model"
	appweb "github.com/steepkit/whisper-cpp-gui/web"
)

// Connection-level limits guard against local Slowloris / header-flood
// clients reaching the auth middleware. These are generous for a localhost
// GUI but finite.
const (
	readHeaderTimeout        = 10 * time.Second
	defaultUploadReadTimeout = 2 * time.Hour
	defaultMaxConnections    = 128
	idleTimeout              = 120 * time.Second
	maxHeaderBytes           = 1 << 20 // 1 MiB
)

// Config carries the wiring needed to construct a Server. main assembles
// it; server owns all HTTP behavior.
type Config struct {
	// Port to bind on 127.0.0.1. 0 picks a random free port.
	Port int
	// Token is the crypto/rand session token every /api/* request must
	// present. Use NewToken to generate it.
	Token string
	// Logger receives structured logs. Never log the token or full query
	// strings (SSE authenticates via query token).
	Logger *slog.Logger
	// Discover reports whisper-cli/ffmpeg discovery state for /api/config.
	// It is called per request so a binary installed while the app runs is
	// picked up without a restart. nil means "nothing found" (tests, early
	// wiring).
	Discover func() exec.Discovery
	// Jobs owns queued and running transcription work after a successful
	// upload submission. Server closes it when Server.Close is called.
	Jobs *job.Manager
	// Models owns fixed-manifest downloads and model state. Server closes it
	// after all jobs and model leases have ended. nil disables model APIs.
	Models ModelService
	// AcquireModel pins a validated model for queued/running job lifetime. When
	// Models is a *model.Manager, New supplies this adapter automatically.
	AcquireModel ModelAcquireFunc
	// Presets is the trusted, application-owned preset catalog. nil loads the
	// built-in catalog.
	Presets *config.Catalog
	// TempRoot is the application-owned root for upload work directories.
	// It must match the root used to construct Jobs.
	TempRoot string
	// ModelPathResolver resolves trusted logical model names to local paths.
	// Request data never controls a model path.
	ModelPathResolver func(logicalName string) (string, error)
	// VADLogicalName is resolved when a preset enables VAD.
	VADLogicalName string
	// UploadBodyLimit bounds the complete multipart body. Zero selects 8 GiB.
	UploadBodyLimit int64
	// UploadReadTimeout bounds the complete request-body read. Zero selects
	// two hours, which is intentionally much shorter than job execution.
	UploadReadTimeout time.Duration
	// MaxConnections bounds accepted HTTP connections. Zero selects 128.
	// It is injectable for live resource-limit tests, not a user-facing knob.
	MaxConnections int
	// IDGenerator creates work-directory-safe job IDs. nil uses job.NewID.
	IDGenerator func() (string, error)
	// UploadFileWriter opens the fixed upload.partial path. It is injectable
	// for deterministic disk-full tests.
	UploadFileWriter func(path string) (io.WriteCloser, error)
}

// Server serves the local GUI on 127.0.0.1 only.
type Server struct {
	token             string
	logger            *slog.Logger
	mux               *http.ServeMux
	listener          net.Listener
	httpServer        *http.Server
	port              int
	discover          func() exec.Discovery
	jobs              *job.Manager
	models            ModelService
	acquireModel      ModelAcquireFunc
	presets           *config.Catalog
	tempRoot          string
	modelPathResolver func(logicalName string) (string, error)
	vadLogicalName    string
	uploadBodyLimit   int64
	uploadReadTimeout time.Duration
	maxConnections    int
	uploadPermit      chan struct{}
	idGenerator       func() (string, error)
	uploadFileWriter  func(path string) (io.WriteCloser, error)
	lifecycleMu       sync.Mutex
	closing           bool
	activeUploads     sync.WaitGroup
	activeModelLeases sync.WaitGroup
	closeOnce         sync.Once
	closeErr          error
}

// NewToken returns a 256-bit hex token from crypto/rand.
func NewToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generating auth token: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// New constructs a Server. Start must be called before serving real
// traffic; handler tests may use Handler with the configured port.
func New(cfg Config) (*Server, error) {
	if cfg.Token == "" {
		return nil, fmt.Errorf("server: token must not be empty")
	}
	presets := cfg.Presets
	if presets == nil {
		var err error
		presets, err = config.Load()
		if err != nil {
			return nil, fmt.Errorf("server: load built-in presets: %w", err)
		}
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	discover := cfg.Discover
	if discover == nil {
		discover = func() exec.Discovery {
			return exec.Discovery{
				WhisperCLI: exec.ToolStatus{Name: "whisper-cli"},
				FFmpeg:     exec.ToolStatus{Name: "ffmpeg"},
			}
		}
	}
	s := &Server{
		token:             cfg.Token,
		logger:            logger,
		mux:               http.NewServeMux(),
		port:              cfg.Port,
		discover:          discover,
		jobs:              cfg.Jobs,
		models:            cfg.Models,
		acquireModel:      cfg.AcquireModel,
		presets:           presets,
		tempRoot:          cfg.TempRoot,
		modelPathResolver: cfg.ModelPathResolver,
		vadLogicalName:    cfg.VADLogicalName,
		uploadBodyLimit:   cfg.UploadBodyLimit,
		uploadReadTimeout: cfg.UploadReadTimeout,
		maxConnections:    cfg.MaxConnections,
		uploadPermit:      make(chan struct{}, 1),
		idGenerator:       cfg.IDGenerator,
		uploadFileWriter:  cfg.UploadFileWriter,
	}
	if s.acquireModel == nil {
		if manager, ok := s.models.(*model.Manager); ok {
			s.acquireModel = func(name string) (ModelLease, error) {
				return manager.Acquire(name)
			}
		}
	}
	if s.tempRoot == "" {
		var err error
		s.tempRoot, err = job.DefaultTempRoot()
		if err != nil {
			return nil, fmt.Errorf("server: resolve default job root: %w", err)
		}
	}
	if s.modelPathResolver == nil {
		s.modelPathResolver = model.LocalPath
	}
	if s.vadLogicalName == "" {
		s.vadLogicalName = model.ModelSileroVAD
	}
	if s.uploadBodyLimit == 0 {
		s.uploadBodyLimit = defaultUploadBodyLimit
	}
	if s.uploadBodyLimit < 0 {
		return nil, fmt.Errorf("server: upload body limit must not be negative")
	}
	if s.uploadReadTimeout == 0 {
		s.uploadReadTimeout = defaultUploadReadTimeout
	}
	if s.uploadReadTimeout < 0 {
		return nil, fmt.Errorf("server: upload read timeout must not be negative")
	}
	if s.maxConnections == 0 {
		s.maxConnections = defaultMaxConnections
	}
	if s.maxConnections < 0 {
		return nil, fmt.Errorf("server: max connections must not be negative")
	}
	if s.idGenerator == nil {
		s.idGenerator = job.NewID
	}
	if s.uploadFileWriter == nil {
		s.uploadFileWriter = openUploadFile
	}
	s.routes()
	return s, nil
}

func (s *Server) routes() {
	s.mux.Handle("GET /api/config", s.authenticated(false, http.HandlerFunc(s.handleConfig)))
	s.mux.Handle("POST /api/jobs", s.authenticated(false, http.HandlerFunc(s.handleUpload)))
	s.mux.Handle("GET /api/jobs/{id}/events", s.authenticated(true, http.HandlerFunc(s.handleJobEvents)))
	s.mux.Handle("POST /api/jobs/{id}/cancel", s.authenticated(false, http.HandlerFunc(s.handleJobCancel)))
	s.mux.Handle("GET /api/jobs/{id}/outputs", s.authenticated(false, http.HandlerFunc(s.handleJobOutputs)))
	s.mux.Handle("GET /api/download/{id}/{file}", s.authenticated(false, http.HandlerFunc(s.handleDownload)))
	s.mux.Handle("GET /api/models", s.authenticated(false, http.HandlerFunc(s.handleModels)))
	s.mux.Handle("POST /api/models/{name}/download", s.authenticated(false, http.HandlerFunc(s.handleModelDownload)))
	s.mux.Handle("GET /api/models/{name}/events", s.authenticated(true, http.HandlerFunc(s.handleModelEvents)))
	s.mux.Handle("DELETE /api/models/{name}", s.authenticated(false, http.HandlerFunc(s.handleModelDelete)))
	// Unknown API paths remain header-authenticated rather than revealing a
	// separate unauthenticated API surface. Exact SSE routes register their
	// own authenticated(true, ...) wrapper above.
	s.mux.Handle("/api/", s.authenticated(false, http.NotFoundHandler()))
	static := http.FileServerFS(appweb.Assets)
	s.mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		static.ServeHTTP(w, r)
	}))
}

// configResponse is the /api/config payload. tools carries discovery
// state plus the brew package the UI should suggest when a binary is
// missing (M2-4); presets and the immutable model manifest join in M2/M3.
type configResponse struct {
	Tools   map[string]configTool `json:"tools"`
	Presets []config.Preset       `json:"presets"`
	Models  []model.Descriptor    `json:"models"`
}

type configTool struct {
	Found       bool   `json:"found"`
	Path        string `json:"path,omitempty"`
	BrewPackage string `json:"brew_package"`
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	d := s.discover()
	resp := configResponse{
		Tools: map[string]configTool{
			"whisper_cli": {Found: d.WhisperCLI.Found, Path: d.WhisperCLI.Path, BrewPackage: "whisper-cpp"},
			"ffmpeg":      {Found: d.FFmpeg.Found, Path: d.FFmpeg.Path, BrewPackage: "ffmpeg"},
		},
		Presets: s.presets.List(),
		Models:  model.Manifest(),
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		s.logger.Error("encoding /api/config response", "error", err)
	}
}

// Handler returns the full handler chain: Host validation for every
// request and Origin validation for /api/*. Token policy is attached to each
// registered API route so only exact SSE handlers can opt into query auth.
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; connect-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
		if !s.hostAllowed(r.Host) {
			s.logger.Warn("rejected request with unexpected Host header", "path", r.URL.Path)
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		if r.URL.Path != "/" && path.Clean(r.URL.Path) != r.URL.Path {
			http.Error(w, "invalid path", http.StatusBadRequest)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			if !s.originAllowed(r.Header.Get("Origin")) {
				s.logger.Warn("rejected request with unexpected Origin header", "path", r.URL.Path)
				http.Error(w, "forbidden origin", http.StatusForbidden)
				return
			}
		}
		s.mux.ServeHTTP(w, r)
	})
}

// hostAllowed accepts only 127.0.0.1:{port} and localhost:{port} to block
// DNS rebinding.
func (s *Server) hostAllowed(host string) bool {
	p := fmt.Sprintf("%d", s.port)
	return host == "127.0.0.1:"+p || host == "localhost:"+p
}

// originAllowed accepts an empty Origin (curl, some same-origin GETs) or
// the exact localhost origins for this port.
func (s *Server) originAllowed(origin string) bool {
	if origin == "" {
		return true
	}
	p := fmt.Sprintf("%d", s.port)
	return origin == "http://127.0.0.1:"+p || origin == "http://localhost:"+p
}

func (s *Server) authenticated(allowQuery bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.tokenValid(r, allowQuery) {
			http.Error(w, "missing or invalid token", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) tokenValid(r *http.Request, allowQuery bool) bool {
	headerValues := r.Header.Values("X-Auth-Token")
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return false
	}
	queryValues, hasQuery := query["token"]
	if len(headerValues) > 0 {
		if len(headerValues) != 1 || headerValues[0] == "" || hasQuery {
			return false
		}
		return tokenEqual(headerValues[0], s.token)
	}
	if !allowQuery || r.Method != http.MethodGet {
		return false
	}
	if !hasQuery || len(queryValues) != 1 || queryValues[0] == "" {
		return false
	}
	return tokenEqual(queryValues[0], s.token)
}

func tokenEqual(got, want string) bool {
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// Start binds 127.0.0.1 (never any other interface) and serves in a
// background goroutine. It returns the base URL without the token.
func (s *Server) Start() (string, error) {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.closing {
		return "", fmt.Errorf("server is closed")
	}
	if s.httpServer != nil || s.listener != nil {
		return "", fmt.Errorf("server is already started")
	}
	baseListener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", s.port))
	if err != nil {
		return "", fmt.Errorf("binding 127.0.0.1:%d: %w", s.port, err)
	}
	ln := newConnectionLimitListener(baseListener, s.maxConnections)
	s.listener = ln
	s.port = ln.Addr().(*net.TCPAddr).Port
	srv := &http.Server{
		Handler:           s.Handler(),
		ReadTimeout:       s.uploadReadTimeout,
		ReadHeaderTimeout: readHeaderTimeout,
		IdleTimeout:       idleTimeout,
		MaxHeaderBytes:    maxHeaderBytes,
	}
	s.httpServer = srv
	go func() {
		if err := srv.Serve(ln); err != nil && !isClosedErr(err) {
			s.logger.Error("http server stopped", "error", err)
		}
	}()
	return fmt.Sprintf("http://127.0.0.1:%d", s.port), nil
}

func isClosedErr(err error) bool {
	return errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed)
}

// Port returns the bound (or configured) port.
func (s *Server) Port() int {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	return s.port
}

// Addr returns the bound listener address, nil before Start.
func (s *Server) Addr() net.Addr {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.listener == nil {
		return nil
	}
	return s.listener.Addr()
}

// Close stops the listener and closes the Manager supplied to Config. Both
// actions run at most once, including when the server was never started.
func (s *Server) Close() error {
	s.closeOnce.Do(func() {
		s.lifecycleMu.Lock()
		s.closing = true
		httpServer := s.httpServer
		listener := s.listener
		s.lifecycleMu.Unlock()

		if httpServer != nil {
			s.closeErr = httpServer.Close()
		} else if listener != nil {
			s.closeErr = listener.Close()
		}
		if isClosedErr(s.closeErr) {
			s.closeErr = nil
		}
		s.activeUploads.Wait()
		if s.jobs != nil {
			s.jobs.Close()
		}
		s.activeModelLeases.Wait()
		if s.models != nil {
			s.models.Close()
		}
	})
	return s.closeErr
}

func (s *Server) beginUpload() bool {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.closing {
		return false
	}
	s.activeUploads.Add(1)
	return true
}

func (s *Server) endUpload() {
	s.activeUploads.Done()
}
