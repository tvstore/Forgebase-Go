# 0028 Go public API of the app model

- Status: Accepted (decided by the working session within G3 of the Go port plan, which the owner approved on 2026-11-06 with *"setuju G1–G8"*; G3 fixed the package layout and left the exact shape of the API to P4 and an ADR). The owner can overrule it.
- Date: 2026-11-07

## Context

[ADR 0012](0012-app-model-and-public-api-boundary.md) decided that apps depend on one public crate and nothing else; [ADR 0026](0026-go-implementation.md) (G3) maps that to two Go packages, the root package `forgebase` and `forgebase/testing`, with everything else under `internal/`, DocTypes, migrations, overlays and fixtures embedded with `//go:embed`, and hooks as interfaces with a no-op base struct. P4 (Rust M5) builds the app model, and this ADR fixes the exact Go shape of it and maps every public item of the Rust crate (`crates/forgebase/src/lib.rs`) to its Go counterpart or to the phase that adds it. The port is a parity port (G1): behaviour, messages and rules are those of ADR 0012; only the spelling of the API is Go.

The guidelines (G2, the maintenance policy): the simplest explicit Go, no globals, no code generation, no plugins; a small surface with godoc on every exported item; nothing is added before a phase needs it.

## Decision

### The root package `forgebase`

```go
func NewApp(name string) *App
func (*App) Name() string
func (*App) Version(version string) *App
func (*App) DependsOn(apps ...string) *App
func (*App) DocTypes(fsys fs.FS) *App
func (*App) Hooks(doctype string, hooks DocTypeHooks) *App
func (*App) Migrations(fsys fs.FS) *App

func New() *Binary
func (*Binary) App(app *App) *Binary
func (*Binary) Check() error      // a *StartupError, or nil
func (*Binary) Run() int          // os.Args[1:]; pass the result to os.Exit
func (*Binary) RunArgs(args ...string) int
```

An application's `main`:

```go
func main() { os.Exit(forgebase.New().App(crm.App()).App(sales.App()).Run()) }
```

and an app is a plain function that returns data:

```go
//go:embed doctypes/*.toml
var doctypes embed.FS

//go:embed migrations/*.sql
var migrations embed.FS

func App() *forgebase.App {
	return forgebase.NewApp("sales").Version("1.0.0").DependsOn("crm").
		DocTypes(doctypes).Hooks("sales_order", OrderRules{}).Migrations(migrations)
}
```

- **The builder mutates and returns its receiver.** `App` and `Binary` are pointers to data; the methods return the receiver so that calls chain. An app is built once, at startup, by one goroutine; neither type is safe for concurrent mutation. Building cannot fail: everything wrong (an invalid name, a missing dependency, a hook on an unknown DocType, an unreadable file, a nil argument) is held and reported together at startup in the `StartupError`, as in Rust.
- **Later phases add methods; no existing call changes.** `Job`, `JobWith` and `Schedule` came with P5 (below); `Report` came with P6 (below); `Method` came with P7.1 (below); `Fixtures` came with P7.2 (below); `ExtendDocType` came with P7.3 (below). `App` and `Binary` have unexported fields only, so a method is an addition. `Binary.RunArgs` takes the arguments without the program name (`os.Args[1:]`), unlike Rust's `run_from`, which takes `args_os` with it.
- **`Binary` and not `Forgebase`.** `forgebase.Forgebase` would stutter; the type is the binary of an application, so it is `forgebase.Binary` and the constructor is `forgebase.New`. The Rust method `app` is `App`.
- **`DocTypes(fs.FS)`** reads every file whose name ends in `.toml`, at any depth, in the order of `fs.WalkDir` (lexical within each directory), and uses the path in the file system as its name in messages (an `embed.FS` of `//go:embed doctypes/*.toml` gives `doctypes/customer.toml`, the path the Rust list wrote). Names that start with a dot are skipped. This replaces the explicit list of `doctypes!` with the embedded directory itself: the list is the embed pattern, and the guard (below) catches a file the pattern does not reach. **The order of the Rust list** decided only the order in which problems are reported (the load order within an app has no other effect: DocTypes are unique and the registry is keyed by name), so lexical order is the mapping; an app that wanted a different order of messages loses nothing else. Calling it again adds more files.
- **`Migrations(fs.FS)`** reads every `*.sql` file the same way. The file is named `NNNN_name.sql`: the digits are the version (positive, at most 2 147 483 647) and the rest is the name recorded in `fb_app_migration`, so `0001_default_customer_groups.sql` is version 1 `default_customer_groups`, as the Rust call `(1, "default_customer_groups", "migrations/0001_default_customer_groups.sql")` wrote it. Any other `.sql` name is a startup error naming the file; other files (a README) are ignored. Within one call the migrations are ordered by version; across calls the versions must still increase, and equal versions are refused with the Rust message. A listing that is out of order, or has a negative version, which Rust could write by hand, cannot be written in file names. The checksum of an applied migration is the SHA-256 of the file text, as in Rust, so a database migrated by the Rust binary verifies in Go while the file is byte-identical.
- **`Hooks(doctype, hooks)`** takes a `DocTypeHooks` value (an interface; a pointer or a value type with value receivers). It is registered in app order, then registration order; a nil value is a startup error. A hook set is shared by every request and must be safe for concurrent use.
- **`Check`** returns a `*StartupError` (the type is an alias of the internal one; `Problems() []string` and `Error()`), and a nil `error`, not a typed nil, when the setup is valid.

The composition rules are those of ADR 0012 and are not repeated here: load order (apps in order, then the `--doctypes` directory as the app `local`, which depends on all of them), ownership (a DocType belongs to one app; the metadata API says which), `DependsOn` (registered earlier, never sorted), Link and Table references only within an app and its dependencies, hooks only for the DocTypes of an app and its dependencies, hook order, the app names `local` and `forgebase` are reserved, every problem reported together (exit code 1, the messages of Rust; the hint of the app-rule message names the Go call: `add DependsOn("crm"), registered before ...`).

### What a hook sees: type aliases of `internal/core`

An application cannot import `internal/core`, so the root package re-exports what a hook implements, receives and changes, as **type aliases** (an alias is the same type: nothing is wrapped or converted) and a few one-line constructors:

`DocTypeHooks`, `BaseHooks` (embed it), `HookContext`, `HookError` and `NewHookError`, `NewHookContext`, `Operation` with `OpInsert` ... `OpTransition`, `TransitionInfo` and `NewTransitionInfo`, `EnqueuedJobs`, `EnqueuedJob` and `NewEnqueuedJobs` (for a test of a hook), `Document`, `Row`, `Value` and `ValueKind` with `KindNull` ... `KindDatetime`, the constructors `Null`, `Text`, `Int`, `DecimalValue`, `Bool`, `NewDate` and `Datetime`, and `Decimal` with `ParseDecimal`, `NewDecimal` and `DecimalFromInt`.

`Date` and `OffsetDateTime` of Rust are the standard `time.Time` (a Date value is a `time.Time` whose clock part is ignored, `NewDate` builds one), so nothing is re-exported for them. The decimal constructor of a `Value` is `DecimalValue` because `Decimal` is the type. `HookContext.Enqueue` feeds the job queue (below): the name of a job that no app registered is refused when the job is written, as in Rust.

**The decimal arithmetic is an addition.** Rust apps compute with `rust_decimal`'s operators (the `sales` example multiplies, sums and rounds); the Go `internal/decimal` had none (ADR 0027: "the core does none"). P4 adds `Sign`, `Add`, `Mul` and `Rescale` to it, in `internal/decimal/arith.go`, and they are reachable through the `Decimal` alias. `Add`, `Mul` and `Rescale` return whether the result fits and never round to make it fit, which is stricter than `rust_decimal` (which scales a product down to fit); `Rescale` to fewer digits rounds half away from zero, the rule the example needs. A hook that needs the other behaviour rounds an operand first. This is the one deliberate extension of a Rust-equivalent surface in P4.

### The package `forgebase/testing`

The package is called `testing` like the standard library's: a test file imports one of them under another name (`fbtest "github.com/tvstore/forgebase-go/testing"`, as the generated app does).

```go
const Password = "correct horse battery"          // of every user made with CreateUser

func NewDB(t testing.TB) *DB                      // a throwaway fb_test_app_* database, dropped by t.Cleanup
func (*DB) URL() string
func (*DB) Migrate(b *forgebase.Binary, dryRun bool) (*Migrated, error)
func (*DB) Exec(sql string)                       // fail the test on error
func (*DB) Rows(sql string) [][]string            // text, NULL as "NULL"
func (*DB) CreateUser(username string, roles ...string)
func (*DB) CreateToken(username string) string    // key:secret
func (*DB) Client(b *forgebase.Binary) *Client

type Migrated struct{ AppMigrations []AppliedMigration }
type AppliedMigration struct{ App string; Version int32; Name string }

func (*Client) Login(username string) *Client
func (*Client) WithToken(keySecret string) *Client
func (*Client) WithoutCSRF() *Client
func (*Client) Get, Post, Patch, Delete, PostRaw ...  // *Reply
type Reply struct{ Status int; Body any; Text string }
func (*Reply) Header(name string) string
func (*Reply) Data() any
func (*Reply) DataObject() map[string]any
func (*Reply) DataList() []any
func (*Reply) ErrorCode() string
func (*Reply) ErrorMessage() string

func AssertDocTypesListed(t testing.TB, app *forgebase.App, dir string)
```

It maps `forgebase::testing` one to one with these differences: a helper takes the `testing.TB` once (`NewDB`) and fails the test with `t.Fatalf` where Rust panics; the database is dropped by `t.Cleanup`, so there is no `cleanup()` and no drop thread; the database never skips (a missing `FORGEBASE_TEST_DATABASE_URL` fails the test with the Rust message); the HTTP client sends requests straight into the `net/http` handler with `httptest` (no socket); the JSON of a request is any value `encoding/json` can marshal (`map[string]any{...}`), and a reply keeps the decoded body (`any`) next to its text, so there is no `Json` or `json!` and no `serde_json` re-export; `Migrate` returns `error` (a `*forgebase.StartupError` when the apps do not fit together, else the message `forgebase migrate` would print) instead of the `MigrateError` enum. **The guard** `AssertDocTypesListed` compares the DocType files in `dir` (relative to the test's working directory, which is the directory of the package under test) with the embedded set, reading `dir` with the same rules as `DocTypes`, and compares paths in full (`doctypes/customer.toml`), so a file embedded from elsewhere does not count; `./` and a trailing `/` in `dir` are ignored.

Since P5, `RunJobs`, `JobRun` and `RunSchedulesAt` (below). Since P6, `CheckReports(b, db)`, and `Migrate` runs it as `TestDb::migrate` does (unless the migration is a dry run). Since P7.1, `WebhookTestSettings(t, caPEM, timeout)` (below). Since P7.2, `Migrated.Fixtures`, `Migrated.FixturesPlanned` and `AppliedFixtures` (below). Since P7.3, `DB.AddCustomField`, `DB.SetCustomFieldEnabled`, `DB.ClientPolling` and `Client.ReloadMetadata` (below). The public surface of `testing` stays in this ADR; later phases extend it here.

### Background jobs and schedules (P5.1)

`App` gets three methods and the root package a few types (all aliases of `internal/app`, like the rest):

```go
func (*App) Job(name string, handler JobHandler) *App
func (*App) JobWith(name string, handler JobHandler, options JobOptions) *App
func (*App) Schedule(job string, schedule Schedule) *App

type JobHandler func(ctx context.Context, job *JobContext) error

func (*JobContext) Name() string                  // <app>.<job>
func (*JobContext) ID() string                    // the same for every attempt
func (*JobContext) Attempt() int                  // from 1
func (*JobContext) Payload() json.RawMessage
func (*JobContext) DecodePayload(v any) error
func (*JobContext) Enqueue(ctx context.Context, name string, payload any) error
func (*JobContext) Documents() *Documents

type JobError struct{ Message string; Permanent bool }
func NewJobError(message string) *JobError           // retried
func NewPermanentJobError(message string) *JobError  // dead after this attempt

func NewJobOptions() JobOptions                      // 5 attempts, 30 s doubling to 1 h, 5 min timeout
func (JobOptions) MaxAttempts(n int) JobOptions
func (JobOptions) Timeout(d time.Duration) JobOptions
func (JobOptions) Backoff(base, cap time.Duration) JobOptions

func EveryMinutes(n int) Schedule
func DailyAt(hour, minute int) Schedule
func WeeklyOn(weekday time.Weekday, hour, minute int) Schedule

func (*Documents) Get(ctx, doctype, name string) (*StoredDocument, error)   // nil, nil when there is none
func (*Documents) List(ctx, doctype string, q ListQuery) ([]*StoredDocument, error)
func (*Documents) Insert(ctx, doctype string, doc Document) (*StoredDocument, error)
func (*Documents) Update(ctx, doctype, name string, version int64, c Changes) (*StoredDocument, error)
func (*Documents) Delete(ctx, doctype, name string, version int64) error
func (*Documents) Submit, Cancel, Amend(ctx, doctype, name string, version int64) (*StoredDocument, error)
type StoredDocument struct{ Name string; Version int64; DocStatus *DocStatus; Document Document }
func NewListQuery() ListQuery                        // .Filter(field, op, value) .OrderBy(field, desc) .Limit(n)
type DocError struct{ Err *db.Error }                // IsRetryable()
```

and, in `forgebase/testing`, `RunJobs(b *forgebase.Binary, d *DB) JobRun` (`JobRun{Done, Retried, Dead int}`) and `RunSchedulesAt(b, d, at time.Time) int`, which fail the test where Rust panics.

The rules are those of ADR 0013 and of the Rust API; what is different is the spelling and what Go cannot do:

- **A handler is a function, not a trait.** Rust has `JobHandler` as a trait with a boxed future because there is no async-trait crate; in Go the simplest thing that holds the same rules is `func(ctx, *JobContext) error`, and a handler that needs state is a closure. It returns `nil` to complete the job; an error is retried or, for a permanent `*JobError`, ends it. **Classification** (the Rust `JobError` and `From<DocError>`): an error that is, or wraps, a `*JobError` is as that says; a `*DocError` or a database error is **retryable when it is a database or connection error** (`ErrPostgres`, lock timeout, pool, changed while reading) and **permanent for every refusal** (validation, permission, hook veto, not found, version conflict, unique violation, invalid list query, unknown job, ...); any other error is retryable. The text stored as `last_error` is the one the command line prints (cut to 2000 characters).
- **The handler gets a `context.Context`, and the abort is cooperative.** In Rust the worker aborts the task at its timeout, at the end of the shutdown grace period and on a second signal, and the future is dropped. A goroutine cannot be stopped, so the context of the attempt is cancelled instead, and a handler that passes it to every call returns promptly. The worker's side of the contract is the same as in Rust and does not depend on the handler: **the transaction of an aborted attempt is never committed** (the completion is written under a lock that the abort takes first), **its connection is closed, not returned to the pool**, the failure is recorded as `timed out after N s` (or the job is given back, without counting the attempt, when the worker stops), and a second signal exits without recording anything, the lease covering the job. A handler that ignores the context keeps its goroutine and its connection until it returns (the worker waits at most 5 seconds for it, then goes on); it can never commit. This is the one behaviour that cannot be identical, and it is documented on `JobHandler`.
- **A panic is a retryable failure** (`panicked: <value>`), the connection is closed, and the worker goes on, as in Rust.
- **`JobOptions` is a value built with `NewJobOptions` and chained methods** (the Rust builder). The zero `JobOptions` is not the defaults: it is invalid and a startup error (`max_attempts must be at least 1`, ...), the same checks as Rust with the same messages. Durations are `time.Duration` (at most 292 years, which no limit of these options approaches).
- **`Schedule` constructors** are free functions named after what they are (`forgebase.DailyAt(6, 30)` reads better than `forgebase.Schedule.DailyAt`); `Weekday` is `time.Weekday` (Sunday is 0; `weekly mon` in the stored text is by name). Hour and minute are `int` and an out-of-range value is a startup error (`the hour 24 must be from 0 to 23`), not a compile error as `u8` almost makes it. `Schedule` is comparable with `==`.
- **Startup errors** are those of Rust, with the Go spelling in the one message that names a function (`register it with App.Job first`; `EveryMinutes(0): the number of minutes must be from 1 to 1440`), plus one that Rust cannot have: a `nil` handler (`job `x` has no handler`). In order: invalid job name, duplicate name in an app, invalid options, a schedule for a job the same app did not register (an app schedules only its own jobs, so `Schedule("a.work", ...)` of another app is refused), a second schedule for a job, invalid values. The app name `forgebase` stays reserved: the framework's own clean-up jobs `forgebase.purge_jobs` (daily 03:00), `forgebase.purge_login_failures` and `forgebase.purge_sessions` (every 60 minutes) exist in every binary, with their schedules. `forgebase.deliver_webhook` (no schedule) is registered too since P7.1, so the worker of a binary without apps counts 4 job types, as in Rust.
- **`Documents` is the one type for jobs and, since P7.1, for methods.** In a job it runs as the system context with the actor `job:<app>.<job>`; in a method as the caller (below); every write and every enqueue is a savepoint of the job's transaction, and `Get` and `List` run in a savepoint too, so a refused read leaves the transaction usable. The getters of `StoredDocument` (`name()`, `version()`, `docstatus()`, `document()`) are exported fields. `ListQuery` is a value whose methods return a changed copy. `Transition(doctype, name, action, expectedVersion)` (workflow, P6) runs the transition pipeline as the handle's caller. Errors are `*DocError`, so the application keeps no dependency on the internal error type; `IsRetryable` is the Rust method.
- **The worker and the commands.** `forgebase worker [--concurrency N]` (1 to 64, pool of N+2 connections) claims only the jobs of the apps installed on its site and the built-in ones, with the log lines of Rust (`worker: started <id>: <n> job type(s), concurrency <c>`, `job <name> <id> done (attempt n)`, `... failed (attempt n of m), retry in <s> s: ...`, `... dead after attempt n: ...`, `stopping: running jobs get up to 30 s ...`, `stopped`); the first SIGINT or SIGTERM stops claiming and lets running jobs finish for 30 seconds (then they are given back); the second exits at once. `forgebase job list|show|retry|enqueue` print what the Rust ones print (the keys of `job show` are in alphabetical order, as `serde_json` writes them without `preserve_order`). The scheduler ticks inside the worker, one slot per poll interval. `migrate` syncs `fb_schedule` under the migration lock and prints the `schedules:` lines; a schedule of an app that is not installed on the site is deleted. A worker checks `fb_meta_version` every 10 seconds and rebuilds its registry when a site custom field changed (P7.3, below).

### App methods and outgoing webhooks (P7.1)

`App` gets one method and the root package the types of a method (aliases of `internal/app`, like the rest):

```go
func (*App) Method(name string, method *Method) *App

func NewMethod(handler MethodHandler) *Method
func (*Method) Roles(roles ...string) *Method   // adds roles; calling it with none is an empty role list
func (*Method) AnyUser() *Method

type MethodHandler func(ctx context.Context, m *MethodContext) (any, error)

func (*MethodContext) Name() string             // <app>.<method>
func (*MethodContext) Caller() MethodCaller     // Username(), Roles(), HasRole(role)
func (*MethodContext) Body() json.RawMessage    // a JSON object, {} when the request had none
func (*MethodContext) DecodeBody(v any) error
func (*MethodContext) Documents() *Documents    // acts as the caller
func (*MethodContext) Enqueue(ctx context.Context, name string, payload any) error

type MethodError struct{ Status int; Message string }
func MethodBadRequest(message string) *MethodError    // 400 method_error
func MethodUnprocessable(message string) *MethodError // 422 method_error
```

and, in `forgebase/testing`, `WebhookTestSettings(t testing.TB, caPEM string, timeout time.Duration)`.

The rules are those of [ADR 0019](0019-app-method-endpoints.md) and of the Rust API; what is different is the spelling and what Go cannot do:

- **A handler is a function, not a trait**, as for jobs. It returns the value that is sent as `{"data": value}` (anything `encoding/json` can marshal; `nil` is `null`) and an error. Rust returns a `serde_json::Value`; Go has no such type, so the result is marshalled **before the commit**: a value that cannot be written as JSON rolls the call back and is a generic 500. The body is `json.RawMessage` (`Body`, `DecodeBody`) for the same reason: the handler decodes it into its own struct. The keys of a map are written in order, as `serde_json` writes a map; a struct is written in field order.
- **Access is validated at startup**, with the Rust messages (`no access given`, `an empty role list`, `both .Roles(..) and .AnyUser() are given`, a duplicate name, an invalid name or role); a nil `Method` or handler is one more (`method `x` has no handler`). `Method` is data: nothing fails before the binary starts.
- **Errors.** `*MethodError` (and any error that wraps one) is the app's own refusal: 400 or 422 `method_error` with the message as written. A `*DocError`, returned as it is or wrapped, is mapped by the same function that maps it for the document API, so the status and code are the document API's (`permission_denied` 403, `not_found` 404, `conflict` 409, `validation_failed` 422, `hook_rejected` 422, `invalid_query` 400, ...). **Anything else is a generic 500**: the HTTP layer logs the error and the client sees `internal server error` and no detail. A panic is the same, and the call's connection is **closed and not returned to the pool** (the transaction is never committed), as the task of the Rust dispatcher dropped it.
- **One transaction, as the caller.** The dispatcher takes a connection, begins a transaction, runs the handler with a `MethodContext` that owns it, commits when the handler returns a nil error and rolls back on every error. `Documents()` is the same type as a job's, built with the user as the caller (`core.Caller` of kind user, never the system context: the context offers no way to become it), so every call, `Transition` included, goes through the functions of the document API with that caller and the one evaluator decides. Writes and `Enqueue` are savepoints and so are `Get` and `List` (the M9.2 amendment), so a refused call leaves the transaction usable. A job enqueued by `Enqueue` is recorded as enqueued by the user.
- **The client going away does not cancel the method.** The handler gets `context.WithoutCancel` of the request's context (Rust ran it on a task of its own): it finishes and commits or rolls back. There is no time limit, as in Rust.
- **The route.** `POST /api/v1/method/{app}.{method}` is in `internal/httpapi`, which knows names and access only (the registry of the site) and a `MethodDispatch` interface that `internal/app` implements and `State.WithMethods` receives: the Rust seam between `forgebase-http` and `forgebase`. Without a dispatcher every call is 404. The order of the checks is that of Rust: authentication and CSRF, the path, the shape of the body (empty is `{}`, not JSON 415, not an object 400, over the limit 413), then the one 404 for a method that does not exist, is not installed on the site or may not be called, then the query parameters (`invalid_query`).
- **`forgebase/testing`.** `DB.Client` gives the client the dispatcher of the binary, so `Client.Post` to a method route works; `CreateToken`, `WithToken`, `WithoutCSRF` and `PostRaw` (P4) are the helpers of the gate. `WebhookTestSettings` sets, once for the process (the first call wins), the CA that the webhook client trusts in place of the system roots and the time limit of a delivery in place of 10 seconds. It fails the test when the PEM holds no certificate (Rust panics).

**Outgoing webhooks (ADR 0020) add no public API besides `WebhookTestSettings`.** The commands, the table and the delivery job are internal; the Go specifics, with the reasons, are:

- **Standard library only (G2).** The signature is `crypto/hmac` with SHA-256, the client is `net/http` with a `Transport` whose `DialContext` connects to the **vetted addresses only** (resolve, check every address against the blocklist, pin: the transport never resolves the name), `Proxy` unset (`HTTPS_PROXY` is not read), `DisableKeepAlives` (one request on a fresh connection), HTTP/1.1 only, no redirect followed, TLS 1.2 or later with the system roots (or the test CA), the Host, SNI and certificate check from the URL. No dependency is added.
- **The URL is checked by a parser of its own** that has the rules and the messages of Rust (which used the `http` crate's `Uri`): `https`, or `http` for a webhook added with `--insecure-http`, a host, no user info, at most 2048 bytes, only the characters of a URI. The request target is the path and query as written, without the fragment. **Differences:** a port above 65535 is an error (Rust's `Uri::port_u16` gives `None` and the scheme's default port is used), and an IPv6 literal with a zone is looked up as a name and fails.
- **Messages never hold the URL.** `net/http` errors are `*url.Error`, which holds the whole URL: the client unwraps them and names only the scheme, host and port (`cannot connect to https://host:443`, `TLS error (x509: certificate signed by unknown authority) with https://host:443`, `the request to ...`, `... did not answer within 10 s`). The text of a TLS failure is that of `crypto/tls` where rustls says `UnknownIssuer`.
- **The job.** `forgebase.deliver_webhook` has the default options with a 30 second timeout; it passes the job's context to the request, so an aborted attempt (a timeout or a stopping worker) stops the request, and the 10 second limit covers the lookup, the connection, TLS, the request and the discarded body (at most 64 KiB are read). A number in the snapshot is forwarded as it was stored (`json.Number`), the body's keys are in order, and `delivery` is the job id.
- **The management** is `db.AddWebhook`, `ListWebhooks`, `GetWebhook`, `SetWebhookEnabled`, `RemoveWebhook` and `RotateWebhookSecret` with the checks and messages of Rust (events, name, DocType, secrets of at least 16 bytes, the secret in no printed form of `db.Webhook`) and the error kind `ErrWebhook` (printed as `webhook: ...`; the commands print the reason alone, as `CliError::Webhook`). The `webhook` commands print what the Rust ones print; `webhook test` exits 0 for a 2xx answer and 1 for anything else.
- **What Rust does and Go keeps.** The body of a delivery forwards `truncated` but not `unavailable` (ADR 0020 says both; the Rust code does only the first), and Go keeps the Rust code.

### Mapping of the public items of `crates/forgebase/src/lib.rs`

| Rust | Go |
|---|---|
| `App` (`new`, `version`, `depends_on`, `doctypes`, `hooks`, `migrations`, `name`) | `App` (`NewApp`, `Version`, `DependsOn`, `DocTypes`, `Hooks`, `Migrations`, `Name`) |
| `App::job`, `job_with`, `schedule` | `Job`, `JobWith`, `Schedule` (P5) |
| `App::extend_doctype`, `fixtures`, `report`, `method` | `App.Report(name, *Report)` (P6, below); `App.Method(name, *Method)` (P7.1, below); `App.Fixtures(fs.FS)` (P7.2, below); `App.ExtendDocType(doctype, fs.FS)` (P7.3, below) |
| `DocTypeFile`, `MigrationFile`, `FixtureFile`, `OverlayFile`, the macros `doctypes!`, `migrations!`, `overlays!`, `fixtures!` | none: `//go:embed` and `fs.FS` (above); fixtures are `App.Fixtures` (P7.2, below), overlays are `App.ExtendDocType` (P7.3, below) |
| `Forgebase` (`new`, `app`, `check`, `run`, `run_from`) | `Binary` (`New`, `App`, `Check`, `Run`, `RunArgs`) |
| `StartupError` | `StartupError` |
| `DocTypeHooks`, `HookContext`, `HookError`, `Operation`, `TransitionInfo`, `EnqueuedJob`, `EnqueuedJobs` | the same names (aliases), `BaseHooks` for the no-op base |
| `Document`, `Row`, `Value` | the same names (aliases) |
| `Changes`, `DocStatus` | the same names (aliases; `DocStatus` has `DocDraft`, `DocSubmitted`, `DocCancelled`) |
| `Decimal` | `Decimal` (alias of `internal/decimal`) |
| `Date`, `OffsetDateTime` | `time.Time` of the standard library |
| `Weekday` | `time.Weekday` |
| `serde_json` | none: `encoding/json` and `any` |
| `Documents`, `DocError`, `FilterOp`, `ListQuery`, `StoredDocument` | the same names (`FilterOp` values are `FilterEq` ... `FilterLike`; `ListQuery` is built with `NewListQuery`); `Documents.Transition` (P6) |
| `JobContext`, `JobError`, `JobHandler`, `JobOptions`, `Schedule` | the same names; `JobHandler` is a function type, `JobError` is made with `NewJobError` and `NewPermanentJobError`, `JobOptions` with `NewJobOptions`, a `Schedule` with `EveryMinutes`, `DailyAt` and `WeeklyOn` |
| `JobFuture` | none: a handler returns `error` |
| `Method`, `MethodCaller`, `MethodContext`, `MethodError`, `MethodFuture`, `MethodHandler` | `Method` (built with `NewMethod(handler)` and `Roles` or `AnyUser`), `MethodCaller`, `MethodContext`, `MethodError` (made with `MethodBadRequest` and `MethodUnprocessable`) and `MethodHandler`, a function type; `MethodFuture` has no counterpart (P7.1, below) |
| `Column`, `Param`, `Report` | `Report` is built with `NewReport(sql)` and the methods `Label`, `Roles`, `Param`, `Column`, `Timeout` and `MaxRows`; a `Param` with `DataParam`, `IntParam`, `DecimalParam`, `DateParam`, `CheckParam`, `LinkParam` or `SelectParam` and the methods `Label`, `Required` and `DefaultValue`; a `Column` with `DataColumn`, `IntColumn`, `DecimalColumn`, `DateColumn`, `DatetimeColumn`, `CheckColumn` or `LinkColumn` and `Label` (P6). Misuse is reported by `migrate` and `App.Run` as the Rust builder reports it |
| `testing` (`TestDb`, `HttpClient`, `Reply`, `Migrated`, `AppliedMigration`, `MigrateError`, `PASSWORD`, `assert_doctypes_listed`, `Json`, `json!`, `serde_json`) | `testing` (`DB`, `Client`, `Reply`, `Migrated`, `AppliedMigration`, `Password`, `AssertDocTypesListed`), see above |
| `testing::run_jobs`, `JobRun`, `run_schedules_at` | `RunJobs`, `JobRun` (fields `Done`, `Retried`, `Dead`), `RunSchedulesAt` (P5) |
| `testing::check_reports` | `CheckReports(b, db)` (P6) |
| `webhook_test_settings` | `WebhookTestSettings(t, caPEM, timeout)` (P7.1, below) |
| `testing::AppliedFixtures`, `Migrated::fixtures` and `fixtures_planned` | `AppliedFixtures`, `Migrated.Fixtures` and `Migrated.FixturesPlanned` (P7.2, below) |
| `TestDb::add_custom_field`, `set_custom_field_enabled`, `client_polling`; `HttpClient::reload_metadata` | `DB.AddCustomField(b, doctype, fieldTOML)`, `DB.SetCustomFieldEnabled(doctype, name, enabled)`, `DB.ClientPolling(b, interval)`; `Client.ReloadMetadata()` (P7.3, below) |
| `forgebase new-app` | `forgebase new-app` (below) |

### `forgebase new-app`

`forgebase new-app NAME --forgebase-path DIR [--dir DIR]` writes a Go module that builds and passes its own tests as created: `go.mod` (module `NAME`; `go` and `toolchain` lines copied from the checkout's `go.mod`; `require github.com/tvstore/forgebase-go v0.0.0` and a `replace` to the absolute checkout path; the checkout's other requirements as `// indirect` lines, and its `go.sum`, so the build uses the versions Forgebase was tested with, the Go counterpart of copying `Cargo.lock`), the app package `NAME` (`NAME.go`, with the sample hook), `doctypes/NAME_note.toml` (byte-identical to the Rust template), `migrations/README.md`, `cmd/NAME/main.go` and its test, `NAME_test.go`, `README.md`, `AGENTS.md` (adapted to Go) and `.gitignore`. The templates are embedded with `//go:embed` and end in `.tpl` so that the Go tool does not compile them. Same placeholders, rendered in one pass; same rules: never overwrite, a failed run removes the directory it created, the same messages. The name rules are those of an app name plus the 54-byte limit; in place of the Rust keywords and reserved crate names a name may not be a Go keyword, a predeclared identifier or a top-level package of the standard library (the generated `main` imports the app package by its name, and an import path such as `fmt` would mean the library). The CI job `app-template` generates `demo` outside the checkout, vets, tests and builds it, migrates, creates a user, serves, logs in and checks that `GET /api/v1/meta/demo_note` says `"app":"demo"`.

### Example apps are modules of their own

`examples/sales` and `examples/sales_extension` are Go modules (`github.com/tvstore/forgebase-go/examples/sales` and `.../sales_extension`) with their own `go.mod`, a `replace` to the checkout (and `sales_extension` to `../sales`), and the checkout's `go.sum`. They could be packages of the root module, which would need no `go.mod` and be covered by `./...`, but then the compiler would let them import `internal/...` (the module root is the parent of `internal/`), and an example exists to show what an application can do **with the public API only**, which only a separate module enforces, the way the Rust examples were separate crates. They add no dependency: the root module's `go.mod` and `go.sum` are unchanged. CI runs each of them in the job `check` (vet, staticcheck, govulncheck, `go test -race`, build, `go mod tidy -diff`), against the same PostgreSQL service. Directory names of the DocTypes (`examples/sales/doctypes`) are unchanged and byte-identical to the Rust files; the end-to-end seed of the desk still copies them.

### App migrations and the sites

`internal/db.MigrateApps` (and `db.Migrate`, which calls it without apps) applies the app migrations under the same session-level lock as the framework migrations and the schema sync: before the sync every applied migration of the binary's apps is verified (a changed checksum, one the app no longer declares, or a new one numbered below an applied one is refused and nothing but the framework migrations is applied), after it each pending migration runs in its own transaction with `lock_timeout` set to `--lock-timeout-ms`, together with its `fb_app_migration` row, and `fb_app` is upserted with the app's name and version. The report text of `migrate` is that of Rust. **Per-site `apps`** (ADR 0014, Rust M7.2) is included because the config already parsed it: a site composes only its installed apps (a problem found only there names the site), and `migrate` warns about an app recorded in the database that the site does not install (its tables and data are kept). `site list` and `site create` come later in P5; the worker is in this phase (above).

## Consequences

- An app imports `github.com/tvstore/forgebase-go` (and, in tests, `.../testing`) and nothing else; the compiler enforces it.
- The Rust macros and their path rules disappear; two mistakes become startup errors that the Rust compiler caught earlier (a `.sql` file with a name that is not `NNNN_name.sql`; a DocType file outside the embed pattern, which the guard catches).
- The public API is small and every addition is a method or a type, not a change.
- The decimal type gained arithmetic that Rust gets from its library.
- **Limit of aliases: `go doc` and pkg.go.dev do not list the methods of an alias of an internal type.** `go doc forgebase.App` shows `type App = app.App` and the package overview names the methods (`DependsOn`, `DocTypes`, ...), but `go doc forgebase.App.DocTypes` finds nothing; the godoc of each method is in `internal/app/apps.go`, which a reader of the repository can open. The same holds for `Binary` and for the hook types. This is the price of having no wrapper code.

## Alternatives considered

- **Explicit lists in Go** (`DocTypes(DocTypeFile{"doctypes/a.toml", text}, ...)` from `embed.FS` reads): more code in every app for the same safety the guard already gives; rejected.
- **A functional options constructor** (`NewApp("sales", WithDependsOn("crm"))`): no gain over chained methods for data this small, and it makes the later additions look like options; rejected.
- **Wrapping core types for the public API:** a second copy of `Document`, `Value` and `HookContext` and conversions on every hook call, to hide package names the compiler already hides; aliases give the same boundary with no code.
- **Wrapper types for `App` and `Binary` in the root package** (a struct with the same methods delegating to `internal/app`): `go doc` and pkg.go.dev would then list every method with its documentation. It costs about sixty lines and a seam so that `forgebase/testing` can reach the wrapped value (an exported accessor, which is API, or a registry, which is a global); not done for now. The owner may ask for it; it changes no call in an app.
- **Examples inside the root module:** see above.

### Fixtures (P7.2)

[ADR 0022](0022-fixtures.md) is the design; this is its Go spelling. `App` gets one method and `forgebase/testing` one type and two fields (no existing call changes):

```go
func (*App) Fixtures(fsys fs.FS) *App

type AppliedFixtures struct{ App string; Inserted, Updated, Kept, Unchanged int }
// Migrated.Fixtures []AppliedFixtures; Migrated.FixturesPlanned bool
```

```go
//go:embed fixtures/*.json
var fixtures embed.FS

forgebase.NewApp("sales").DocTypes(doctypes).Migrations(migrations).Fixtures(fixtures)
```

- **`Fixtures(fs.FS)`** reads every file whose name ends in `.json`, at any depth, in the order of `fs.WalkDir` (lexical within each directory), with the path in the file system as its name in messages (`fixtures/customer_group.json`, the path the Rust `fixtures!` list wrote). Names that start with a dot are skipped; a file that is not UTF-8 is a startup error naming it. Calling it again adds more files. This replaces the explicit list of `fixtures![..]` by the embedded directory, as `DocTypes` and `Migrations` do. **Difference from Rust: the order of the files.** The Rust list fixed the order in which files, and so DocTypes, are applied (it matters when a record links to a record of another file of the same app); here it is the lexical order of the paths, so an app that needs another order names its files `1_groups.json`, `2_lists.json`. Within a file the order of the records is the file's, as in Rust, and apps are applied in app order.
- **The rules, the messages and the checksum are those of ADR 0022**, byte for byte: the startup checks (file, DocType, naming, identity, keys, JSON types, numbers) with the Rust messages; the canonical JSON and its SHA-256 are produced by `internal/httpapi` (`canonical.go`) with a number formatter written for Go (`strconv` with precision -1 is the shortest text that reads back, the same digits as Rust's `{:e}`), and a set of test vectors (input, canonical text, SHA-256 computed by Python's `hashlib`, including `1e21`, `0.1`, `-0.0` and a 30-digit integer) fixes it in `canonical_test.go`, so a database that the Rust binary fixtured is not fixtured again by the Go binary. The parser is the one of the HTTP API (serde_json's rules and messages), so `-0`, an integer above `uint64` and the rest of the numbers that ADR 0022 refuses are refused here too, and the same numbers are accepted.
- **Applying** is `internal/db` (`fixtures.go`): one transaction per app with `lock_timeout`, the table of ADR 0022, the system caller `fixture:<app>`, and a refused record is `db.ErrFixtureFailed` (the message `fixtures of app <app> were not applied: <file>: record <n> (<name>): <reason>` of ADR 0022), returned in `MigrateReport.FixturesFailure` together with the report of what was applied, which `forgebase migrate` prints first. The dry run reads `fb_fixture` and the DocType tables and writes nothing.
- **`forgebase/testing`:** `DB.Migrate` applies the fixtures like `forgebase migrate`; a refused record is the returned error. `Migrated.Fixtures` lists the apps that have fixture records, in app order, with the counts (zero in a dry run while framework migrations are pending, when `FixturesPlanned` is false).

### Overlays, site custom fields and user permissions (P7.3)

`App` gets one method (an alias of `internal/app`, like the rest) and `forgebase/testing` four:

```go
func (*App) ExtendDocType(doctype string, fsys fs.FS) *App

func (*DB) AddCustomField(b *forgebase.Binary, doctype, fieldTOML string)
func (*DB) SetCustomFieldEnabled(doctype, name string, enabled bool)
func (*DB) ClientPolling(b *forgebase.Binary, interval time.Duration) *Client
func (*Client) ReloadMetadata()
```

The rules are those of [ADR 0023](0023-metadata-overlays.md), [ADR 0024](0024-field-permission-levels.md) and [ADR 0025](0025-user-permissions.md), and of the Rust API; what is different is the spelling and what Go cannot do:

- **`ExtendDocType(doctype, fs.FS)`** is the Rust `extend_doctype(doctype, overlays![...])`. Like `DocTypes` and `Fixtures`, it reads every file whose name ends in `.toml`, at any depth, in the order of `fs.WalkDir` (lexical within each directory), with the path in the file system as its name in messages (`overlays/customer.toml`); names that start with a dot are skipped; calling it again adds more files, for the same DocType or another. Rust's list order (`overlays!["a.toml", "b.toml"]`) is the lexical order here, and it decides only the order of the fields the overlay adds and of the problems. The overlay files are not guarded by `AssertDocTypesListed` (the guard of Rust does not look at them either).
- **The startup errors** are those of Rust, in the same order (file problems first: a file that does not parse, one without `[[fields]]`; then the overlay rules, per overlay in app order and then file order: an unknown target, the app's own DocType, a target that no dependency of the app owns, a child DocType, a Table field, a name that starts with `custom_`, a name the DocType or an earlier overlay has already, the problems of the field itself). The hint of the dependency message names the Go call, as the one of the app rule does: `add DependsOn("crm"), registered before ...`.
- **The metadata of a site** is the registry of the apps (with their overlays) plus its enabled custom fields. `internal/db` has them as `RegistryHolder` (an atomic pointer to a registry that is never changed after it is built: `Snapshot`, `Reload`, `Poll`, `Base`), `EffectiveRegistry`, `AddCustomField`, `SetCustomFieldEnabled` and `ListCustomFields`. A request takes one snapshot at its start and keeps it. `State.StartMetaReload(ctx, log)` runs the version check of a site in a goroutine every `Config.MetaReloadInterval` (10 seconds; 0 turns it off); the Rust task ends when the last clone of the state is dropped, which Go cannot see, so the goroutine ends with the context that `serve` gives it. The worker checks every 10 seconds from its slots, one slot per interval. A stored field that no longer validates is skipped with a warning on standard error, every rebuild, and never fails the site.
- **`migrate` and the warnings.** The schema sync works from the registry plus every stored custom field, enabled or disabled, so a missing column comes back. The warnings about skipped fields are written as `warning:` lines to `db.SyncOptions.Warn` (standard error when nil; the command passes its own `Stderr`) before the sync, where Rust writes them with `eprintln`, so a sync that fails afterwards still shows them; `db.MigrateReport.CustomFieldWarnings` has the same messages.
- **The Administrator-only routes** are `GET|POST /api/v1/customize/{doctype}/fields`, `POST .../fields/{name}/disable|enable`, `GET|POST /api/v1/user-permissions` and `POST /api/v1/user-permissions/remove`; the commands are `customize field add|list|disable|enable` and `user permission add|list|remove`. Both use the same functions of `internal/db`, run as `cli` or as the administrator, and the commands read the registry of the site context, which has the enabled custom fields: `doc`, `data`, `webhook add` and `job enqueue` see them and refuse the disabled ones.
- **`forgebase/testing`.** `DB.Client` is `ClientPolling` at the default interval, and the polling stops with the test. `AddCustomField` takes the body of one `[[fields]]` table, as the command does, and `RunJobs` and `RunSchedulesAt` load the registry with the custom fields the database has, as a worker does.

The `notYet` mechanism of the command line (the commands that were parsed and then refused with the phase that brings them) is removed: every command exists.
