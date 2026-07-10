# 0002: Use Go standard library and vanilla web assets

Status: Accepted

Date: 2026-07-09

## Context

The app is for local lab distribution and should be maintainable as a small source-built Homebrew package. Extra frameworks and build chains increase supply-chain, packaging, and long-term maintenance cost.

## Decision

The backend uses only the Go standard library. Routing uses Go 1.22+ `http.ServeMux`. The frontend uses vanilla HTML, CSS, and JavaScript embedded into the Go binary. React/Vue/Svelte, npm/bundlers, CDN assets, Electron, DBs, Docker, Python runtime dependencies, cgo, and additional Go module dependencies are out of scope unless humans explicitly approve a specification change.

## Consequences

- The app remains easy to build from source through Homebrew.
- Tests and harness checks must use standard Go tooling where possible.
- Some convenience tooling, such as Playwright-based UI tests or golangci-lint, is not part of the default project dependency set.

