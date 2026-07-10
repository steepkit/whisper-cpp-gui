# 0005: Use Codex GPT-5.5 xhigh for peer review

Status: Superseded

Superseded by: 0007-select-codex-model-by-task-risk.md

Date: 2026-07-09

## Context

The project uses Claude Code as orchestrator and Codex as an independent peer engineer. Review quality matters more than latency for security-sensitive code and rescue work.

## Decision

Codex invocations use `gpt-5.5` with `model_reasoning_effort="xhigh"`. Commands must explicitly pass `--model gpt-5.5` and `-c 'model_reasoning_effort="xhigh"'`, or use a profile file that fixes the same settings.

## Consequences

- Review and rescue runs are slower and costlier, but more deliberate.
- Codex output is advisory peer review. Pass/fail remains based on tests, acceptance criteria, CI, and human approval.
- Before implementation work starts, humans must confirm the active Codex account can use the required model and effort setting.
