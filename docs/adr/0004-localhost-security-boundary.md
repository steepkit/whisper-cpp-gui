# 0004: Enforce localhost security boundaries

Status: Superseded

Superseded by: 0009-refine-localhost-token-bootstrap.md

Date: 2026-07-09

## Context

The app is local-only, but a localhost HTTP server still has attack surface: DNS rebinding, cross-origin requests, token bypass, path traversal, resource exhaustion, and accidental file exposure.

## Decision

The server binds only to `127.0.0.1`. All `/api/*` routes require a startup-generated random token. Host headers must be `127.0.0.1:{port}` or `localhost:{port}`. Non-empty Origin headers must match the same allowed localhost origins; empty Origin is allowed for CLI/curl and requests that do not send Origin. Downloads must stay under the job output directory after path cleaning. Uploads stream through `MultipartReader()` and have a default size limit. Child processes never go through a shell.

## Consequences

- Even local API endpoints must have authentication and request-origin tests.
- SSE uses query token because browser `EventSource` cannot set custom headers; this accepted risk must be documented.
- Resource limits and cleanup are part of security, not just UX.
