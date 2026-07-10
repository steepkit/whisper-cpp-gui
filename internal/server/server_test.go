package server

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	wexec "github.com/steepkit/whisper-cpp-gui/internal/exec"
	"github.com/steepkit/whisper-cpp-gui/internal/model"
)

const testToken = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func newTestServer(t *testing.T, port int) *Server {
	t.Helper()
	s, err := New(Config{
		Port:   port,
		Token:  testToken,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

// doRequest hits the full handler chain with controllable Host, Origin,
// and token placement.
func doRequest(s *Server, method, target, host, origin, headerToken string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	req.Host = host
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if headerToken != "" {
		req.Header.Set("X-Auth-Token", headerToken)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

func TestAPIAuthAndHostAndOrigin(t *testing.T) {
	const port = 8123
	host := fmt.Sprintf("127.0.0.1:%d", port)
	cases := []struct {
		name        string
		target      string
		host        string
		origin      string
		headerToken string
		wantStatus  int
	}{
		{"no token is 401", "/api/config", host, "", "", http.StatusUnauthorized},
		{"wrong header token is 401", "/api/config", host, "", "wrong", http.StatusUnauthorized},
		{"wrong query token is 401", "/api/config?token=wrong", host, "", "", http.StatusUnauthorized},
		{"header token ok", "/api/config", host, "", testToken, http.StatusOK},
		{"query token rejected on ordinary API", "/api/config?token=" + testToken, host, "", "", http.StatusUnauthorized},
		{"localhost host ok", "/api/config", fmt.Sprintf("localhost:%d", port), "", testToken, http.StatusOK},
		{"forged host is 403", "/api/config", "evil.example:80", "", testToken, http.StatusForbidden},
		{"forged host wrong port is 403", "/api/config", "127.0.0.1:9999", "", testToken, http.StatusForbidden},
		{"host check also guards static", "/", "evil.example:80", "", "", http.StatusForbidden},
		{"static with valid host needs no token", "/", host, "", "", http.StatusOK},
		{"empty origin ok", "/api/config", host, "", testToken, http.StatusOK},
		{"loopback origin ok", "/api/config", host, fmt.Sprintf("http://127.0.0.1:%d", port), testToken, http.StatusOK},
		{"localhost origin ok", "/api/config", host, fmt.Sprintf("http://localhost:%d", port), testToken, http.StatusOK},
		{"cross-site origin is 403", "/api/config", host, "http://evil.example", testToken, http.StatusForbidden},
		{"https origin is 403", "/api/config", host, fmt.Sprintf("https://127.0.0.1:%d", port), testToken, http.StatusForbidden},
		{"wrong-port origin is 403", "/api/config", host, "http://127.0.0.1:9999", testToken, http.StatusForbidden},
		{"origin check precedes token: bad origin without token is 403", "/api/config", host, "http://evil.example", "", http.StatusForbidden},
		// Hardening cases (G4 suggestions): case, trailing dot, IPv6,
		// 127.0.0.2, null Origin, subdomain trick.
		{"uppercase host is 403", "/api/config", fmt.Sprintf("LOCALHOST:%d", port), "", testToken, http.StatusForbidden},
		{"trailing-dot host is 403", "/api/config", fmt.Sprintf("localhost.:%d", port), "", testToken, http.StatusForbidden},
		{"ipv6 loopback host is 403", "/api/config", fmt.Sprintf("[::1]:%d", port), "", testToken, http.StatusForbidden},
		{"127.0.0.2 host is 403", "/api/config", fmt.Sprintf("127.0.0.2:%d", port), "", testToken, http.StatusForbidden},
		{"portless host is 403", "/api/config", "127.0.0.1", "", testToken, http.StatusForbidden},
		{"null origin is 403", "/api/config", host, "null", testToken, http.StatusForbidden},
		{"subdomain-trick origin is 403", "/api/config", host, fmt.Sprintf("http://localhost.evil.com:%d", port), testToken, http.StatusForbidden},
		{"uppercase-scheme origin is 403", "/api/config", host, fmt.Sprintf("HTTP://127.0.0.1:%d", port), testToken, http.StatusForbidden},
	}
	s := newTestServer(t, port)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := doRequest(s, http.MethodGet, tc.target, tc.host, tc.origin, tc.headerToken)
			if rec.Code != tc.wantStatus {
				t.Errorf("got %d, want %d (body: %s)", rec.Code, tc.wantStatus, strings.TrimSpace(rec.Body.String()))
			}
		})
	}
}

func TestQueryTokenIsLimitedToSSEPaths(t *testing.T) {
	const port = 8123
	host := fmt.Sprintf("127.0.0.1:%d", port)
	s := newTestServer(t, port)
	cases := []struct {
		name       string
		method     string
		target     string
		wantStatus int
	}{
		{"job events accepts query auth", http.MethodGet, "/api/jobs/job-1/events?token=" + testToken, http.StatusServiceUnavailable},
		{"model events accepts query auth", http.MethodGet, "/api/models/model-1/events?token=" + testToken, http.StatusServiceUnavailable},
		{"wrong SSE token rejected", http.MethodGet, "/api/jobs/job-1/events?token=wrong", http.StatusUnauthorized},
		{"POST is never query authenticated", http.MethodPost, "/api/jobs/job-1/events?token=" + testToken, http.StatusUnauthorized},
		{"lookalike path rejected", http.MethodGet, "/api/jobs/job-1/events/extra?token=" + testToken, http.StatusUnauthorized},
		{"escaped slash cannot opt into SSE auth", http.MethodGet, "/api/jobs/job%2Fevents?token=" + testToken, http.StatusUnauthorized},
		{"lowercase escaped slash cannot opt into SSE auth", http.MethodGet, "/api/jobs/job%2fevents?token=" + testToken, http.StatusUnauthorized},
		{"trailing slash is rejected", http.MethodGet, "/api/jobs/job-1/events/?token=" + testToken, http.StatusBadRequest},
		{"duplicate slash is rejected", http.MethodGet, "/api/jobs//events?token=" + testToken, http.StatusBadRequest},
		{"dot segment is rejected", http.MethodGet, "/api/jobs/../events?token=" + testToken, http.StatusBadRequest},
		{"duplicate query token rejected", http.MethodGet, "/api/jobs/job-1/events?token=" + testToken + "&token=" + testToken, http.StatusUnauthorized},
		{"malformed query token rejected", http.MethodGet, "/api/jobs/job-1/events?token=" + testToken + "&token=%zz", http.StatusUnauthorized},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := doRequest(s, tc.method, tc.target, host, "", "")
			if rec.Code != tc.wantStatus {
				t.Errorf("got %d, want %d (body: %s)", rec.Code, tc.wantStatus, strings.TrimSpace(rec.Body.String()))
			}
		})
	}
}

func TestHeaderPresencePreventsQueryFallback(t *testing.T) {
	const port = 8123
	host := fmt.Sprintf("127.0.0.1:%d", port)
	s := newTestServer(t, port)

	cases := []struct {
		name    string
		headers []string
		query   string
	}{
		{"empty header", []string{""}, testToken},
		{"wrong header", []string{"wrong"}, testToken},
		{"correct header plus query", []string{testToken}, testToken},
		{"duplicate header", []string{testToken, testToken}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			target := "/api/jobs/job-1/events"
			if tc.query != "" {
				target += "?token=" + tc.query
			}
			req := httptest.NewRequest(http.MethodGet, target, nil)
			req.Host = host
			req.Header["X-Auth-Token"] = tc.headers
			rec := httptest.NewRecorder()
			s.Handler().ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("got %d, want 401", rec.Code)
			}
		})
	}
}

func TestMalformedQueryFailsClosedWithValidHeader(t *testing.T) {
	const port = 8123
	host := fmt.Sprintf("127.0.0.1:%d", port)
	s := newTestServer(t, port)
	rec := doRequest(s, http.MethodGet, "/api/config?value=%zz", host, "", testToken)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("malformed query with valid header got %d, want 401", rec.Code)
	}
}

func TestSecurityResponseHeadersAndTokenFreeLogs(t *testing.T) {
	const port = 8123
	var logs bytes.Buffer
	s, err := New(Config{
		Port:   port,
		Token:  testToken,
		Logger: slog.New(slog.NewTextHandler(&logs, nil)),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	rec := doRequest(s, http.MethodGet, "/api/config?token="+testToken, "evil.example", "", "")
	if rec.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Errorf("Referrer-Policy = %q, want no-referrer", rec.Header().Get("Referrer-Policy"))
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", rec.Header().Get("Cache-Control"))
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", rec.Header().Get("X-Content-Type-Options"))
	}
	if rec.Header().Get("Cross-Origin-Resource-Policy") != "same-origin" {
		t.Errorf("Cross-Origin-Resource-Policy = %q, want same-origin", rec.Header().Get("Cross-Origin-Resource-Policy"))
	}
	if !strings.Contains(rec.Header().Get("Content-Security-Policy"), "script-src 'self'") {
		t.Errorf("Content-Security-Policy missing script-src self: %q", rec.Header().Get("Content-Security-Policy"))
	}
	if strings.Contains(logs.String(), testToken) || strings.Contains(logs.String(), "token=") {
		t.Errorf("request logs exposed token or query: %q", logs.String())
	}
}

func TestStaticBootstrapAssetsAreServed(t *testing.T) {
	const port = 8123
	host := fmt.Sprintf("127.0.0.1:%d", port)
	s := newTestServer(t, port)

	index := doRequest(s, http.MethodGet, "/", host, "", "")
	if index.Code != http.StatusOK || !strings.Contains(index.Body.String(), `src="/bootstrap.js"`) {
		t.Fatalf("index response = %d %q", index.Code, index.Body.String())
	}
	script := doRequest(s, http.MethodGet, "/bootstrap.js", host, "", "")
	if script.Code != http.StatusOK || !strings.Contains(script.Body.String(), "window.history.replaceState") {
		t.Fatalf("bootstrap.js response = %d %q", script.Code, script.Body.String())
	}
}

// TestStartBindsLoopbackOnly asserts the listener address is 127.0.0.1 and
// that the base URL points there.
func TestStartBindsLoopbackOnly(t *testing.T) {
	s := newTestServer(t, 0)
	baseURL, err := s.Start()
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Close()
	addr, ok := s.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("Addr is %T, want *net.TCPAddr", s.Addr())
	}
	if got := addr.IP.String(); got != "127.0.0.1" {
		t.Errorf("bound IP = %s, want 127.0.0.1", got)
	}
	if want := fmt.Sprintf("http://127.0.0.1:%d", addr.Port); baseURL != want {
		t.Errorf("baseURL = %s, want %s", baseURL, want)
	}
}

// TestStartFixedPort covers --port behavior: it must bind the requested
// port and fail clearly when the port is occupied.
func TestStartFixedPort(t *testing.T) {
	// Reserve a free port, release it, then ask the server for it.
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("probe listen: %v", err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	probe.Close()

	s := newTestServer(t, port)
	if _, err := s.Start(); err != nil {
		t.Fatalf("Start on free fixed port %d: %v", port, err)
	}
	defer s.Close()
	if s.Port() != port {
		t.Errorf("Port() = %d, want %d", s.Port(), port)
	}

	// Same port again must fail with a clear error.
	s2 := newTestServer(t, port)
	if _, err := s2.Start(); err == nil {
		s2.Close()
		t.Fatalf("Start on occupied port %d succeeded, want error", port)
	} else if !strings.Contains(err.Error(), fmt.Sprintf("127.0.0.1:%d", port)) {
		t.Errorf("occupied-port error should name the address, got: %v", err)
	}
}

func TestConnectionLimitBoundsAndReleasesKeepAliveConnections(t *testing.T) {
	const limit = 2
	s, err := New(Config{
		Port:           0,
		Token:          testToken,
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		MaxConnections: limit,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Start(); err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	first, err := openKeepAliveConnection(s)
	if err != nil {
		t.Fatalf("first connection: %v", err)
	}
	defer first.Close()
	second, err := openKeepAliveConnection(s)
	if err != nil {
		t.Fatalf("second connection: %v", err)
	}
	defer second.Close()

	if overflow, err := openKeepAliveConnection(s); err == nil {
		overflow.Close()
		t.Fatalf("connection above limit %d was accepted", limit)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close first connection: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		replacement, err := openKeepAliveConnection(s)
		if err == nil {
			replacement.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("connection slot was not released after Close")
}

func openKeepAliveConnection(s *Server) (net.Conn, error) {
	connection, err := net.DialTimeout("tcp", s.Addr().String(), time.Second)
	if err != nil {
		return nil, err
	}
	failed := true
	defer func() {
		if failed {
			_ = connection.Close()
		}
	}()
	if err := connection.SetDeadline(time.Now().Add(time.Second)); err != nil {
		return nil, err
	}
	request := fmt.Sprintf("GET / HTTP/1.1\r\nHost: 127.0.0.1:%d\r\nConnection: keep-alive\r\n\r\n", s.Port())
	if _, err := io.WriteString(connection, request); err != nil {
		return nil, err
	}
	response, err := http.ReadResponse(bufio.NewReader(connection), &http.Request{Method: http.MethodGet})
	if err != nil {
		return nil, err
	}
	if _, err := io.Copy(io.Discard, response.Body); err != nil {
		_ = response.Body.Close()
		return nil, err
	}
	if err := response.Body.Close(); err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET / status = %d", response.StatusCode)
	}
	if err := connection.SetDeadline(time.Time{}); err != nil {
		return nil, err
	}
	failed = false
	return connection, nil
}

// TestTokenValidatedAfterStartWithRandomPort exercises the live server end
// to end so the Host check uses the actual bound port.
func TestTokenValidatedAfterStartWithRandomPort(t *testing.T) {
	s := newTestServer(t, 0)
	baseURL, err := s.Start()
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Close()

	resp, err := http.Get(baseURL + "/api/config")
	if err != nil {
		t.Fatalf("GET without token: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("without token got %d, want 401", resp.StatusCode)
	}

	req, _ := http.NewRequest(http.MethodGet, baseURL+"/api/config", nil)
	req.Header.Set("X-Auth-Token", testToken)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET with token: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("with token got %d, want 200", resp.StatusCode)
	}
}

func TestNewTokenIsRandomHex(t *testing.T) {
	a, err := NewToken()
	if err != nil {
		t.Fatalf("NewToken: %v", err)
	}
	b, err := NewToken()
	if err != nil {
		t.Fatalf("NewToken: %v", err)
	}
	if len(a) != 64 || a == b {
		t.Errorf("tokens must be 64 hex chars and unique: %q vs %q", a, b)
	}
}

func TestNewRequiresToken(t *testing.T) {
	if _, err := New(Config{Token: ""}); err == nil {
		t.Fatal("New with empty token must fail")
	}
}

func TestNewRejectsNegativeUploadReadTimeout(t *testing.T) {
	if _, err := New(Config{Token: testToken, UploadReadTimeout: -time.Second}); err == nil {
		t.Fatal("New with a negative upload read timeout must fail")
	}
}

func TestNewRejectsNegativeConnectionLimit(t *testing.T) {
	if _, err := New(Config{Token: testToken, MaxConnections: -1}); err == nil {
		t.Fatal("New with a negative connection limit must fail")
	}
}

// TestConfigReportsDiscovery is the M1-2 handler acceptance test:
// /api/config must reflect the injected discovery state and name the brew
// packages the UI suggests for missing binaries.
func TestConfigReportsDiscovery(t *testing.T) {
	const port = 8123
	host := fmt.Sprintf("127.0.0.1:%d", port)
	s, err := New(Config{
		Port:   port,
		Token:  testToken,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Discover: func() wexec.Discovery {
			return wexec.Discovery{
				WhisperCLI: wexec.ToolStatus{Name: "whisper-cli", Path: "/opt/homebrew/bin/whisper-cli", Found: true},
				FFmpeg:     wexec.ToolStatus{Name: "ffmpeg"},
			}
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	rec := doRequest(s, http.MethodGet, "/api/config", host, "", testToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	var resp struct {
		Tools map[string]struct {
			Found       bool   `json:"found"`
			Path        string `json:"path"`
			BrewPackage string `json:"brew_package"`
		} `json:"tools"`
		Presets []struct {
			ID      string   `json:"id"`
			Name    string   `json:"name"`
			Model   string   `json:"model"`
			VAD     bool     `json:"vad"`
			Outputs []string `json:"outputs"`
		} `json:"presets"`
		Models []model.Descriptor `json:"models"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parsing response: %v (body: %s)", err, rec.Body.String())
	}
	wc := resp.Tools["whisper_cli"]
	if !wc.Found || wc.Path != "/opt/homebrew/bin/whisper-cli" || wc.BrewPackage != "whisper-cpp" {
		t.Errorf("whisper_cli = %+v, want found with brew package whisper-cpp", wc)
	}
	ff := resp.Tools["ffmpeg"]
	if ff.Found || ff.BrewPackage != "ffmpeg" {
		t.Errorf("ffmpeg = %+v, want not found with brew package ffmpeg", ff)
	}
	if len(resp.Presets) != 2 || resp.Presets[0].ID != "ja_fast" || resp.Presets[0].Name != "preset.ja_fast.name" ||
		resp.Presets[0].Model != "large-v3-turbo-q5_0" || !resp.Presets[0].VAD || strings.Join(resp.Presets[0].Outputs, ",") != "txt,srt,vtt" {
		t.Fatalf("presets = %+v", resp.Presets)
	}
	if want := model.Manifest(); !slices.Equal(resp.Models, want) {
		t.Fatalf("models = %+v, want %+v", resp.Models, want)
	}
}

// TestConfigDefaultsToNothingFound guards the nil-Discover fallback.
func TestConfigDefaultsToNothingFound(t *testing.T) {
	const port = 8123
	s := newTestServer(t, port)
	rec := doRequest(s, http.MethodGet, "/api/config", fmt.Sprintf("127.0.0.1:%d", port), "", testToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if strings.Contains(rec.Body.String(), `"found":true`) {
		t.Errorf("nil Discover must report nothing found: %s", rec.Body.String())
	}
}
