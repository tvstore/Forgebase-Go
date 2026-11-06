# 0002 Modular monolith and workspace layout

- Status: Accepted. Amended by ADR 0012 (2026-10-29): forgebase-cli renamed to forgebase, now lib + bin (see [ADR 0012](0012-app-model-and-public-api-boundary.md))
- Date: 2026-10-28

## Context

The owner's principles call for a modular monolith first and no premature fragmentation ([AGENTS.md](../../AGENTS.md#2-core-engineering-principles)). Frappe's runtime needs several processes (web, workers, scheduler, Redis, Node realtime); see the [feature matrix](../blueprint/feature-matrix.md).

## Decision

One Cargo workspace with four crates: `forgebase-core` (pure metadata, validation, lifecycle definitions, permission evaluator; no I/O, async or SQL), `forgebase-db` (PostgreSQL access, schema sync, migrations, job queue), `forgebase-http` (axum routes, auth middleware, REST API), `forgebase-cli` (the `forgebase` binary). Dependency direction: cli → http → db → core; core depends on nothing internal. A React + TypeScript `desk/` is added at M4. Jobs and scheduler are a module in `forgebase-db` plus a `worker` command. Do not split further until a real boundary problem appears.

## Alternatives considered

- Single crate: weaker boundaries; core would not be forced to stay free of I/O.
- Many fine-grained crates: more release and dependency overhead without a demonstrated need.
- Microservices or a separate jobs service: rejected as unneeded complexity.

## Consequences

- The business-rule core is unit-testable without a database.
- Crate boundaries must be respected in review; adding a crate needs a justification.
- See [architecture](../blueprint/architecture.md).
