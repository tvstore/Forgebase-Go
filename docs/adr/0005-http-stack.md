# 0005 HTTP stack: axum on tokio

- Status: Accepted
- Date: 2026-10-28

## Context

The HTTP layer must be thin, fit JSON-in/JSON-out documents, and add few dependencies. Evidence: [dependency-evaluation](../blueprint/dependency-evaluation.md#1-http-server).

## Decision

Use axum (0.8.x) on tokio, with tower-http only as needed (body limits, tracing, CORS, compression). Confine axum types to `forgebase-http`. Enable explicit tokio features rather than `full`. TLS is terminated at a reverse proxy.

## Alternatives considered

- actix-web: stable 4.x line, but 139 packages (about 3x axum), its own middleware model (Unverified), MSRV 1.89.
- poem, rocket: smaller communities; nothing Forgebase needs that axum lacks.
- Other runtimes (smol, glommio): force different HTTP and DB stacks; async-std is deprecated.

## Consequences

- axum is 0.x: expect a breaking migration roughly every 1-2 years; keeping it inside one crate limits the blast radius.
- Middleware comes from the tower ecosystem.

## Implementation note (M3.3, 2026-10-29)

No decision changed. `forgebase-http` uses `axum` 0.8.9 with only the `http1`, `json`, `query` and `tokio` features, and **no `tower-http`**: the body limit is axum's `DefaultBodyLimit`, CORS is deliberately not used (owner decision 8: no CORS), and tracing and compression are not needed yet. Admission notes: [dependency-evaluation](../blueprint/dependency-evaluation.md#admission-notes-for-m33-2026-10-29).
