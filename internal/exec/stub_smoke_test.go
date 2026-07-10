package exec

import (
	"encoding/json"
	"os"
	osexec "os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func stubPath(t *testing.T, name string) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("..", "..", "testdata", "stubs", name))
	if err != nil {
		t.Fatalf("resolving stub path: %v", err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("stub %s not found: %v", name, err)
	}
	return p
}

type invocation struct {
	Stub string            `json:"stub"`
	Argv []string          `json:"argv"`
	Env  map[string]string `json:"env"`
}

func readInvocation(t *testing.T, dir string) invocation {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "invocation.json"))
	if err != nil {
		t.Fatalf("reading invocation.json: %v", err)
	}
	var inv invocation
	if err := json.Unmarshal(data, &inv); err != nil {
		t.Fatalf("parsing invocation.json: %v", err)
	}
	return inv
}

// TestWhisperCliStubProducesOutputs keeps the fake whisper-cli honest: it
// must record its argv, emit progress on stderr, and generate txt/srt/vtt
// following the -of prefix.
func TestWhisperCliStubProducesOutputs(t *testing.T) {
	dir := t.TempDir()
	prefix := filepath.Join(dir, "result")
	cmd := osexec.Command(stubPath(t, "whisper-cli"),
		"-m", "model.bin", "-f", "in.wav", "--vad", "--vad-model", "vad.bin", "-of", prefix, "-pp")
	cmd.Env = append(os.Environ(), "STUB_PROGRESS_INTERVAL=10ms")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("stub run failed: %v\nstderr: %s", err, stderr.String())
	}
	for _, ext := range []string{".txt", ".srt", ".vtt"} {
		if _, err := os.Stat(prefix + ext); err != nil {
			t.Errorf("expected output %s%s: %v", prefix, ext, err)
		}
	}
	if !strings.Contains(stderr.String(), "progress = 100%") {
		t.Errorf("stderr missing progress lines: %q", stderr.String())
	}
	inv := readInvocation(t, dir)
	if inv.Stub != "whisper-cli" {
		t.Errorf("invocation.json stub = %q, want whisper-cli", inv.Stub)
	}
	if len(inv.Argv) == 0 || inv.Argv[0] != "-m" {
		t.Errorf("invocation.json argv not recorded: %v", inv.Argv)
	}
}

func TestWhisperCliStubRequiresPrintProgressFlag(t *testing.T) {
	dir := t.TempDir()
	cmd := osexec.Command(stubPath(t, "whisper-cli"), "-of", filepath.Join(dir, "result"), "-otxt")
	cmd.Env = append(os.Environ(), "STUB_PROGRESS_INTERVAL=10ms")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stderr.String(), "progress =") {
		t.Fatalf("stub emitted progress without -pp: %q", stderr.String())
	}
}

func TestWhisperCliStubAcceptsLongPrintProgressFlag(t *testing.T) {
	dir := t.TempDir()
	cmd := osexec.Command(stubPath(t, "whisper-cli"), "-of", filepath.Join(dir, "result"), "-otxt", "--print-progress")
	cmd.Env = append(os.Environ(), "STUB_PROGRESS_INTERVAL=10ms")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr.String(), "progress = 100%") {
		t.Fatalf("stub did not honor --print-progress: %q", stderr.String())
	}
}

// TestWhisperCliStubFailureModes covers STUB_FAIL and STUB_NO_VAD.
func TestWhisperCliStubFailureModes(t *testing.T) {
	cases := []struct {
		name string
		env  []string
		args []string
	}{
		{"forced failure", []string{"STUB_FAIL=1"}, []string{"-f", "in.wav"}},
		{"vad unsupported", []string{"STUB_NO_VAD=1", "STUB_PROGRESS_INTERVAL=10ms"}, []string{"--vad"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			args := append(tc.args, "-of", filepath.Join(dir, "r"))
			cmd := osexec.Command(stubPath(t, "whisper-cli"), args...)
			cmd.Env = append(os.Environ(), tc.env...)
			if err := cmd.Run(); err == nil {
				t.Fatalf("expected non-zero exit")
			}
			// The invocation must be recorded even on failure so cleanup
			// tests can assert what was attempted.
			readInvocation(t, dir)
		})
	}
}

// TestWhisperCliStubHelpTogglesVAD verifies --help output switches between
// VAD-capable and VAD-less builds via STUB_NO_VAD.
func TestWhisperCliStubHelpTogglesVAD(t *testing.T) {
	out, err := osexec.Command(stubPath(t, "whisper-cli"), "--help").Output()
	if err != nil {
		t.Fatalf("--help failed: %v", err)
	}
	if !strings.Contains(string(out), "--vad-model") || !regexp.MustCompile(`--vad\s`).Match(out) {
		t.Errorf("default --help must advertise both --vad and --vad-model:\n%s", out)
	}
	cmd := osexec.Command(stubPath(t, "whisper-cli"), "--help")
	cmd.Env = append(os.Environ(), "STUB_NO_VAD=1")
	out, err = cmd.Output()
	if err != nil {
		t.Fatalf("--help (STUB_NO_VAD) failed: %v", err)
	}
	if strings.Contains(string(out), "--vad") {
		t.Errorf("STUB_NO_VAD --help must not advertise VAD flags:\n%s", out)
	}
}

// TestFfmpegStubWritesWav verifies the fake ffmpeg records argv and writes
// a RIFF/WAVE file at the final-argument output path.
func TestFfmpegStubWritesWav(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "out.wav")
	if err := osexec.Command(stubPath(t, "ffmpeg"), "-i", "in.mp4", "-ar", "16000", "-ac", "1", out).Run(); err != nil {
		t.Fatalf("ffmpeg stub failed: %v", err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("reading wav: %v", err)
	}
	if len(data) < 44 || string(data[:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		t.Errorf("output is not a RIFF/WAVE file (len=%d)", len(data))
	}
	inv := readInvocation(t, dir)
	if inv.Stub != "ffmpeg" {
		t.Errorf("invocation.json stub = %q, want ffmpeg", inv.Stub)
	}

	cmd := osexec.Command(stubPath(t, "ffmpeg"), "-i", "in.mp4", filepath.Join(dir, "f", "o.wav"))
	cmd.Env = append(os.Environ(), "STUB_FAIL=1")
	if err := cmd.Run(); err == nil {
		t.Fatalf("STUB_FAIL=1 must exit non-zero")
	}
}
