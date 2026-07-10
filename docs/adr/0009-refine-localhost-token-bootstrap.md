# 0009: Refine localhost token bootstrap and threat boundary

Status: Superseded

Superseded by: 0010-keep-bootstrap-token-out-of-launcher-argv.md

Date: 2026-07-10

Supersedes: 0004-localhost-security-boundary.md

## Context

The original design put the bearer token in the startup query URL, stdout, and browser-launch argv. This protects against remote web origins and DNS rebinding, but leaves the token in browser history and process/log surfaces. Fully protecting a secret from a malicious process running as the same OS user is not a realistic boundary for this local desktop workflow.

## Decision

The threat model covers malicious web pages, DNS rebinding, other OS users, malformed local requests, resource exhaustion, and untrusted download responses. A malicious process already running with the same OS user privileges is out of scope.

The browser receives the startup token in a URL fragment. The SPA reads it into memory and immediately removes it with `history.replaceState`. Normal API requests accept only `X-Auth-Token`; EventSource SSE endpoints alone accept a query token. Responses set `Referrer-Policy: no-referrer` and `Cache-Control: no-store`. Normal successful startup prints only the token-free base URL; `--no-browser` or browser-launch failure may print the bootstrap URL.

Host, Origin, loopback bind, constant-time token comparison, path confinement, streaming limits, and no-shell child execution remain mandatory.

## Consequences

- The unavoidable launcher argv exposure is an accepted consequence of the same-user threat exclusion.
- Token persistence in URL history, ordinary request queries, referrers, and normal logs is reduced.
- M1-1 and M1-5 tests must distinguish ordinary API authentication from SSE authentication.
