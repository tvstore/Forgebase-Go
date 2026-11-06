// Package db holds PostgreSQL access: the connection pool, schema sync, framework migrations,
// documents, authentication storage and the job queue. It corresponds to the Rust crate
// forgebase-db.
//
// Implemented so far (P1.2): Connect and ConnectSized (the pool), Error, QuoteIdent,
// MigrateFramework with the embedded migrations/*.sql (byte-identical to the Rust files: a database
// migrated by one implementation is served by the other), and Sync, the additive DocType schema
// sync. The test helper is the package internal/db/dbtest.
//
// Implemented in P2.1: the document pipeline (document.go, docstatus.go, links.go, naming.go,
// access.go, scope.go, field_levels.go, sql.go and value.go): Insert, Get, Update, Delete, Submit,
// Cancel and Amend with links, child tables, lifecycle hooks, naming series and the read scope; the
// outbox of jobs and webhook deliveries (outbox.go); the JSON form of a document (json.go).
//
// Implemented in P2.2: Migrate (migrate_all.go), the framework migrations and the schema sync under
// one session-level lock, with the report type that the app migrations, schedules and fixtures of
// later phases fill in.
//
// Import direction: db may import internal/core. It must not import internal/httpapi,
// internal/app or the root package forgebase.
package db
