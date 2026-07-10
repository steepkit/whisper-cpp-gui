# 0007: Select Codex model and effort by task risk

Status: Accepted

Date: 2026-07-10

Supersedes: 0005-use-codex-gpt-55-xhigh-for-peer-review.md

## Context

Fixing every Codex invocation to one model and xhigh effort gives consistent reviews but spends unnecessary latency and compute on routine exploration, formatting, and deterministic verification. Model availability also changes over time.

## Decision

The orchestrator selects model and reasoning effort per task. G0/G4, security-sensitive work, difficult design, and rescue use an available frontier model at high or xhigh effort. Routine exploration, bounded implementation, and verification use the least costly setting that remains reliable. Every delegated run records the exact model, effort, prompt template, and sandbox.

The sandbox remains explicit on every invocation: read-only for review/design and workspace-write for implementation/QA. Model selection never changes the permission boundary.

## Consequences

- Critical work retains deep independent review without imposing xhigh cost on every task.
- Review records, rather than a permanent model name, provide reproducibility and auditability.
- Pre-flight validates the model selected for the current task.

