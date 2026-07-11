# 0016: Waive physical Mac validation for v0.1.1

Status: Accepted

Date: 2026-07-12

## Context

Version `v0.1.1` is a patch release for the browser session reload defect fixed
after `v0.1.0`. The change was exercised with the complete stub and handler
suite, GitHub-hosted macOS build/startup CI, and Firefox on Ubuntu, including
same-tab reload and stale SSE authentication recovery.

The owner still has no physical Apple Silicon Mac and no opportunity to use
one. The physical M5 procedure therefore remains unexecuted. ADR 0013 waived
that procedure only for `v0.1.0` and explicitly prevented later releases from
inheriting its decision automatically.

## Decision

The owner explicitly waives physical M5 validation for `v0.1.1` and authorizes
publication with every physical-Mac M5 item still marked not verified. This is
a version-specific release exception, not a successful M5 result. The owner
made this decision on 2026-07-12 after reviewing the reload fix, its independent
review, automated checks, and the continued absence of physical Mac access.

The unverified scope remains the complete physical workflow: real lecture
media, both presets, VAD, txt/srt/vtt output, progress and logs, cancellation,
browser downloads, bootstrap recovery on macOS, Apple Silicon behavior, 8 GB
large-v3 suitability, Downloads permissions, automatic browser launch, and
real model download timing and SHA256 verification.

Publication requires green source CI, a tag that resolves to the reviewed
public `main` commit, an anonymously downloadable archive with a verified
SHA256, a Homebrew Formula PR pinned to that archive, green macOS source-install
and `brew test` CI, and a post-publication `brew upgrade` version check. Release
notes and user documentation must retain the physical-validation warning.

## Consequences

- `v0.1.1` may be published after every non-physical gate passes.
- GitHub-hosted macOS and Homebrew checks remain packaging evidence only.
- The reload fix can reach existing Homebrew installations without claiming
  physical GUI or workload compatibility.
- No preset decision is made without 8 GB physical-Mac evidence.
- Later releases do not inherit this waiver automatically.
