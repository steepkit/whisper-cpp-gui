package model

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func cleanupOldPartials(directory string, manifest []manifestEntry, now time.Time, maxAge time.Duration) error {
	directoryEntries, err := os.ReadDir(directory)
	if err != nil {
		return fmt.Errorf("read model directory for partial cleanup: %w", err)
	}
	cutoff := now.Add(-maxAge)
	for _, directoryEntry := range directoryEntries {
		filename, owned := ownedPartialModel(directoryEntry.Name(), manifest)
		if !owned || directoryEntry.Type()&os.ModeSymlink != 0 {
			continue
		}
		lock, err := tryModelLock(directory, filename, true)
		if errors.Is(err, ErrModelBusy) {
			continue
		}
		if err != nil {
			return fmt.Errorf("lock model for partial cleanup: %w", err)
		}
		path := filepath.Join(directory, directoryEntry.Name())
		info, inspectErr := os.Lstat(path)
		if inspectErr == nil && info.Mode().IsRegular() && info.ModTime().Before(cutoff) {
			inspectErr = os.Remove(path)
		}
		if errors.Is(inspectErr, os.ErrNotExist) {
			inspectErr = nil
		}
		closeErr := lock.close()
		if inspectErr != nil {
			return fmt.Errorf("clean stale model partial: %w", inspectErr)
		}
		if closeErr != nil {
			return fmt.Errorf("unlock model after partial cleanup: %w", closeErr)
		}
	}
	return nil
}

func ownedPartialName(name string, entries []manifestEntry) bool {
	_, ok := ownedPartialModel(name, entries)
	return ok
}

func ownedPartialModel(name string, entries []manifestEntry) (string, bool) {
	for _, entry := range entries {
		prefix := "." + entry.descriptor.Filename + "."
		if strings.HasPrefix(name, prefix) && strings.HasSuffix(name, ".partial") && len(name) > len(prefix)+len(".partial") {
			return entry.descriptor.Filename, true
		}
	}
	return "", false
}
