// Package core holds the pure part of Forgebase: metadata (DocTypes, fields, overlays, workflow),
// validation, lifecycle definitions and permission evaluation. It does no I/O: no SQL, no network,
// no file access. It corresponds to the Rust crate forgebase-core.
//
// Import direction: core imports only the standard library and never another Forgebase package.
package core
