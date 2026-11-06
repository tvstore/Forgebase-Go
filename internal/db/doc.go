// Package db holds PostgreSQL access: the connection pool, schema sync, framework migrations,
// documents, authentication storage and the job queue. It corresponds to the Rust crate
// forgebase-db.
//
// Import direction: db may import internal/core. It must not import internal/httpapi,
// internal/app or the root package forgebase.
package db
