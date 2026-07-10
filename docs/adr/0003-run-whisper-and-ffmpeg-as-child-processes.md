# 0003: Run whisper-cli and ffmpeg as child processes

Status: Accepted

Date: 2026-07-09

## Context

Embedding whisper.cpp through cgo or a native app wrapper would increase platform-specific complexity. The target users are expected to install Homebrew packages, and tests in this environment cannot rely on real `whisper-cli`, `ffmpeg`, or macOS.

## Decision

The app runs Homebrew-provided `whisper-cli` and `ffmpeg` as child processes through `exec.Command` with explicit argument slices. Shell execution through `sh -c` is forbidden. Tests use fake binaries under `testdata/stubs/`.

## Consequences

- OS-specific process behavior is isolated behind `internal/exec`.
- CI can exercise the full pipeline through stubs.
- Real binary behavior must be recorded in `docs/notes.md` and reflected back into stubs.

