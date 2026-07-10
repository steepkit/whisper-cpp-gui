package exec

import (
	"os"
	"path/filepath"
	"testing"
)

// placeTool creates an executable (or not) fake binary and returns its path.
func placeTool(t *testing.T, dir, name string, executable bool) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	path := filepath.Join(dir, name)
	mode := os.FileMode(0o644)
	if executable {
		mode = 0o755
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), mode); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
	return path
}

// TestFindToolSearchOrder is the acceptance test for M1-2: explicit config
// path wins over PATH, PATH order is respected, and a stale explicit path
// falls back to PATH.
func TestFindToolSearchOrder(t *testing.T) {
	root := t.TempDir()
	explicitDir := filepath.Join(root, "explicit")
	pathA := filepath.Join(root, "path-a")
	pathB := filepath.Join(root, "path-b")

	explicit := placeTool(t, explicitDir, "whisper-cli", true)
	inPathA := placeTool(t, pathA, "whisper-cli", true)
	placeTool(t, pathB, "whisper-cli", true)
	pathEnv := pathA + string(os.PathListSeparator) + pathB

	cases := []struct {
		name     string
		explicit string
		pathEnv  string
		want     string
		found    bool
	}{
		{"explicit beats PATH", explicit, pathEnv, explicit, true},
		{"first PATH dir wins", "", pathEnv, inPathA, true},
		{"stale explicit falls back to PATH", filepath.Join(root, "missing", "whisper-cli"), pathEnv, inPathA, true},
		{"nothing found", "", filepath.Join(root, "empty"), "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := findTool("whisper-cli", tc.explicit, tc.pathEnv, nil)
			if got.Found != tc.found || got.Path != tc.want {
				t.Errorf("findTool = %+v, want path %q found %v", got, tc.want, tc.found)
			}
		})
	}
}

// TestFindToolBrewFallback verifies extra (brew) directories are searched
// after PATH.
func TestFindToolBrewFallback(t *testing.T) {
	root := t.TempDir()
	brew := filepath.Join(root, "brew-bin")
	inBrew := placeTool(t, brew, "whisper-cli", true)
	got := findTool("whisper-cli", "", filepath.Join(root, "empty"), []string{brew})
	if !got.Found || got.Path != inBrew {
		t.Errorf("findTool = %+v, want brew fallback %q", got, inBrew)
	}
}

// TestFindToolSkipsNonExecutable ensures a plain file on PATH does not
// count as a discovered binary.
func TestFindToolSkipsNonExecutable(t *testing.T) {
	dir := t.TempDir()
	placeTool(t, dir, "ffmpeg", false)
	got := findTool("ffmpeg", "", dir, nil)
	if got.Found {
		t.Errorf("non-executable file must not be discovered: %+v", got)
	}
}

func TestDiscoverReportsBothTools(t *testing.T) {
	dir := t.TempDir()
	placeTool(t, dir, "whisper-cli", true)
	// goos "testos" yields no brew dirs, keeping the test hermetic even on
	// hosts that really have linuxbrew binaries installed.
	d := Discover(UserConfig{}, dir, "testos")
	if !d.WhisperCLI.Found {
		t.Errorf("whisper-cli should be found: %+v", d.WhisperCLI)
	}
	if d.FFmpeg.Found {
		t.Errorf("ffmpeg should be missing: %+v", d.FFmpeg)
	}
}

func TestLoadUserConfig(t *testing.T) {
	dir := t.TempDir()

	t.Run("missing file is zero config", func(t *testing.T) {
		cfg, err := LoadUserConfig(filepath.Join(dir, "nope.json"))
		if err != nil || cfg != (UserConfig{}) {
			t.Errorf("got %+v, %v; want zero config, nil", cfg, err)
		}
	})

	t.Run("valid file", func(t *testing.T) {
		p := filepath.Join(dir, "config.json")
		if err := os.WriteFile(p, []byte(`{"whisper_cli_path":"/x/whisper-cli","ffmpeg_path":"/x/ffmpeg"}`), 0o644); err != nil {
			t.Fatal(err)
		}
		cfg, err := LoadUserConfig(p)
		if err != nil {
			t.Fatalf("LoadUserConfig: %v", err)
		}
		if cfg.WhisperCLIPath != "/x/whisper-cli" || cfg.FFmpegPath != "/x/ffmpeg" {
			t.Errorf("unexpected config: %+v", cfg)
		}
	})

	t.Run("broken JSON is an error", func(t *testing.T) {
		p := filepath.Join(dir, "broken.json")
		if err := os.WriteFile(p, []byte(`{`), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadUserConfig(p); err == nil {
			t.Error("broken JSON must return an error")
		}
	})

	t.Run("oversized file is rejected without loading it all", func(t *testing.T) {
		p := filepath.Join(dir, "huge.json")
		f, err := os.Create(p)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.Truncate(maxUserConfigBytes + 1); err != nil {
			t.Fatal(err)
		}
		f.Close()
		if _, err := LoadUserConfig(p); err == nil {
			t.Error("oversized config must return an error")
		}
	})

	t.Run("directory is rejected", func(t *testing.T) {
		p := filepath.Join(dir, "as-dir")
		if err := os.Mkdir(p, 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadUserConfig(p); err == nil {
			t.Error("non-regular file must return an error")
		}
	})
}
