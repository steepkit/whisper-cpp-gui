//go:build linux || darwin

package exec

import (
	"context"
	"errors"
	"fmt"
	osexec "os/exec"
	"syscall"
	"time"
)

const processGroupPollInterval = 10 * time.Millisecond

// Run executes spec directly in a new process group. If ctx is cancelled,
// the whole group receives SIGTERM, followed by SIGKILL after the grace
// period if any group member remains.
func (r *ProcessRunner) Run(ctx context.Context, spec CommandSpec) error {
	if cause := context.Cause(ctx); cause != nil {
		return fmt.Errorf("running %q cancelled before start: %w", spec.Path, cause)
	}

	cmd := osexec.Command(spec.Path, spec.Args...)
	cmd.Dir = spec.Dir
	cmd.Env = spec.Env
	cmd.Stdout = spec.Stdout
	cmd.Stderr = spec.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		if cause := context.Cause(ctx); cause != nil {
			return fmt.Errorf("starting %q while cancelled: %w", spec.Path, errors.Join(cause, err))
		}
		return fmt.Errorf("starting %q: %w", spec.Path, err)
	}

	waitCh := make(chan error, 1)
	go func() {
		waitCh <- cmd.Wait()
	}()

	select {
	case err := <-waitCh:
		if cause := context.Cause(ctx); cause != nil {
			// Cancellation and Wait can become ready together. The direct child
			// is already reaped on this path, but descendants may still be alive
			// in its process group and must receive the same TERM/KILL sequence.
			return r.terminateAndWait(spec.Path, cmd.Process.Pid, cause, waitCh, true, err)
		}
		if err != nil {
			return fmt.Errorf("running %q: %w", spec.Path, err)
		}
		return nil
	case <-ctx.Done():
		return r.terminateAndWait(spec.Path, cmd.Process.Pid, context.Cause(ctx), waitCh, false, nil)
	}
}

func (r *ProcessRunner) terminateAndWait(
	path string,
	processGroupID int,
	cause error,
	waitCh <-chan error,
	parentReaped bool,
	waitErr error,
) error {
	var lifecycleErrs []error
	if err := signalProcessGroup(processGroupID, syscall.SIGTERM); err != nil {
		lifecycleErrs = append(lifecycleErrs, fmt.Errorf("sending SIGTERM: %w", err))
	}

	graceTimer := time.NewTimer(r.gracePeriod())
	defer graceTimer.Stop()
	pollTimer := time.NewTicker(processGroupPollInterval)
	defer pollTimer.Stop()

	groupCheckFailed := false
	for {
		if parentReaped {
			alive, err := processGroupAlive(processGroupID)
			if err != nil && !groupCheckFailed {
				lifecycleErrs = append(lifecycleErrs, fmt.Errorf("checking process group: %w", err))
				groupCheckFailed = true
			}
			if err == nil && !alive {
				return cancelledProcessError(path, cause, waitErr, lifecycleErrs)
			}
		}

		select {
		case waitErr = <-waitCh:
			parentReaped = true
		case <-pollTimer.C:
			// Polling only matters after the direct child has been reaped. It
			// lets well-behaved descendants exit without consuming the full grace.
		case <-graceTimer.C:
			if err := signalProcessGroup(processGroupID, syscall.SIGKILL); err != nil {
				lifecycleErrs = append(lifecycleErrs, fmt.Errorf("sending SIGKILL: %w", err))
			}
			if !parentReaped {
				waitErr = <-waitCh
			}
			return cancelledProcessError(path, cause, waitErr, lifecycleErrs)
		}
	}
}

func signalProcessGroup(processGroupID int, signal syscall.Signal) error {
	err := syscall.Kill(-processGroupID, signal)
	if err == nil || errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

func processGroupAlive(processGroupID int) (bool, error) {
	err := syscall.Kill(-processGroupID, 0)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, syscall.ESRCH):
		return false, nil
	case errors.Is(err, syscall.EPERM):
		return true, nil
	default:
		return true, err
	}
}

func cancelledProcessError(path string, cause, waitErr error, lifecycleErrs []error) error {
	if cause == nil {
		cause = context.Canceled
	}
	errs := []error{cause}
	if waitErr != nil {
		errs = append(errs, fmt.Errorf("waiting for process: %w", waitErr))
	}
	errs = append(errs, lifecycleErrs...)
	return fmt.Errorf("running %q cancelled: %w", path, errors.Join(errs...))
}
