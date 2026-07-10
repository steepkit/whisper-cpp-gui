package model

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var errRedirectPolicy = errors.New("download redirect rejected")

func (m *Manager) download(ctx context.Context, r *record) ErrorCode {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, sourceURL(r.entry).String(), nil)
	if err != nil {
		return ErrorCodeNetwork
	}
	response, err := m.client.Do(request)
	if err != nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		if errors.Is(ctx.Err(), context.Canceled) {
			return ErrorCodeCancelled
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return ErrorCodeTimeout
		}
		if errors.Is(err, errRedirectPolicy) {
			return ErrorCodeRedirectPolicy
		}
		return ErrorCodeNetwork
	}
	if response.StatusCode != http.StatusOK {
		_ = response.Body.Close()
		return ErrorCodeHTTPStatus
	}
	descriptor := r.entry.descriptor
	if response.ContentLength > descriptor.Size {
		_ = response.Body.Close()
		return ErrorCodeContentLength
	}
	body := newIdleReadCloser(response.Body, m.config.downloadIdleTimeout)
	defer body.Close()

	partial, err := os.CreateTemp(m.directory, "."+descriptor.Filename+".*.partial")
	if err != nil {
		return ErrorCodeStorage
	}
	partialPath := partial.Name()
	published := false
	defer func() {
		if partial != nil {
			_ = partial.Close()
		}
		if !published {
			_ = os.Remove(partialPath)
		}
	}()
	if err := partial.Chmod(0o600); err != nil {
		return ErrorCodeStorage
	}

	hash := sha256.New()
	progress := &progressWriter{
		writer:   io.MultiWriter(partial, hash),
		expected: descriptor.Size,
		step:     m.config.progressStepBytes,
		onWrite: func(total int64) {
			m.progress(r, total)
		},
	}
	limited := io.LimitReader(body, descriptor.Size+1)
	written, err := io.CopyBuffer(progress, limited, make([]byte, 32*1024))
	// Closing an HTTP body for timeout/cancellation may surface as a clean EOF.
	// Classify the controlling context/idle state before interpreting a short
	// successful copy as a size mismatch.
	if err != nil || ctx.Err() != nil || body.TimedOut() {
		return classifyCopyError(ctx, body.TimedOut(), progress.writeErr)
	}
	if written > descriptor.Size {
		return ErrorCodeSizeMismatch
	}
	if written != descriptor.Size {
		return ErrorCodeSizeMismatch
	}
	if hex.EncodeToString(hash.Sum(nil)) != descriptor.SHA256 {
		return ErrorCodeChecksum
	}
	if err := partial.Sync(); err != nil {
		return ErrorCodeStorage
	}
	if err := partial.Close(); err != nil {
		partial = nil
		return ErrorCodeStorage
	}
	partial = nil
	if err := os.Rename(partialPath, m.modelPath(r)); err != nil {
		return ErrorCodeStorage
	}
	published = true
	return ErrorCodeNone
}

func classifyCopyError(ctx context.Context, idleTimedOut bool, writeErr error) ErrorCode {
	if errors.Is(ctx.Err(), context.Canceled) {
		return ErrorCodeCancelled
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || idleTimedOut {
		return ErrorCodeTimeout
	}
	if writeErr != nil {
		return ErrorCodeStorage
	}
	return ErrorCodeNetwork
}

type progressWriter struct {
	writer       io.Writer
	written      int64
	expected     int64
	step         int64
	lastNotified int64
	writeErr     error
	onWrite      func(total int64)
}

func (w *progressWriter) Write(data []byte) (int, error) {
	n, err := w.writer.Write(data)
	w.written += int64(n)
	if err != nil {
		w.writeErr = err
	}
	if n > 0 && (w.written-w.lastNotified >= w.step || w.written >= w.expected) {
		total := w.written
		if total > w.expected {
			total = w.expected
		}
		if total > w.lastNotified {
			w.lastNotified = total
			w.onWrite(total)
		}
	}
	return n, err
}

type idleReadCloser struct {
	source    io.ReadCloser
	activity  chan struct{}
	done      chan struct{}
	closeOnce sync.Once
	stopOnce  sync.Once
	timedOut  atomic.Bool
}

func newIdleReadCloser(source io.ReadCloser, timeout time.Duration) *idleReadCloser {
	reader := &idleReadCloser{
		source:   source,
		activity: make(chan struct{}, 1),
		done:     make(chan struct{}),
	}
	go reader.watch(timeout)
	return reader
}

func (r *idleReadCloser) Read(data []byte) (int, error) {
	n, err := r.source.Read(data)
	if n > 0 {
		select {
		case r.activity <- struct{}{}:
		default:
		}
	}
	return n, err
}

func (r *idleReadCloser) Close() error {
	r.stopOnce.Do(func() { close(r.done) })
	var result error
	r.closeOnce.Do(func() { result = r.source.Close() })
	return result
}

func (r *idleReadCloser) TimedOut() bool {
	return r.timedOut.Load()
}

func (r *idleReadCloser) watch(timeout time.Duration) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case <-r.activity:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(timeout)
		case <-timer.C:
			r.timedOut.Store(true)
			r.closeOnce.Do(func() { _ = r.source.Close() })
			return
		case <-r.done:
			return
		}
	}
}

func validateDownloadURL(candidate *url.URL) error {
	if candidate == nil || candidate.Scheme != "https" || candidate.User != nil || candidate.Port() != "" {
		return errRedirectPolicy
	}
	host := strings.ToLower(candidate.Hostname())
	if host == "huggingface.co" || host == "cdn-lfs.huggingface.co" ||
		strings.HasSuffix(host, ".cdn.hf.co") || strings.HasSuffix(host, ".xethub.hf.co") {
		return nil
	}
	return fmt.Errorf("%w: host is not allowed", errRedirectPolicy)
}
