// Package app holds the application model and its machinery: the app builder, the command-line
// commands, the worker and the sites. It corresponds to the internals of the Rust crate forgebase.
//
// Implemented in P2.2 (the command-line skeleton, for the stock binary, which has no apps): the
// argument parser and the help (cli.go, cli_help.go and the command tree in cli_commands.go, which
// reproduce clap 4), the config file and the sites (config.go), the DocType directory and its
// composition (doctypes.go, compose.go), `migrate` and its report (report.go), the `doc` commands
// (run.go) and the error type (error.go). Run is the entry point of cmd/forgebase. The commands that
// are not ported yet are parsed completely and fail with the phase that brings them (run.go, notYet).
//
// Added in P3.1: `doc list` (run.go), and the `user` and `token` commands (cli_auth.go).
//
// Added in P4: the app model (apps.go: App and Binary, which the root package forgebase re-exports as type aliases),
// the composition of apps (compose.go), the apps of a site (config.go, run.go), and `new-app` (new_app.go and
// templates/). The design is ADR 0028.
//
// What is public. The root package forgebase re-exports several types of this package as type aliases. An
// alias exposes the whole type: every exported field and method of an aliased type is public API (the list is
// in ADR 0028). Code that only the framework needs must be a free function or unexported, never an exported
// method of such a type. The methods are documented here: `go doc ./internal/app App`.
//
// Import direction: app may import internal/httpapi, internal/db and internal/core. It must not
// import the root package forgebase; the root package imports app and re-exports the public API.
package app
