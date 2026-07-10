# 0012: Bound active localhost HTTP connections

Status: Accepted

Date: 2026-07-10

## Context

Read-header and idle deadlines bound the lifetime of a single localhost connection, but do not bound the number of simultaneous file descriptors and `net/http` goroutines. A different OS user can discover the loopback port and continuously replenish unauthenticated keep-alive connections faster than the idle deadline, exhausting the victim process before request authentication runs.

## Decision

The listener handed to `http.Server` admits at most 128 active connections. Connections above the limit are closed before `net/http` creates a serving goroutine, with a short fixed backoff to avoid an accept/close busy loop. The limit is an internal constant; tests may inject a smaller value through server configuration.

The existing 10-second header deadline, two-hour complete request-body deadline, 120-second idle deadline, and 1 MiB header limit remain mandatory. M1-5 must reconsider response-side deadlines when long-lived SSE and downloads are introduced.

## Consequences

- Unauthenticated clients can still temporarily occupy available slots, but process FD and goroutine use remains bounded.
- Normal browser use has ample headroom while a local connection flood cannot grow memory use without limit.
- Live tests must verify overflow rejection and slot release after a connection closes.
