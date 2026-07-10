// Package job implements the job state machine
// (queued -> running -> done|failed|cancelled), the in-memory job store,
// and the serial execution queue with cancellation.
//
// Layer rule: job may depend on exec; it must not reference HTTP concepts.
package job
