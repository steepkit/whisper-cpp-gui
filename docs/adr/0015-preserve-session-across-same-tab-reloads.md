# 0015: Preserve sessions across same-tab reloads

Status: Accepted

Date: 2026-07-11

Supersedes: 0010-keep-bootstrap-token-out-of-launcher-argv.md

## Context

ADR 0010 kept the startup bearer token only in JavaScript memory after removing it from the URL. Reloading the page therefore discarded authentication while the local server and a potentially long-running transcription job continued. Restarting the application to obtain a new bootstrap token also cancels active work, so memory-only storage makes an ordinary browser reload operationally unsafe.

## Decision

The private mode `0700` bootstrap directory, mode `0600` redirect file, fragment transfer, immediate `history.replaceState`, token-free launcher argv/stdout, header-only ordinary API authentication, and SSE-only query authentication remain mandatory.

After validating the 64-character lowercase hexadecimal fragment token, the SPA stores it in origin- and tab-scoped `sessionStorage`. On a same-tab reload with no fragment, the SPA validates and restores that token. A malformed token fragment is removed and falls back to an already stored valid token instead of destroying a recoverable session.

The active job ID is stored in the same session scope; after reload the SPA verifies that the job still exists and reconnects to its SSE snapshot. Transient lookup failures use a short bounded backoff. Invalid stored values, unauthorized ordinary API responses, unauthorized SSE connections confirmed by a header-authenticated API probe, and missing jobs clear the corresponding session entries. If browser storage is unavailable, the current page remains usable with the fragment token but reload recovery is unavailable.

The application does not store authentication in `localStorage` or cookies. Server-side token generation, validation, Host/Origin checks, and process lifetime remain unchanged.

## Consequences

- Reloading the original tab preserves API access and restores progress, cancellation, logs, and outputs for the active or most recent retained job.
- A new tab or a manually entered token-free URL is not guaranteed to inherit the page session and still requires the private bootstrap path.
- Browser session restoration may retain `sessionStorage`, but the token is useful only while the matching application process and origin remain alive. This is an accepted residual risk within the existing threat model.
- Embedded same-origin scripts can read the token, so the self-only CSP, absence of third-party scripts, and UI string handling remain security requirements.
- Tests must cover fragment removal and fallback, session-only storage, ordinary/SSE authentication failure, stale-entry clearing, bounded restoration retry, and active-job SSE reconnection.
