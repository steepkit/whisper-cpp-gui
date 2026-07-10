package exec

import (
	"io"
	"time"
)

const defaultTerminationGrace = 5 * time.Second

// CommandSpec describes one direct child-process invocation. Path is executed
// with Args as-is; no shell is involved. A nil Env inherits the current
// process environment, matching os/exec.Cmd semantics.
type CommandSpec struct {
	Path   string
	Args   []string
	Dir    string
	Env    []string
	Stdout io.Writer
	Stderr io.Writer
}

// ProcessRunner runs commands and owns their process-group lifecycle.
type ProcessRunner struct {
	terminationGrace time.Duration
}

// NewProcessRunner returns a runner with the production termination grace.
func NewProcessRunner() *ProcessRunner {
	return &ProcessRunner{terminationGrace: defaultTerminationGrace}
}

// newProcessRunner allows process tests to shorten the termination grace.
func newProcessRunner(terminationGrace time.Duration) *ProcessRunner {
	return &ProcessRunner{terminationGrace: terminationGrace}
}

func (r *ProcessRunner) gracePeriod() time.Duration {
	if r == nil || r.terminationGrace <= 0 {
		return defaultTerminationGrace
	}
	return r.terminationGrace
}
