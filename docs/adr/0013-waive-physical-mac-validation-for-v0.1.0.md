# 0013: Waive physical Mac validation for v0.1.0

Status: Accepted

Date: 2026-07-11

## Context

M5 requires a physical Apple Silicon lab Mac, including an 8 GB machine, real
lecture media, real model downloads, Downloads permissions, and browser launch
behavior. The repository owner has no such machine and no opportunity to use
one. Keeping M5 as an absolute release gate would leave the completed project
permanently undistributable.

Automated coverage includes the full stub pipeline on Ubuntu, a real
Linuxbrew `whisper-cli` and `ffmpeg` compatibility check, and macOS 14 build
and startup smoke. The release additionally requires a clean GitHub-hosted
macOS Homebrew source-install gate. These checks do not reproduce the physical
M5 scenarios.

## Decision

The owner explicitly waives physical M5 validation for `v0.1.0` and authorizes
publication with the M5 items still marked not verified. The waiver is
version-specific and is a release exception, not a successful M5 result.
The owner made this decision on 2026-07-11 after confirming that no physical
Apple Silicon Mac is available and there will be no opportunity to use one.

The entire physical M5 procedure remains unexecuted. This includes both presets,
VAD, txt/srt/vtt output, SSE progress and logs, cancellation, browser downloads,
bootstrap recovery, physical Apple Silicon behavior, 8 GB large-v3 suitability,
Downloads permissions, automatic browser launch, and real model download timing
and SHA256 verification. Release notes and user documentation must disclose this
scope. The current two presets remain unchanged; no evidence-based medium-preset
decision can be made.

A clean GitHub-hosted macOS runner may satisfy packaging installation and
`brew test`, but it does not satisfy physical GUI, memory, or real-workload
validation.

## Consequences

- `v0.1.0` may be published after all non-physical gates pass.
- Users receive an explicit warning instead of an unsupported claim of Mac
  validation.
- Failures found later on physical Apple Silicon are handled in a patch release.
- Future releases do not inherit this waiver automatically.
