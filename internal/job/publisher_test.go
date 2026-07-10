package job

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
)

func TestSanitizeOutputStem(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{input: "lecture.mp3", want: "lecture"},
		{input: "../../escape.wav", want: "escape"},
		{input: `C:\\secret\\class.m4a`, want: "class"},
		{input: "講義 第1回.mov", want: "講義 第1回"},
		{input: "bad\x00:name?.wav", want: "bad-name"},
		{input: ".mp3", want: "transcription"},
		{input: "---", want: "transcription"},
	}
	for _, test := range tests {
		t.Run(test.input, func(t *testing.T) {
			if got := sanitizeOutputStem(test.input); got != test.want {
				t.Errorf("sanitizeOutputStem(%q) = %q, want %q", test.input, got, test.want)
			}
		})
	}
	long := strings.Repeat("界", 100)
	if got := sanitizeOutputStem(long); len(got) > maxOutputStemBytes || !strings.HasPrefix(long, got) {
		t.Fatalf("long sanitized stem is not a bounded UTF-8 prefix: %q (%d bytes)", got, len(got))
	}
}

func TestPublisherPublishesAllowlistedOutputsAndHandlesCollisions(t *testing.T) {
	home := t.TempDir()
	if err := os.Mkdir(filepath.Join(home, "Downloads"), 0o755); err != nil {
		t.Fatal(err)
	}
	publisher := NewPublisher(PublisherConfig{HomeDir: func() (string, error) { return home, nil }})

	first := publishFixture(t, publisher, "job-first", "lecture.mp3", []string{"txt", "srt"})
	second := publishFixture(t, publisher, "job-second", "lecture.mp3", []string{"txt"})
	if filepath.Base(first.FinalOutputDir) != "lecture" {
		t.Fatalf("first output dir = %q", first.FinalOutputDir)
	}
	if filepath.Base(second.FinalOutputDir) != "lecture-2" {
		t.Fatalf("second output dir = %q", second.FinalOutputDir)
	}
	if strings.Join(first.OutputFiles, ",") != "transcript.txt,transcript.srt" {
		t.Fatalf("output files = %v", first.OutputFiles)
	}
	data, err := os.ReadFile(filepath.Join(first.FinalOutputDir, "transcript.txt"))
	if err != nil || string(data) != "txt content" {
		t.Fatalf("published txt = %q, %v", data, err)
	}
	entries, err := os.ReadDir(filepath.Join(home, "Downloads", "whisper-cpp-gui"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".reserve") || strings.HasSuffix(entry.Name(), ".partial") {
			t.Errorf("publish artifact remains: %s", entry.Name())
		}
	}
}

func TestPublisherFallsBackWhenDownloadsIsMissing(t *testing.T) {
	home := t.TempDir()
	publisher := NewPublisher(PublisherConfig{HomeDir: func() (string, error) { return home, nil }})
	result := publishFixture(t, publisher, "job-fallback", "audio.wav", []string{"vtt"})
	wantParent := filepath.Join(home, "whisper-cpp-gui")
	if filepath.Dir(result.FinalOutputDir) != wantParent {
		t.Fatalf("output parent = %q, want %q", filepath.Dir(result.FinalOutputDir), wantParent)
	}
	info, err := os.Stat(wantParent)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("output root mode = %v", info.Mode())
	}
}

func TestPublisherRollbackLeavesSourcesAndNoArtifacts(t *testing.T) {
	home := t.TempDir()
	if err := os.Mkdir(filepath.Join(home, "Downloads"), 0o755); err != nil {
		t.Fatal(err)
	}
	calls := 0
	publisher := NewPublisher(PublisherConfig{
		HomeDir: func() (string, error) { return home, nil },
		CopyFile: func(ctx context.Context, source, destination string) error {
			calls++
			if calls == 2 {
				return errors.New("injected copy failure")
			}
			return copyRegularFile(ctx, source, destination)
		},
	})
	request, sources := publisherFixture(t, "job-rollback", "class.wav", []string{"txt", "srt"})
	if _, err := publisher.Publish(context.Background(), request); err == nil {
		t.Fatal("Publish succeeded, want injected error")
	}
	for _, source := range sources {
		if _, err := os.Stat(source); err != nil {
			t.Errorf("source was lost after rollback: %s: %v", source, err)
		}
	}
	root := filepath.Join(home, "Downloads", "whisper-cpp-gui")
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("rollback left output artifacts: %v", entryNames(entries))
	}
}

func TestPublisherRejectsSymlinkSourcesAndDownloads(t *testing.T) {
	home := t.TempDir()
	target := t.TempDir()
	if err := os.Symlink(target, filepath.Join(home, "Downloads")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	publisher := NewPublisher(PublisherConfig{HomeDir: func() (string, error) { return home, nil }})
	request, _ := publisherFixture(t, "job-download-link", "class.wav", []string{"txt"})
	if _, err := publisher.Publish(context.Background(), request); err == nil {
		t.Fatal("Publish accepted a symlink Downloads directory")
	}

	if err := os.Remove(filepath.Join(home, "Downloads")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(home, "Downloads"), 0o755); err != nil {
		t.Fatal(err)
	}
	request, sources := publisherFixture(t, "job-source-link", "class.wav", []string{"txt"})
	if err := os.Remove(sources[0]); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, sources[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := publisher.Publish(context.Background(), request); err == nil {
		t.Fatal("Publish accepted a symlink generated output")
	}
}

func TestPublisherConcurrentCollisionsAreUnique(t *testing.T) {
	home := t.TempDir()
	if err := os.Mkdir(filepath.Join(home, "Downloads"), 0o755); err != nil {
		t.Fatal(err)
	}
	publisher := NewPublisher(PublisherConfig{HomeDir: func() (string, error) { return home, nil }})
	const count = 12
	results := make(chan RunResult, count)
	errs := make(chan error, count)
	var group sync.WaitGroup
	for index := range count {
		request, _ := publisherFixture(t, fmt.Sprintf("job-%d", index), "same.wav", []string{"txt"})
		group.Add(1)
		go func() {
			defer group.Done()
			result, err := publisher.Publish(context.Background(), request)
			if err != nil {
				errs <- err
				return
			}
			results <- result
		}()
	}
	group.Wait()
	close(errs)
	close(results)
	for err := range errs {
		t.Errorf("Publish: %v", err)
	}
	seen := make(map[string]struct{})
	for result := range results {
		seen[result.FinalOutputDir] = struct{}{}
	}
	if len(seen) != count {
		t.Fatalf("unique output directories = %d, want %d", len(seen), count)
	}
	basenames := make([]string, 0, len(seen))
	for directory := range seen {
		basenames = append(basenames, filepath.Base(directory))
	}
	sort.Strings(basenames)
	if basenames[0] != "same" {
		t.Errorf("collision names = %v", basenames)
	}
}

func publishFixture(t *testing.T, publisher *Publisher, jobID, filename string, formats []string) RunResult {
	t.Helper()
	request, _ := publisherFixture(t, jobID, filename, formats)
	result, err := publisher.Publish(context.Background(), request)
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	return result
}

func publisherFixture(t *testing.T, jobID, filename string, formats []string) (PublishRequest, []string) {
	t.Helper()
	workDir := filepath.Join(t.TempDir(), jobID)
	generated := filepath.Join(workDir, "transcribe")
	if err := os.MkdirAll(generated, 0o700); err != nil {
		t.Fatal(err)
	}
	prefix := filepath.Join(generated, "out")
	sources := make([]string, 0, len(formats))
	for _, format := range formats {
		path := prefix + "." + format
		if err := os.WriteFile(path, []byte(format+" content"), 0o600); err != nil {
			t.Fatal(err)
		}
		sources = append(sources, path)
	}
	return PublishRequest{
		JobID:            jobID,
		WorkDir:          workDir,
		OutputPrefix:     prefix,
		OriginalFilename: filename,
		Formats:          formats,
	}, sources
}

func entryNames(entries []os.DirEntry) []string {
	names := make([]string, len(entries))
	for index, entry := range entries {
		names[index] = entry.Name()
	}
	return names
}
