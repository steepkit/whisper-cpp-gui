package exec

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// maxUserConfigBytes caps the optional config file. It only holds two
// paths; anything larger is treated as corruption/attack rather than read
// into memory inside a request handler.
const maxUserConfigBytes = 64 << 10 // 64 KiB

// ToolStatus reports whether a required binary was found and where.
type ToolStatus struct {
	Name  string `json:"name"`
	Path  string `json:"path,omitempty"`
	Found bool   `json:"found"`
}

// UserConfig is the optional user settings file. It carries explicit
// binary paths only (docs/implementation_plan.md §9); everything else is
// internal constants.
type UserConfig struct {
	WhisperCLIPath string `json:"whisper_cli_path,omitempty"`
	FFmpegPath     string `json:"ffmpeg_path,omitempty"`
}

// UserConfigPath returns {os.UserConfigDir()}/whisper-cpp-gui/config.json.
func UserConfigPath() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolving user config dir: %w", err)
	}
	return filepath.Join(base, "whisper-cpp-gui", "config.json"), nil
}

// LoadUserConfig reads the config file at path. A missing file is not an
// error: it returns the zero config. The file must be a regular file no
// larger than maxUserConfigBytes so a tampered or runaway config cannot
// force a large allocation in a request handler.
func LoadUserConfig(path string) (UserConfig, error) {
	var cfg UserConfig
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, fmt.Errorf("opening user config %s: %w", path, err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return cfg, fmt.Errorf("stat user config %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return cfg, fmt.Errorf("user config %s is not a regular file", path)
	}
	if info.Size() > maxUserConfigBytes {
		return cfg, fmt.Errorf("user config %s is too large (%d bytes, limit %d)", path, info.Size(), maxUserConfigBytes)
	}

	// LimitReader guards against the size growing between Stat and Read.
	data, err := io.ReadAll(io.LimitReader(f, maxUserConfigBytes+1))
	if err != nil {
		return cfg, fmt.Errorf("reading user config %s: %w", path, err)
	}
	if int64(len(data)) > maxUserConfigBytes {
		return cfg, fmt.Errorf("user config %s exceeds %d bytes", path, maxUserConfigBytes)
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("parsing user config %s: %w", path, err)
	}
	return cfg, nil
}

// brewDirs lists Homebrew binary directories appended after $PATH so brew
// installs are found even when the GUI is launched outside a brew-aware
// shell.
func brewDirs(goos string) []string {
	switch goos {
	case "darwin":
		return []string{"/opt/homebrew/bin", "/usr/local/bin"}
	case "linux":
		return []string{"/home/linuxbrew/.linuxbrew/bin", "/usr/local/bin"}
	default:
		return nil
	}
}

func isExecutable(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	return info.Mode()&0o111 != 0
}

// findTool resolves one binary. Search order: explicit config path first,
// then each $PATH entry, then extraDirs (Homebrew directories in
// production; injectable so tests stay hermetic). An explicit path that
// does not point at an executable falls through to the PATH search so a
// stale config entry does not brick discovery.
func findTool(name, explicit, pathEnv string, extraDirs []string) ToolStatus {
	if explicit != "" && isExecutable(explicit) {
		return ToolStatus{Name: name, Path: explicit, Found: true}
	}
	dirs := filepath.SplitList(pathEnv)
	dirs = append(dirs, extraDirs...)
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		candidate := filepath.Join(dir, name)
		if isExecutable(candidate) {
			return ToolStatus{Name: name, Path: candidate, Found: true}
		}
	}
	return ToolStatus{Name: name}
}

// Discovery is the result of locating both required binaries.
type Discovery struct {
	WhisperCLI ToolStatus `json:"whisper_cli"`
	FFmpeg     ToolStatus `json:"ffmpeg"`
}

// Discover locates whisper-cli and ffmpeg. pathEnv and goos are parameters
// so tests can control the search space.
func Discover(cfg UserConfig, pathEnv, goos string) Discovery {
	extra := brewDirs(goos)
	return Discovery{
		WhisperCLI: findTool("whisper-cli", cfg.WhisperCLIPath, pathEnv, extra),
		FFmpeg:     findTool("ffmpeg", cfg.FFmpegPath, pathEnv, extra),
	}
}
