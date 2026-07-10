package job

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPrepareTempRootAndCreateWorkDir(t *testing.T) {
	root := filepath.Join(t.TempDir(), "nested", "jobs")
	prepared, err := PrepareTempRoot(root)
	if err != nil {
		t.Fatalf("PrepareTempRoot: %v", err)
	}
	info, err := os.Lstat(prepared)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Fatalf("prepared root mode = %v, want private directory", info.Mode())
	}
	dir, err := CreateWorkDir(prepared, "job-safe")
	if err != nil {
		t.Fatalf("CreateWorkDir: %v", err)
	}
	if dir != filepath.Join(prepared, "job-safe") {
		t.Fatalf("work dir = %q", dir)
	}
	info, err = os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Fatalf("work dir mode = %v, %v", info.Mode(), err)
	}
	if _, err := CreateWorkDir(prepared, "../escape"); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("invalid ID error = %v", err)
	}
}

func TestPrepareDefaultTempRootUsesPrivateUserCache(t *testing.T) {
	home := t.TempDir()
	cacheOverride := filepath.Join(home, "cache")
	sharedTemp := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", cacheOverride)
	t.Setenv("TMPDIR", sharedTemp)

	cacheDir, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		t.Fatal(err)
	}
	appRoot := filepath.Join(cacheDir, "whisper-cpp-gui")
	if err := os.Mkdir(appRoot, 0o777); err != nil {
		t.Fatal(err)
	}
	// A different user can reserve this predictable shared-temp name, but the
	// default job root must not inspect or depend on it.
	if err := os.Symlink(t.TempDir(), filepath.Join(sharedTemp, "whisper-cpp-gui")); err != nil {
		t.Logf("shared-temp symlink fixture unavailable: %v", err)
	}

	root, err := PrepareTempRoot("")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(appRoot, "jobs"); root != want {
		t.Fatalf("default temp root = %q", root)
	}
	for _, path := range []string{appRoot, root} {
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
			t.Fatalf("private temp directory %q mode = %v", path, info.Mode())
		}
	}
}

func TestPrepareTempRootRejectsFinalSymlink(t *testing.T) {
	target := t.TempDir()
	link := filepath.Join(t.TempDir(), "jobs-link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := PrepareTempRoot(link); err == nil {
		t.Fatal("PrepareTempRoot accepted a symlink root")
	}
}

func TestRecoveryMarkerRetentionAndCleanup(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC)
	createRecovery := func(id string, created time.Time) string {
		t.Helper()
		dir, err := CreateWorkDir(root, id)
		if err != nil {
			t.Fatalf("CreateWorkDir(%s): %v", id, err)
		}
		if err := MarkRecovery(dir, id, created); err != nil {
			t.Fatalf("MarkRecovery(%s): %v", id, err)
		}
		return dir
	}
	fresh := createRecovery("job-fresh", now.Add(-DefaultRecoveryTTL+time.Second))
	expired := createRecovery("job-expired", now.Add(-DefaultRecoveryTTL))

	unmarked, err := CreateWorkDir(root, "job-unmarked")
	if err != nil {
		t.Fatal(err)
	}
	if err := CleanupExpiredRecovery(root, now, DefaultRecoveryTTL); err != nil {
		t.Fatalf("CleanupExpiredRecovery: %v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("fresh recovery was removed: %v", err)
	}
	if _, err := os.Stat(expired); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expired recovery remains: %v", err)
	}
	if _, err := os.Stat(unmarked); err != nil {
		t.Fatalf("unmarked directory was removed: %v", err)
	}
	entries, err := os.ReadDir(fresh)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() != RecoveryMarkerName {
			t.Errorf("temporary marker artifact remains: %s", entry.Name())
		}
	}
}

func TestMalformedRecoveryIsConservativelyBounded(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC)
	createMalformed := func(id string, age time.Duration) string {
		t.Helper()
		dir, err := CreateWorkDir(root, id)
		if err != nil {
			t.Fatal(err)
		}
		marker := filepath.Join(dir, RecoveryMarkerName)
		if err := os.WriteFile(marker, []byte("not json"), 0o600); err != nil {
			t.Fatal(err)
		}
		timestamp := now.Add(-age)
		if err := os.Chtimes(marker, timestamp, timestamp); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	fresh := createMalformed("job-malformed-fresh", DefaultRecoveryTTL-time.Second)
	expired := createMalformed("job-malformed-old", DefaultRecoveryTTL)
	if err := CleanupExpiredRecovery(root, now, DefaultRecoveryTTL); err == nil {
		t.Fatal("fresh malformed marker did not report a warning error")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("fresh malformed recovery was removed: %v", err)
	}
	if _, err := os.Stat(expired); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old malformed recovery remains: %v", err)
	}
}

func TestRecoveryCleanupDoesNotFollowDirectorySymlink(t *testing.T) {
	root := t.TempDir()
	target := t.TempDir()
	protected := filepath.Join(target, "protected")
	if err := os.WriteFile(protected, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "job-link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := CleanupExpiredRecovery(root, time.Now(), DefaultRecoveryTTL); err != nil {
		t.Fatalf("CleanupExpiredRecovery: %v", err)
	}
	if _, err := os.Stat(protected); err != nil {
		t.Fatalf("symlink target was touched: %v", err)
	}
}

func TestRecoveryCleanupRejectsRootSymlink(t *testing.T) {
	if err := CleanupExpiredRecovery("", time.Now(), DefaultRecoveryTTL); err == nil {
		t.Fatal("CleanupExpiredRecovery accepted an empty root")
	}
	target := t.TempDir()
	protected, err := CreateWorkDir(target, "job-protected")
	if err != nil {
		t.Fatal(err)
	}
	if err := MarkRecovery(protected, "job-protected", time.Now().Add(-2*DefaultRecoveryTTL)); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "recovery-root-link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := CleanupExpiredRecovery(link, time.Now(), DefaultRecoveryTTL); err == nil {
		t.Fatal("CleanupExpiredRecovery accepted a symlink root")
	}
	if _, err := os.Stat(protected); err != nil {
		t.Fatalf("symlink target was modified: %v", err)
	}
}

func TestCleanupStaleWorkDirsRemovesOnlyAbandonedJobs(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	createUnmarked := func(id string, age time.Duration) string {
		t.Helper()
		dir, err := CreateWorkDir(root, id)
		if err != nil {
			t.Fatal(err)
		}
		stamp := now.Add(-age)
		if err := os.Chtimes(dir, stamp, stamp); err != nil {
			t.Fatal(err)
		}
		return dir
	}

	fresh := createUnmarked("job-stale-fresh", DefaultStaleWorkTTL-time.Second)
	boundary := createUnmarked("job-stale-boundary", DefaultStaleWorkTTL)
	old := createUnmarked("job-stale-old", DefaultStaleWorkTTL+time.Hour)
	unrelated := filepath.Join(root, "not-a-job!")
	if err := os.Mkdir(unrelated, 0o700); err != nil {
		t.Fatal(err)
	}
	stamp := now.Add(-2 * DefaultStaleWorkTTL)
	if err := os.Chtimes(unrelated, stamp, stamp); err != nil {
		t.Fatal(err)
	}

	recovery, err := CreateWorkDir(root, "job-recovery-fresh")
	if err != nil {
		t.Fatal(err)
	}
	if err := MarkRecovery(recovery, "job-recovery-fresh", now.Add(-DefaultRecoveryTTL+time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(recovery, stamp, stamp); err != nil {
		t.Fatal(err)
	}

	if err := CleanupStaleWorkDirs(root, now, DefaultStaleWorkTTL, DefaultRecoveryTTL); err != nil {
		t.Fatalf("CleanupStaleWorkDirs: %v", err)
	}
	for label, path := range map[string]string{
		"fresh work":     fresh,
		"unrelated dir":  unrelated,
		"fresh recovery": recovery,
	} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("%s was removed: %v", label, err)
		}
	}
	for label, path := range map[string]string{
		"boundary work": boundary,
		"old work":      old,
	} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s remains: %v", label, err)
		}
	}
}

func TestCleanupStaleWorkDirsValidatesTTLs(t *testing.T) {
	root := t.TempDir()
	if err := CleanupStaleWorkDirs(root, time.Now(), 0, DefaultRecoveryTTL); err == nil {
		t.Fatal("zero stale TTL was accepted")
	}
	if err := CleanupStaleWorkDirs(root, time.Now(), DefaultStaleWorkTTL, 0); err == nil {
		t.Fatal("zero recovery TTL was accepted")
	}
}
