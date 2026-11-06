# 0007 License: MIT

- Status: Accepted by owner
- Date: 2026-10-28
- Note (2026-10-31): the repository was transferred to `tvstore/Forgebase` and made public by the owner ([owner decisions](../owner-decisions.md)); since then Forgebase is open source under MIT. The decision below is unchanged; its wording about the private repository describes the time it was taken.

## Context

The repository already contains an MIT `LICENSE` ("Copyright (c) 2026 Agil Mahendra"). The repository is currently private; the project is intended to be open source. Dependency licenses must be compatible ([dependency-evaluation](../blueprint/dependency-evaluation.md#license-compatibility-with-mit)).

## Decision

Keep the MIT license as is. The owner decided this on 2026-10-28. Until the repository is public, documents say Forgebase is intended to be open source under MIT, not that it is open source.

## Alternatives considered

- Apache-2.0: the owner's instructions named it as the initial candidate, with MIT as the alternative. The owner chose to keep the existing MIT license. No further alternatives were evaluated.

## Consequences

- Dependencies must be MIT-compatible; enforced by cargo-deny ([maintenance-policy](../blueprint/maintenance-policy.md#cargo-deny)).
- Apache-2.0 dependencies require keeping NOTICE files where present.
- Changing the license later requires a new ADR and the owner's approval.
