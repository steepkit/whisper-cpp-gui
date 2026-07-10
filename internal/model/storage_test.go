package model

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestLocalPathInKnownModels(t *testing.T) {
	directory := t.TempDir()
	cases := []struct {
		name     string
		model    string
		filename string
	}{
		{name: "large v3", model: ModelLargeV3Q50, filename: "ggml-large-v3-q5_0.bin"},
		{name: "large v3 turbo", model: ModelLargeV3TurboQ50, filename: "ggml-large-v3-turbo-q5_0.bin"},
		{name: "silero vad", model: ModelSileroVAD, filename: "ggml-silero-v6.2.0.bin"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			got, err := LocalPathIn(directory, test.model)
			if err != nil {
				t.Fatalf("LocalPathIn: %v", err)
			}
			want := filepath.Join(directory, test.filename)
			if got != want {
				t.Errorf("LocalPathIn = %q, want %q", got, want)
			}
		})
	}
}

func TestLocalPathInRejectsInvalidNames(t *testing.T) {
	directory := t.TempDir()
	cases := []struct {
		name    string
		model   string
		wantErr error
	}{
		{name: "empty", model: "", wantErr: ErrInvalidModelName},
		{name: "dot", model: ".", wantErr: ErrInvalidModelName},
		{name: "parent", model: "..", wantErr: ErrInvalidModelName},
		{name: "slash", model: "../" + ModelLargeV3Q50, wantErr: ErrInvalidModelName},
		{name: "backslash", model: `..\large-v3-q5_0`, wantErr: ErrInvalidModelName},
		{name: "unknown", model: "not-a-model", wantErr: ErrUnknownModel},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			_, err := LocalPathIn(directory, test.model)
			if !errors.Is(err, test.wantErr) {
				t.Errorf("LocalPathIn(%q) error = %v, want %v", test.model, err, test.wantErr)
			}
		})
	}

	if _, err := LocalPathIn("", ModelLargeV3Q50); err == nil {
		t.Error("LocalPathIn accepted an empty directory")
	}
}

func TestDirectoryUsesUserConfigDirectory(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)

	got, err := Directory()
	if err != nil {
		t.Fatalf("Directory: %v", err)
	}
	userConfigDir, err := os.UserConfigDir()
	if err != nil {
		t.Fatalf("UserConfigDir: %v", err)
	}
	want := filepath.Join(userConfigDir, "whisper-cpp-gui", "models")
	if got != want {
		t.Errorf("Directory = %q, want %q", got, want)
	}
}

func TestLocalPathUsesDirectory(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)

	got, err := LocalPath(ModelSileroVAD)
	if err != nil {
		t.Fatalf("LocalPath: %v", err)
	}
	userConfigDir, err := os.UserConfigDir()
	if err != nil {
		t.Fatalf("UserConfigDir: %v", err)
	}
	want := filepath.Join(userConfigDir, "whisper-cpp-gui", "models", "ggml-silero-v6.2.0.bin")
	if got != want {
		t.Errorf("LocalPath = %q, want %q", got, want)
	}
}
