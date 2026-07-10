# 0006: Use explicit Codex sandbox flags

Status: Accepted

Date: 2026-07-10

## Context

Codex can load project `.codex/config.toml` only for trusted projects, and named profiles are resolved from `$CODEX_HOME/<name>.config.toml`. Relying on profile resolution alone makes the effective sandbox depend on local environment state.

## Decision

All scripted Codex invocations must pass `--sandbox read-only` for review/design work or `--sandbox workspace-write` for implementation/rescue work. Profiles may still be used as convenience layers, but they are not the authority for the sandbox boundary. Do not point `CODEX_HOME` at the repository just to load project configuration, because `CODEX_HOME` also contains authentication and local state.

## Consequences

- Review and implementation commands are more verbose but deterministic.
- Profile files remain useful for secondary defaults such as network settings.
- Pre-flight checks must verify Codex version, model availability, profile resolution, explicit sandbox flags, and project config trust/load before Codex is used operationally.
