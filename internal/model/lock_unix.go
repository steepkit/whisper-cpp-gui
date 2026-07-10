//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package model

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

type unixModelLock struct {
	file *os.File
}

func tryModelLock(directory, filename string, exclusive bool) (modelLock, error) {
	path := filepath.Join(directory, "."+filename+".lock")
	if info, err := os.Lstat(path); err == nil {
		if !safeLockFile(info) {
			return nil, fmt.Errorf("model lock is not a safe regular file")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect model lock: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open model lock: %w", err)
	}
	pathInfo, pathErr := os.Lstat(path)
	openedInfo, openedErr := file.Stat()
	if pathErr != nil || openedErr != nil || !safeLockFile(pathInfo) || !safeLockFile(openedInfo) || !os.SameFile(pathInfo, openedInfo) {
		_ = file.Close()
		return nil, fmt.Errorf("opened model lock is not the expected safe regular file")
	}
	operation := syscall.LOCK_SH | syscall.LOCK_NB
	if exclusive {
		operation = syscall.LOCK_EX | syscall.LOCK_NB
	}
	if err := syscall.Flock(int(file.Fd()), operation); err != nil {
		_ = file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, ErrModelBusy
		}
		return nil, fmt.Errorf("lock model: %w", err)
	}
	return &unixModelLock{file: file}, nil
}

func safeLockFile(info os.FileInfo) bool {
	return info != nil && info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 && info.Mode().Perm()&0o022 == 0
}

func (l *unixModelLock) close() error {
	if l == nil || l.file == nil {
		return nil
	}
	err := syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	if closeErr := l.file.Close(); err == nil {
		err = closeErr
	}
	l.file = nil
	return err
}
