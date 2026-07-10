package model

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type record struct {
	entry           manifestEntry
	state           State
	bytesDownloaded int64
	errorCode       ErrorCode
	leases          int
}

// Manager owns model downloads, subscriptions, validation, and leases.
type Manager struct {
	directory string
	client    *http.Client
	config    normalizedConfig
	validate  func(string, Descriptor) State

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	checks sync.WaitGroup

	mu             sync.Mutex
	closed         bool
	entries        []manifestEntry
	records        map[string]*record
	subscribers    map[string]map[uint64]chan Event
	nextSubscriber uint64
	closeOnce      sync.Once
}

type normalizedConfig struct {
	subscriberCapacity  int
	subscriberLimit     int
	downloadTimeout     time.Duration
	downloadIdleTimeout time.Duration
	progressStepBytes   int64
}

// NewManager inspects existing files and removes only owned partials older
// than the configured age. Full checksum validation occurs before use.
func NewManager(config Config) (*Manager, error) {
	if config.SubscriberCapacity < 0 || config.SubscriberLimit < 0 || config.DownloadTimeout < 0 ||
		config.DownloadIdleTimeout < 0 || config.ProgressStepBytes < 0 {
		return nil, fmt.Errorf("model manager limits must not be negative")
	}
	directory := config.Directory
	if directory == "" {
		var err error
		directory, err = Directory()
		if err != nil {
			return nil, err
		}
	}
	if !filepath.IsAbs(directory) {
		return nil, fmt.Errorf("model directory must be absolute")
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, fmt.Errorf("create model directory: %w", err)
	}
	directoryInfo, err := os.Lstat(directory)
	if err != nil {
		return nil, fmt.Errorf("inspect model directory: %w", err)
	}
	if directoryInfo.Mode()&os.ModeSymlink != 0 || !directoryInfo.IsDir() {
		return nil, fmt.Errorf("model directory is not a physical directory")
	}
	if directoryInfo.Mode().Perm()&0o077 != 0 {
		if err := os.Chmod(directory, 0o700); err != nil {
			return nil, fmt.Errorf("make model directory private: %w", err)
		}
		directoryInfo, err = os.Lstat(directory)
		if err != nil || !directoryInfo.IsDir() || directoryInfo.Mode()&os.ModeSymlink != 0 || directoryInfo.Mode().Perm()&0o077 != 0 {
			return nil, fmt.Errorf("model directory did not remain a private physical directory")
		}
	}
	now := time.Now
	if config.now != nil {
		now = config.now
	}
	entries := approvedManifest[:]
	if config.manifestOverride != nil {
		entries = config.manifestOverride
	}
	entries = append([]manifestEntry(nil), entries...)
	if err := validateManifest(entries); err != nil {
		return nil, err
	}
	if err := cleanupOldPartials(directory, entries, now(), DefaultPartialMaxAge); err != nil {
		return nil, err
	}

	capacity := config.SubscriberCapacity
	if capacity == 0 {
		capacity = DefaultSubscriberCapacity
	}
	limit := config.SubscriberLimit
	if limit == 0 {
		limit = DefaultSubscriberLimit
	}
	downloadTimeout := config.DownloadTimeout
	if downloadTimeout == 0 {
		downloadTimeout = DefaultDownloadTimeout
	}
	downloadIdleTimeout := config.DownloadIdleTimeout
	if downloadIdleTimeout == 0 {
		downloadIdleTimeout = DefaultDownloadIdleTimeout
	}
	progressStepBytes := config.ProgressStepBytes
	if progressStepBytes == 0 {
		progressStepBytes = DefaultProgressStepBytes
	}
	ctx, cancel := context.WithCancel(context.Background())
	validate := validateModelFile
	if config.validateFile != nil {
		validate = config.validateFile
	}
	m := &Manager{
		directory: directory,
		client:    downloadClient(config.Client),
		validate:  validate,
		config: normalizedConfig{
			subscriberCapacity:  capacity,
			subscriberLimit:     limit,
			downloadTimeout:     downloadTimeout,
			downloadIdleTimeout: downloadIdleTimeout,
			progressStepBytes:   progressStepBytes,
		},
		ctx:         ctx,
		cancel:      cancel,
		entries:     entries,
		records:     make(map[string]*record, len(entries)),
		subscribers: make(map[string]map[uint64]chan Event),
	}
	for _, entry := range entries {
		r := &record{entry: entry}
		state, err := inspectModelFile(filepath.Join(directory, entry.descriptor.Filename), entry.descriptor)
		if err != nil {
			cancel()
			return nil, fmt.Errorf("inspect model %q: %w", entry.descriptor.Name, err)
		}
		r.state = state
		if state == StateDownloaded {
			r.bytesDownloaded = entry.descriptor.Size
		}
		m.records[entry.descriptor.Name] = r
	}
	return m, nil
}

// Snapshots returns an ordered copy after a cheap metadata refresh. Full
// checksum validation happens before Download short-circuits and before use.
func (m *Manager) Snapshots() ([]Snapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, ErrManagerClosed
	}
	result := make([]Snapshot, 0, len(m.entries))
	for _, entry := range m.entries {
		r := m.records[entry.descriptor.Name]
		if err := m.refreshLocked(r); err != nil {
			return nil, err
		}
		result = append(result, snapshotOf(r))
	}
	return result, nil
}

// Snapshot returns one model after the same cheap metadata refresh.
func (m *Manager) Snapshot(name string) (Snapshot, error) {
	if _, err := lookupEntry(name); err != nil {
		return Snapshot{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return Snapshot{}, ErrManagerClosed
	}
	r := m.records[name]
	if err := m.refreshLocked(r); err != nil {
		return Snapshot{}, err
	}
	return snapshotOf(r), nil
}

// Download starts one background download. The Manager context owns its
// lifetime, and Close cancels and waits for it.
func (m *Manager) Download(name string) (Snapshot, error) {
	if _, err := lookupEntry(name); err != nil {
		return Snapshot{}, err
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return Snapshot{}, ErrManagerClosed
	}
	r := m.records[name]
	if r.state == StateDownloading {
		m.mu.Unlock()
		return Snapshot{}, ErrDownloadInProgress
	}
	lock, err := tryModelLock(m.directory, r.entry.descriptor.Filename, true)
	if err != nil {
		m.mu.Unlock()
		if errors.Is(err, ErrModelBusy) {
			return Snapshot{}, ErrModelBusy
		}
		return Snapshot{}, err
	}
	m.checks.Add(1)
	m.mu.Unlock()
	defer m.checks.Done()

	state := m.validate(m.modelPath(r), r.entry.descriptor)
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		_ = lock.close()
		return Snapshot{}, ErrManagerClosed
	}
	if state == StateDownloaded {
		r.state = state
		r.bytesDownloaded = r.entry.descriptor.Size
		r.errorCode = ErrorCodeNone
		snapshot := snapshotOf(r)
		m.mu.Unlock()
		_ = lock.close()
		return snapshot, ErrAlreadyDownloaded
	}
	if state == StateInvalid {
		if err := os.Remove(m.modelPath(r)); err != nil && !errors.Is(err, os.ErrNotExist) {
			m.mu.Unlock()
			_ = lock.close()
			return Snapshot{}, fmt.Errorf("remove invalid model before download: %w", err)
		}
	}
	r.state = StateDownloading
	r.bytesDownloaded = 0
	r.errorCode = ErrorCodeNone
	snapshot := snapshotOf(r)
	m.publishLocked(name, Event{Type: EventState, Name: name, State: StateDownloading})
	m.wg.Add(1)
	m.mu.Unlock()

	go m.runDownload(r, lock)
	return snapshot, nil
}

// Delete removes a model only while no local lease or cooperating process is
// using or publishing it.
func (m *Manager) Delete(name string) error {
	if _, err := lookupEntry(name); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return ErrManagerClosed
	}
	r := m.records[name]
	if r.state == StateDownloading {
		return ErrDownloadInProgress
	}
	if r.leases > 0 {
		return ErrModelInUse
	}
	lock, err := tryModelLock(m.directory, r.entry.descriptor.Filename, true)
	if err != nil {
		if errors.Is(err, ErrModelBusy) {
			return ErrModelInUse
		}
		return err
	}
	defer lock.close()
	if err := os.Remove(m.modelPath(r)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("delete model: %w", err)
	}
	r.state = StateMissing
	r.bytesDownloaded = 0
	r.errorCode = ErrorCodeNone
	return nil
}

// Acquire obtains a shared cross-process lease and revalidates the model while
// deletion and publication are excluded.
func (m *Manager) Acquire(name string) (*Lease, error) {
	if _, err := lookupEntry(name); err != nil {
		return nil, err
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, ErrManagerClosed
	}
	r := m.records[name]
	if r.state == StateDownloading {
		m.mu.Unlock()
		return nil, ErrModelUnavailable
	}
	lock, err := tryModelLock(m.directory, r.entry.descriptor.Filename, false)
	if err != nil {
		m.mu.Unlock()
		if errors.Is(err, ErrModelBusy) {
			return nil, ErrModelUnavailable
		}
		return nil, err
	}
	m.checks.Add(1)
	m.mu.Unlock()
	defer m.checks.Done()

	state := m.validate(m.modelPath(r), r.entry.descriptor)
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		_ = lock.close()
		return nil, ErrManagerClosed
	}
	r.state = state
	r.errorCode = ErrorCodeNone
	if state == StateDownloaded {
		r.bytesDownloaded = r.entry.descriptor.Size
	} else {
		r.bytesDownloaded = 0
	}
	if state != StateDownloaded {
		m.mu.Unlock()
		_ = lock.close()
		return nil, ErrModelUnavailable
	}
	r.leases++
	lease := &Lease{manager: m, name: name, path: m.modelPath(r), lock: lock}
	m.mu.Unlock()
	return lease, nil
}

// Subscribe atomically captures a Snapshot and registers for subsequent
// bounded best-effort events. Terminal snapshots return a closed channel.
func (m *Manager) Subscribe(name string) (Snapshot, <-chan Event, func(), error) {
	if _, err := lookupEntry(name); err != nil {
		return Snapshot{}, nil, nil, err
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return Snapshot{}, nil, nil, ErrManagerClosed
	}
	r := m.records[name]
	if err := m.refreshLocked(r); err != nil {
		m.mu.Unlock()
		return Snapshot{}, nil, nil, err
	}
	snapshot := snapshotOf(r)
	ch := make(chan Event, m.config.subscriberCapacity)
	if r.state != StateDownloading {
		close(ch)
		m.mu.Unlock()
		return snapshot, ch, func() {}, nil
	}
	if len(m.subscribers[name]) >= m.config.subscriberLimit {
		m.mu.Unlock()
		return Snapshot{}, nil, nil, ErrSubscriberLimit
	}
	m.nextSubscriber++
	id := m.nextSubscriber
	if m.subscribers[name] == nil {
		m.subscribers[name] = make(map[uint64]chan Event)
	}
	m.subscribers[name][id] = ch
	m.mu.Unlock()

	var once sync.Once
	unsubscribe := func() {
		once.Do(func() {
			m.mu.Lock()
			defer m.mu.Unlock()
			modelSubscribers := m.subscribers[name]
			current, exists := modelSubscribers[id]
			if !exists || current != ch {
				return
			}
			delete(modelSubscribers, id)
			close(ch)
			if len(modelSubscribers) == 0 {
				delete(m.subscribers, name)
			}
		})
	}
	return snapshot, ch, unsubscribe, nil
}

// Close cancels all downloads, waits for their owned partial cleanup, and
// closes subscriptions. It is safe to call repeatedly.
func (m *Manager) Close() {
	m.closeOnce.Do(func() {
		m.mu.Lock()
		m.closed = true
		m.cancel()
		m.mu.Unlock()

		m.checks.Wait()
		m.wg.Wait()

		m.mu.Lock()
		m.closeAllSubscribersLocked()
		m.mu.Unlock()
		if transport, ok := m.client.Transport.(interface{ CloseIdleConnections() }); ok {
			transport.CloseIdleConnections()
		}
	})
}

func (m *Manager) runDownload(r *record, lock modelLock) {
	defer m.wg.Done()
	downloadContext, cancel := context.WithTimeout(m.ctx, m.config.downloadTimeout)
	code := m.download(downloadContext, r)
	cancel()
	finalState := m.validate(m.modelPath(r), r.entry.descriptor)
	_ = lock.close()

	m.mu.Lock()
	defer m.mu.Unlock()
	r.state = finalState
	r.errorCode = code
	if finalState == StateDownloaded {
		r.bytesDownloaded = r.entry.descriptor.Size
		r.errorCode = ErrorCodeNone
	}
	m.publishLocked(r.entry.descriptor.Name, Event{
		Type:            EventState,
		Name:            r.entry.descriptor.Name,
		State:           r.state,
		BytesDownloaded: r.bytesDownloaded,
		ErrorCode:       r.errorCode,
	})
	m.closeSubscribersLocked(r.entry.descriptor.Name)
}

func (m *Manager) modelPath(r *record) string {
	return filepath.Join(m.directory, r.entry.descriptor.Filename)
}

func (m *Manager) refreshLocked(r *record) error {
	if r.state == StateDownloading {
		return nil
	}
	lock, err := tryModelLock(m.directory, r.entry.descriptor.Filename, false)
	if errors.Is(err, ErrModelBusy) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("lock model for state refresh: %w", err)
	}
	state, inspectErr := inspectModelFile(m.modelPath(r), r.entry.descriptor)
	closeErr := lock.close()
	if inspectErr != nil {
		return fmt.Errorf("inspect model for state refresh: %w", inspectErr)
	}
	if closeErr != nil {
		return fmt.Errorf("unlock model after state refresh: %w", closeErr)
	}
	// A checksum failure discovered by Acquire remains invalid until an explicit
	// Download revalidates or replaces the file. Cheap list refreshes deliberately
	// do not hash multi-gigabyte models.
	if r.state == StateInvalid && state == StateDownloaded {
		return nil
	}
	if state != r.state {
		r.state = state
		r.errorCode = ErrorCodeNone
		if state == StateDownloaded {
			r.bytesDownloaded = r.entry.descriptor.Size
		} else {
			r.bytesDownloaded = 0
		}
	}
	return nil
}

func (m *Manager) progress(r *record, total int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r.state != StateDownloading || total <= r.bytesDownloaded {
		return
	}
	r.bytesDownloaded = total
	m.publishLocked(r.entry.descriptor.Name, Event{
		Type:            EventProgress,
		Name:            r.entry.descriptor.Name,
		State:           StateDownloading,
		BytesDownloaded: total,
	})
}

func (m *Manager) publishLocked(name string, event Event) {
	modelSubscribers := m.subscribers[name]
	for id, ch := range modelSubscribers {
		select {
		case ch <- event:
		default:
			close(ch)
			delete(modelSubscribers, id)
		}
	}
	if len(modelSubscribers) == 0 {
		delete(m.subscribers, name)
	}
}

func (m *Manager) closeSubscribersLocked(name string) {
	for id, ch := range m.subscribers[name] {
		close(ch)
		delete(m.subscribers[name], id)
	}
	delete(m.subscribers, name)
}

func (m *Manager) closeAllSubscribersLocked() {
	for name := range m.subscribers {
		m.closeSubscribersLocked(name)
	}
}

func snapshotOf(r *record) Snapshot {
	return Snapshot{
		Model:           r.entry.descriptor,
		State:           r.state,
		BytesDownloaded: r.bytesDownloaded,
		ErrorCode:       r.errorCode,
	}
}

func validateModelFile(path string, descriptor Descriptor) State {
	state, pathInfo, err := inspectModelFileInfo(path, descriptor)
	if err != nil || state != StateDownloaded {
		return state
	}
	file, err := os.Open(path)
	if err != nil {
		return StateInvalid
	}
	openedInfo, err := file.Stat()
	if err != nil || !openedInfo.Mode().IsRegular() || openedInfo.Mode().Perm()&0o022 != 0 ||
		openedInfo.Size() != descriptor.Size || !os.SameFile(pathInfo, openedInfo) {
		_ = file.Close()
		return StateInvalid
	}
	hash := sha256.New()
	_, copyErr := io.Copy(hash, file)
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil || hex.EncodeToString(hash.Sum(nil)) != descriptor.SHA256 {
		return StateInvalid
	}
	return StateDownloaded
}

func inspectModelFile(path string, descriptor Descriptor) (State, error) {
	state, _, err := inspectModelFileInfo(path, descriptor)
	return state, err
}

func inspectModelFileInfo(path string, descriptor Descriptor) (State, os.FileInfo, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return StateMissing, nil, nil
	}
	if err != nil {
		return StateInvalid, nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o022 != 0 || info.Size() != descriptor.Size {
		return StateInvalid, info, nil
	}
	return StateDownloaded, info, nil
}

func downloadClient(injected *http.Client) *http.Client {
	transport := http.RoundTripper(&http.Transport{
		Proxy:                 nil,
		DialContext:           (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          10,
		MaxIdleConnsPerHost:   2,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		ExpectContinueTimeout: time.Second,
	})
	timeout := time.Duration(0)
	if injected != nil {
		timeout = injected.Timeout
		if injected.Transport != nil {
			transport = injected.Transport
		}
	}
	return &http.Client{
		Transport: transport,
		Timeout:   timeout,
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if len(via) > 5 {
				return errRedirectPolicy
			}
			if err := validateDownloadURL(request.URL); err != nil {
				return errRedirectPolicy
			}
			return nil
		},
	}
}

// Lease pins a validated model against cooperating deletion. Path is intended
// only for internal process execution and is not a public DTO field.
type Lease struct {
	manager *Manager
	name    string
	path    string
	lock    modelLock
	once    sync.Once
}

// Path returns the validated model path for process execution.
func (l *Lease) Path() string {
	if l == nil {
		return ""
	}
	return l.path
}

// Close releases the lease. It is safe to call repeatedly.
func (l *Lease) Close() error {
	if l == nil {
		return nil
	}
	var result error
	l.once.Do(func() {
		l.manager.mu.Lock()
		if r := l.manager.records[l.name]; r != nil && r.leases > 0 {
			r.leases--
		}
		result = l.lock.close()
		l.manager.mu.Unlock()
	})
	return result
}
