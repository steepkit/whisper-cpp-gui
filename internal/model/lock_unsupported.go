//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd

package model

func tryModelLock(_, _ string, _ bool) (modelLock, error) {
	return nil, ErrCrossProcessLockUnsupported
}
