# 0008: Use a tool-neutral active orchestrator

Status: Accepted

Date: 2026-07-10

## Context

The initial workflow named one Claude Code session, Fable, as the permanent orchestrator. That session became unavailable while implementation was in progress. Review gates and ownership rules must survive a change of agent product or session.

## Decision

The project has one active Orchestrator / Chief Engineer at a time. Claude Code or Codex may fill the role. The active orchestrator owns decomposition, delegation, integration, verification, review records, and recommendations to the human Product Owner. Independent review must run in a separate context and keep the G0/G2 blind-review discipline.

`AGENTS.md`, `docs/implementation_plan.md`, and `docs/agentic_workflow.md` are tool-neutral. `CLAUDE.md` only adapts the common workflow when Claude Code is active.

## Consequences

- Work can resume after an agent/session outage without changing engineering gates.
- Subagent names may differ by tool, but verifier/reviewer/engineer responsibilities remain stable.
- Human approval remains the final authority for the listed G5 decisions.

