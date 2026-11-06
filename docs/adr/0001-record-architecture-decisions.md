# 0001 Record architecture decisions

- Status: Accepted
- Date: 2026-10-28

## Context

Forgebase prioritizes long-term maintainability. Important decisions (database layer, extension model, license) are expensive to reverse and easy to forget or re-litigate. [AGENTS.md](../../AGENTS.md) requires that important decisions be recorded.

## Decision

Record each significant architecture decision as a short Markdown ADR in `docs/adr/`, numbered sequentially, using the template in [README.md](README.md). Accepted ADRs are immutable; they are superseded by a new ADR, not edited.

## Alternatives considered

- Decisions only in the blueprint docs: they change over time and lose the "why".
- Decisions in PR descriptions or chat: hard to find, not versioned with the code.
- A heavier process (RFCs): more overhead than a small project needs.

## Consequences

- A small writing cost per decision, in exchange for a durable history.
- Blueprint documents link to ADRs for status instead of restating it.
