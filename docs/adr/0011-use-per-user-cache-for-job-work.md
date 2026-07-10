# 0011: Use the per-user cache for recoverable job work

Status: Accepted

Date: 2026-07-10

## Context

A fixed `/tmp/whisper-cpp-gui` parent can be reserved by another OS user before the victim's first launch. On sticky-bit systems the victim can detect the foreign symlink or directory but cannot remove it, so rejecting it still creates a persistent launch denial of service. A process-random directory avoids squatting but cannot support seven-day publish-failure recovery across restarts.

## Decision

Recoverable job work lives under `os.UserCacheDir()/whisper-cpp-gui/jobs`. The application and jobs directories are real directories restricted to mode `0700`, and each job keeps its cryptographically random ID. This stable, user-owned location supports restart cleanup without relying on a predictable name in a shared temporary namespace.

One-shot browser bootstrap files continue to use `os.MkdirTemp()` because their directory names are unpredictable, their permissions are private, and they do not need restart recovery.

## Consequences

- Another OS user cannot reserve the default job path without already being able to modify the victim's cache directory.
- Publish-failure recovery remains discoverable for seven-day cleanup after restart.
- Crash leftovers persist in the user cache until the startup sweep removes them; M1-5 must bound unmarked stale directories as already planned.
- Tests must prove the default root is under `os.UserCacheDir()` and does not depend on a fixed shared-temp name.
