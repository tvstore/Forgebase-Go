// Package core holds the pure part of Forgebase: metadata (DocTypes, fields, overlays, workflow),
// validation, lifecycle definitions and permission evaluation. It does no I/O: no SQL, no network,
// no file access. It corresponds to the Rust crate forgebase-core, one Go file per Rust module:
//
//	ident.go       identifier rules and table naming
//	meta.go        the DocType model, its validation and the Registry
//	load.go        TOML loading (DocType files, overlays, single fields)
//	value.go       the dynamic value type of document fields
//	document.go    documents and their validation against a DocType
//	series.go      naming series patterns
//	workflow.go    the states and transitions of a DocType's workflow
//	permission.go  rights, the Caller and the evaluators (the one place that decides access)
//	hooks.go       lifecycle hooks as an interface with a no-op base struct
//	overlay.go     fields that an app or a site adds to a DocType
//	report.go      the definition of an app-defined query report
//	method.go      the definition of an app method endpoint
//
// A Registry and the DocTypes, reports and methods it holds are built at startup and are read-only
// afterwards; they are shared by pointer between goroutines and must not be changed once added (a
// change makes a copy: DocType.Clone, Registry.Clone).
//
// What is public. The root package forgebase re-exports several types of this package (the hook types,
// Document, Row, Value, HookContext, TransitionInfo, EnqueuedJobs) as type aliases. An alias exposes the whole
// type: every exported field and method of an aliased type is public API (the list is in ADR 0028), so code
// that only the framework needs must be a free function or unexported, never an exported method of such a type.
//
// Import direction: core imports only the standard library, github.com/BurntSushi/toml and
// internal/decimal (a leaf package without I/O), and never another Forgebase package (ADR 0026, ADR
// 0027).
package core
