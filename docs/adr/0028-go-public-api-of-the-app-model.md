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
- **Later phases add methods; no existing call changes.** `Job`, `JobWith` and `Schedule` (P5), `Report` (P6), `Method`, `Fixtures` and `ExtendDocType` (P7) are not added yet. `App` and `Binary` have unexported fields only, so a method is an addition. `Binary.RunArgs` takes the arguments without the program name (`os.Args[1:]`), unlike Rust's `run_from`, which takes `args_os` with it.
- **`Binary` and not `Forgebase`.** `forgebase.Forgebase` would stutter; the type is the binary of an application, so it is `forgebase.Binary` and the constructor is `forgebase.New`. The Rust method `app` is `App`.
- **`DocTypes(fs.FS)`** reads every file whose name ends in `.toml`, at any depth, in the order of `fs.WalkDir` (lexical within each directory), and uses the path in the file system as its name in messages (an `embed.FS` of `//go:embed doctypes/*.toml` gives `doctypes/customer.toml`, the path the Rust list wrote). Names that start with a dot are skipped. This replaces the explicit list of `doctypes!` with the embedded directory itself: the list is the embed pattern, and the guard (below) catches a file the pattern does not reach. **The order of the Rust list** decided only the order in which problems are reported (the load order within an app has no other effect: DocTypes are unique and the registry is keyed by name), so lexical order is the mapping; an app that wanted a different order of messages loses nothing else. Calling it again adds more files.
- **`Migrations(fs.FS)`** reads every `*.sql` file the same way. The file is named `NNNN_name.sql`: the digits are the version (positive, at most 2 147 483 647) and the rest is the name recorded in `fb_app_migration`, so `0001_default_customer_groups.sql` is version 1 `default_customer_groups`, as the Rust call `(1, "default_customer_groups", "migrations/0001_default_customer_groups.sql")` wrote it. Any other `.sql` name is a startup error naming the file; other files (a README) are ignored. Within one call the migrations are ordered by version; across calls the versions must still increase, and equal versions are refused with the Rust message. A listing that is out of order, or has a negative version, which Rust could write by hand, cannot be written in file names. The checksum of an applied migration is the SHA-256 of the file text, as in Rust, so a database migrated by the Rust binary verifies in Go while the file is byte-identical.
- **`Hooks(doctype, hooks)`** takes a `DocTypeHooks` value (an interface; a pointer or a value type with value receivers). It is registered in app order, then registration order; a nil value is a startup error. A hook set is shared by every request and must be safe for concurrent use.
- **`Check`** returns a `*StartupError` (the type is an alias of the internal one; `Problems() []string` and `Error()`), and a nil `error`, not a typed nil, when the setup is valid.

The composition rules are those of ADR 0012 and are not repeated here: load order (apps in order, then the `--doctypes` directory as the app `local`, which depends on all of them), ownership (a DocType belongs to one app; the metadata API says which), `DependsOn` (registered earlier, never sorted), Link and Table references only within an app and its dependencies, hooks only for the DocTypes of an app and its dependencies, hook order, the app names `local` and `forgebase` are reserved, every problem reported together (exit code 1, the messages of Rust; the hint of the app-rule message names the Go call: `add DependsOn("crm"), registered before ...`).

### What a hook sees: type aliases of `internal/core`

An application cannot import `internal/core`, so the root package re-exports what a hook implements, receives and changes, as **type aliases** (an alias is the same type: nothing is wrapped or converted) and a few one-line constructors:

`DocTypeHooks`, `BaseHooks` (embed it), `HookContext`, `HookError` and `NewHookError`, `NewHookContext`, `Operation` with `OpInsert` ... `OpTransition`, `TransitionInfo` and `NewTransitionInfo`, `EnqueuedJobs`, `EnqueuedJob` and `NewEnqueuedJobs` (for a test of a hook), `Document`, `Row`, `Value` and `ValueKind` with `KindNull` ... `KindDatetime`, the constructors `Null`, `Text`, `Int`, `DecimalValue`, `Bool`, `NewDate` and `Datetime`, and `Decimal` with `ParseDecimal`, `NewDecimal` and `DecimalFromInt`.

`Date` and `OffsetDateTime` of Rust are the standard `time.Time` (a Date value is a `time.Time` whose clock part is ignored, `NewDate` builds one), so nothing is re-exported for them. The decimal constructor of a `Value` is `DecimalValue` because `Decimal` is the type. `HookContext.Enqueue` exists now; the job queue it feeds arrives in P5 (the name of a job that no app registered is refused when the job is written, as in Rust).

**The decimal arithmetic is an addition.** Rust apps compute with `rust_decimal`'s operators (the `sales` example multiplies, sums and rounds); the Go `internal/decimal` had none (ADR 0027: "the core does none"). P4 adds `Sign`, `Add`, `Mul` and `Rescale` to it, in `internal/decimal/arith.go`, and they are reachable through the `Decimal` alias. `Add`, `Mul` and `Rescale` return whether the result fits and never round to make it fit, which is stricter than `rust_decimal` (which scales a product down to fit); `Rescale` to fewer digits rounds half away from zero, the rule the example needs. A hook that needs the other behaviour rounds an operand first. This is the one deliberate extension of a Rust-equivalent surface in P4. **This amends [ADR 0027](0027-decimal-and-toml.md)**, whose "Known limits" say that the type has no arithmetic; ADR 0027 is not edited, and the index marks it as amended. The other deliberate deviation is that `Add` and `Mul` return not-ok where `rust_decimal` would scale the result down to fit (it rounds a product to fit 96 bits, an overflowing sum of mixed scales likewise); it is listed in the CHANGELOG parity list.

### What is public: the rule for aliased types

A type alias exposes the whole type: **every exported field and method of an aliased internal type is public API**, whether or not this ADR lists it, and an internal-only helper must therefore be a free function of its internal package (as `app.ComposeBinary`, `app.DocTypePaths` and `app.FilePaths` are, for `forgebase/testing`) or unexported, never an exported method. `internal/app` and `internal/core` say so in their package documentation. The exported members of the aliased types today (inspected from their method sets and fields):

| Alias | Exported fields | Exported methods |
|---|---|---|
| `App` | none | `DependsOn`, `DocTypes`, `Hooks`, `Migrations`, `Name`, `Version` |
| `Binary` | none | `App`, `Check`, `Run`, `RunArgs` |
| `StartupError` | none | `Error`, `Problems` |
| `DocTypeHooks` (interface) | | `BeforeInsert`, `Validate`, `BeforeDelete`, `BeforeSubmit`, `BeforeCancel`, `BeforeTransition` |
| `BaseHooks` | none | the same six, doing nothing |
| `HookContext` | `DocType`, `Actor`, `Operation`, `Transition` | `Enqueue`, `Equal`, `WithJobs`, `WithTransition` |
| `HookError` | none | `Error`, `Message` |
| `TransitionInfo` | `Action`, `From`, `To` | none |
| `EnqueuedJobs` | none | `Take` |
| `EnqueuedJob` | none | `Name`, `Payload` |
| `Document` | `Name`, `Values`, `Tables` | none |
| `Row` (map type) | | none |
| `Value` | `Kind`, `Text`, `Int`, `Dec`, `Bool`, `Time` | `Equal`, `KindName` |
| `ValueKind`, `Operation` | | none |
| `Decimal` | none | `Add`, `Cmp`, `Equal`, `IsNegative`, `IsZero`, `Mantissa` (a `*big.Int`), `MantissaDigits`, `Mul`, `Neg`, `Normalize`, `Rescale`, `Scale`, `Sign`, `String` |

`Value.Dec` has the internal type `decimal.Decimal`, which is the alias `Decimal`. The methods of these types are documented at their definitions: `go doc ./internal/app App.DocTypes` (run inside the module) shows the text that `go doc forgebase.App.DocTypes` cannot, and the package overview of `forgebase` names the methods.

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

Not in this version, with the phase that adds it: `run_jobs`, `JobRun` and `run_schedules_at` (P5); `check_reports` (P6, and `Migrate` will run it as `TestDb::migrate` does); `Migrated.Fixtures`, `AppliedFixtures`, `add_custom_field`, `set_custom_field_enabled`, `client_polling` and `reload_metadata`, and `webhook_test_settings` (P7). The public surface of `testing` stays in this ADR; later phases extend it here.

### Mapping of the public items of `crates/forgebase/src/lib.rs`

| Rust | Go |
|---|---|
| `App` (`new`, `version`, `depends_on`, `doctypes`, `hooks`, `migrations`, `name`) | `App` (`NewApp`, `Version`, `DependsOn`, `DocTypes`, `Hooks`, `Migrations`, `Name`) |
| `App::extend_doctype`, `fixtures`, `job`, `job_with`, `schedule`, `report`, `method` | later: `ExtendDocType`, `Fixtures` (P7); `Job`, `JobWith`, `Schedule` (P5); `Report` (P6); `Method` (P7) |
| `DocTypeFile`, `MigrationFile`, `FixtureFile`, `OverlayFile`, the macros `doctypes!`, `migrations!`, `overlays!`, `fixtures!` | none: `//go:embed` and `fs.FS` (above); fixtures and overlays arrive with `Fixtures` and `ExtendDocType` in P7 |
| `Forgebase` (`new`, `app`, `check`, `run`, `run_from`) | `Binary` (`New`, `App`, `Check`, `Run`, `RunArgs`) |
| `StartupError` | `StartupError` |
| `DocTypeHooks`, `HookContext`, `HookError`, `Operation`, `TransitionInfo`, `EnqueuedJob`, `EnqueuedJobs` | the same names (aliases), `BaseHooks` for the no-op base |
| `Document`, `Row`, `Value` | the same names (aliases) |
| `Changes`, `DocStatus` | P5, with the documents handle of a job (`Changes` and `DocStatus` are what `Documents` takes and returns) |
| `Decimal` | `Decimal` (alias of `internal/decimal`) |
| `Date`, `OffsetDateTime` | `time.Time` of the standard library |
| `Weekday` | `time.Weekday`, with `Schedule` (P5) |
| `serde_json` | none: `encoding/json` and `any` |
| `Documents`, `DocError`, `FilterOp`, `ListQuery`, `StoredDocument` | P5 |
| `JobContext`, `JobError`, `JobFuture`, `JobHandler`, `JobOptions`, `Schedule` | P5 |
| `Method`, `MethodCaller`, `MethodContext`, `MethodError`, `MethodFuture`, `MethodHandler` | P7 |
| `Column`, `Param`, `Report` | P6 |
| `testing` (`TestDb`, `HttpClient`, `Reply`, `Migrated`, `AppliedMigration`, `MigrateError`, `PASSWORD`, `assert_doctypes_listed`, `Json`, `json!`, `serde_json`) | `testing` (`DB`, `Client`, `Reply`, `Migrated`, `AppliedMigration`, `Password`, `AssertDocTypesListed`), see above |
| `testing::run_jobs`, `JobRun`, `run_schedules_at`, `check_reports`, `AppliedFixtures`, `webhook_test_settings`, `add_custom_field` ... | P5, P6, P7 as listed above |
| `forgebase new-app` | `forgebase new-app` (below) |

### `forgebase new-app`

`forgebase new-app NAME --forgebase-path DIR [--dir DIR]` writes a Go module that builds and passes its own tests as created: `go.mod` (module `NAME`; `go` and `toolchain` lines copied from the checkout's `go.mod`; `require github.com/tvstore/forgebase-go v0.0.0` and a `replace` to the absolute checkout path; the checkout's other requirements as `// indirect` lines, and its `go.sum`, so the build uses the versions Forgebase was tested with, the Go counterpart of copying `Cargo.lock`), the app package `NAME` (`app.go`, with the sample hook), `doctypes/NAME_note.toml` (byte-identical to the Rust template), `migrations/README.md`, `cmd/NAME/main.go` and its test, `app_test.go`, `README.md`, `AGENTS.md` (adapted to Go) and `.gitignore` (`/bin/`, where the README builds the binary: `go build -o bin/NAME ./cmd/NAME`, so no app name can make the file ignore its own directories). The templates are embedded with `//go:embed` and end in `.tpl` so that the Go tool does not compile them. Same placeholders, rendered in one pass; same rules: never overwrite, a failed run removes the directory it created, the same messages. The name rules are those of an app name plus the 54-byte limit; in place of the Rust keywords and reserved crate names a name may not be a Go keyword, a predeclared identifier or a top-level package of the standard library (the package clause uses the name, and an import path such as `fmt` would mean the library). The go command also treats some names specially, and each of these was verified by generating a module and running `go build`: the package patterns `std`, `all`, `tool` and `work` (not importable), `toolchain` (not a module path), `mod` ("disallowed import path"), `builtin` and `arena` (directories of `$GOROOT/src` that are not in `go list std`, so the import path is ambiguous with the library), the Windows device names (`con`, `prn`, `aux`, `nul`, `com1` to `com9`, `lpt1` to `lpt9`, which a module path may not have as an element) and `testdata` (the go tool skips a directory of that name, so `./...` would miss `cmd/testdata`). `main` (a library cannot be package `main`), `cmd`, `internal` and `vendor` are refused too; the last three as a precaution only (they are directories of `$GOROOT/src`; no build of them was shown to fail). Nothing else needs refusing: the generated files import the app package under the alias `apppkg` and are named `app.go` and `app_test.go`, so identifiers the templates declare (`t`, `binary`, `doctype`, ...) and file-name suffixes Go interprets (`_test`, `_linux`) cannot clash with the app name; a test generates and vets a module for each of a list of such names and checks that each refused name is refused. The CI job `app-template` generates `demo` outside the checkout, vets, tests and builds it, migrates, creates a user, serves, logs in and checks that `GET /api/v1/meta/demo_note` says `"app":"demo"`.

### Example apps are modules of their own

`examples/sales` and `examples/sales_extension` are Go modules (`github.com/tvstore/forgebase-go/examples/sales` and `.../sales_extension`) with their own `go.mod`, a `replace` to the checkout (and `sales_extension` to `../sales`), and the checkout's `go.sum`. They could be packages of the root module, which would need no `go.mod` and be covered by `./...`, but then the compiler would let them import `internal/...` (the module root is the parent of `internal/`), and an example exists to show what an application can do **with the public API only**, which only a separate module enforces, the way the Rust examples were separate crates. They add no dependency: the root module's `go.mod` and `go.sum` are unchanged. CI runs each of them in the job `check` (vet, staticcheck, govulncheck, `go test -race`, build, `go mod tidy -diff`), against the same PostgreSQL service. Directory names of the DocTypes (`examples/sales/doctypes`) are unchanged and byte-identical to the Rust files; the end-to-end seed of the desk still copies them.

### App migrations and the sites

`internal/db.MigrateApps` (and `db.Migrate`, which calls it without apps) applies the app migrations under the same session-level lock as the framework migrations and the schema sync: before the sync every applied migration of the binary's apps is verified (a changed checksum, one the app no longer declares, or a new one numbered below an applied one is refused and nothing but the framework migrations is applied), after it each pending migration runs in its own transaction with `lock_timeout` set to `--lock-timeout-ms`, together with its `fb_app_migration` row, and `fb_app` is upserted with the app's name and version. The report text of `migrate` is that of Rust. **Per-site `apps`** (ADR 0014, Rust M7.2) is included because the config already parsed it: a site composes only its installed apps (a problem found only there names the site), and `migrate` warns about an app recorded in the database that the site does not install (its tables and data are kept). `site list`, `site create` and the worker are P5.

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
