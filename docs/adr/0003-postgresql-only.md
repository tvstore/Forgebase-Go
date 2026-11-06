# 0003 PostgreSQL only

- Status: Accepted
- Date: 2026-10-28

## Context

The owner's initial direction names PostgreSQL. Frappe's primary database is MariaDB (Unverified, forum-sourced); its PostgreSQL support status could not be confirmed from official docs (see [feature matrix](../blueprint/feature-matrix.md)). Forgebase relies on mostly transactional DDL, `SELECT ... FOR UPDATE SKIP LOCKED`, advisory locks, `numeric` and `timestamptz`, and may use `LISTEN/NOTIFY` later.

## Decision

Support PostgreSQL only. Pin a supported major version (for example 15 or later; exact floor Undecided, decided when the related milestone is planned). No abstraction layer over multiple database engines.

## Alternatives considered

- Multi-database support (MariaDB/SQLite): multiplies the test matrix and blocks use of Postgres-specific features that remove Redis and extra services.
- SQLite for development: different DDL and locking semantics would hide bugs.

## Consequences

- Simpler code and tests; one datastore for data, jobs, sessions and (later) realtime.
- Users who cannot run PostgreSQL are not served.
- Tests run against a real PostgreSQL ([maintenance-policy](../blueprint/maintenance-policy.md#testing-strategy)).
