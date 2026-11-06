// Package app holds the application model and its machinery: the app builder, the command-line
// commands, the worker and the sites. It corresponds to the internals of the Rust crate forgebase.
//
// Import direction: app may import internal/httpapi, internal/db and internal/core. It must not
// import the root package forgebase; the root package imports app and re-exports the public API.
package app
