# Architecture Decision Records

An ADR records one significant decision: the context, what was decided, what else was considered, and the consequences. ADRs are immutable once **Accepted**: to change a decision, write a new ADR that supersedes the old one. Process rules: [maintenance-policy](../blueprint/maintenance-policy.md#documentation-and-adr-rules). Design overview: [architecture](../blueprint/architecture.md).

## Template

```markdown
# NNNN Title

- Status: Proposed | Accepted | Accepted by owner | Superseded by NNNN | Rejected
- Date: YYYY-MM-DD

## Context
## Decision
## Alternatives considered
## Consequences
```

## Status values

| Status | Meaning |
|---|---|
| Proposed | Recommendation awaiting owner decision. |
| Accepted | Confirmed by the owner, or a process decision recorded as Accepted. |
| Accepted by owner | An Accepted decision that the owner explicitly made or provided. |
| Superseded by NNNN | Replaced by a later ADR. |
| Rejected | Considered and declined. |

## Index

| ADR | Title | Status |
|---|---|---|
| [0001](0001-record-architecture-decisions.md) | Record architecture decisions | Accepted |
| [0002](0002-modular-monolith-and-workspace-layout.md) | Modular monolith and workspace layout | Accepted (amended by 0012) |
| [0003](0003-postgresql-only.md) | PostgreSQL only | Accepted |
| [0004](0004-database-access-layer.md) | Database access layer | Accepted |
| [0005](0005-http-stack.md) | HTTP stack: axum on tokio | Accepted |
| [0006](0006-extension-model.md) | Extension model: compile-time apps | Accepted |
| [0007](0007-license-mit.md) | License: MIT | Accepted by owner |
| [0008](0008-model-selection-policy.md) | Model selection policy for the agent workflow | Accepted by owner |
| [0009](0009-lifecycle-pipeline-and-hooks.md) | Lifecycle pipeline and hooks | Accepted |
| [0010](0010-authentication-and-sessions.md) | Authentication and sessions | Accepted |
| [0011](0011-permission-model-phase-1.md) | Permission model, phase 1 | Accepted (extended by M9.3: the `export` and `import` rights; by M10.2: field levels, [ADR 0024](0024-field-permission-levels.md); by M10.3: user permissions, [ADR 0025](0025-user-permissions.md)) |
| [0012](0012-app-model-and-public-api-boundary.md) | App model and public API boundary | Accepted |
| [0013](0013-jobs-scheduler-and-transactional-outbox.md) | Jobs, scheduler and transactional outbox | Accepted |
| [0014](0014-multi-site-database-per-site.md) | Multi-site: database per site | Accepted |
| [0015](0015-workflow-states-and-transitions.md) | Workflow: states and transitions in the DocType file | Accepted |
| [0016](0016-query-reports-read-only-execution.md) | Query reports: app-defined SQL, read-only execution | Accepted |
| [0017](0017-desk-styling-tailwind.md) | Desk styling: Tailwind CSS, theme tokens, no component kits | Accepted (supersedes UX-5; amended by the UX.4 section: Inter, copied icons, OFL-1.1 for fonts) |
| [0018](0018-postgres-tls.md) | TLS to PostgreSQL | Accepted by owner |
| [0019](0019-app-method-endpoints.md) | App method endpoints | Accepted (amended by M9.2: reads run in a savepoint) |
| [0020](0020-outgoing-webhooks.md) | Outgoing webhooks | Accepted (amended by M9.3: 16-byte secrets, an unavailable snapshot, the Azure WireServer) |
| [0021](0021-import-and-export.md) | Import and export | Accepted |
| [0022](0022-fixtures.md) | Fixtures | Accepted |
| [0023](0023-metadata-overlays.md) | Metadata overlays: app overlays, site custom fields, live reload | Accepted |
| [0024](0024-field-permission-levels.md) | Field permission levels: `perm_level`, rule `level`, hidden fields are unknown | Accepted |
| [0025](0025-user-permissions.md) | User permissions: restrict a user to some documents, `row_scope`, SQL on every path | Accepted |
