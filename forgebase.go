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
//   - Jobs and schedules. A job name follows the rules of a DocType name, is unique within its app and is
//     known as `<app>.<job>`; invalid [JobOptions] are startup errors. A schedule names a job the same app
//     registered (an app schedules only its own jobs), at most once, with valid values. The app name
//     `forgebase` is reserved for the framework's own clean-up jobs, which every binary has.
//   - Startup errors. All of the above is checked before anything runs; every problem is reported
//     together ([StartupError], exit code 1).
//
// # Hooks
//
// A hook set is a type that implements [DocTypeHooks]. Embed [BaseHooks], whose methods do nothing,
// and override the points you need. Hooks get the document ([Document], [Row], [Value]) and a
// [HookContext]; they have no database access and may enqueue a background job with HookContext.Enqueue.
// See DocTypeHooks for the pipeline and the rules.
//
// # Background jobs
//
// An app registers a job with [App.Job] and may run it on a [Schedule] with [App.Schedule]. A job is a
// [JobHandler], a function that gets a [JobContext] with the payload, the [Documents] of the job (the
// trusted system context) and Enqueue:
//
//	func App() *forgebase.App {
//		return forgebase.NewApp("crm").
//			DocTypes(doctypes).
//			Job("welcome", welcome).
//			Job("digest", digest).
//			Schedule("digest", forgebase.DailyAt(6, 30))
//	}
//
// A hook enqueues a job in the transaction of the document operation (the job exists if and only if the
// change committed), another job in its own transaction, an operator with `forgebase job enqueue`.
// `forgebase worker` runs the jobs: at least once, in one transaction together with the completion,
// retried with a growing delay and dead after the attempts. See [JobHandler] for the rules of delivery.
//
// # Query reports
//
// An app defines a report with [App.Report]: SQL compiled into the binary (an embedded file), the roles
// that may run it, typed [Param]s and typed [Column]s. It runs read-only with a timeout and a row limit
// through GET /api/v1/report and POST /api/v1/report/{app}.{name}; `forgebase migrate` prepares every report
// and refuses one that does not fit its declaration. See [Report] and ADR 0016.
//
// # The binary
//
// `forgebase [OPTIONS] COMMAND`: migrate (framework migrations, DocType schema sync and app
// migrations and the sync of the schedules), doc (insert, get, list, update, delete, submit, cancel,
// amend, and with a workflow transition), user, token, job (list, show, retry, enqueue), worker (runs the background jobs and the
// scheduler), serve (the HTTP API; no migrations) and new-app (writes a new app module that builds and passes its tests; needs
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
// The package forgebase/testing gives a throwaway PostgreSQL database, an in-process HTTP client, RunJobs
// to run the jobs that are due and a guard that finds a DocType file missing from the embedded set.
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

// ---- background jobs --------------------------------------------------------------------------

// JobHandler is what a background job does: a function that returns nil to complete the job or an error to
// retry it. Register it with [App.Job]. The godoc of the type has the rules of delivery.
type JobHandler = app.JobHandler

// JobContext is what a running job knows (Name, ID, Attempt, Payload, DecodePayload) and may do (Enqueue,
// Documents). It owns the transaction the job runs in.
type JobContext = app.JobContext

// JobError is why a job failed; NewJobError is retried, NewPermanentJobError goes straight to dead.
type JobError = app.JobError

// NewJobError is a failure that is worth retrying.
func NewJobError(message string) *JobError { return app.NewJobError(message) }

// NewPermanentJobError is a failure that retrying cannot fix: the job is dead after this attempt.
func NewPermanentJobError(message string) *JobError { return app.NewPermanentJobError(message) }

// JobOptions is how a job is run and retried: the attempts, the timeout and the backoff. Start from
// [NewJobOptions] and give it to [App.JobWith].
type JobOptions = app.JobOptions

// NewJobOptions is the defaults: 5 attempts, a 30 second delay that doubles up to 1 hour, a 5 minute timeout.
func NewJobOptions() JobOptions { return app.NewJobOptions() }

// Schedule is when a scheduled job runs, in UTC: every N minutes, every day at a time, every week on a day at
// a time. Build one with [EveryMinutes], [DailyAt] or [WeeklyOn], and give it to [App.Schedule].
type Schedule = app.Schedule

// EveryMinutes is a schedule for every n minutes, from 1 to 1440, aligned to the Unix epoch.
func EveryMinutes(n int) Schedule { return app.EveryMinutes(n) }

// DailyAt is a schedule for every day at hour:minute UTC.
func DailyAt(hour, minute int) Schedule { return app.DailyAt(hour, minute) }

// WeeklyOn is a schedule for every week on weekday at hour:minute UTC.
func WeeklyOn(weekday time.Weekday, hour, minute int) Schedule {
	return app.WeeklyOn(weekday, hour, minute)
}

// Documents are the documents a job can reach, as the trusted system context: Get, List, Insert, Update, Delete,
// Submit, Cancel, Transition and Amend. Get it from JobContext.Documents.
type Documents = app.Documents

// StoredDocument is a document as a job reads it: its name, version, docstatus and the [Document] with the
// values and table rows.
type StoredDocument = app.StoredDocument

// DocError is why a [Documents] call failed; in a job a database or connection error is retried and every
// refusal is permanent.
type DocError = app.DocError

// ListQuery is a query for Documents.List: filters, sort keys and a page size. Start from [NewListQuery].
type ListQuery = app.ListQuery

// NewListQuery is a query without filters, in name order.
func NewListQuery() ListQuery { return app.NewListQuery() }

// FilterOp is how a [ListQuery] filter compares a field with a value.
type FilterOp = app.FilterOp

// The filter operators of a ListQuery.
const (
	FilterEq   = app.FilterEq
	FilterNe   = app.FilterNe
	FilterLt   = app.FilterLt
	FilterLe   = app.FilterLe
	FilterGt   = app.FilterGt
	FilterGe   = app.FilterGe
	FilterLike = app.FilterLike
)

// ---- query reports ----------------------------------------------------------------------------

// Report is an app-defined SQL report, for [App.Report]: the SQL, the roles that may run it, typed parameters
// and typed result columns. Build one with [NewReport]; the methods are Label, Roles, Param, Column, Timeout
// and MaxRows, and each returns the Report so that calls chain. The godoc of the type has the rules of the
// SQL.
type Report = app.Report

// NewReport is a report that runs this SQL, usually an embedded file:
//
//	//go:embed reports/sales_by_customer.sql
//	var salesByCustomerSQL string
//
//	forgebase.NewReport(salesByCustomerSQL).Label("Sales by customer").Roles("Sales Manager")
func NewReport(sql string) *Report { return app.NewReport(sql) }

// Param is a parameter of a [Report]; Label, Required and DefaultValue refine it. The order of
// Report.Param calls is the order of $1..$n in the SQL.
type Param = app.Param

// DataParam is a text parameter of at most 140 characters.
func DataParam(name string) *Param { return app.DataParam(name) }

// IntParam is a 64-bit integer parameter.
func IntParam(name string) *Param { return app.IntParam(name) }

// DecimalParam is a fixed-point parameter, a string in the API ("12.50").
func DecimalParam(name string) *Param { return app.DecimalParam(name) }

// DateParam is a calendar date parameter, YYYY-MM-DD in the API.
func DateParam(name string) *Param { return app.DateParam(name) }

// CheckParam is a boolean parameter.
func CheckParam(name string) *Param { return app.CheckParam(name) }

// LinkParam is the name of a document of doctype; it is only bound, not checked for existence.
func LinkParam(name, doctype string) *Param { return app.LinkParam(name, doctype) }

// SelectParam is one of options.
func SelectParam(name string, options ...string) *Param { return app.SelectParam(name, options...) }

// Column is a result column of a [Report]; Label refines it. The columns are positional: the statement
// returns them in the order of the Report.Column calls.
type Column = app.Column

// DataColumn is a text column (text or varchar).
func DataColumn(name string) *Column { return app.DataColumn(name) }

// IntColumn is an integer column (int2, int4 or int8).
func IntColumn(name string) *Column { return app.IntColumn(name) }

// DecimalColumn is a fixed-point column (numeric), a string in the API.
func DecimalColumn(name string) *Column { return app.DecimalColumn(name) }

// DateColumn is a date column (date).
func DateColumn(name string) *Column { return app.DateColumn(name) }

// DatetimeColumn is a point-in-time column (timestamptz), RFC 3339 in UTC in the API.
func DatetimeColumn(name string) *Column { return app.DatetimeColumn(name) }

// CheckColumn is a boolean column (bool).
func CheckColumn(name string) *Column { return app.CheckColumn(name) }

// LinkColumn is the name of a document of doctype (text or varchar); the desk shows it as a link.
func LinkColumn(name, doctype string) *Column { return app.LinkColumn(name, doctype) }

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

// Changes are the changes of an update: new field values and new rows for Table fields.
type Changes = core.Changes

// DocStatus is the state of a document of a submittable DocType.
type DocStatus = core.DocStatus

// The docstatus values.
const (
	// DocDraft is a document being edited.
	DocDraft = core.Draft
	// DocSubmitted is a submitted, locked document.
	DocSubmitted = core.Submitted
	// DocCancelled is a document cancelled after submission.
	DocCancelled = core.Cancelled
)

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
