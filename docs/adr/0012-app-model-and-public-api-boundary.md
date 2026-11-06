# 0012 App model and public API boundary

- Status: Accepted. Public surface extended by M6.1 and M6.2 (see [ADR 0013](0013-jobs-scheduler-and-transactional-outbox.md)), M8.1 (see [ADR 0015](0015-workflow-states-and-transitions.md)) and M8.4 (see [ADR 0016](0016-query-reports-read-only-execution.md)). Original status: Accepted (owner decisions 1 to 4 and 6 and the API boundary of the M5 plan v2, 2026-10-29; implementation details of M5.1 separated below)
- Date: 2026-10-29

## Context

[ADR 0006](0006-extension-model.md) decided that a Forgebase app is a Rust crate compiled into the user's binary and registered through an explicit list. Until M4 the repository had no way to write such an app: the binary (`forgebase-cli`) was an executable only, the DocTypes were loaded from a directory at runtime, and hooks could only be registered by code that depends on the internal crates. The owner wants Forgebase to be open to extension the way ERPNext and custom apps stack on Frappe: a framework, apps on top of it, and apps on top of apps, with low long-term maintenance cost. M5 builds that. M5.1 (this ADR) is the framework part; M5.2 adds `forgebase new-app` and the guide, M5.3 adds example apps.

## Decision

The decisions below are the owner's (M5 plan v2, 2026-10-29).

1. **One entry crate, `forgebase`.** `forgebase-cli` is renamed to `forgebase` and is a library and a binary (the binary is still called `forgebase`). The workspace still has four crates. [ADR 0002](0002-modular-monolith-and-workspace-layout.md) is amended (its status says so; its decision text is unchanged).
2. **DocTypes are embedded in the binary** through an explicit list (`forgebase::doctypes!`) of files that are read at compile time, plus a guard test helper that fails when a file in the app's directory is not in the list. No directory scanning at runtime, no code generation.
3. **Composition.** A binary has several apps, registered in an **explicit order**; `depends_on` names apps that must come earlier and is checked. An app may register hooks for the DocTypes of the apps it depends on. An app cannot change another app's DocType definition (custom fields on another app's DocType are a later milestone, M10: the model leaves room for an overlay applied after the app's own definitions).
4. **App data migrations.** An app declares versioned SQL migrations that `forgebase migrate` applies once per database, and `fb_app` records which apps and versions were migrated.
5. (Unchanged) No runtime plugins ([ADR 0006](0006-extension-model.md)); hooks have no database access ([ADR 0009](0009-lifecycle-pipeline-and-hooks.md)).
6. **`forgebase::testing`**, behind the cargo feature `testing`, gives apps the helpers for their own tests.

**Public API boundary.** Apps depend on the `forgebase` crate **only**. `forgebase-core`, `forgebase-db` and `forgebase-http` are internal: no compatibility promise, their crate documentation says so. `forgebase` re-exports exactly what an app needs (list below). The version is 0.x: the public API may change between minor versions, and every change goes in the changelog.

### The public surface (crate root of `forgebase`)

| Item | Purpose |
|---|---|
| `App` (`new`, `version`, `depends_on`, `doctypes`, `hooks(doctype, impl DocTypeHooks + 'static)`, `migrations`, `name`; `Debug`) | An app, built with a small builder. Only data: building it cannot fail. |
| `DocTypeFile` (`new`, `path`, `toml`), `MigrationFile` (`new`, `version`, `name`, `path`, `sql`), macros `doctypes!`, `migrations!` | The embedded files: path and contents, resolved relative to the calling crate. Private fields, `#[non_exhaustive]`; `new` builds one by hand (a test). |
| `Forgebase` (`new`, `app`, `check`, `run`, `run_from`) | A binary: its apps in order, and the command line. |
| `StartupError` (`problems`, `Display`) | Every problem of the app setup, together. |
| `DocTypeHooks`, `HookContext` (`new`; `#[non_exhaustive]`), `HookError`, `Operation` (`#[non_exhaustive]`), `Document`, `Row`, `Value` (`#[non_exhaustive]`), `Decimal`, `Date`, `OffsetDateTime` | What a hook implements, receives and changes. `Decimal`, `Date` and `OffsetDateTime` are the payloads of `Value`, re-exported so that an app uses the same versions. |
| `testing` (feature `testing`): `TestDb`, `HttpClient`, `Reply`, `Migrated`, `AppliedMigration` (`new`; the last three are `#[non_exhaustive]`), `MigrateError`, `PASSWORD`, `assert_doctypes_listed`, `Json`, `json!`, `serde_json` | Helpers for tests of an app. |

Nothing else is public. The stock `forgebase` binary is `Forgebase::new().run()`: no apps, DocTypes from `--doctypes DIR`.

### Public surface update (M6.1)

[ADR 0013](0013-jobs-scheduler-and-transactional-outbox.md) (the M6 plan, owner decisions of 2026-10-30) adds these items to the crate root of `forgebase`. Nothing else becomes public; the existing items are unchanged except `HookContext`, which gets two methods.

| Item | Purpose |
|---|---|
| `App::job(name, impl JobHandler + 'static)`, `App::job_with(name, handler, JobOptions)` | Register a background job; its full name is `<app>.<name>`. An invalid or duplicate name and invalid options are startup errors. |
| `JobHandler` (`run`), `JobFuture` | What a job implements: a boxed `Send` future (no async-trait crate). |
| `JobContext` (`name`, `id`, `attempt`, `payload`, `enqueue`, `documents`; `#[non_exhaustive]`, no public fields) | What a running job knows and may do, inside the job's transaction. |
| `Documents` (`get`, `list`, `insert`, `update`, `delete`, `submit`, `cancel`, `amend`), `StoredDocument` (`name`, `version`, `docstatus`, `document`; `#[non_exhaustive]`), `ListQuery` (`new`, `filter`, `order_by`, `limit`; `#[non_exhaustive]`), `FilterOp` (`Eq`, `Ne`, `Lt`, `Le`, `Gt`, `Ge`, `Like`; `#[non_exhaustive]`) | The documents handle of a job (the system context, actor `job:<app>.<job>`). They wrap the types of `forgebase-db`, which are not re-exported. |
| `JobError` (`new`, `permanent`, `is_permanent`, `message`), `JobOptions` (`new`, `max_attempts`, `timeout`, `backoff`; `#[non_exhaustive]`) | A job's failure (retryable or permanent) and its options (5 attempts, 30 s doubling to 1 h, 5 min timeout). |
| `HookContext::enqueue(name, payload)`, `HookContext::with_jobs(&EnqueuedJobs)`, `EnqueuedJobs` (`new`, `take`; `#[non_exhaustive]`), `EnqueuedJob` (`name`, `payload`; `#[non_exhaustive]`) | A hook asks for a job, written with the document change (the outbox). `with_jobs` lets an app's unit test see what a hook enqueued. `HookContext` stays `Copy`. |
| `serde_json` (the crate), `Changes`, `DocStatus` | The payload type, re-exported so an app uses the same version as the framework; `Changes` and `DocStatus` are what `Documents` takes and returns (`Changes` was already the argument of `update`). |
| `testing::run_jobs(&Forgebase, &TestDb) -> JobRun` (`done`, `retried`, `dead`; `#[non_exhaustive]`) | Runs the jobs that are due with the worker's own code. |

### Public surface update (M6.2)

[ADR 0013](0013-jobs-scheduler-and-transactional-outbox.md) (M6.2) adds these items to the crate root of `forgebase`. Nothing else becomes public.

| Item | Purpose |
|---|---|
| `Schedule` (`every_minutes`, `daily_at`, `weekly_on`; opaque, `#[non_exhaustive]`, `Debug`, `Clone`, `PartialEq`) | When a job runs: every N minutes (epoch aligned), daily or weekly, UTC only. Invalid values are startup errors. |
| `Weekday` (`time::Weekday`, re-exported) | The day of `Schedule::weekly_on`; the same `time` version as `Date` and `OffsetDateTime`. |
| `App::schedule(job, Schedule)` | Runs one of the app's own jobs on a schedule. A job the app did not register, a second schedule for a job and an invalid schedule are startup errors. |
| `testing::run_schedules_at(&Forgebase, &TestDb, OffsetDateTime) -> usize` | Enqueues the schedules that are due at the given time with the worker's own tick; `run_jobs` then runs them. |

The app name `forgebase` is reserved for the framework's own jobs (a startup error for an app).

### Public surface update (M8.1)

[ADR 0015](0015-workflow-states-and-transitions.md) (the M8 plan, owner decisions of 2026-10-30) adds these items. Nothing else becomes public; the existing items are unchanged.

| Item | Purpose |
|---|---|
| `Operation::Transition` (`Operation` is `#[non_exhaustive]`) | The operation a hook runs for when a document of a DocType with a workflow takes a transition, and for the `validate` hooks of a move that does not submit or cancel. A move that submits or cancels runs its hooks with `Operation::Submit` or `Operation::Cancel`. |
| `HookContext::transition` (a public field, `Option<&TransitionInfo>`), `HookContext::with_transition(&TransitionInfo)` | The transition a hook runs for: `Some` for the `before_transition` hook and for `Operation::Transition`, `None` for every other operation. `with_transition` lets an app's unit test of a hook build the context. `HookContext` stays `Copy`. |
| `TransitionInfo` (`new(action, from, to)`; public fields `action`, `from`, `to`; `#[non_exhaustive]`) | What the hook is told: the action and the two states. |
| `DocTypeHooks::before_transition(&self, ctx, &mut Document)` | The new hook point, with a default that does nothing: it runs after the permission checks and before `validate`, `before_submit` and `before_cancel` of the move; it may veto, change values and enqueue jobs. |

The workflow itself (`Workflow`, `WorkflowState`, `Transition` and the rest of `forgebase-core::workflow`) is **not** re-exported: an app writes it in the DocType file (`[workflow]`), and the types stay internal like the other metadata types. `Documents` (the job API) has no `transition` in M8.1.

### Public surface update (M8.4)

[ADR 0016](0016-query-reports-read-only-execution.md) (the M8 plan, owner decisions of 2026-10-30) adds these items. Nothing else becomes public; the existing items are unchanged.

| Item | Purpose |
|---|---|
| `Report` (`sql(&str)`, `label`, `roles`, `param`, `column`, `timeout`, `max_rows`; `#[non_exhaustive]`, no public fields) | An app-defined SQL report: trusted SQL compiled into the binary, the roles that may run it, typed parameters and columns. Parameters and columns are positional (`$1..$n` and the result columns in order). Limits default to 30 s and 10 000 rows and may be raised to 300 s and 100 000. |
| `Param` (`data`, `int`, `decimal`, `date`, `check`, `link(name, doctype)`, `select(name, &[options])`; `label`, `required`, `default_value`; `#[non_exhaustive]`) | A typed parameter, checked like the field of the same kind and always bound. |
| `Column` (`data`, `int`, `decimal`, `date`, `datetime`, `check`, `link(name, doctype)`; `label`; `#[non_exhaustive]`) | A typed result column; the SQL type must fit the kind (checked by `migrate`). |
| `App::report(name, Report)` | Defines a report, known as `<app>.<name>`. Every mistake is a startup error. |
| `testing::check_reports(&Forgebase, &TestDb) -> Vec<String>` (feature `testing`) | The static check of the reports' SQL that `forgebase migrate` runs after the migrations; an app's tests can run it. `TestDb::migrate` runs it too. |

The definition types (`ReportDef` and the rest of `forgebase-core::report`) and the execution (`forgebase-db::reports`) are **not** re-exported. `Value`, `Decimal` and `Date` (already public) are what `Param::default_value` takes.

### Public surface update (M9.1)

[ADR 0019](0019-app-method-endpoints.md) (the M9 plan, owner decisions of 2026-10-30) adds these items and makes one breaking change. Nothing else becomes public.

| Item | Purpose |
|---|---|
| `Method` (`new(handler)`, `roles(&[..])`, `any_user()`; no public fields) | A method and who may call it: exactly one of the two access calls is required, or it is a startup error. |
| `MethodHandler` (`call(&self, &mut MethodContext) -> MethodFuture`) and `MethodFuture<'a>` | What a method does; a boxed future like `JobHandler`. |
| `MethodContext` (`name`, `caller`, `body`, `documents`, `enqueue`; `#[non_exhaustive]`) | What a running method knows and may do, in its transaction. `documents()` acts as the caller; `enqueue` writes a job in the same transaction. |
| `MethodCaller` (`username`, `roles`, `has_role`) | The user who called, read-only. The system context is not reachable from a method. |
| `MethodError` (`bad_request(msg)`, `unprocessable(msg)`, `From<DocError>`) | Why a method failed; every error rolls the transaction back. |
| `DocError` (`is_retryable`, `Display`, `From<DocError> for JobError`) | The error of every `Documents` method. |
| `Documents::transition(doctype, name, action, version)` | A workflow action, for jobs and methods. |
| `App::method(name, Method)` | Registers a method, known as `<app>.<name>`. |
| `testing`: `TestDb::create_token`, `HttpClient::with_token`, `HttpClient::without_csrf`, `HttpClient::post_raw` (feature `testing`) | For the tests of methods: an API token client, a cookie client without the CSRF header, and a raw body. |

**Breaking change.** The methods of `Documents` return `Result<_, DocError>` instead of `Result<_, JobError>`. `?` into a `JobError` works unchanged, with the same retry rule; code that stored or matched the `JobError` of a `Documents` call converts with `.into()`, or uses `DocError::is_retryable` where it called `JobError::is_permanent`.

### Public surface update (M9.2)

[ADR 0020](0020-outgoing-webhooks.md) adds one item and nothing else becomes public. The webhook commands are part of the command-line tool, which has no compatibility promise beyond its documented behaviour.

| Item | Purpose |
|---|---|
| `testing::webhook_test_settings(ca_pem, timeout)` (feature `testing`) | For the tests of an app that sends webhooks: the PEM certificate of the test CA that outgoing webhooks trust over `https://`, and the time limit of one delivery. Set once for the process; the first call wins. |

The `forgebase` crate now depends on `axum` and `tower` without the `testing` feature (they were optional); both were already compiled in through `forgebase-http`.

### Public surface update (M9.4)

Amendment of 2026-11-01 (the text above is unchanged). [ADR 0022](0022-fixtures.md) (the M9 plan, owner decision 10 of 2026-10-30) adds these items; nothing else becomes public and nothing breaks.

| Item | Purpose |
|---|---|
| `App::fixtures(&'static [FixtureFile])` | Adds fixture files: records the app ships, applied by `migrate` through the document pipeline (inserted if missing, updated only when the app changes the record, never deleted). Calling it again adds more files. |
| `fixtures!` | Embeds JSON files with `include_str!`, paths relative to the calling crate's root, like `doctypes!` and `migrations!`. |
| `FixtureFile` (`new(path, json)`, `path()`, `json()`; `#[non_exhaustive]`, no public fields) | One fixture file, for the macro and for tests. |
| `testing::Migrated::fixtures`, `Migrated::fixtures_planned` (false in a dry run while framework migrations are pending, where the counts are zero) and `testing::AppliedFixtures` (`app`, `inserted`, `updated`, `kept`, `unchanged`; `#[non_exhaustive]`; feature `testing`) | What `TestDb::migrate` did with the fixtures of each app that has any. `TestDb::migrate` applies fixtures like `forgebase migrate`. |

Every mistake in a fixture file that needs no database is a startup error with the other app problems (`StartupError`).

### Public surface update (M10.1)

Amendment of 2026-11-01 (the text above is unchanged). [ADR 0023](0023-metadata-overlays.md) (the M10 plan, owner decisions 1 to 5 and 8 of 2026-10-31) adds these items; nothing else becomes public. It fulfils what point 3 of the Decision above left to M10: *"An app cannot change another app's DocType definition (custom fields on another app's DocType are a later milestone, M10: the model leaves room for an overlay applied after the app's own definitions)."* An app still cannot change or redefine a DocType, but it may now **add fields** to one of an app it depends on, through an overlay; the DocType stays owned by its one app. The reservation of the prefix `custom_` below is a **breaking change** for an app that used such a field name.

| Item | Purpose |
|---|---|
| `App::extend_doctype(doctype, &'static [OverlayFile])` | Adds the fields of overlay files to a DocType of an app this app depends on. Calling it again adds more files. Startup errors for an unknown target, a target that no dependency owns (the app's own DocTypes too), a child DocType, a Table field, a name that is taken (the message names both apps) and a name that starts with `custom_`. |
| `overlays!` | Embeds overlay files (TOML with only `[[fields]]`) with `include_str!`, paths relative to the calling crate's root, like `doctypes!`. |
| `OverlayFile` (`new(path, toml)`, `path()`, `toml()`; `#[non_exhaustive]`, no public fields) | One overlay file, for the macro and for tests. |
| `testing::TestDb::client_polling(&Forgebase, Duration)`, `TestDb::add_custom_field(&Forgebase, doctype, field_toml)`, `TestDb::set_custom_field_enabled(doctype, name, enabled)` and `testing::HttpClient::reload_metadata()` (feature `testing`) | Site custom fields in an app's tests: add or disable a field as the command line does, give a test server a short reload interval, and rebuild its registry at once. `run_jobs` and `run_schedules_at` run with the custom fields of the database, like a worker. `testing::Reply::header(name)` reads a response header. |

**A new reserved prefix.** A field name that starts with `custom_` is refused in an app's own DocType files (and in `--doctypes` files) with the other startup errors: the prefix belongs to site custom fields. This is a change in behaviour for an app that used such a name; none of the examples or templates did.

The metadata API, the command line (`forgebase customize field ...`) and the HTTP routes under `/api/v1/customize` are not Rust API and are documented in the guides.

## Implementation details (M5.1)

These were chosen while implementing M5.1; they are to be reviewed with that change, not owner decisions.

**Loading order.** (1) Check the apps themselves (names, dependencies, order, migration versions). (2) The DocTypes of all apps, in app order, each file parsed and added to one registry with its owner. (3) The `--doctypes DIR` DocTypes as one more app, named `local`, that depends on every app of the binary (so the stock binary and any binary can still load a directory). (4) The reference checks: the existing ones (`Registry::check_references`) and the app rule below. (5) The hooks, in app order, then registration order within an app. (6) At `migrate`, the app migrations in app order. All problems found are collected and reported together (`StartupError`, exit code 1); the reference and hook checks only run when every DocType file loaded, because a broken file would make them report follow-up errors. Every command checks this before it does anything else.

**Startup errors.** Invalid app name (the rules of a DocType name; `local` is reserved); a duplicate app name; a dependency that is not registered; a dependency registered **after** its dependent (there is no sorting: the order in `main` is the order); an app that depends on itself; a DocType defined by two apps (the message names both apps and both files); a DocType file that cannot be parsed or is invalid (names the app and the file); hooks for an unknown DocType, for a child DocType, or for a DocType of an app that is not the app itself or a (transitive) dependency; a Link or Table field that targets a DocType of an app that is not the app itself or a (transitive) dependency; migration versions that are not positive and strictly increasing.

**Ownership.** `forgebase_core::meta::Registry` records an owner tag (a string) per DocType (`add_owned`, `owner`); it attaches no meaning to it, so `forgebase-core` has no app concept beyond that string. The metadata API returns `"app"` for each DocType in `GET /api/v1/meta` and `GET /api/v1/meta/{doctype}` (`null` when the registry records no owner; a field addition, backward compatible). The desk does not use it yet.

**Hook order.** For one DocType: app order, then registration order within the app. An app that extends another runs after it, and its hook can reject what the first app accepted; the first app's changes to the document are visible to the second.

**App migrations.** Framework migration 0004 creates `fb_app` (`name`, `version`, `migrated_at`) and `fb_app_migration` (`app`, `version`, `name`, `checksum`, `applied_at`). `migrate` runs: framework migrations, verification of the applied app migrations, schema sync of all apps' DocTypes, then per app (in app order) its pending migrations by version, then the upsert of its `fb_app` row. The advisory lock of the framework migrations is taken as a **session-level** lock for the whole `migrate` (the framework migrations take the same key per transaction on the same connection, which does not conflict), so two `migrate` runs never interleave; the wait is bounded by `--lock-timeout-ms`. Each app migration is one transaction together with its `fb_app_migration` row; one that fails is rolled back and stops the run, and the migrations before it stay applied (so do the framework migrations and the schema sync). An applied migration whose SQL changed (SHA-256 computed by PostgreSQL, like the framework migrations) or that the app does not declare any more is an error; this is checked **before** the schema sync, so a refused run applies nothing but possibly the framework migrations. A declared migration that is not applied but has a **lower version than an applied one of the same app** is an error (`OutOfOrderAppMigration`) and nothing is applied, like an unknown applied migration: new migrations must be numbered above every applied one, so the order in which migrations ran is the same in every database. Each app migration runs with `SET LOCAL lock_timeout` set (bound parameter of `set_config`, as in the schema sync) to the `--lock-timeout-ms` value, so one that waits for a lock longer than that fails and is rolled back. The session-level lock lives on one connection that is **taken out of the pool** (`deadpool_postgres::Object::take`) and closed when `migrate` ends, also on a panic or when the future is dropped, so the lock cannot leak into the pool; if the lock wait fails, that error is reported even when the `RESET lock_timeout` that follows fails too. Apps that are not in the binary are left as they are (nothing is uninstalled; their rows stay). `migrate --dry-run` lists the pending app migrations without running them; while framework migrations are pending it lists all declared ones, because no app migration can be applied before migration 0004. `fb_app.version` is whatever the app declares (`NULL` if it declares none); `migrated_at` is the time of the last `migrate` that saw the app.

**SQL and trust.** App migrations are SQL only (no Rust-code migrations). They run with the same database role as everything else: app code is trusted and compiled in (see the [security model](../blueprint/security-model.md)). They must not contain `BEGIN` or `COMMIT`.

**Embedded files.** The macros expand to `include_str!(concat!(env!("CARGO_MANIFEST_DIR"), "/", path))`, evaluated in the crate that calls the macro, so paths are relative to the app crate's root. The expansion is `const { &[...] }` of `DocTypeFile::__new(..)` / `MigrationFile::__new(..)` calls (a `#[doc(hidden)]` constructor that may change with the macros), giving a `&'static [DocTypeFile]` or `&'static [MigrationFile]`; the macros use `::core::include_str!`, `::core::concat!` and `::core::env!`, so they do not depend on what the calling crate has in scope. A test can build a file by hand with `DocTypeFile::new` / `MigrationFile::new` (use them inside `const { &[...] }` to get a `'static` slice). The guard `testing::assert_doctypes_listed(&app, env!("CARGO_MANIFEST_DIR"), "doctypes")` compares the paths **relative to the crate root** (`doctypes/customer.toml`), exactly as written in `doctypes!`, so a file listed from another directory, or a file with the same name elsewhere, does not count as listed.

**`testing`.** `TestDb` creates a database `fb_test_app_*` on the server of `FORGEBASE_TEST_DATABASE_URL`, runs `migrate` for a `Forgebase`, creates users, runs SQL and hands out an `HttpClient` (the router called in process through `tower`, session cookie and CSRF handled). It adds `axum` and `tower` as optional dependencies of `forgebase` (both are already in the tree through `forgebase-http`). The crate's own integration tests turn the feature on with a dev-dependency on itself, so a plain `cargo test` runs them.

**The `--doctypes` option.** It is optional in a binary with apps. The stock binary has no apps, so there it is still required (a usage error, exit code 2, as before).

**Evolvable public types.** The boundary is fixed now, so each type that an app receives or builds is shaped so that it can grow without a breaking change:

- `DocTypeFile` and `MigrationFile`: private fields, getters, `#[non_exhaustive]`, a documented `new` for hand construction and a hidden `__new` for the macros.
- `HookContext`: `#[non_exhaustive]` with `HookContext::new(doctype, actor, operation)` (a `const fn`); fields stay public to read. Only the framework builds one, apart from a test that calls a hook directly.
- `Value`: `#[non_exhaustive]`, because new field types are on the roadmap and each adds a variant. Code outside `forgebase-core` matches with a wildcard arm; inside `forgebase-core` matches stay exhaustive. The two matches in the internal crates (binding a value to SQL in `forgebase-db`, writing it as JSON in `forgebase-http`) end in an explicit error for an unknown kind, not a silent default.
- `Operation`: already `#[non_exhaustive]`.
- `Document` (and `Row`, an alias of a map): left as plain data on purpose. A hook reads and changes `values`, `tables` and `name`, and `Document` implements `Default`, so an app that needs a new one writes `Document { name: .., ..Default::default() }`, which keeps compiling when a field is added. Making it `#[non_exhaustive]` would only force a constructor on every user for no gain.
- `HookError`: private message with `new` / `message`; `DocTypeHooks`: new methods can be added with a default body.
- `App::hooks` takes `impl DocTypeHooks + 'static` and wraps it in an `Arc` itself, so the public signature has no `Arc<dyn ...>`. One hook object per call: to share logic between DocTypes build one value for each (they can share state through an `Arc` field of their own). No blanket `impl DocTypeHooks for Arc<T>`: it would have to forward every method, and a method added later and forgotten there would silently fall back to the default.
- `Forgebase::run_from(args)` runs the command line with the given arguments (as clap's `try_parse_from`), and returns the exit code instead of exiting the process, also for help and usage errors.

## Implementation details (M5.2)

Chosen while implementing M5.2; to be reviewed with that change, not owner decisions (the owner decided what `new-app` is: one crate that is both the app library and a runnable binary, a path dependency on a Forgebase checkout, and an `AGENTS.md` for future agents).

- **The command.** `forgebase new-app <name> --forgebase-path <checkout> [--dir <parent>]` is a command of every Forgebase binary (it needs no apps and no database; the stock binary is the intended one). It creates `<parent>/<name>/` and never overwrites: an existing target is an error, and when writing fails the directory it created is removed again (a missing `--dir` and its parents are created first and are not removed). The placeholders of the templates are replaced in one pass, so a checkout path that contains `{{name}}` is written as it is. The name goes through the same function as the app names at startup (`app_name_problems`: the rules of a DocType name, `local` reserved), is limited to 54 bytes so that the sample DocType `<name>_note` fits the 59 bytes of a DocType name, and must not be a name that clashes with the crate's dependencies (`forgebase`, `tokio`, `std`, `core`, `alloc`, `test`, `proc_macro`); Rust keywords are refused too (`KEYWORDS` in `new_app.rs`, from the Rust reference: Cargo does not refuse them as package names, but `<name>::app()` in the generated `main.rs` would not compile). A refusal prints `invalid app name:` and one bullet per problem. The checkout is checked by reading `<checkout>/crates/forgebase/Cargo.toml` (it must be the package `forgebase`).
- **The template.** Plain files in `crates/forgebase/templates/app/`, embedded with `include_str!` (`Cargo.toml` is stored as `Cargo.toml.tpl` so that Cargo does not see a second package there, and the sample DocType as `doctypes/note.toml`, written as `doctypes/<name>_note.toml`). Four placeholders by simple string replacement, no template engine: `{{name}}`, `{{title}}` (`my_app` becomes `My App`: labels and the role `My App User`), `{{forgebase_path}}` and `{{rust_version}}` (`CARGO_PKG_RUST_VERSION` of the `forgebase` crate, which is the workspace MSRV, so the template cannot drift from it). A unit test fails on a placeholder the code does not know.
- **The path dependency.** An absolute `--forgebase-path` is written as given; a relative one is made absolute (canonicalized), because it is relative to where the command runs, not to the new crate, and a path computed relative to the crate would break when the crate is moved. The path is written into a TOML basic string with `\` and `"` escaped; a path that is not UTF-8 or has a control character is refused.
- **`testing` in the generated crate.** `forgebase` is listed twice: in `[dependencies]` without features and in `[dev-dependencies]` with `features = ["testing"]`. With the resolver of edition 2024 the dev-dependency features apply to tests and examples only, so the release binary does not contain `axum`/`tower` through `testing` (the helper code is not compiled into it). The alternative, `features = ["testing"]` on the normal dependency, would ship the helpers in every binary. `tokio` (macros, multi-thread runtime) is a dev-dependency of the generated crate, for `#[tokio::test]`; it is already in the Forgebase dependency tree and is not a new dependency of Forgebase.
- **Lock file.** The generated crate is its own workspace (`[workspace]` in its manifest, so a parent `Cargo.toml` cannot take it over) and gets a **copy of the checkout's `Cargo.lock`**. Cargo accepts a lock file written for another root package: it drops the entries the app does not use and adds the app, keeping the locked versions (checked: a generated app resolves the same versions as the workspace). The first Cargo command therefore has to be allowed to change the file (not `--locked`); afterwards `--locked` works. This gives a new app the dependency versions that Forgebase's tests and `cargo deny` cover instead of whatever crates.io has that day, at the price of one copied file; if the checkout has no `Cargo.lock`, nothing is copied. The alternative (leave it unlocked and document it) was rejected: the first build of every app would silently differ from what CI proves.
- **Tests of the generated crate.** The sample tests are the DocType-list guard, `Forgebase::check`, a hook test with `HookContext::new` (no database) and an end-to-end test with `TestDb`, a user with the sample role and the HTTP client. Like all Forgebase tests that need PostgreSQL, the last one **fails** with a clear message when `FORGEBASE_TEST_DATABASE_URL` is not set; it never skips.
- **Proof.** The CI job `app-template` builds the stock binary, generates `demo` outside the checkout, runs `cargo test` in it, builds it, and with the binary of the app runs `migrate`, creates a user with the sample role, serves, logs in with `curl` and reads `GET /api/v1/meta/demo_note`, which must say `"app":"demo"`. It sets `RUSTUP_TOOLCHAIN` to the toolchain of the checkout's `rust-toolchain.toml`, because the generated crate is outside the checkout. A Rust test generates without building (`tests/new_app.rs`): building needs the network and minutes, so it is the job's work. Run it locally as CONTRIBUTING.md says.
- **Follow-ups of M5.1.** `testing::AppliedMigration` (apps build it to compare) is `#[non_exhaustive]` with `AppliedMigration::new`; `Migrated` and `Reply` are `#[non_exhaustive]` and their fields stay public to read (nothing outside the crate builds them, so no constructor and no getters are needed). `assert_doctypes_listed` ignores a leading `./` on listed paths too. The dry run of `migrate_apps` while framework migrations are pending verifies and lists the app migrations when `fb_app_migration` exists (decided with `to_regclass`, exposed as `forgebase_db::app_migration_tables_exist`, an internal item), instead of listing every declared migration as pending; with only migrations up to 0004 in the tree the case cannot happen for real yet, so it is tested twice: the decision itself, and a simulation (migration 0004 is made pending again while the app tables, moved to another schema on the search path, exist) in which a dry run reports a changed applied migration and lists only the truly pending ones.

## Implementation details (M5.3)

Two example apps, `examples/sales` and `examples/sales_extension` (workspace members), exist as the proof that the public `forgebase` API is enough: the M5.3 change touches no file under `crates/`, and the apps depend on the `forgebase` crate only.

## Alternatives considered

- **Several public crates** (`forgebase-core`, `-db`, `-http` all public): a larger compatibility surface to keep stable for no benefit; rejected by the owner in favour of one entry crate.
- **Runtime plugins, `inventory`/`linkme` registration, proc macros**: against [ADR 0006](0006-extension-model.md) and the maintenance goal. An explicit function that returns data (`App`) and an explicit list in `main` is the whole mechanism.
- **Sorting apps by their dependencies**: hides the real order, which matters for hooks. The order is explicit and a wrong one is an error.
- **Scanning the `doctypes/` directory at compile time (build script or proc macro)**: magic and a build dependency; the explicit list plus a guard test costs one line per DocType.
- **Rust-code migrations**: more API surface and trust questions; SQL covers data backfills. Revisit with a real need.
- **Letting an app add fields to another app's DocType**: the Frappe "custom field" feature. Left to M10 (an overlay on top of the app's definitions) so the DocType definition stays owned by exactly one app.

## Consequences

- An app is a normal crate with a function `app() -> forgebase::App`; a binary is a normal crate with a `main` that lists the apps. Nothing is discovered at runtime, so an agent or a human finds all the wiring by reading `main`.
- Adding or changing what `forgebase` exports is an API change: update this list and the changelog.
- The internal crates can be refactored freely as long as the public surface above holds.
- A DocType can only be extended by hooks and, later (M10), by an overlay; it cannot be redefined by another app.
- The app model adds one migration (0004) and two small tables; uninstalling an app is out of scope.
