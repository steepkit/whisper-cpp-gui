package model

import (
	"fmt"
	"os"
	"path/filepath"
)

// Directory returns the application-owned directory for downloaded models.
func Directory() (string, error) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolve user config directory: %w", err)
	}
	if !filepath.IsAbs(configDir) {
		return "", fmt.Errorf("user config directory is not absolute")
	}
	return filepath.Join(configDir, "whisper-cpp-gui", "models"), nil
}

// LocalPath returns the fixed local path for a known logical model name.
func LocalPath(name string) (string, error) {
	directory, err := Directory()
	if err != nil {
		return "", err
	}
	return LocalPathIn(directory, name)
}

// LocalPathIn returns the fixed local path for a known logical model name in
// directory. Callers cannot choose the filename through name.
func LocalPathIn(directory, name string) (string, error) {
	if directory == "" {
		return "", fmt.Errorf("model directory must not be empty")
	}
	entry, err := lookupEntry(name)
	if err != nil {
		return "", err
	}
	return filepath.Join(directory, entry.descriptor.Filename), nil
}
