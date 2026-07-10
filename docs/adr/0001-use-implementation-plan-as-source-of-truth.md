# 0001: Use implementation_plan.md as the source of truth

Status: Accepted

Date: 2026-07-09

## Context

The repository originally had both `plan.md` and `docs/implementation_plan.md`. Keeping two living specification files creates stale references and gives agents competing sources of truth.

## Decision

`docs/implementation_plan.md` is the single source of truth for scope, milestones, task acceptance criteria, risks, and security boundaries. The former `plan.md` content is integrated there, and `plan.md` is not kept.

## Consequences

- Agents only need one specification entry point.
- `AGENTS.md` can stay short and point to `docs/implementation_plan.md`.
- Future changes to scope or acceptance criteria require human approval because they modify the source of truth.

