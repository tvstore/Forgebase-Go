// Package forgebase is the public API of Forgebase, a framework for building business applications
// from metadata (DocTypes), written in Go.
//
// This package and its sibling package forgebase/testing (the directory testing/) are the only
// public Go API of the module (ADR 0026, ADR 0012). An application is a Go module that imports this
// package and nothing else from the framework. Every other package lives under internal/ and cannot
// be imported from outside the module.
//
// The import direction is: cmd -> forgebase (this package) -> internal/app -> internal/httpapi ->
// internal/db -> internal/core. Packages under internal/ never import this package.
//
// The package is a skeleton in phase P0; the API arrives phase by phase.
package forgebase

// Version is the version of the framework. It is the value printed by `forgebase --version`.
const Version = "0.0.0"
