package job

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

type runnerFunc func(context.Context, RunRequest, Observer) error

func (f runnerFunc) Run(ctx context.Context, request RunRequest, observer Observer) (RunResult, error) {
	return RunResult{}, f(ctx, request, observer)
}

type resultRunnerFunc func(context.Context, RunRequest, Observer) (RunResult, error)

func (f resultRunnerFunc) Run(ctx context.Context, request RunRequest, observer Observer) (RunResult, error) {
	return f(ctx, request, observer)
}

type manualClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*manualTimer
}

type manualTimer struct {
	clock    *manualClock
	due      time.Time
	callback func()
	active   bool
}

func newManualClock() *manualClock {
	return &manualClock{now: time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC)}
}

func (c *manualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *manualClock) AfterFunc(delay time.Duration, callback func()) Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	timer := &manualTimer{
		clock:    c,
		due:      c.now.Add(delay),
		callback: callback,
		active:   true,
	}
	c.timers = append(c.timers, timer)
	return timer
}

func (c *manualClock) Advance(duration time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(duration)
	var callbacks []func()
	for _, timer := range c.timers {
		if timer.active && !timer.due.After(c.now) {
			timer.active = false
			callbacks = append(callbacks, timer.callback)
		}
	}
	c.mu.Unlock()
	for _, callback := range callbacks {
		callback()
	}
}

func (t *manualTimer) Stop() bool {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	wasActive := t.active
	t.active = false
	return wasActive
}

func TestStatusTransitions(t *testing.T) {
	tests := []struct {
		name string
		from Status
		to   Status
		want bool
	}{
		{name: "queued to running", from: StatusQueued, to: StatusRunning, want: true},
		{name: "queued to cancelled", from: StatusQueued, to: StatusCancelled, want: true},
		{name: "running to done", from: StatusRunning, to: StatusDone, want: true},
		{name: "running to failed", from: StatusRunning, to: StatusFailed, want: true},
		{name: "running to cancelled", from: StatusRunning, to: StatusCancelled, want: true},
		{name: "queued cannot finish", from: StatusQueued, to: StatusDone, want: false},
		{name: "terminal cannot restart", from: StatusDone, to: StatusRunning, want: false},
		{name: "running cannot queue", from: StatusRunning, to: StatusQueued, want: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			job := &record{status: test.from}
			err := transition(job, test.to)
			if test.want && err != nil {
				t.Fatalf("transition returned error: %v", err)
			}
			if !test.want && !errors.Is(err, ErrInvalidTransition) {
				t.Fatalf("transition error = %v, want ErrInvalidTransition", err)
			}
		})
	}
}

func TestApprovedDefaultLimits(t *testing.T) {
	config, err := normalizeConfig(Config{TempRoot: t.TempDir()})
	if err != nil {
		t.Fatalf("normalizeConfig: %v", err)
	}
	if config.queueCapacity != 4 || config.runTimeout != 12*time.Hour {
		t.Fatalf("queue/timeout defaults = %d/%s, want 4/12h", config.queueCapacity, config.runTimeout)
	}
	if config.logCapacityBytes != 512*1024 || config.logLineBytes != 8*1024 || config.stderrCapacityBytes != 64*1024 {
		t.Fatalf("log defaults = %d/%d/%d", config.logCapacityBytes, config.logLineBytes, config.stderrCapacityBytes)
	}
	if config.terminalCapacity != 100 || config.terminalTTL != 24*time.Hour {
		t.Fatalf("terminal defaults = %d/%s, want 100/24h", config.terminalCapacity, config.terminalTTL)
	}
	if config.subscriberLimit != 16 {
		t.Fatalf("subscriber limit = %d, want 16", config.subscriberLimit)
	}
}

func TestQueuedAndRunningCancellation(t *testing.T) {
	started := make(chan string, 2)
	causes := make(chan error, 2)
	runner := runnerFunc(func(ctx context.Context, request RunRequest, _ Observer) error {
		started <- request.ID
		<-ctx.Done()
		causes <- context.Cause(ctx)
		return ctx.Err()
	})
	m := newTestManager(t, runner, Config{QueueCapacity: 2})

	submit(t, m, "running", "")
	if id := receive(t, started); id != "running" {
		t.Fatalf("started job = %q, want running", id)
	}
	submit(t, m, "queued", "")
	if err := m.Cancel("queued"); err != nil {
		t.Fatalf("cancel queued: %v", err)
	}
	queued, err := m.Snapshot("queued")
	if err != nil {
		t.Fatalf("snapshot queued: %v", err)
	}
	if queued.Status != StatusCancelled {
		t.Fatalf("queued status = %s, want cancelled", queued.Status)
	}

	if err := m.Cancel("running"); err != nil {
		t.Fatalf("cancel running: %v", err)
	}
	running := waitTerminal(t, m, "running")
	if running.Status != StatusCancelled {
		t.Fatalf("running status = %s, want cancelled", running.Status)
	}
	if cause := receive(t, causes); !errors.Is(cause, errUserCancelled) {
		t.Fatalf("runner cancellation cause = %v, want user cancellation", cause)
	}
	select {
	case id := <-started:
		t.Fatalf("cancelled queued job invoked Runner: %s", id)
	default:
	}
}

func TestQueuedCancelDispatchRaceEndsCancelledExactlyOnce(t *testing.T) {
	for iteration := range 64 {
		t.Run(fmt.Sprintf("iteration-%d", iteration), func(t *testing.T) {
			blockerStarted := make(chan struct{}, 1)
			releaseBlocker := make(chan struct{})
			targetInvoked := make(chan struct{}, 1)
			var cleanupMu sync.Mutex
			cleanupCalls := make(map[string]int)

			runner := runnerFunc(func(ctx context.Context, request RunRequest, _ Observer) error {
				if request.ID == "blocker" {
					blockerStarted <- struct{}{}
					<-releaseBlocker
					return nil
				}
				targetInvoked <- struct{}{}
				<-ctx.Done()
				return ctx.Err()
			})
			m := newTestManager(t, runner, Config{Cleanup: func(workDir string) error {
				cleanupMu.Lock()
				cleanupCalls[workDir]++
				cleanupMu.Unlock()
				return nil
			}})

			submit(t, m, "blocker", "")
			receive(t, blockerStarted)
			target := submit(t, m, "target", "")

			startRace := make(chan struct{})
			cancelResult := make(chan error, 1)
			go func() {
				<-startRace
				cancelResult <- m.Cancel(target.ID)
			}()
			go func() {
				<-startRace
				close(releaseBlocker)
			}()
			close(startRace)

			if err := receive(t, cancelResult); err != nil {
				t.Fatalf("Cancel error = %v, want nil", err)
			}
			snapshot := waitTerminal(t, m, target.ID)
			if snapshot.Status != StatusCancelled {
				t.Fatalf("target status = %s, want cancelled", snapshot.Status)
			}
			select {
			case <-targetInvoked:
				// Dequeue won the race; Cancel stopped the running job.
			default:
				// Cancel won the race; Runner was never invoked.
			}
			cleanupMu.Lock()
			calls := cleanupCalls[target.WorkDir]
			cleanupMu.Unlock()
			if calls != 1 {
				t.Fatalf("target cleanup calls = %d, want 1", calls)
			}
		})
	}
}

func TestRunningCompletionCancelRaceHasOneTerminalResult(t *testing.T) {
	for iteration := range 32 {
		t.Run(fmt.Sprintf("iteration-%d", iteration), func(t *testing.T) {
			started := make(chan struct{}, 1)
			release := make(chan struct{})
			var cleanupMu sync.Mutex
			cleanupCalls := 0
			runner := runnerFunc(func(ctx context.Context, _ RunRequest, _ Observer) error {
				started <- struct{}{}
				select {
				case <-release:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			})
			m := newTestManager(t, runner, Config{Cleanup: func(string) error {
				cleanupMu.Lock()
				cleanupCalls++
				cleanupMu.Unlock()
				return nil
			}})
			submit(t, m, "race", "")
			receive(t, started)

			startRace := make(chan struct{})
			cancelResult := make(chan error, 1)
			go func() {
				<-startRace
				cancelResult <- m.Cancel("race")
			}()
			go func() {
				<-startRace
				close(release)
			}()
			close(startRace)

			cancelErr := receive(t, cancelResult)
			if cancelErr != nil && !errors.Is(cancelErr, ErrNotCancelable) {
				t.Fatalf("Cancel error = %v", cancelErr)
			}
			snapshot := waitTerminal(t, m, "race")
			if snapshot.Status != StatusDone && snapshot.Status != StatusCancelled {
				t.Fatalf("race status = %s, want done or cancelled", snapshot.Status)
			}
			cleanupMu.Lock()
			defer cleanupMu.Unlock()
			if cleanupCalls != 1 {
				t.Fatalf("cleanup calls = %d, want 1", cleanupCalls)
			}
		})
	}
}

func TestTimeoutIsFailedAndDistinctFromCancel(t *testing.T) {
	clock := newManualClock()
	started := make(chan struct{}, 1)
	cause := make(chan error, 1)
	runner := runnerFunc(func(ctx context.Context, _ RunRequest, _ Observer) error {
		started <- struct{}{}
		<-ctx.Done()
		cause <- context.Cause(ctx)
		return ctx.Err()
	})
	m := newTestManager(t, runner, Config{Clock: clock, RunTimeout: time.Minute})

	submit(t, m, "timeout", "")
	receive(t, started)
	clock.Advance(time.Minute)
	snapshot := waitTerminal(t, m, "timeout")
	if snapshot.Status != StatusFailed || snapshot.ErrorCode != ErrorCodeTimeout {
		t.Fatalf("terminal state = %s/%s, want failed/timeout", snapshot.Status, snapshot.ErrorCode)
	}
	if got := receive(t, cause); !errors.Is(got, errRunTimeout) {
		t.Fatalf("runner cancellation cause = %v, want timeout", got)
	}
}

func TestRunnerExecutionIsSerialFIFO(t *testing.T) {
	started := make(chan string, 3)
	release := make(chan struct{})
	var mu sync.Mutex
	active := 0
	maximum := 0
	runner := runnerFunc(func(ctx context.Context, request RunRequest, _ Observer) error {
		mu.Lock()
		active++
		if active > maximum {
			maximum = active
		}
		mu.Unlock()
		started <- request.ID
		select {
		case <-release:
		case <-ctx.Done():
			return ctx.Err()
		}
		mu.Lock()
		active--
		mu.Unlock()
		return nil
	})
	m := newTestManager(t, runner, Config{})

	for _, id := range []string{"one", "two", "three"} {
		submit(t, m, id, "")
		if id == "one" {
			if got := receive(t, started); got != id {
				t.Fatalf("first start = %q, want %q", got, id)
			}
		}
	}
	select {
	case id := <-started:
		t.Fatalf("job %q started before first release", id)
	default:
	}

	for _, want := range []string{"two", "three"} {
		release <- struct{}{}
		if got := receive(t, started); got != want {
			t.Fatalf("start = %q, want %q", got, want)
		}
	}
	release <- struct{}{}
	waitTerminal(t, m, "three")
	mu.Lock()
	defer mu.Unlock()
	if maximum != 1 {
		t.Fatalf("maximum concurrent Runner calls = %d, want 1", maximum)
	}
}

func TestQueueFullIsNonBlocking(t *testing.T) {
	started := make(chan struct{}, 1)
	runner := runnerFunc(func(ctx context.Context, _ RunRequest, _ Observer) error {
		select {
		case started <- struct{}{}:
		default:
		}
		<-ctx.Done()
		return ctx.Err()
	})
	m := newTestManager(t, runner, Config{
		QueueCapacity: 4,
		IDGenerator: func() (string, error) {
			panic("ID generator called for a full queue")
		},
	})

	submit(t, m, "running", "")
	receive(t, started)
	for index := range 4 {
		submit(t, m, fmt.Sprintf("queued-%d", index), "")
	}
	if _, err := m.Submit(RunRequest{ID: "overflow"}); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("overflow Submit error = %v, want ErrQueueFull", err)
	}
	if _, err := m.Submit(RunRequest{}); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("anonymous overflow Submit error = %v, want ErrQueueFull", err)
	}
	for index := range 4 {
		if err := m.Cancel(fmt.Sprintf("queued-%d", index)); err != nil {
			t.Fatalf("cancel queued-%d: %v", index, err)
		}
	}
	if err := m.Cancel("running"); err != nil {
		t.Fatalf("cancel running: %v", err)
	}
	waitTerminal(t, m, "running")
}

func TestLogAndStderrCapsWithHugeLine(t *testing.T) {
	huge := strings.Repeat("界", 1<<20) + string([]byte{0xff, 0x80})
	runner := runnerFunc(func(_ context.Context, _ RunRequest, observer Observer) error {
		for index := range 20 {
			observer.Log(LogStreamStdout, fmt.Sprintf("line-%02d-%s", index, huge))
		}
		observer.Log(LogStreamStderr, huge)
		observer.Progress(150)
		return nil
	})
	m := newTestManager(t, runner, Config{
		LogCapacityBytes:    96,
		LogLineBytes:        17,
		StderrCapacityBytes: 65,
	})

	submit(t, m, "logs", "")
	snapshot := waitTerminal(t, m, "logs")
	retained := 0
	for _, entry := range snapshot.Logs {
		retained += len(entry.Line) + 1
		if len(entry.Line) > 17 {
			t.Fatalf("line bytes = %d, want <= 17", len(entry.Line))
		}
		if !utf8.ValidString(entry.Line) {
			t.Fatalf("invalid UTF-8 log line: %q", entry.Line)
		}
	}
	if retained > 96 {
		t.Fatalf("retained log bytes = %d, want <= 96", retained)
	}
	if len(snapshot.StderrTail) > 65 {
		t.Fatalf("stderr tail bytes = %d, want <= 65", len(snapshot.StderrTail))
	}
	if !utf8.ValidString(snapshot.StderrTail) {
		t.Fatalf("stderr tail is not valid UTF-8: %q", snapshot.StderrTail)
	}
	if snapshot.Progress != 100 {
		t.Fatalf("progress = %d, want clamped 100", snapshot.Progress)
	}
}

func TestTerminalRecordCountAndTTLPruning(t *testing.T) {
	clock := newManualClock()
	runner := runnerFunc(func(context.Context, RunRequest, Observer) error { return nil })
	m := newTestManager(t, runner, Config{
		Clock:            clock,
		TerminalCapacity: 3,
		TerminalTTL:      10 * time.Minute,
	})

	for index := range 5 {
		id := fmt.Sprintf("job-%d", index)
		submit(t, m, id, "")
		waitTerminal(t, m, id)
	}
	snapshots := m.Snapshots()
	if len(snapshots) != 3 {
		t.Fatalf("retained terminal records = %d, want 3", len(snapshots))
	}
	for index, want := range []string{"job-2", "job-3", "job-4"} {
		if snapshots[index].ID != want {
			t.Fatalf("snapshot[%d].ID = %q, want %q", index, snapshots[index].ID, want)
		}
	}

	clock.Advance(10 * time.Minute)
	if snapshots := m.Snapshots(); len(snapshots) != 0 {
		t.Fatalf("records after TTL = %d, want 0", len(snapshots))
	}
}

func TestPruningNeverRemovesActiveJobs(t *testing.T) {
	clock := newManualClock()
	started := make(chan struct{}, 1)
	runner := runnerFunc(func(ctx context.Context, _ RunRequest, _ Observer) error {
		started <- struct{}{}
		<-ctx.Done()
		return ctx.Err()
	})
	m := newTestManager(t, runner, Config{Clock: clock, TerminalTTL: time.Minute})

	submit(t, m, "running", "")
	receive(t, started)
	submit(t, m, "queued", "")
	clock.Advance(2 * time.Minute)
	if snapshots := m.Snapshots(); len(snapshots) != 2 {
		t.Fatalf("active records after TTL = %d, want 2", len(snapshots))
	}
	if err := m.Cancel("queued"); err != nil {
		t.Fatalf("cancel queued: %v", err)
	}
	if err := m.Cancel("running"); err != nil {
		t.Fatalf("cancel running: %v", err)
	}
	waitTerminal(t, m, "running")
}

func TestSlowSubscriberOverflowAndSnapshotResync(t *testing.T) {
	observerReady := make(chan Observer, 1)
	release := make(chan struct{})
	runner := runnerFunc(func(ctx context.Context, _ RunRequest, observer Observer) error {
		observerReady <- observer
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	m := newTestManager(t, runner, Config{SubscriberCapacity: 1})

	submit(t, m, "slow", "")
	observer := receive(t, observerReady)
	initial, events, unsubscribe, err := m.Subscribe("slow")
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer unsubscribe()
	if initial.Status != StatusRunning {
		t.Fatalf("initial status = %s, want running", initial.Status)
	}
	observer.Progress(1)
	observer.Progress(2)

	first, ok := receiveOK(t, events)
	if !ok || first.Type != EventProgress || first.Progress != 1 {
		t.Fatalf("first event = %+v, %v; want progress 1", first, ok)
	}
	if _, ok := receiveOK(t, events); ok {
		t.Fatal("overflowed subscription remained open")
	}

	observer.Progress(73)
	close(release)
	resynced := waitTerminal(t, m, "slow")
	if resynced.Status != StatusDone || resynced.Progress != 73 {
		t.Fatalf("resynced snapshot = %s/%d, want done/73", resynced.Status, resynced.Progress)
	}
}

func TestSubscriberCountIsBounded(t *testing.T) {
	started := make(chan struct{}, 1)
	runner := runnerFunc(func(ctx context.Context, _ RunRequest, _ Observer) error {
		started <- struct{}{}
		<-ctx.Done()
		return ctx.Err()
	})
	m := newTestManager(t, runner, Config{SubscriberLimit: 2})
	submit(t, m, "subscribers", "")
	receive(t, started)

	_, _, unsubscribeFirst, err := m.Subscribe("subscribers")
	if err != nil {
		t.Fatal(err)
	}
	_, _, unsubscribeSecond, err := m.Subscribe("subscribers")
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribeSecond()
	if _, _, _, err := m.Subscribe("subscribers"); !errors.Is(err, ErrSubscriberLimit) {
		t.Fatalf("third Subscribe error = %v, want ErrSubscriberLimit", err)
	}
	unsubscribeFirst()
	_, _, unsubscribeReplacement, err := m.Subscribe("subscribers")
	if err != nil {
		t.Fatalf("Subscribe after unsubscribe: %v", err)
	}
	unsubscribeReplacement()
	if err := m.Cancel("subscribers"); err != nil {
		t.Fatal(err)
	}
	waitTerminal(t, m, "subscribers")
}

func TestRunnerResultCommittedOnlyOnSuccess(t *testing.T) {
	outputDir := filepath.Join(t.TempDir(), "outputs")
	runner := resultRunnerFunc(func(_ context.Context, request RunRequest, _ Observer) (RunResult, error) {
		result := RunResult{FinalOutputDir: outputDir, OutputFiles: []string{"transcript.txt"}}
		switch request.ID {
		case "failed-result":
			return result, errors.New("failed after producing metadata")
		case "invalid-result":
			result.OutputFiles = []string{"arbitrary.bin"}
		}
		return result, nil
	})
	m := newTestManager(t, runner, Config{})

	submitRequest(t, m, RunRequest{ID: "successful-result", OutputFormats: []string{"txt"}})
	success := waitTerminal(t, m, "successful-result")
	if success.Status != StatusDone || success.OutputDir != outputDir || !reflect.DeepEqual(success.OutputFiles, []string{"transcript.txt"}) {
		t.Fatalf("successful result snapshot = %+v", success)
	}
	success.OutputFiles[0] = "mutated"
	again, err := m.Snapshot("successful-result")
	if err != nil || again.OutputFiles[0] != "transcript.txt" {
		t.Fatalf("output metadata was not defensively copied: %+v, %v", again, err)
	}

	submitRequest(t, m, RunRequest{ID: "failed-result", OutputFormats: []string{"txt"}})
	failed := waitTerminal(t, m, "failed-result")
	if failed.Status != StatusFailed || failed.OutputDir != "" || len(failed.OutputFiles) != 0 {
		t.Fatalf("failed runner result was committed: %+v", failed)
	}

	submitRequest(t, m, RunRequest{ID: "invalid-result", OutputFormats: []string{"txt"}})
	invalid := waitTerminal(t, m, "invalid-result")
	if invalid.Status != StatusFailed || invalid.ErrorCode != ErrorCodeRunFailed ||
		invalid.OutputDir != "" || len(invalid.OutputFiles) != 0 {
		t.Fatalf("invalid output filename was committed: %+v", invalid)
	}
}

func TestCommittedOutputWinsCancellationRace(t *testing.T) {
	started := make(chan struct{}, 1)
	outputDir := filepath.Join(t.TempDir(), "outputs")
	runner := resultRunnerFunc(func(ctx context.Context, _ RunRequest, _ Observer) (RunResult, error) {
		started <- struct{}{}
		<-ctx.Done()
		return RunResult{
			FinalOutputDir: outputDir,
			OutputFiles:    []string{"transcript.txt"},
		}, nil
	})
	m := newTestManager(t, runner, Config{})

	submitRequest(t, m, RunRequest{ID: "publish-cancel-race", OutputFormats: []string{"txt"}})
	receive(t, started)
	if err := m.Cancel("publish-cancel-race"); err != nil {
		t.Fatal(err)
	}
	snapshot := waitTerminal(t, m, "publish-cancel-race")
	if snapshot.Status != StatusDone || snapshot.OutputDir != outputDir ||
		!reflect.DeepEqual(snapshot.OutputFiles, []string{"transcript.txt"}) {
		t.Fatalf("committed publish snapshot = %+v", snapshot)
	}
}

func TestPublishFailurePreservedOnlyWithValidMarker(t *testing.T) {
	now := time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC)
	var cleanupMu sync.Mutex
	cleanupCalls := make(map[string]int)
	runner := resultRunnerFunc(func(_ context.Context, request RunRequest, _ Observer) (RunResult, error) {
		if err := os.MkdirAll(request.WorkDir, 0o700); err != nil {
			return RunResult{}, err
		}
		if request.ID == "marked" {
			if err := MarkRecovery(request.WorkDir, request.ID, now); err != nil {
				return RunResult{}, err
			}
		}
		return RunResult{}, &RunError{
			Code:            ErrorCodeOutputPublishFailed,
			Err:             errors.New("publish failed"),
			PreserveWorkDir: true,
		}
	})
	m := newTestManager(t, runner, Config{Cleanup: func(workDir string) error {
		cleanupMu.Lock()
		cleanupCalls[workDir]++
		cleanupMu.Unlock()
		return os.RemoveAll(workDir)
	}})

	markedRequest := submit(t, m, "marked", "")
	marked := waitTerminal(t, m, "marked")
	if marked.Status != StatusFailed || marked.ErrorCode != ErrorCodeOutputPublishFailed || marked.RecoveryPath != markedRequest.WorkDir {
		t.Fatalf("marked recovery snapshot = %+v", marked)
	}
	if _, err := os.Stat(markedRequest.WorkDir); err != nil {
		t.Fatalf("marked recovery was removed: %v", err)
	}

	unmarkedRequest := submit(t, m, "unmarked", "")
	unmarked := waitTerminal(t, m, "unmarked")
	if unmarked.RecoveryPath != "" {
		t.Fatalf("unmarked publish failure exposed recovery path %q", unmarked.RecoveryPath)
	}
	if _, err := os.Stat(unmarkedRequest.WorkDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unmarked publish failure was retained: %v", err)
	}
	cleanupMu.Lock()
	defer cleanupMu.Unlock()
	if cleanupCalls[markedRequest.WorkDir] != 0 || cleanupCalls[unmarkedRequest.WorkDir] != 1 {
		t.Fatalf("cleanup calls = %v", cleanupCalls)
	}
}

func TestRunningPhaseIsVisibleAndClearedAtTerminal(t *testing.T) {
	phaseSet := make(chan struct{}, 1)
	release := make(chan struct{})
	runner := runnerFunc(func(_ context.Context, _ RunRequest, observer Observer) error {
		observer.Phase(PhaseConverting)
		phaseSet <- struct{}{}
		<-release
		return nil
	})
	m := newTestManager(t, runner, Config{})
	submit(t, m, "phase", "")
	receive(t, phaseSet)
	running, err := m.Snapshot("phase")
	if err != nil || running.Status != StatusRunning || running.Phase != PhaseConverting {
		t.Fatalf("running phase snapshot = %+v, %v", running, err)
	}
	close(release)
	terminal := waitTerminal(t, m, "phase")
	if terminal.Status != StatusDone || terminal.Phase != "" {
		t.Fatalf("terminal phase snapshot = %+v", terminal)
	}
}

func TestCleanupExactlyOnceOnEveryTerminalPath(t *testing.T) {
	clock := newManualClock()
	started := make(chan string, 4)
	invoked := make(chan string, 8)
	runner := runnerFunc(func(ctx context.Context, request RunRequest, _ Observer) error {
		invoked <- request.ID
		switch request.Preset {
		case "failed":
			return errors.New("runner failed")
		case "wait":
			started <- request.ID
			<-ctx.Done()
			return ctx.Err()
		default:
			return nil
		}
	})
	var mu sync.Mutex
	cleanupCalls := make(map[string]int)
	m := newTestManager(t, runner, Config{
		Clock:      clock,
		RunTimeout: time.Minute,
		Cleanup: func(workDir string) error {
			mu.Lock()
			cleanupCalls[workDir]++
			mu.Unlock()
			return nil
		},
	})

	for _, test := range []struct {
		id     string
		preset string
	}{
		{id: "done"},
		{id: "failed", preset: "failed"},
	} {
		submit(t, m, test.id, test.preset)
		waitTerminal(t, m, test.id)
	}

	submit(t, m, "cancelled", "wait")
	if id := receive(t, started); id != "cancelled" {
		t.Fatalf("started = %q, want cancelled", id)
	}
	if err := m.Cancel("cancelled"); err != nil {
		t.Fatalf("cancel running: %v", err)
	}
	waitTerminal(t, m, "cancelled")

	submit(t, m, "timeout", "wait")
	if id := receive(t, started); id != "timeout" {
		t.Fatalf("started = %q, want timeout", id)
	}
	clock.Advance(time.Minute)
	waitTerminal(t, m, "timeout")

	submit(t, m, "blocker", "wait")
	if id := receive(t, started); id != "blocker" {
		t.Fatalf("started = %q, want blocker", id)
	}
	submit(t, m, "queued-cancel", "")
	if err := m.Cancel("queued-cancel"); err != nil {
		t.Fatalf("cancel queued: %v", err)
	}
	if err := m.Cancel("blocker"); err != nil {
		t.Fatalf("cancel blocker: %v", err)
	}
	waitTerminal(t, m, "blocker")

	for _, id := range []string{"done", "failed", "cancelled", "timeout", "blocker", "queued-cancel"} {
		snapshot, err := m.Snapshot(id)
		if err != nil {
			t.Fatalf("snapshot %s: %v", id, err)
		}
		mu.Lock()
		calls := cleanupCalls[snapshot.WorkDir]
		mu.Unlock()
		if calls != 1 {
			t.Fatalf("cleanup calls for %s = %d, want 1", id, calls)
		}
	}

	close(invoked)
	for id := range invoked {
		if id == "queued-cancel" {
			t.Fatal("queued-cancel invoked Runner")
		}
	}
}

func TestCleanupFailureRecordsWarningWithoutChangingStatus(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	m := newTestManager(t,
		runnerFunc(func(context.Context, RunRequest, Observer) error { return nil }),
		Config{Cleanup: func(string) error {
			mu.Lock()
			calls++
			mu.Unlock()
			return errors.New("disk busy")
		}},
	)

	submit(t, m, "warning", "")
	snapshot := waitTerminal(t, m, "warning")
	if snapshot.Status != StatusDone {
		t.Fatalf("status = %s, want done", snapshot.Status)
	}
	if len(snapshot.Warnings) != 1 || snapshot.Warnings[0].Code != WarningCodeCleanupFailed {
		t.Fatalf("warnings = %+v, want cleanup_failed", snapshot.Warnings)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Fatalf("cleanup calls = %d, want 1", calls)
	}
}

func TestCloseStopsWorkerCleansJobsAndClosesSubscribers(t *testing.T) {
	started := make(chan string, 1)
	runnerStopped := make(chan error, 1)
	runner := runnerFunc(func(ctx context.Context, request RunRequest, _ Observer) error {
		started <- request.ID
		<-ctx.Done()
		runnerStopped <- context.Cause(ctx)
		return ctx.Err()
	})
	var mu sync.Mutex
	cleanupCalls := make(map[string]int)
	m := newTestManager(t, runner, Config{Cleanup: func(workDir string) error {
		mu.Lock()
		cleanupCalls[workDir]++
		mu.Unlock()
		return nil
	}})

	submit(t, m, "running", "")
	if id := receive(t, started); id != "running" {
		t.Fatalf("started = %q, want running", id)
	}
	queued := submit(t, m, "queued", "")
	_, events, unsubscribe, err := m.Subscribe("running")
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer unsubscribe()

	closed := make(chan struct{})
	go func() {
		m.Close()
		close(closed)
	}()
	receiveOK(t, closed)
	if cause := receive(t, runnerStopped); !errors.Is(cause, ErrManagerClosed) {
		t.Fatalf("runner stop cause = %v, want ErrManagerClosed", cause)
	}
	if _, ok := receiveOK(t, events); ok {
		for {
			if _, ok := receiveOK(t, events); !ok {
				break
			}
		}
	}

	running, err := m.Snapshot("running")
	if err != nil {
		t.Fatalf("snapshot running: %v", err)
	}
	queuedAfter, err := m.Snapshot("queued")
	if err != nil {
		t.Fatalf("snapshot queued: %v", err)
	}
	if running.Status != StatusFailed || running.ErrorCode != ErrorCodeManagerClosed {
		t.Fatalf("running after Close = %s/%s", running.Status, running.ErrorCode)
	}
	if queuedAfter.Status != StatusCancelled {
		t.Fatalf("queued after Close = %s, want cancelled", queuedAfter.Status)
	}
	mu.Lock()
	if cleanupCalls[running.WorkDir] != 1 || cleanupCalls[queued.WorkDir] != 1 {
		t.Fatalf("cleanup calls = %+v, want once for both jobs", cleanupCalls)
	}
	mu.Unlock()

	m.Close()
	if _, err := m.Submit(RunRequest{ID: "after-close"}); !errors.Is(err, ErrManagerClosed) {
		t.Fatalf("Submit after Close error = %v, want ErrManagerClosed", err)
	}
}

func TestRunnerErrorCodeAndPanicBecomeFailed(t *testing.T) {
	runner := runnerFunc(func(_ context.Context, request RunRequest, _ Observer) error {
		if request.ID == "coded" {
			return &RunError{Code: "conversion_failed", Err: errors.New("bad audio")}
		}
		panic("bad runner")
	})
	m := newTestManager(t, runner, Config{})

	for _, test := range []struct {
		id   string
		code ErrorCode
	}{
		{id: "coded", code: "conversion_failed"},
		{id: "panic", code: ErrorCodeRunFailed},
	} {
		submit(t, m, test.id, "")
		snapshot := waitTerminal(t, m, test.id)
		if snapshot.Status != StatusFailed || snapshot.ErrorCode != test.code {
			t.Fatalf("%s state = %s/%s, want failed/%s", test.id, snapshot.Status, snapshot.ErrorCode, test.code)
		}
	}
}

func newTestManager(t *testing.T, runner Runner, config Config) *Manager {
	t.Helper()
	if config.TempRoot == "" {
		config.TempRoot = t.TempDir()
	}
	m, err := NewManager(runner, config)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	t.Cleanup(m.Close)
	return m
}

func submit(t *testing.T, manager *Manager, id, preset string) Snapshot {
	t.Helper()
	return submitRequest(t, manager, RunRequest{ID: id, Preset: preset})
}

func submitRequest(t *testing.T, manager *Manager, request RunRequest) Snapshot {
	t.Helper()
	snapshot, err := manager.Submit(request)
	if err != nil {
		t.Fatalf("Submit %s: %v", request.ID, err)
	}
	return snapshot
}

func waitTerminal(t *testing.T, manager *Manager, id string) Snapshot {
	t.Helper()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	for {
		snapshot, events, unsubscribe, err := manager.Subscribe(id)
		if err != nil {
			t.Fatalf("Subscribe %s: %v", id, err)
		}
		if isTerminal(snapshot.Status) && !snapshot.CleanupPending {
			unsubscribe()
			return snapshot
		}

		for {
			select {
			case _, ok := <-events:
				if !ok {
					unsubscribe()
					goto resubscribe
				}
			case <-deadline.C:
				unsubscribe()
				t.Fatalf("timed out waiting for terminal job %s", id)
			}
		}
	resubscribe:
	}
}

func receive[T any](t *testing.T, channel <-chan T) T {
	t.Helper()
	value, ok := receiveOK(t, channel)
	if !ok {
		t.Fatal("channel closed before receiving value")
	}
	return value
}

func receiveOK[T any](t *testing.T, channel <-chan T) (T, bool) {
	t.Helper()
	select {
	case value, ok := <-channel:
		return value, ok
	case <-time.After(3 * time.Second):
		var zero T
		t.Fatal("timed out waiting for channel")
		return zero, false
	}
}
