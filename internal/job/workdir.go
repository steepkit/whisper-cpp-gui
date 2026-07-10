package job

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

const (
	RecoveryMarkerName = ".recovery.json"
	DefaultRecoveryTTL = 7 * 24 * time.Hour
	// A second app instance must not remove work waiting behind four 12-hour
	// jobs in the serial queue. Seventy-two hours covers that 60-hour maximum
	// with margin until the application has a single-instance lock.
	DefaultStaleWorkTTL = 72 * time.Hour
	maxRecoveryBytes    = 1024
)

type recoveryMarker struct {
	Version   int       `json:"version"`
	JobID     string    `json:"job_id"`
	CreatedAt time.Time `json:"created_at"`
}

// PrepareTempRoot creates and resolves the private application-owned job root.
func PrepareTempRoot(root string) (string, error) {
	if root == "" {
		defaultRoot, err := DefaultTempRoot()
		if err != nil {
			return "", err
		}
		appRoot, err := preparePrivateDirectory(filepath.Dir(defaultRoot), true)
		if err != nil {
			return "", fmt.Errorf("prepare application cache root: %w", err)
		}
		return preparePrivateDirectory(filepath.Join(appRoot, "jobs"), false)
	}
	return preparePrivateDirectory(root, true)
}

func preparePrivateDirectory(path string, createParents bool) (string, error) {
	if path == "" {
		return "", fmt.Errorf("job temp root must not be empty")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve job temp root: %w", err)
	}
	if createParents {
		err = os.MkdirAll(absolute, 0o700)
	} else {
		err = os.Mkdir(absolute, 0o700)
		if errors.Is(err, fs.ErrExist) {
			err = nil
		}
	}
	if err != nil {
		return "", fmt.Errorf("create job temp root: %w", err)
	}
	originalInfo, err := os.Lstat(absolute)
	if err != nil {
		return "", fmt.Errorf("inspect job temp root: %w", err)
	}
	if !originalInfo.IsDir() || originalInfo.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("job temp root is not a real directory")
	}
	if err := os.Chmod(absolute, 0o700); err != nil {
		return "", fmt.Errorf("restrict job temp root permissions: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("resolve job temp root symlinks: %w", err)
	}
	info, err := os.Lstat(resolved)
	if err != nil {
		return "", fmt.Errorf("inspect job temp root: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("job temp root is not a real directory")
	}
	return filepath.Clean(resolved), nil
}

// CreateWorkDir creates the only work-directory shape accepted by Manager.
func CreateWorkDir(root, id string) (string, error) {
	if !validJobID(id) {
		return "", fmt.Errorf("%w: invalid job ID", ErrInvalidRequest)
	}
	resolvedRoot, err := PrepareTempRoot(root)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(resolvedRoot, id)
	if err := os.Mkdir(dir, 0o700); err != nil {
		return "", fmt.Errorf("create job work directory: %w", err)
	}
	return dir, nil
}

// MarkRecovery marks a publish-failed work directory for bounded retention.
func MarkRecovery(workDir, jobID string, createdAt time.Time) error {
	if !validJobID(jobID) || filepath.Base(workDir) != jobID {
		return fmt.Errorf("invalid recovery job directory")
	}
	markerPath := filepath.Join(workDir, RecoveryMarkerName)
	if _, err := os.Lstat(markerPath); err == nil {
		return fmt.Errorf("recovery marker already exists")
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("inspect recovery marker: %w", err)
	}
	file, err := os.CreateTemp(workDir, ".recovery-*.tmp")
	if err != nil {
		return fmt.Errorf("create recovery marker temp file: %w", err)
	}
	tempPath := file.Name()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tempPath)
		}
	}()
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return fmt.Errorf("restrict recovery marker permissions: %w", err)
	}
	if err := json.NewEncoder(file).Encode(recoveryMarker{Version: 1, JobID: jobID, CreatedAt: createdAt.UTC()}); err != nil {
		_ = file.Close()
		return fmt.Errorf("write recovery marker: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync recovery marker: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close recovery marker: %w", err)
	}
	if err := os.Rename(tempPath, markerPath); err != nil {
		return fmt.Errorf("publish recovery marker: %w", err)
	}
	committed = true
	return nil
}

// CleanupExpiredRecovery removes only direct child directories carrying a
// valid recovery marker older than ttl. Symlinks and unrelated directories
// are never followed or removed.
func CleanupExpiredRecovery(root string, now time.Time, ttl time.Duration) error {
	return cleanupStaleWorkDirs(root, now, 0, ttl)
}

// CleanupStaleWorkDirs removes abandoned direct job directories after a
// conservative TTL and applies the longer recovery TTL to publish failures.
// Symlinks, invalid job directory names, and unrelated entries are skipped.
func CleanupStaleWorkDirs(root string, now time.Time, staleTTL, recoveryTTL time.Duration) error {
	if staleTTL <= 0 {
		return fmt.Errorf("stale work TTL must be positive")
	}
	return cleanupStaleWorkDirs(root, now, staleTTL, recoveryTTL)
}

func cleanupStaleWorkDirs(root string, now time.Time, staleTTL, recoveryTTL time.Duration) error {
	if recoveryTTL <= 0 {
		return fmt.Errorf("recovery TTL must be positive")
	}
	if root == "" {
		return fmt.Errorf("recovery root must not be empty")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("resolve recovery root: %w", err)
	}
	rootInfo, err := os.Lstat(absolute)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect recovery root: %w", err)
	}
	if !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("recovery root is not a real directory")
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return fmt.Errorf("resolve recovery root symlinks: %w", err)
	}
	entries, err := os.ReadDir(resolved)
	if err != nil {
		return fmt.Errorf("read recovery root: %w", err)
	}

	var cleanupErrors []error
	for _, entry := range entries {
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || !validJobID(entry.Name()) {
			continue
		}
		dir := filepath.Join(resolved, entry.Name())
		markerPath := filepath.Join(dir, RecoveryMarkerName)
		marker, err := readRecoveryMarker(markerPath)
		if err == nil && marker.JobID != entry.Name() {
			err = fmt.Errorf("recovery marker job ID does not match its directory")
		}
		if errors.Is(err, fs.ErrNotExist) {
			if staleTTL == 0 {
				continue
			}
			info, statErr := os.Lstat(dir)
			if statErr != nil {
				cleanupErrors = append(cleanupErrors, fmt.Errorf("inspect stale work %s: %w", entry.Name(), statErr))
				continue
			}
			if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.ModTime().After(now.Add(-staleTTL)) {
				continue
			}
			if removeErr := os.RemoveAll(dir); removeErr != nil {
				cleanupErrors = append(cleanupErrors, fmt.Errorf("remove stale work %s: %w", entry.Name(), removeErr))
			}
			continue
		}
		if err != nil {
			info, statErr := os.Lstat(markerPath)
			if statErr == nil && info.Mode().IsRegular() && !info.ModTime().After(now.Add(-recoveryTTL)) {
				if removeErr := os.RemoveAll(dir); removeErr != nil {
					cleanupErrors = append(cleanupErrors, fmt.Errorf("remove malformed recovery %s: %w", entry.Name(), removeErr))
				}
				continue
			}
			cleanupErrors = append(cleanupErrors, fmt.Errorf("inspect recovery %s: %w", entry.Name(), err))
			continue
		}
		if marker.CreatedAt.After(now.Add(-recoveryTTL)) {
			continue
		}
		if err := os.RemoveAll(dir); err != nil {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("remove recovery %s: %w", entry.Name(), err))
		}
	}
	return errors.Join(cleanupErrors...)
}

func readRecoveryMarker(path string) (recoveryMarker, error) {
	var marker recoveryMarker
	info, err := os.Lstat(path)
	if err != nil {
		return marker, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > maxRecoveryBytes {
		return marker, fmt.Errorf("invalid recovery marker")
	}
	file, err := os.Open(path)
	if err != nil {
		return marker, err
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, maxRecoveryBytes+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&marker); err != nil {
		return marker, err
	}
	if marker.Version != 1 || !validJobID(marker.JobID) || marker.CreatedAt.IsZero() {
		return marker, fmt.Errorf("recovery marker fields are invalid")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return marker, fmt.Errorf("recovery marker has trailing data")
	}
	return marker, nil
}
