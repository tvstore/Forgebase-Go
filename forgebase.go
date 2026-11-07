// Package forgebase is the public API of Forgebase, a framework for building business applications
// from metadata (DocTypes), written in Go.
//
// This package and its sibling package forgebase/testing (the directory testing/) are the only
// public Go API of the module (ADR 0026, ADR 0012, ADR 0028). An application is a Go module that
// imports this package and nothing else from the framework. Every other package lives under internal/
// and cannot be imported from outside the module. The API is 0.x: it may still change between
// minor versions, and every change is listed in CHANGELOG.md.
//
// # Apps
//
// An [App] is a named set of DocTypes (TOML files embedded with //go:embed), lifecycle hooks and
// SQL data migrations. A binary is built from apps in an explicit order (the main of an application):
//
//	func main() {
//		os.Exit(forgebase.New().App(crm.App()).App(sales.App()).Run())
//	}
//
// There is no global registry, no code generation and no runtime plugin: the list above is the whole
// wiring, and an app is a plain function that returns data:
//
//	//go:embed doctypes/*.toml
//	var doctypes embed.FS
//
//	//go:embed migrations/*.sql
//	var migrations embed.FS
//
//	func App() *forgebase.App {
//		return forgebase.NewApp("sales").
//			Version("1.0.0").
//			DependsOn("crm").
//			DocTypes(doctypes).
//			Hooks("sales_order", OrderRules{}).
//			Migrations(migrations)
//	}
//
// The rules of the composition:
//
//   - Loading order. The DocTypes of all apps in app order, then the DocTypes of the --doctypes DIR
//     directory (the app `local`, which depends on all the others), then the reference checks, then the
//     hooks in app order. The app migrations run at `migrate`, after the schema sync, in app order.
//   - Ownership. Every DocType belongs to the app that defines it (the metadata API says which: "app").
//     A DocType name used by two apps is a startup error. An app cannot change another app's DocType.
//   - Dependencies. DependsOn names apps that must be added to the binary earlier. An app may link to
//     DocTypes of itself and of its dependencies only, and may register hooks for them.
//   - Hooks. For one DocType the hooks run in app order, then in registration order within an app: an
//     app that extends another app's DocType runs after it, and its hooks can reject what the first app
//     accepted.
//   - App migrations. Versioned SQL (files named NNNN_name.sql), once per database, checksummed, each in
//     its own transaction (with the --lock-timeout-ms lock timeout); a changed or removed applied
//     migration, or a new one numbered below an applied one, is refused. fb_app records which app and
//     version were migrated. App code is trusted: the SQL runs with the database role of the binary.
//   - Startup errors. All of the above is checked before anything runs; every problem is reported
//     together ([StartupError], exit code 1).
//
// # Hooks
//
// A hook set is a type that implements [DocTypeHooks]. Embed [BaseHooks], whose methods do nothing,
// and override the points you need. Hooks get the document ([Document], [Row], [Value]) and a
// [HookContext]; they have no database access and may enqueue a background job with HookContext.Enqueue
// (the job queue arrives in a later phase). See DocTypeHooks for the pipeline and the rules.
//
// # The binary
//
// `forgebase [OPTIONS] COMMAND`: migrate (framework migrations, DocType schema sync and app
// migrations), doc (insert, get, list, update, delete, submit, cancel, amend), user, token, serve (the
// HTTP API; no migrations) and new-app (writes a new app module that builds and passes its tests; needs
// no database, see docs/guide/first-app.md). Configuration comes from FORGEBASE_DATABASE_URL (the
// database of the one implicit site `default`), and for serve from FORGEBASE_LISTEN,
// FORGEBASE_ALLOWED_HOSTS, FORGEBASE_DEV_INSECURE_COOKIES and FORGEBASE_DESK_DIR. With --config
// forgebase.toml there are the sites listed in it, one database each (ADR 0014); a site may list the
// `apps` installed on it. The binary is a trusted local tool: `doc` commands run as the system
// context, so no permission rules apply to them. Real users and their permissions apply to the HTTP API.
//
// Exit codes: 0 success; 1 runtime, validation, startup or database error (message on standard error,
// starting with "error:"); 2 usage error.
//
// # Tests of an app
//
// The package forgebase/testing gives a throwaway PostgreSQL database, an in-process HTTP client and
// a guard that finds a DocType file missing from the embedded set.
package forgebase

import (
	"time"

	"github.com/tvstore/forgebase-go/internal/app"
	"github.com/tvstore/forgebase-go/internal/core"
	"github.com/tvstore/forgebase-go/internal/decimal"
)

// Version is the version of the framework. It is the value printed by `forgebase --version`.
const Version = app.Version

// ---- apps and the binary ----------------------------------------------------------------------

// App is a named unit of an application: DocTypes, hooks and data migrations. Build one with
// [NewApp]; the methods are DependsOn, DocTypes, Hooks, Migrations, Version and Name, and each
// returns the App so that calls chain.
type App = app.App

// NewApp starts an app. The name follows the rules of a DocType name (lower-case ASCII letters, digits and
// underscores, starting with a letter) and must be unique in the binary; `local` and `forgebase` are
// reserved.
func NewApp(name string) *App { return app.NewApp(name) }

// Binary is a Forgebase binary: the apps compiled into it, in order, and the command line that runs
// them. Build one with [New]; the methods are App, Check, Run and RunArgs.
type Binary = app.Binary

// New is a binary without apps. Add the apps with Binary.App, in dependency order, and run it:
//
//	os.Exit(forgebase.New().App(crm.App()).App(sales.App()).Run())
//
// The stock `forgebase` binary is forgebase.New().Run(): no apps, DocTypes from --doctypes DIR.
func New() *Binary { return app.NewBinary() }

// StartupError is everything wrong with the apps of a binary and its DocType files, found at startup,
// all problems together. Binary.Check and the commands return it; Problems lists the messages and Error
// prints them in two lists, "invalid app setup" and "cannot load DocType files".
type StartupError = app.StartupError

// ---- what a hook implements, receives and changes ---------------------------------------------

// DocTypeHooks are the lifecycle hooks of one DocType: BeforeInsert, Validate, BeforeDelete,
// BeforeSubmit, BeforeCancel and BeforeTransition. Embed [BaseHooks] and override what you need. The
// godoc of the interface has the pipeline order and the rules every hook follows.
type DocTypeHooks = core.DocTypeHooks

// BaseHooks implements DocTypeHooks with methods that do nothing. Embed it in a hook set.
type BaseHooks = core.BaseHooks

// HookContext is what a hook is told about the operation besides the document: the DocType, the actor,
// the [Operation] and, for a workflow transition, the [TransitionInfo].
type HookContext = core.HookContext

// NewHookContext is a context for operation on doctype by actor, for a test that calls a hook directly.
func NewHookContext(doctype, actor string, operation Operation) HookContext {
	return core.NewHookContext(doctype, actor, operation)
}

// HookError is what a hook returns to reject an operation (any error rejects it; this type is a
// convenient one). The message is shown to the caller.
type HookError = core.HookError

// NewHookError is a hook error with the given message.
func NewHookError(message string) *HookError { return core.NewHookError(message) }

// TransitionInfo is the workflow transition a hook runs for (HookContext.Transition).
type TransitionInfo = core.TransitionInfo

// NewTransitionInfo is the transition action from state from to state to, for a test that calls
// BeforeTransition directly.
func NewTransitionInfo(action, from, to string) *TransitionInfo {
	return core.NewTransitionInfo(action, from, to)
}

// EnqueuedJobs collects the jobs the hooks of an operation enqueue; EnqueuedJob is one of them. A unit
// test of a hook gives its HookContext a collector with HookContext.WithJobs and reads what was
// enqueued with Take.
type EnqueuedJobs = core.EnqueuedJobs

// EnqueuedJob is a job a hook enqueued: its full name and its JSON payload.
type EnqueuedJob = core.EnqueuedJob

// NewEnqueuedJobs is an empty collector for HookContext.WithJobs.
func NewEnqueuedJobs() *EnqueuedJobs { return core.NewEnqueuedJobs() }

// Operation is the operation a hook runs for.
type Operation = core.Operation

// The operations of a hook.
const (
	OpInsert     = core.OpInsert
	OpUpdate     = core.OpUpdate
	OpDelete     = core.OpDelete
	OpSubmit     = core.OpSubmit
	OpCancel     = core.OpCancel
	OpAmend      = core.OpAmend
	OpTransition = core.OpTransition
)

// Document is a document as a hook sees it: its name, its field values and the rows of its tables.
type Document = core.Document

// Row is one row of a child table: field values keyed by field name.
type Row = core.Row

// Value is a single field value. Build one with Null, Text, Int, DecimalValue, Bool, NewDate or
// Datetime; Kind says which member is meaningful.
type Value = core.Value

// ValueKind is the variant of a Value.
type ValueKind = core.ValueKind

// The kinds of a Value.
const (
	KindNull     = core.KindNull
	KindText     = core.KindText
	KindInt      = core.KindInt
	KindDecimal  = core.KindDecimal
	KindBool     = core.KindBool
	KindDate     = core.KindDate
	KindDatetime = core.KindDatetime
)

// Null is the value "no value".
func Null() Value { return core.Null() }

// Text is a text value (Data, Text, Select and Link fields).
func Text(s string) Value { return core.Text(s) }

// Int is an integer value.
func Int(n int64) Value { return core.Int(n) }

// DecimalValue is a decimal value (Decimal fields).
func DecimalValue(d Decimal) Value { return core.Decimal(d) }

// Bool is a boolean value (Check fields).
func Bool(b bool) Value { return core.Bool(b) }

// NewDate is a Date value for the calendar day year-month-day.
func NewDate(year int, month time.Month, day int) Value { return core.NewDate(year, month, day) }

// Datetime is a Datetime value.
func Datetime(t time.Time) Value { return core.Datetime(t) }

// ---- decimals ---------------------------------------------------------------------------------

// Decimal is the fixed-point number of a Decimal field: a 96-bit mantissa and a scale of 0 to 28, which
// is part of its text form ("12.50" stays "12.50"). It behaves like the Rust crate rust_decimal (ADR 0027).
// A Decimal is an immutable value; compare with Cmp or Equal, not with ==. Its methods: String, Scale,
// Sign, IsZero, IsNegative, Cmp, Equal, Neg, Normalize, Add, Mul and Rescale (Add, Mul and Rescale report
// whether the result fits).
type Decimal = decimal.Decimal

// ParseDecimal parses a decimal number as the Rust crate rust_decimal does: an optional sign, digits with
// at most one point, an optional exponent.
func ParseDecimal(s string) (Decimal, error) { return decimal.Parse(s) }

// NewDecimal is mantissa / 10^scale, for example NewDecimal(1250, 2) is 12.50. It panics when scale is
// above 28.
func NewDecimal(mantissa int64, scale uint32) Decimal { return decimal.New(mantissa, scale) }

// DecimalFromInt is n as a Decimal of scale 0.
func DecimalFromInt(n int64) Decimal { return decimal.FromInt(n) }
