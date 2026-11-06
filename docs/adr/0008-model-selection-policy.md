# 0008 Model selection policy for the agent workflow

- Status: Accepted by owner
- Date: 2026-10-28

## Context

Forgebase is developed with Claude agents under a four-stage workflow. Stages differ in the cost of a wrong decision, so the owner provided a policy for choosing models per stage.

## Decision

Adopt the owner-provided Model Selection Policy. Summary only; the normative text is [AGENTS.md, section 9](../../AGENTS.md#9-model-selection-policy).

| Stage | Preferred model |
|---|---|
| 1 Plan | Opus 5.5 |
| 2 Doing | Sonnet 5.5 |
| 3 Review | Opus 5.5 |
| 4 Confirmation | Sonnet 5.5 or Haiku 5.5 |
| Escalation | Fable 5.1 (selective, cannot override approved decisions) |

Key rules: verify availability and disclose any substitution; never claim a model was used unless it was; models do not replace independent review or the approval gates; at the start of each stage state the stage, the model actually used (if verifiable), the objective and whether approval is needed; at the end report work done, checks, status, any model substitution and open issues.

## Alternatives considered

None; the policy was provided by the owner.

## Consequences

- AGENTS.md is the single source of truth; other documents link to it.
- Changing the policy requires the owner's approval and an update to AGENTS.md (and a superseding ADR).
