//go:build !linux && !darwin

package exec

import (
	"context"
	"fmt"
	"runtime"
)

// Run reports unsupported platforms explicitly. Process-group cancellation is
// implemented only for the project's supported macOS and Linux targets.
func (r *ProcessRunner) Run(ctx context.Context, spec CommandSpec) error {
	if cause := context.Cause(ctx); cause != nil {
		return fmt.Errorf("running %q cancelled before start: %w", spec.Path, cause)
	}
	return fmt.Errorf("running child processes is unsupported on %s", runtime.GOOS)
}
