// Package db holds PostgreSQL access: the connection pool, schema sync, framework migrations,
// documents, authentication storage and the job queue. It corresponds to the Rust crate
// forgebase-db.
//
// Implemented so far (P1.2): Connect and ConnectSized (the pool), Error, QuoteIdent,
// MigrateFramework with the embedded migrations/*.sql (byte-identical to the Rust files: a database
// migrated by one implementation is served by the other), and Sync, the additive DocType schema
// sync. The test helper is the package internal/db/dbtest.
//
// Import direction: db may import internal/core. It must not import internal/httpapi,
// internal/app or the root package forgebase.
package db
