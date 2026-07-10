# 0010: Keep the bootstrap token out of launcher argv

Status: Accepted

Date: 2026-07-10

Supersedes: 0009-refine-localhost-token-bootstrap.md

## Context

ADR 0009 allowed the fragment bootstrap URL in `open` / `xdg-open` argv and in manual fallback stdout because malicious same-user processes are outside the threat model. On systems where process argv is visible across OS users, however, that exposure also lets an in-scope different user steal the bearer token and submit arbitrary jobs. Printing the same URL for `--no-browser` or launch failure can also leak it through captured output.

## Decision

The application writes the token-bearing fragment URL only to an `index.html` file inside a randomly named mode `0700` temporary directory. The file is mode `0600`, sets a no-referrer policy, and redirects the browser to the fragment URL. `open` / `xdg-open` receives only this token-free file path. Manual fallback stdout also prints only the file path.

The file is removed after a successful browser handoff or when the application closes. Fragment removal in the SPA, header-only authentication for ordinary APIs, SSE-only query authentication, and the same-user threat exclusion remain unchanged.

## Consequences

- Process argv and application stdout no longer expose the bearer token to other OS users.
- `--no-browser` users open a private bootstrap file instead of copying a secret URL.
- A crash can leave a private temporary file, but its token becomes invalid when that application process exits.
- Tests must verify directory/file permissions, token-free launcher argv and stdout, and cleanup on close.
