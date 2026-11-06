# Your first Forgebase app

This guide takes you from nothing to an app with tests that runs, extends another app and upgrades safely. Every command here was run against the repository at the time of writing. Background and decisions: [ADR 0012](../adr/0012-app-model-and-public-api-boundary.md) and [the architecture](../blueprint/architecture.md#the-app-model-m51-accepted-by-adr-0012).

An app is a Rust crate with three kinds of content: **DocTypes** (TOML files that describe your data and who may use it), **hooks** (Rust code that runs at fixed points of a document operation) and **migrations** (SQL that changes data once per database). Apps are compiled into one binary that you build and run. There are no plugins and no runtime discovery: everything is listed explicitly in `src/lib.rs` and `src/main.rs`.

## What you need

- A Forgebase checkout (the repository is public at <https://github.com/tvstore/Forgebase>; apps use it by path, see [the dependency](#the-forgebase-dependency)) and the Rust toolchain it needs (`rust-version` in its `Cargo.toml`).
- A PostgreSQL 16 server. The tests need a superuser (or `CREATEDB`) URL; running the app needs an empty database.

## 1. Create the app

Build the stock binary once, then generate:

```sh
cargo build -p forgebase                      # in the Forgebase checkout
target/debug/forgebase new-app crm --forgebase-path . --dir ~/apps
```

The name is the app name and the crate name: lowercase letters, digits and underscores, starting with a letter, at most 54 bytes (it is also the prefix of your DocTypes). The command refuses an existing directory, never overwrites, and needs no database. It creates `~/apps/crm`:

```
crm/
  Cargo.toml              package, forgebase path dependency, its own [workspace]
  Cargo.lock              copied from the Forgebase checkout
  src/lib.rs              pub fn app() -> forgebase::App
  src/main.rs             Forgebase::new().app(crm::app()).run()
  doctypes/crm_note.toml  one sample DocType, with permissions for the role "Crm User"
  migrations/README.md    how to add SQL data migrations
  tests/app.rs            guard test, startup check, a hook test, an end-to-end test
  README.md  AGENTS.md  .gitignore
```

`AGENTS.md` is for the people and agents who will maintain the app: where things live, and the rules (depend on `forgebase` only, list every DocType, additive DocType changes, append-only migrations, pure hooks). Keep it up to date.

## 2. Test and run it

```sh
cd ~/apps/crm
export FORGEBASE_TEST_DATABASE_URL=postgres://user:password@localhost:5432/postgres
cargo test                                    # 4 tests; each creates and drops its own database
```

The tests that need PostgreSQL **fail** with a clear message when the variable is not set; they never skip. Then run it against an empty database:

```sh
export FORGEBASE_DATABASE_URL=postgres://user:password@localhost:5432/crm
cargo run -- migrate                          # framework tables, schema sync, app migrations
echo 'a long password' | cargo run -- user create ann --role "Crm User"
FORGEBASE_DEV_INSECURE_COOKIES=1 cargo run -- serve    # http://127.0.0.1:8000/api/v1
```

Log in with `POST /api/v1/login` and `{"username": "ann", "password": "..."}`; the reply sets the session cookie. `GET /api/v1/meta/crm_note` then returns the DocType with `"app": "crm"`. `serve` does not migrate: run `migrate` after every change to DocTypes or migrations. `cargo run -- --help` lists the other commands. The binary of an app has all the commands of the stock `forgebase` binary, and does not need `--doctypes`.

## 3. DocTypes are embedded

`src/lib.rs` lists the files:

```rust
.doctypes(forgebase::doctypes!["doctypes/crm_note.toml"])
```

The files are read at **compile time**, with paths relative to the crate root, so the binary does not depend on the files at run time and a change to a file needs a rebuild. There is no directory scanning, so a file you forget to list would silently be missing; the test `every_doctype_file_is_listed` (it calls `forgebase::testing::assert_doctypes_listed`) fails instead. The file format is in [the architecture](../blueprint/architecture.md#doctype-file-format). Name every DocType with the app name as prefix: DocType names are global, and a name used by two apps is a startup error.

Change DocTypes additively. Adding a field or a DocType is applied by `migrate`; changing a field's type or length is refused (nothing is changed); reshaping existing data is a job for a migration.

## 4. Hooks

A hook set implements `forgebase::DocTypeHooks` for a DocType and is registered with `.hooks("crm_note", NoteRules)`. The sample trims the title and rejects a blank one. Hooks receive the document and a `HookContext`, run inside the operation's transaction, and have **no database access**. They run before the framework's own validation, so a value may be missing or of another kind: look before you change. The order of the hooks in each operation is in [ADR 0009](../adr/0009-lifecycle-pipeline-and-hooks.md) and in the documentation of `DocTypeHooks` (`cargo doc -p forgebase --open`). A hook is tested without a database by calling it with `HookContext::new(..)`, as the sample test does.

A DocType can also have an **approval workflow**: states and transitions with roles, written in the DocType file (`[workflow]`), and a `before_transition` hook for conditions. A transition needs a role of the transition and the right it stands for, and plain `submit` and `cancel` are refused on such a DocType. The full guide, with the `sales` example, is [workflow, saved views and reports](workflow-and-reports.md); the short version is in [Getting started](getting-started.md#workflow) and the rules are in [ADR 0015](../adr/0015-workflow-states-and-transitions.md).

## 5. Extend another app

Two complete, tested examples live in the Forgebase checkout: [`examples/sales`](../../examples/sales/README.md) (DocTypes, hooks, a data migration) and [`examples/sales_extension`](../../examples/sales_extension/README.md), which depends on `sales`, links to its `customer` and adds a hook to its `sales_order`. Read them next to this section; their tests are the model for yours.

Make a second app, `tasks`, that links to the notes of `crm` and adds a rule to them.

```sh
target/debug/forgebase new-app tasks --forgebase-path . --dir ~/apps   # in the Forgebase checkout
```

`tasks` starts with the same sample as `crm`, which it does not need. Remove its sample DocType, link `crm`, and then replace three files.

```sh
cd ~/apps/tasks
rm doctypes/tasks_note.toml
cargo add crm --path ../crm      # adds crm = { version = "0.1.0", path = "../crm" } to [dependencies]
```

`doctypes/tasks_task.toml` (new):

```toml
name = "tasks_task"
label = "Task"

[[fields]]
name = "subject"
type = "data"
required = true

[[fields]]
name = "note"
type = "link"          # a Link to a DocType of an app we depend on
target = "crm_note"

[[permissions]]
role = "Tasks User"
read = true
write = true
create = true
```

`src/lib.rs` (replaces the sample, including its `NoteRules` hook):

```rust
use forgebase::{App, DocTypeHooks, Document, HookContext, HookError, Value};

pub fn app() -> App {
    App::new("tasks")
        .version(env!("CARGO_PKG_VERSION"))
        .depends_on(&["crm"])
        .doctypes(forgebase::doctypes!["doctypes/tasks_task.toml"])
        .hooks("crm_note", NoDrafts) // a hook on a DocType of crm
}

/// Rejects a note whose title starts with `DRAFT:`.
pub struct NoDrafts;

impl DocTypeHooks for NoDrafts {
    fn validate(&self, _ctx: &HookContext<'_>, doc: &mut Document) -> Result<(), HookError> {
        if let Some(Value::Text(title)) = doc.values.get("title")
            && title.starts_with("DRAFT:")
        {
            return Err(HookError::new("a draft note cannot be saved"));
        }
        Ok(())
    }
}
```

`src/main.rs` lists the dependency first:

```rust
use std::process::ExitCode;

fn main() -> ExitCode {
    forgebase::Forgebase::new()
        .app(crm::app())
        .app(tasks::app())
        .run()
}
```

`tests/app.rs` (replaces the sample tests, which test the sample DocType and hook that are gone). It builds the same binary as `main.rs`, checks the hook without a database, and runs both apps end to end with a user that has both roles:

```rust
use forgebase::testing::{TestDb, assert_doctypes_listed, json};
use forgebase::{DocTypeHooks, Document, Forgebase, HookContext, Operation, Value};

use tasks::{NoDrafts, app};

/// The same apps, in the same order, as `main.rs`.
fn binary() -> Forgebase {
    Forgebase::new().app(crm::app()).app(tasks::app())
}

#[test]
fn every_doctype_file_is_listed() {
    assert_doctypes_listed(&app(), env!("CARGO_MANIFEST_DIR"), "doctypes");
}

#[test]
fn the_apps_fit_together() {
    binary().check().expect("valid apps");
}

#[test]
fn a_draft_note_is_rejected_by_the_hook() {
    let ctx = HookContext::new("crm_note", "ann", Operation::Insert);
    let mut doc = Document::default();
    doc.values
        .insert("title".to_owned(), Value::Text("DRAFT: idea".to_owned()));
    assert!(NoDrafts.validate(&ctx, &mut doc).is_err());

    doc.values
        .insert("title".to_owned(), Value::Text("Plan".to_owned()));
    NoDrafts.validate(&ctx, &mut doc).expect("not a draft");
}

#[tokio::test]
async fn tasks_link_to_notes_and_drafts_are_refused() {
    let binary = binary();
    let db = TestDb::new().await;
    db.migrate(&binary, false).await.expect("migrate");
    db.create_user("ann", &["Crm User", "Tasks User"]).await;
    let ann = db.client(&binary).login("ann").await;

    // The hook of `tasks` runs on the DocType of `crm`.
    let draft = ann
        .post("/api/v1/resource/crm_note", json!({"title": "DRAFT: idea"}))
        .await;
    assert_eq!(draft.status, 422, "{:?}", draft.body);

    let note = ann
        .post("/api/v1/resource/crm_note", json!({"title": "Plan"}))
        .await;
    assert_eq!(note.status, 201, "{:?}", note.body);
    let note_name = note.data()["name"].as_str().expect("name").to_owned();

    // A task links to the note of the other app.
    let task = ann
        .post(
            "/api/v1/resource/tasks_task",
            json!({"subject": "Call", "note": note_name}),
        )
        .await;
    assert_eq!(task.status, 201, "{:?}", task.body);
    db.cleanup().await;
}
```

Run `cargo test` in both crates (`crm` still passes on its own). The rules of the model:

- `depends_on` names apps that must be registered **earlier**. Nothing is sorted; a wrong order is an error.
- An app may link to, and register hooks for, DocTypes of **itself and its dependencies** only.
- An app **cannot redefine another app's DocType**, but it can **add fields** to it with an overlay (the next subsection). Everything else is extended by hooks and by DocTypes of your own.
- Hooks of one DocType run in app order, then in registration order: the hook of `tasks` runs after the hooks of `crm`, and can reject what `crm` accepted.
- Every DocType reports its owner in the metadata API (`"app"`).

### Add a field to another app's DocType (overlays)

A worked example with `sales_extension`, and the site custom fields that complete it, is in [Customizing and restricting data](customizing.md#extending-another-apps-doctype).

`tasks` depends on `crm`, so it may add fields to `crm_note` without changing the DocType or the `crm` crate ([ADR 0023](../adr/0023-metadata-overlays.md)). An overlay file holds only `[[fields]]` entries, written exactly like those of a DocType file, with every property a field has except the type `table`:

```toml
# overlays/crm_note.toml
[[fields]]
name = "priority"
type = "select"
options = ["low", "normal", "high"]
```

```rust
.depends_on(&["crm"])
.extend_doctype("crm_note", forgebase::overlays!["overlays/crm_note.toml"])
```

`overlays!` embeds files like `doctypes!`, relative to the crate's root. The fields are appended after the fields of `crm_note` (apps in order, files in order), `migrate` adds the column, the hooks of every app see the field, and the `[[permissions]]` of `crm_note` apply to it (an overlay field may carry a `perm_level` as well; the rules of that level are the `level` rules of `crm_note`'s `[[permissions]]`, because an overlay file holds only `[[fields]]`, see [field permission levels](getting-started.md#field-permission-levels)). The DocType is still owned by `crm` (`"app": "crm"` in the metadata API).

Startup errors, each naming the app and the file: an unknown target; a target that none of your dependencies owns (your **own** DocTypes too: put the field in the DocType's file); a child DocType; a `table` field; a field name that the DocType or another overlay already has (the message names both apps); and a name that starts with `custom_`, a prefix reserved for [site custom fields](cli.md#customize-field-commands-m101) (an app's own DocType fields may not start with it either).

Two things are yours to watch: a `required` overlay field on a DocType that already has documents makes the next update of each of them fail until the field is filled in, and a `unique` field creates a unique index over the existing rows (they hold NULL for it, and NULLs never collide). The overlay of the example is `examples/sales_extension/overlays/customer.toml` (`loyalty_tier`).

## 6. Upgrade with migrations

`migrate` applies the schema changes of your DocTypes by itself. For changes to *data* (a backfill, a rename of a value) add a SQL migration: a file `migrations/0001_trim_subjects.sql`, listed in `src/lib.rs`:

```rust
.migrations(forgebase::migrations![
    (1, "trim_subjects", "migrations/0001_trim_subjects.sql"),
])
```

`forgebase migrate` runs them after the schema sync, in app order and version order, **once per database**, each in its own transaction together with the row in `fb_app_migration` that records it. A failing migration is rolled back and stops the run; the ones before it stay applied. The SQL must not contain `BEGIN` or `COMMIT`.

Migrations are **append-only**. The checksum of every applied migration is verified at every `migrate` (also with `--dry-run`), before anything but the framework migrations is changed:

- an applied migration that was **edited** is refused (`was changed after it was applied`);
- an applied migration that you **removed** from the list is refused;
- a **new** migration with a version lower than one already applied is refused (`a new migration must have a version above every applied one`), so number new migrations above the highest one. The numbers need not be consecutive.

`forgebase migrate --dry-run` shows what would be applied and changes nothing. Two `migrate` runs at the same time wait for each other (`--lock-timeout-ms`). `fb_app` records each app and the version it declared at its last migrate.

## 7. Background jobs and schedules

Work that should not happen inside a request, or that must happen later or on a timer, is a **job**. The full design is [ADR 0013](../adr/0013-jobs-scheduler-and-transactional-outbox.md); the running examples are `sales` and `sales_extension` (section 5), and every snippet below is from them.

**Register a job.** A job is a `JobHandler`, registered with `App::job`; it is known as `<app>.<name>`:

```rust
App::new("sales")
    .job("log_order_event", LogOrderEvent) // the job is `sales.log_order_event`
```

```rust
impl JobHandler for LogOrderEvent {
    fn run<'a>(&'a self, ctx: &'a mut JobContext<'_>) -> JobFuture<'a> {
        Box::pin(async move { /* use ctx */ Ok(()) })
    }
}
```

The handler returns a **boxed future** (`JobFuture`) because Forgebase has no async-trait dependency. Copy what you need out of `ctx.payload()` into owned values first: `ctx.documents()` borrows `ctx` mutably. The options (5 attempts, 30 s backoff doubling up to 1 h, 5 min timeout) are the defaults; `App::job_with` takes a `JobOptions`.

**Enqueue from a hook, with the outbox.** A hook calls `ctx.enqueue(name, payload)`. The job is written in the **same transaction** as the document change, after all hooks have run, so it exists only if the operation commits: a refused submit leaves no job.

```rust
fn before_cancel(&self, ctx: &HookContext<'_>, doc: &Document) -> Result<(), HookError> {
    let order = doc.name.as_deref().ok_or_else(|| HookError::new("no name"))?;
    ctx.enqueue("sales.log_order_event", json!({"order": order, "event": "cancelled"}))
}
```

`json!` is `forgebase::serde_json::json`. Never put a secret in a payload: it is stored in clear text. In a test, a hook needs a collector, or `enqueue` fails: `HookContext::new(..).with_jobs(&jobs)`, then `jobs.take()` lists what was enqueued (`EnqueuedJobs`).

**Be idempotent.** Delivery is **at least once**: a job can run twice (a worker crash, an operator's `job enqueue`). Everything the handler does to the database is atomic with the job's completion, but make the handler safe to repeat anyway, and any effect outside the database (an email) must be. Two ways from the examples: a row whose name is the idempotency key, looked up first (`get("sales_order_log", "<order>-<event>")`, which keeps the normal path quiet; for two runs at the same moment the unique name refuses the second insert, a failed insert runs in a savepoint so the transaction stays usable, and the handler looks again after it and returns `Ok` if the row now exists: a unique violation is a permanent `JobError`, so check for the row rather than for the error), and a field that remembers the source (`customer_visit.source_order`, looked up with a `list` filter). `ctx.id()` (the same for every attempt) and `ctx.attempt()` are there for effects that need a key or a log line.

**Read and write documents.** `ctx.documents()` offers `get`, `list`, `insert`, `update`, `delete`, `submit`, `cancel`, `amend` and `transition` (a workflow action). Each returns `Result<_, DocError>`, and `?` turns it into the `JobError` below. It runs as the **system context** (no permission rules) with the actor `job:<app>.<job>`, which becomes the `owner` and `modified_by` of what the job writes. Validation and hooks run as usual, each write is a savepoint, and everything commits or rolls back with the job. `list` returns one page of at most 500 documents and has no offset.

**Errors.** A returned `JobError::new(..)` (and a database error) is retried with backoff, then the job is `dead` until `forgebase job retry`. `JobError::permanent(..)` skips the retries: use it for what retrying cannot fix, such as a payload that is wrong or an order that no longer exists. Errors of `ctx.documents()` already follow this rule, so `?` is usually enough.

**Run on a schedule.** `App::schedule(job, Schedule)` enqueues one of the app's own jobs on a timer: every N minutes, every day or every week, **UTC only** (no cron, no time zones):

```rust
.job("daily_summary", DailySummary)
.schedule("daily_summary", Schedule::daily_at(2, 0))
```

The job gets `{"scheduled_for": "<the tick, RFC 3339 UTC>"}`: the time that was due, not the time the worker noticed it, so use it as the idempotency key (`sales.daily_summary` summarizes the day before it and names the row by that day). A tick is enqueued once however many workers run. Ticks missed while no worker ran are **not caught up**: one job runs, then the next future time. `forgebase migrate` stores the schedules, so run it after you add, change or remove one (it deletes schedules the binary does not declare).

**Run it.** Jobs need a worker; schedules need a worker and `migrate`:

```sh
cargo run -p sales -- migrate
cargo run -p sales -- worker                  # until Ctrl-C or SIGTERM; --concurrency N
cargo run -p sales -- job list --status dead  # job show <id>, job retry <id>, job enqueue <name> --payload '{..}'
```

**Test it.** With the `testing` feature: `run_jobs(&binary, &db).await` runs every due job with the worker's own code and returns how many ended `done`, `retried` or `dead`; jobs that hooks and other jobs enqueue run too. `run_schedules_at(&binary, &db, time).await` enqueues the schedules that are due at `time` (read `next_run_at` from `fb_schedule` with `TestDb::rows`, and use `OffsetDateTime::from_unix_timestamp`), and `run_jobs` then runs them. To test delivery twice, copy a job row with `TestDb::exec` (`INSERT INTO fb_job ... SELECT ... FROM fb_job`) and run again. A job that is retried is queued again after its backoff, so `run_jobs` does not run it again in the same call. The tests of `sales` and `sales_extension` show each of these.

**Common errors.** A schedule for a job the app did not register is a startup error, reported with the other mistakes of the app before any command runs:

```
error: invalid app setup:
  - app `sales`: schedule for job `daily_sumary`, which `sales` did not register (register it with App::job first; an app schedules only its own jobs)
```

An unknown job name in `ctx.enqueue` is found at run time, not at startup (it is a string): it fails the **operation** that enqueued it when the job is written, with a message like ``unknown job `sales.log_order_evnt` (jobs are registered by the apps of the binary as `<app>.<job>`)``. A job of an app that the worker's binary does not contain stays `queued` until a binary that has it runs a worker.

**Query reports.** An app can also define a read-only SQL report (`App::report`, with `Report`, `Param` and `Column`) that is run by the roles it names; the SQL is compiled in, its parameters are `$1..$n` and always bound, and `forgebase migrate` checks it against the database. Row-level rules do not apply inside a report. The guide with the `sales` report is [workflow, saved views and reports](workflow-and-reports.md); the design, the limits and what to watch for are in [ADR 0016](../adr/0016-query-reports-read-only-execution.md).

## 8. App methods

An app can add its own endpoint, `POST /api/v1/method/<app>.<method>`, for an operation that is not a plain create, read or change of one document. A method is a `MethodHandler`, registered with `App::method` and **one** of `.roles(&[..])` or `.any_user()` (neither, both or an empty list is a startup error: there are no guest methods). It runs **as the person who called** and in **one transaction**. The design is [ADR 0019](../adr/0019-app-method-endpoints.md).

```rust
use forgebase::serde_json::json;
use forgebase::{
    App, FilterOp, ListQuery, Method, MethodContext, MethodError, MethodFuture, MethodHandler, Value,
};

struct OrdersOf;

impl MethodHandler for OrdersOf {
    fn call<'a>(&'a self, ctx: &'a mut MethodContext<'_>) -> MethodFuture<'a> {
        Box::pin(async move {
            let Some(customer) = ctx.body()["customer"].as_str().map(str::to_owned) else {
                return Err(MethodError::bad_request("Say which customer."));
            };
            let query = ListQuery::new()
                .filter("customer", FilterOp::Eq, Value::Text(customer))
                .limit(500);
            // As the caller: a user who may read only their own orders gets only those.
            let orders = ctx.documents().list("sales_order", &query).await?;
            let names: Vec<&str> = orders.iter().map(|order| order.name()).collect();
            Ok(json!({ "orders": names }))
        })
    }
}

pub fn app() -> App {
    App::new("sales")
        .method("orders_of", Method::new(OrdersOf).roles(&["Sales User", "Sales Manager"]))
}
```

- `ctx.body()` is the request's JSON object (`{}` when there is none) and `ctx.caller()` the user's name and roles; `ctx.documents()` is the same handle a job has, but **it acts as the caller**: the permission rules of the DocTypes (`if_owner`, read rules, read-only fields, workflow roles) decide every call, so a method can do no more than its caller could.
- `ctx.enqueue("sales.some_job", payload)` writes a job in the method's transaction: it exists only if the method succeeds, and it runs later as the system context.
- `Ok(json)` commits and answers `200 {"data": json}`. Every error rolls the whole call back: `MethodError::bad_request(message)` (400) and `MethodError::unprocessable(message)` (422) answer `method_error` with **your message, which the client sees**, so write it for users; a `DocError` from `?` keeps the document API's status and code (`permission_denied` 403, `conflict` 409, `validation_failed` 422, ...); anything else, and a panic, is a generic 500.
- A person without a role of the method, an unknown method and a method of an app that the site does not install all get the same 404.
- **A real one:** the `sales` example has `sales.order_summary` ([`examples/sales/src/lib.rs`](../../examples/sales/src/lib.rs)), walked through with curl in [Integrating with other systems](integration.md#1-app-methods-another-system-asks-a-question).
- **Test it** with `forgebase::testing`: `db.client(&binary).login("ann").await.post("/api/v1/method/sales.orders_of", json!({"customer": "ada@example.com"}))`, and `db.create_token(user)` with `client.with_token(..)` for a token call. The route, its statuses and a curl call are in the [HTTP API guide](http-api.md#app-methods).

## 9. Fixtures

Some records should exist on every site that installs your app: the customer groups, the territories, a default price list. Ship them as a **fixture**: a JSON file with the records of one DocType, listed in `src/lib.rs`.

`fixtures/customer_group.json` (for a `customer_group` DocType with the naming field `title` and a `description` field):

```json
{
  "doctype": "customer_group",
  "records": [
    {"title": "Retail"},
    {"title": "Wholesale", "description": "Volume customers"}
  ]
}
```

```rust
.fixtures(forgebase::fixtures![
    "fixtures/customer_group.json",
])
```

Each record has the shape of the body of `POST /api/v1/resource/customer_group`: field values by name, a Link as the name of the target, a Table field as an array of row objects. The paths are relative to your crate's root, like `doctypes!` and `migrations!`, and the list is explicit.

`forgebase migrate` applies fixtures **after the app migrations**, in app order (an app's dependencies first), then file order, then record order. Every record goes through the same pipeline as any other write, as the system caller `fixture:<app>`: hooks, validation, naming, links and **webhooks** all run, so a webhook on `customer_group` is called for each record that `migrate` writes. All the fixtures of one app run in **one transaction**: a record that fails (a field that is too long, a link to a document that does not exist) rolls back that app's fixtures, and `migrate` stops with a message that names the app, the file and the record. `migrate` prints one line per app that has fixtures:

```
fixtures: sales: 2 inserted, 1 updated, 0 kept, 5 unchanged
```

`migrate --dry-run` prints the plan (`would insert 2, update 1, keep 0, 5 unchanged`) and writes nothing.

**The rules, in plain words.** Forgebase remembers a checksum of each record as you shipped it (table `fb_fixture`), so it can tell your changes from the site's:

| Situation | What `migrate` does |
|---|---|
| The record is new and the document does not exist | Inserts it. |
| The record is new and the site already has a document with that name | Leaves the document as it is (**kept**) and tracks the record from now on. The site's data wins. |
| You did not change the record | **Nothing**, even if the site edited or deleted the document. The site's decision stands. |
| You changed the record and the document exists | Updates **only the fields the record has** (a table the record has replaces the rows). Other fields, and everything else the site changed, stay. |
| You changed the record and the site deleted the document | Inserts it again: you shipped a new version. |
| You removed the record from the file | **Nothing.** Fixtures never delete; the document stays. |

So a site's edit survives until you ship a new version of that record. To let sites change a value freely, leave it out of the record, or never change the record after you ship it. To bring back something a site deleted, change the record.

**Which DocTypes.** Only a DocType with **field naming** (`naming = { field = "title" }`), because the name must be the same on every site: a random or series name is different each time. A record must have a non-empty text value for the naming field. **Not allowed:** a DocType with random or series naming, a submittable one, one with a workflow (a fixture update must not fight the lifecycle), and a child DocType (put its rows in the parent's table field). The DocType must belong to your app or to an app you depend on, and one file may hold the records of one DocType (one file per DocType per app). Two apps may not ship the same record. Every one of these mistakes, a key that is not a field, a read-only field and a value of the wrong JSON type is a **startup error**, reported with the other problems of your app. Whether a value is valid (a required field, a length, a link that exists) is checked when the record is applied, because a hook may fill or change fields.

**Tests.** `TestDb::migrate` applies fixtures too, and `Migrated::fixtures` has the counts of each app, so you can test that your records exist after `db.migrate(&binary, false)`. Each site applies its own fixtures: the fixtures of an app that a site does not install are not applied there.

The `sales` example ships a fixture next to its SQL migration, so one `migrate` shows an insert and an adoption: see [Integrating with other systems](integration.md#4-fixtures-records-the-app-ships). The design and the alternatives are in [ADR 0022](../adr/0022-fixtures.md).

Running your app for real (several sites, a reverse proxy, workers, upgrades, backups and restores) is in the [operations guide](operations.md).

## The public API boundary

Apps depend on the `forgebase` crate **only**. `forgebase-core`, `forgebase-db` and `forgebase-http` are internal: they change without notice, and an app that uses them will break. `forgebase` re-exports everything an app needs (the list is in [ADR 0012](../adr/0012-app-model-and-public-api-boundary.md#the-public-surface-crate-root-of-forgebase)): `App`, `Forgebase`, `doctypes!`, `migrations!`, `DocTypeHooks`, `HookContext`, `HookError`, `Operation`, `Document`, `Value` and the helpers of `forgebase::testing` (enable the cargo feature `testing` in `[dev-dependencies]` only, as the template does, so it is not in your binary). The version is 0.x: the API may still change between minor versions, and every change is in the [changelog](../../CHANGELOG.md). If you need something that is not exported, ask for it; do not work around it.

## The Forgebase dependency

The generated `Cargo.toml` points at your checkout: `forgebase = { path = "<checkout>/crates/forgebase" }`. A relative `--forgebase-path` is written as an absolute path, because it is relative to where you ran the command, not to the new crate. To depend on a git revision instead, replace the two `forgebase` lines with `forgebase = { git = "https://github.com/tvstore/Forgebase", rev = "<commit>" }` (the second one with `features = ["testing"]`).

The app is its own Cargo workspace (`[workspace]` in its `Cargo.toml`), so a `Cargo.toml` in a parent directory does not take it over. `new-app` copies the checkout's `Cargo.lock` into the app, so your first build uses the dependency versions Forgebase is tested and license-checked with, not whatever is newest. Cargo drops the entries the app does not use and adds the app itself on first use, so the first command must not be `--locked`; after that it can be. Commit `Cargo.lock`. Updating dependencies later is your decision (`cargo update`).

The toolchain: `rust-version` in the generated `Cargo.toml` is the minimum Forgebase supports. The Forgebase checkout pins a toolchain in its `rust-toolchain.toml`, which does not apply to an app outside the checkout; use the same one (`rustup toolchain install <version>` and `cargo +<version> ...`, or your own `rust-toolchain.toml`).

## Common errors

Everything about how the apps fit together is checked **before any command runs**, and all problems are reported together (exit code 1). Real examples, from the apps of section 5:

```
error: invalid app setup:
  - app `tasks` depends on `crm`, which is registered after it; register `crm` first (apps are never sorted)
  - app `tasks`: doctypes/tasks_task.toml: field `note` of doctype `tasks_task` refers to `crm_note`, which belongs to app `crm`; app `tasks` does not depend on `crm` (add .depends_on(&["crm"]), registered before `tasks`)
  - app `tasks`: hooks registered for DocType `crm_note` of app `crm`, which `tasks` does not depend on
```

The first line is the cause; the other two follow from it. This is the output of `tasks` with `.app(tasks::app()).app(crm::app())` in `main.rs`. Without `.depends_on(&["crm"])` at all, only the last two lines appear (the second example below).

```
error: invalid app setup:
  - app `tasks`: doctypes/tasks_task.toml: field `note` of doctype `tasks_task` refers to `crm_note`, which belongs to app `crm`; app `tasks` does not depend on `crm` (add .depends_on(&["crm"]), registered before `tasks`)
  - app `tasks`: hooks registered for DocType `crm_note` of app `crm`, which `tasks` does not depend on
```

```
error: invalid app setup:
  - app `tasks`: hooks registered for unknown DocType `crm_notes`
```

```
error: cannot load DocType files:
  - app `tasks`: doctypes/tasks_task.toml: doctype `crm_note` is already defined by app `crm` (doctypes/crm_note.toml); a DocType belongs to one app
```

A DocType file that is not listed is caught by the test, not at startup:

```
app `tasks`: the embedded DocType list and .../tasks/doctypes differ; in the directory but not in doctypes![..]: ["doctypes/tasks_other.toml"]; in doctypes![..] but not in the directory: []
```

Migration errors (from `migrate`):

```
error: migration 1 (trim_subjects) of app `tasks` was changed after it was applied: expected checksum 1aded17b..., database has 80f843fe...
error: migration 2 (late) of app `tasks` is not applied, but migration 3 of the app is; a new migration must have a version above every applied one (renumber it above 3); nothing was applied
```

Other things you may meet:

- `FORGEBASE_TEST_DATABASE_URL is not set ... Tests never skip.`: set it to a superuser or `CREATEDB` URL (`postgres://user:password@localhost:5432/postgres`).
- `FORGEBASE_DATABASE_URL is not set`: the app's own database for `migrate`, `serve`, `user` and `doc`.
- `new-app`: `... is not a Forgebase checkout`: `--forgebase-path` must be the repository root (it contains `crates/forgebase`); `... already exists; new-app never overwrites`: choose another name or `--dir`.
- A hook cannot reach the database or the network on purpose ([ADR 0009](../adr/0009-lifecycle-pipeline-and-hooks.md)). Compute from the document, or reject the operation.
