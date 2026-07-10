package job

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

var (
	errUserCancelled = errors.New("job cancelled by user")
	errRunTimeout    = errors.New("job run timed out")
	errRunFinished   = errors.New("job run finished")
)

type normalizedConfig struct {
	queueCapacity       int
	runTimeout          time.Duration
	logCapacityBytes    int
	logLineBytes        int
	stderrCapacityBytes int
	terminalCapacity    int
	terminalTTL         time.Duration
	subscriberCapacity  int
	subscriberLimit     int
	tempRoot            string
	cleanup             CleanupFunc
	clock               Clock
	idGenerator         func() (string, error)
}

type record struct {
	request        RunRequest
	status         Status
	phase          Phase
	progress       int
	progressKnown  bool
	logs           logBuffer
	stderrTail     string
	outputFiles    []string
	recoveryPath   string
	errorCode      ErrorCode
	err            string
	warnings       []Warning
	queuedAt       time.Time
	startedAt      time.Time
	finishedAt     time.Time
	terminalSeq    uint64
	cancel         context.CancelCauseFunc
	stopCause      error
	cleanupStarted bool
	cleanupPending bool
}

type cleanupTask struct {
	id      string
	workDir string
}

// Manager owns the in-memory store and one serial Runner worker.
type Manager struct {
	runner Runner
	config normalizedConfig

	mu             sync.Mutex
	jobs           map[string]*record
	queue          []string
	subscribers    map[string]map[uint64]chan Event
	nextSubscriber uint64
	nextTerminal   uint64
	closed         bool
	cleanupCount   int
	cleanupIdle    chan struct{}

	rootContext context.Context
	rootCancel  context.CancelCauseFunc
	wake        chan struct{}
	workerDone  chan struct{}
	closeOnce   sync.Once
}

// NewManager starts a single worker using runner.
func NewManager(runner Runner, config Config) (*Manager, error) {
	if runner == nil {
		return nil, fmt.Errorf("%w: runner is required", ErrInvalidRequest)
	}
	normalized, err := normalizeConfig(config)
	if err != nil {
		return nil, err
	}

	rootContext, rootCancel := context.WithCancelCause(context.Background())
	cleanupIdle := make(chan struct{})
	close(cleanupIdle)
	m := &Manager{
		runner:      runner,
		config:      normalized,
		jobs:        make(map[string]*record),
		subscribers: make(map[string]map[uint64]chan Event),
		cleanupIdle: cleanupIdle,
		rootContext: rootContext,
		rootCancel:  rootCancel,
		wake:        make(chan struct{}, 1),
		workerDone:  make(chan struct{}),
	}
	go m.worker()
	return m, nil
}

// Submit adds request to the bounded FIFO without waiting for queue space.
func (m *Manager) Submit(request RunRequest) (Snapshot, error) {
	m.mu.Lock()
	m.pruneLocked(m.config.clock.Now())
	if m.closed {
		m.mu.Unlock()
		return Snapshot{}, ErrManagerClosed
	}
	if len(m.queue) >= m.config.queueCapacity {
		m.mu.Unlock()
		return Snapshot{}, ErrQueueFull
	}
	m.mu.Unlock()

	if request.ID == "" {
		id, err := m.config.idGenerator()
		if err != nil {
			return Snapshot{}, fmt.Errorf("assign job ID: %w", err)
		}
		request.ID = id
	}
	if err := m.normalizeRequest(&request); err != nil {
		return Snapshot{}, err
	}
	request.OutputFormats = append([]string(nil), request.OutputFormats...)

	m.mu.Lock()
	m.pruneLocked(m.config.clock.Now())
	if m.closed {
		m.mu.Unlock()
		return Snapshot{}, ErrManagerClosed
	}
	if len(m.queue) >= m.config.queueCapacity {
		m.mu.Unlock()
		return Snapshot{}, ErrQueueFull
	}
	if _, exists := m.jobs[request.ID]; exists {
		m.mu.Unlock()
		return Snapshot{}, ErrDuplicateID
	}

	now := m.config.clock.Now()
	job := &record{
		request:  request,
		status:   StatusQueued,
		logs:     newLogBuffer(m.config.logCapacityBytes, m.config.logLineBytes),
		queuedAt: now,
	}
	m.jobs[request.ID] = job
	m.queue = append(m.queue, request.ID)
	snapshot := snapshotOf(job)
	m.publishLocked(request.ID, Event{Type: EventState, JobID: request.ID, Status: StatusQueued})
	m.mu.Unlock()

	m.signalWorker()
	return snapshot, nil
}

// Snapshot returns the authoritative current state of one job.
func (m *Manager) Snapshot(id string) (Snapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneLocked(m.config.clock.Now())
	job, exists := m.jobs[id]
	if !exists {
		return Snapshot{}, ErrNotFound
	}
	return snapshotOf(job), nil
}

// Snapshots returns all retained records ordered by enqueue time and ID.
func (m *Manager) Snapshots() []Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneLocked(m.config.clock.Now())

	result := make([]Snapshot, 0, len(m.jobs))
	for _, job := range m.jobs {
		result = append(result, snapshotOf(job))
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].QueuedAt.Equal(result[j].QueuedAt) {
			return result[i].ID < result[j].ID
		}
		return result[i].QueuedAt.Before(result[j].QueuedAt)
	})
	return result
}

// Cancel cancels a queued job without invoking Runner, or asks the running
// Runner to stop. Repeated user cancellation of the running job is idempotent.
func (m *Manager) Cancel(id string) error {
	var cancel context.CancelCauseFunc
	var cleanup *cleanupTask

	m.mu.Lock()
	m.pruneLocked(m.config.clock.Now())
	job, exists := m.jobs[id]
	if !exists {
		m.mu.Unlock()
		return ErrNotFound
	}

	switch job.status {
	case StatusQueued:
		m.removeQueuedLocked(id)
		cleanup = m.markTerminalLocked(job, StatusCancelled, "", nil, true)
	case StatusRunning:
		switch job.stopCause {
		case nil:
			job.stopCause = errUserCancelled
			cancel = job.cancel
		case errUserCancelled:
			m.mu.Unlock()
			return nil
		default:
			m.mu.Unlock()
			return ErrNotCancelable
		}
	default:
		m.mu.Unlock()
		return ErrNotCancelable
	}
	m.mu.Unlock()

	if cancel != nil {
		cancel(errUserCancelled)
	}
	if cleanup != nil {
		m.runCleanup(*cleanup)
	}
	return nil
}

// Subscribe atomically captures a Snapshot and registers for subsequent
// best-effort events. The returned cancel function is safe to call repeatedly.
func (m *Manager) Subscribe(id string) (Snapshot, <-chan Event, func(), error) {
	m.mu.Lock()
	m.pruneLocked(m.config.clock.Now())
	if m.closed {
		m.mu.Unlock()
		return Snapshot{}, nil, nil, ErrManagerClosed
	}
	job, exists := m.jobs[id]
	if !exists {
		m.mu.Unlock()
		return Snapshot{}, nil, nil, ErrNotFound
	}

	snapshot := snapshotOf(job)
	ch := make(chan Event, m.config.subscriberCapacity)
	if isTerminal(job.status) && !job.cleanupPending {
		close(ch)
		m.mu.Unlock()
		return snapshot, ch, func() {}, nil
	}
	if len(m.subscribers[id]) >= m.config.subscriberLimit {
		m.mu.Unlock()
		return Snapshot{}, nil, nil, ErrSubscriberLimit
	}
	m.nextSubscriber++
	idNumber := m.nextSubscriber
	if m.subscribers[id] == nil {
		m.subscribers[id] = make(map[uint64]chan Event)
	}
	m.subscribers[id][idNumber] = ch
	m.mu.Unlock()

	var once sync.Once
	unsubscribe := func() {
		once.Do(func() {
			m.mu.Lock()
			defer m.mu.Unlock()
			jobSubscribers := m.subscribers[id]
			current, exists := jobSubscribers[idNumber]
			if !exists || current != ch {
				return
			}
			delete(jobSubscribers, idNumber)
			close(ch)
			if len(jobSubscribers) == 0 {
				delete(m.subscribers, id)
			}
		})
	}
	return snapshot, ch, unsubscribe, nil
}

// Close cancels active work, cleans all queued work, and waits for Manager's
// worker and cleanup paths to finish. It is safe to call repeatedly.
func (m *Manager) Close() {
	m.closeOnce.Do(func() {
		var cancel context.CancelCauseFunc
		var cleanups []cleanupTask

		m.mu.Lock()
		m.closed = true
		for _, id := range m.queue {
			job := m.jobs[id]
			if job == nil || job.status != StatusQueued {
				continue
			}
			if cleanup := m.markTerminalLocked(job, StatusCancelled, "", nil, true); cleanup != nil {
				cleanups = append(cleanups, *cleanup)
			}
		}
		m.queue = nil
		for _, job := range m.jobs {
			if job.status == StatusRunning && job.stopCause == nil {
				job.stopCause = ErrManagerClosed
				cancel = job.cancel
			}
		}
		m.mu.Unlock()

		m.rootCancel(ErrManagerClosed)
		if cancel != nil {
			cancel(ErrManagerClosed)
		}
		for _, cleanup := range cleanups {
			m.runCleanup(cleanup)
		}
		m.signalWorker()
		<-m.workerDone

		m.mu.Lock()
		cleanupIdle := m.cleanupIdle
		m.mu.Unlock()
		<-cleanupIdle

		m.mu.Lock()
		m.closeAllSubscribersLocked()
		m.mu.Unlock()
	})
}

func (m *Manager) worker() {
	defer close(m.workerDone)
	for {
		request, runContext, cancel, ok := m.takeNext()
		if ok {
			m.execute(request, runContext, cancel)
			continue
		}

		select {
		case <-m.wake:
		case <-m.rootContext.Done():
			return
		}
	}
}

func (m *Manager) takeNext() (RunRequest, context.Context, context.CancelCauseFunc, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for len(m.queue) > 0 {
		id := m.queue[0]
		copy(m.queue, m.queue[1:])
		m.queue[len(m.queue)-1] = ""
		m.queue = m.queue[:len(m.queue)-1]

		job := m.jobs[id]
		if job == nil || job.status != StatusQueued {
			continue
		}
		if m.closed {
			return RunRequest{}, nil, nil, false
		}
		if err := transition(job, StatusRunning); err != nil {
			panic(err)
		}
		job.startedAt = m.config.clock.Now()
		runContext, cancel := context.WithCancelCause(m.rootContext)
		job.cancel = cancel
		m.publishLocked(id, Event{Type: EventState, JobID: id, Status: StatusRunning})
		return job.request, runContext, cancel, true
	}
	return RunRequest{}, nil, nil, false
}

func (m *Manager) execute(request RunRequest, runContext context.Context, cancel context.CancelCauseFunc) {
	timer := m.config.clock.AfterFunc(m.config.runTimeout, func() {
		m.stopRunning(request.ID, errRunTimeout)
	})
	observer := &managerObserver{manager: m, id: request.ID, active: true}
	runResult, err := invokeRunner(m.runner, runContext, request, observer)
	observer.close()
	timer.Stop()
	if err == nil {
		if validationErr := validateRunResult(runResult, request.OutputFormats); validationErr != nil {
			err = &RunError{Code: ErrorCodeRunFailed, Err: validationErr}
		}
	}

	m.mu.Lock()
	job := m.jobs[request.ID]
	if job == nil || job.status != StatusRunning {
		m.mu.Unlock()
		cancel(errRunFinished)
		return
	}
	job.cancel = nil
	result := terminalResult(job.stopCause, err, runResult.FinalOutputDir != "")
	if result.preserveWorkDir {
		marker, markerErr := readRecoveryMarker(filepath.Join(job.request.WorkDir, RecoveryMarkerName))
		if markerErr == nil && marker.JobID != job.request.ID {
			markerErr = fmt.Errorf("recovery marker job ID does not match")
		}
		if markerErr != nil {
			result.preserveWorkDir = false
			result.err = errors.Join(result.err, fmt.Errorf("validate output recovery marker: %w", markerErr))
		}
	}
	if result.preserveWorkDir {
		job.recoveryPath = job.request.WorkDir
	}
	if result.status == StatusDone {
		job.request.OutputDir = runResult.FinalOutputDir
		job.outputFiles = append(job.outputFiles[:0], runResult.OutputFiles...)
	}
	cleanup := m.markTerminalLocked(job, result.status, result.code, result.err, !result.preserveWorkDir)
	m.mu.Unlock()

	cancel(errRunFinished)
	if cleanup != nil {
		m.runCleanup(*cleanup)
	}
}

func invokeRunner(runner Runner, ctx context.Context, request RunRequest, observer Observer) (result RunResult, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("runner panic: %v", recovered)
		}
	}()
	return runner.Run(ctx, request, observer)
}

func validateRunResult(result RunResult, requestedFormats []string) error {
	if result.FinalOutputDir == "" && len(result.OutputFiles) == 0 {
		return nil
	}
	if result.FinalOutputDir == "" || !filepath.IsAbs(result.FinalOutputDir) || len(result.OutputFiles) == 0 {
		return fmt.Errorf("runner returned incomplete output metadata")
	}
	formats, err := validateOutputFormats(requestedFormats)
	if err != nil {
		return fmt.Errorf("validate requested output formats: %w", err)
	}
	if len(result.OutputFiles) != len(formats) {
		return fmt.Errorf("runner returned %d output files for %d requested formats", len(result.OutputFiles), len(formats))
	}
	for index, format := range formats {
		want := "transcript." + format
		if result.OutputFiles[index] != want {
			return fmt.Errorf("runner returned output filename %q, want %q", result.OutputFiles[index], want)
		}
	}
	return nil
}

type terminalOutcome struct {
	status          Status
	code            ErrorCode
	err             error
	preserveWorkDir bool
}

func terminalResult(stopCause, runErr error, outputsCommitted bool) terminalOutcome {
	// A non-empty successful RunResult means the publisher's final rename has
	// committed. Prefer done if cancellation races with that commit so the
	// published directory is not orphaned from the job record.
	if runErr == nil && outputsCommitted {
		return terminalOutcome{status: StatusDone}
	}
	switch stopCause {
	case errUserCancelled:
		return terminalOutcome{status: StatusCancelled}
	case errRunTimeout:
		return terminalOutcome{status: StatusFailed, code: ErrorCodeTimeout, err: errRunTimeout}
	case ErrManagerClosed:
		return terminalOutcome{status: StatusFailed, code: ErrorCodeManagerClosed, err: ErrManagerClosed}
	}
	if runErr == nil {
		return terminalOutcome{status: StatusDone}
	}

	code := ErrorCodeRunFailed
	var coded *RunError
	if errors.As(runErr, &coded) && coded.Code != "" {
		code = coded.Code
	}
	result := terminalOutcome{status: StatusFailed, code: code, err: runErr}
	if coded != nil && coded.Code == ErrorCodeOutputPublishFailed && coded.PreserveWorkDir {
		result.preserveWorkDir = true
	}
	return result
}

func (m *Manager) stopRunning(id string, cause error) {
	var cancel context.CancelCauseFunc
	m.mu.Lock()
	job := m.jobs[id]
	if job != nil && job.status == StatusRunning && job.stopCause == nil {
		job.stopCause = cause
		cancel = job.cancel
	}
	m.mu.Unlock()
	if cancel != nil {
		cancel(cause)
	}
}

type managerObserver struct {
	manager *Manager
	id      string
	mu      sync.Mutex
	active  bool
}

func (o *managerObserver) close() {
	o.mu.Lock()
	o.active = false
	o.mu.Unlock()
}

func (o *managerObserver) Log(stream LogStream, line string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if !o.active {
		return
	}
	m := o.manager
	m.mu.Lock()
	defer m.mu.Unlock()
	job := m.jobs[o.id]
	if job == nil || job.status != StatusRunning {
		return
	}
	entry := job.logs.append(stream, line)
	if entry.Stream == LogStreamStderr {
		job.stderrTail = appendUTF8Tail(job.stderrTail, line, m.config.stderrCapacityBytes)
	}
	eventEntry := entry
	m.publishLocked(o.id, Event{Type: EventLog, JobID: o.id, Log: &eventEntry})
}

func (o *managerObserver) Progress(percent int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if !o.active {
		return
	}
	if percent < 0 {
		percent = 0
	} else if percent > 100 {
		percent = 100
	}
	m := o.manager
	m.mu.Lock()
	defer m.mu.Unlock()
	job := m.jobs[o.id]
	if job == nil || job.status != StatusRunning {
		return
	}
	job.progress = percent
	job.progressKnown = true
	m.publishLocked(o.id, Event{Type: EventProgress, JobID: o.id, Progress: percent})
}

func (o *managerObserver) Phase(phase Phase) {
	switch phase {
	case PhaseConverting, PhaseTranscribing, PhaseMoving:
	default:
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if !o.active {
		return
	}
	m := o.manager
	m.mu.Lock()
	defer m.mu.Unlock()
	job := m.jobs[o.id]
	if job == nil || job.status != StatusRunning {
		return
	}
	job.phase = phase
	m.publishLocked(o.id, Event{Type: EventState, JobID: o.id, Status: StatusRunning, Phase: phase})
}

func (m *Manager) markTerminalLocked(job *record, status Status, code ErrorCode, terminalErr error, scheduleCleanup bool) *cleanupTask {
	if err := transition(job, status); err != nil {
		panic(err)
	}
	job.errorCode = code
	if terminalErr != nil {
		job.err = terminalErr.Error()
	}
	job.finishedAt = m.config.clock.Now()
	job.phase = ""
	m.nextTerminal++
	job.terminalSeq = m.nextTerminal
	job.cancel = nil
	if !scheduleCleanup {
		job.cleanupStarted = true
		job.cleanupPending = false
		m.publishLocked(job.request.ID, Event{
			Type:      EventState,
			JobID:     job.request.ID,
			Status:    job.status,
			ErrorCode: job.errorCode,
		})
		m.closeSubscribersLocked(job.request.ID)
		return nil
	}
	if job.cleanupStarted {
		return nil
	}
	job.cleanupStarted = true
	job.cleanupPending = true
	m.cleanupStartedLocked()
	return &cleanupTask{id: job.request.ID, workDir: job.request.WorkDir}
}

func (m *Manager) runCleanup(task cleanupTask) {
	err := invokeCleanup(m.config.cleanup, task.workDir)
	m.mu.Lock()
	job := m.jobs[task.id]
	if job != nil {
		job.cleanupPending = false
		if err != nil {
			job.warnings = append(job.warnings, Warning{
				Code:    WarningCodeCleanupFailed,
				Message: err.Error(),
			})
		}
		m.publishLocked(task.id, Event{
			Type:      EventState,
			JobID:     task.id,
			Status:    job.status,
			ErrorCode: job.errorCode,
		})
		m.closeSubscribersLocked(task.id)
	}
	m.cleanupFinishedLocked()
	m.pruneLocked(m.config.clock.Now())
	m.mu.Unlock()
}

func invokeCleanup(cleanup CleanupFunc, workDir string) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("cleanup panic: %v", recovered)
		}
	}()
	if err := cleanup(workDir); err != nil {
		return fmt.Errorf("cleanup %q: %w", workDir, err)
	}
	return nil
}

func transition(job *record, target Status) error {
	if !validTransition(job.status, target) {
		return fmt.Errorf("%w: %s to %s", ErrInvalidTransition, job.status, target)
	}
	job.status = target
	return nil
}

func (m *Manager) publishLocked(id string, event Event) {
	jobSubscribers := m.subscribers[id]
	for subscriberID, ch := range jobSubscribers {
		select {
		case ch <- event:
		default:
			close(ch)
			delete(jobSubscribers, subscriberID)
		}
	}
	if len(jobSubscribers) == 0 {
		delete(m.subscribers, id)
	}
}

func (m *Manager) closeSubscribersLocked(id string) {
	for subscriberID, ch := range m.subscribers[id] {
		close(ch)
		delete(m.subscribers[id], subscriberID)
	}
	delete(m.subscribers, id)
}

func (m *Manager) closeAllSubscribersLocked() {
	for id := range m.subscribers {
		m.closeSubscribersLocked(id)
	}
}

func (m *Manager) removeQueuedLocked(id string) {
	for index, queuedID := range m.queue {
		if queuedID != id {
			continue
		}
		copy(m.queue[index:], m.queue[index+1:])
		m.queue[len(m.queue)-1] = ""
		m.queue = m.queue[:len(m.queue)-1]
		return
	}
}

func (m *Manager) pruneLocked(now time.Time) {
	terminal := make([]*record, 0)
	for id, job := range m.jobs {
		if !isTerminal(job.status) || job.cleanupPending {
			continue
		}
		if !job.finishedAt.IsZero() && now.Sub(job.finishedAt) >= m.config.terminalTTL {
			m.closeSubscribersLocked(id)
			delete(m.jobs, id)
			continue
		}
		terminal = append(terminal, job)
	}
	if len(terminal) <= m.config.terminalCapacity {
		return
	}
	sort.Slice(terminal, func(i, j int) bool {
		return terminal[i].terminalSeq < terminal[j].terminalSeq
	})
	for _, job := range terminal[:len(terminal)-m.config.terminalCapacity] {
		m.closeSubscribersLocked(job.request.ID)
		delete(m.jobs, job.request.ID)
	}
}

func (m *Manager) cleanupStartedLocked() {
	if m.cleanupCount == 0 {
		m.cleanupIdle = make(chan struct{})
	}
	m.cleanupCount++
}

func (m *Manager) cleanupFinishedLocked() {
	m.cleanupCount--
	if m.cleanupCount == 0 {
		close(m.cleanupIdle)
	}
}

func (m *Manager) signalWorker() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

func snapshotOf(job *record) Snapshot {
	request := job.request
	request.OutputFormats = append([]string(nil), job.request.OutputFormats...)
	warnings := make([]Warning, len(job.warnings))
	copy(warnings, job.warnings)
	outputFiles := append([]string(nil), job.outputFiles...)
	return Snapshot{
		RunRequest:     request,
		Status:         job.status,
		Phase:          job.phase,
		Progress:       job.progress,
		ProgressKnown:  job.progressKnown,
		Logs:           job.logs.snapshot(),
		StderrTail:     job.stderrTail,
		OutputFiles:    outputFiles,
		RecoveryPath:   job.recoveryPath,
		ErrorCode:      job.errorCode,
		Error:          job.err,
		Warnings:       warnings,
		QueuedAt:       job.queuedAt,
		StartedAt:      job.startedAt,
		FinishedAt:     job.finishedAt,
		CleanupPending: job.cleanupPending,
	}
}

func (m *Manager) normalizeRequest(request *RunRequest) error {
	if !validJobID(request.ID) {
		return fmt.Errorf("%w: invalid job ID", ErrInvalidRequest)
	}
	if request.WorkDir == "" {
		request.WorkDir = filepath.Join(m.config.tempRoot, request.ID)
	}
	absolute, err := filepath.Abs(request.WorkDir)
	if err != nil {
		return fmt.Errorf("%w: resolve work directory: %w", ErrInvalidRequest, err)
	}
	absolute = filepath.Clean(absolute)
	expected := filepath.Join(m.config.tempRoot, request.ID)
	if absolute != expected {
		return fmt.Errorf("%w: work directory must equal the generated job directory", ErrInvalidRequest)
	}
	request.WorkDir = absolute
	return nil
}

func validJobID(id string) bool {
	if len(id) == 0 || len(id) > 128 {
		return false
	}
	for _, value := range []byte(id) {
		if (value >= 'a' && value <= 'z') || (value >= 'A' && value <= 'Z') || (value >= '0' && value <= '9') || value == '-' || value == '_' {
			continue
		}
		return false
	}
	return true
}

func normalizeConfig(config Config) (normalizedConfig, error) {
	if config.QueueCapacity < 0 || config.RunTimeout < 0 || config.LogCapacityBytes < 0 ||
		config.LogLineBytes < 0 || config.StderrCapacityBytes < 0 || config.TerminalCapacity < 0 ||
		config.TerminalTTL < 0 || config.SubscriberCapacity < 0 || config.SubscriberLimit < 0 {
		return normalizedConfig{}, fmt.Errorf("%w: config limits cannot be negative", ErrInvalidRequest)
	}
	if config.QueueCapacity == 0 {
		config.QueueCapacity = DefaultQueueCapacity
	}
	if config.RunTimeout == 0 {
		config.RunTimeout = DefaultRunTimeout
	}
	if config.LogCapacityBytes == 0 {
		config.LogCapacityBytes = DefaultLogCapacityBytes
	}
	if config.LogLineBytes == 0 {
		config.LogLineBytes = DefaultLogLineBytes
	}
	if config.StderrCapacityBytes == 0 {
		config.StderrCapacityBytes = DefaultStderrCapacityBytes
	}
	if config.TerminalCapacity == 0 {
		config.TerminalCapacity = DefaultTerminalCapacity
	}
	if config.TerminalTTL == 0 {
		config.TerminalTTL = DefaultTerminalTTL
	}
	if config.SubscriberCapacity == 0 {
		config.SubscriberCapacity = DefaultSubscriberCapacity
	}
	if config.SubscriberLimit == 0 {
		config.SubscriberLimit = DefaultSubscriberLimit
	}
	if config.LogCapacityBytes < minimumLogCapacityBytes || config.LogLineBytes < 1 ||
		config.StderrCapacityBytes < minimumStderrCapacityBytes {
		return normalizedConfig{}, fmt.Errorf("%w: byte capacities are too small", ErrInvalidRequest)
	}
	tempRoot, err := PrepareTempRoot(config.TempRoot)
	if err != nil {
		return normalizedConfig{}, fmt.Errorf("%w: prepare temp root: %w", ErrInvalidRequest, err)
	}
	if config.Cleanup == nil {
		config.Cleanup = os.RemoveAll
	}
	if config.Clock == nil {
		config.Clock = realClock{}
	}
	if config.IDGenerator == nil {
		config.IDGenerator = NewID
	}
	return normalizedConfig{
		queueCapacity:       config.QueueCapacity,
		runTimeout:          config.RunTimeout,
		logCapacityBytes:    config.LogCapacityBytes,
		logLineBytes:        config.LogLineBytes,
		stderrCapacityBytes: config.StderrCapacityBytes,
		terminalCapacity:    config.TerminalCapacity,
		terminalTTL:         config.TerminalTTL,
		subscriberCapacity:  config.SubscriberCapacity,
		subscriberLimit:     config.SubscriberLimit,
		tempRoot:            tempRoot,
		cleanup:             config.Cleanup,
		clock:               config.Clock,
		idGenerator:         config.IDGenerator,
	}, nil
}
