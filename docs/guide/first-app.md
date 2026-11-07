# Your first Forgebase app

> Go port status: the app model (this guide, sections 1 to 6) and the background jobs (section 7) are ported ([ADR 0028](../adr/0028-go-public-api-of-the-app-model.md)). Sections 8 and 9 (reports, app methods, fixtures, overlays) describe behaviour whose Go API arrives with its phase (P6, P7, see the [README](../../README.md#status)): their text is that of the Rust implementation, which does not change, and they say where the Go spelling is not there yet.

This guide takes you from nothing to an app with tests that runs, extends another app and upgrades safely. Every command here was run against the repository at the time of writing. Background and decisions: [ADR 0012](../adr/0012-app-model-and-public-api-boundary.md), [ADR 0028](../adr/0028-go-public-api-of-the-app-model.md) (the Go shape of the API) and [the architecture](../blueprint/architecture.md#the-app-model-m51-accepted-by-adr-0012).

An app is a Go module with three kinds of content: **DocTypes** (TOML files that describe your data and who may use it), **hooks** (Go code that runs at fixed points of a document operation) and **migrations** (SQL that changes data once per database). Apps are compiled into one binary that you build and run. There are no plugins and no runtime discovery: everything is wired explicitly in the app's `App()` function and in `cmd/<app>/main.go`.

## What you need

- A Forgebase checkout (the repository is public at <https://github.com/tvstore/forgebase-go>; apps use it by path, see [the dependency](#the-forgebase-dependency)) and the Go toolchain it needs (the `go` and `toolchain` lines of its `go.mod`).
- A PostgreSQL 16 server. The tests need a superuser (or `CREATEDB`) URL; running the app needs an empty database.

## 1. Create the app

Build the stock binary once, then generate:

```sh
go build -o bin/forgebase ./cmd/forgebase                     # in the Forgebase checkout
bin/forgebase new-app crm --forgebase-path . --dir ~/apps
```

The name is the app name and the module name: lowercase letters, digits and underscores, starting with a letter, at most 54 bytes (it is also the prefix of your DocTypes). It is the name of the Go package too, so it may not be a Go keyword, a predeclared identifier or a package of the standard library (`fmt`, `net`, ...). The command refuses an existing directory, never overwrites, and needs no database. It creates `~/apps/crm`:

```
crm/
  go.mod                  module crm; requires forgebase-go with a replace to the checkout
  go.sum                  copied from the Forgebase checkout
  crm.go                  func App() *forgebase.App, and the sample hook
  cmd/crm/main.go         forgebase.New().App(crm.App()).Run()
  cmd/crm/main_test.go    the binary starts and refuses what it should
  doctypes/crm_note.toml  one sample DocType, with permissions for the role "Crm User"
  migrations/README.md    how to add SQL data migrations
  crm_test.go             guard test, startup check, a hook test, an end-to-end test
  README.md  AGENTS.md  .gitignore
```

`AGENTS.md` is for the people and agents who will maintain the app: where things live, and the rules (import `forgebase` and `forgebase/testing` only, embed every DocType, additive DocType changes, append-only migrations, pure hooks). Keep it up to date.

## 2. Test and run it

```sh
cd ~/apps/crm
export FORGEBASE_TEST_DATABASE_URL=postgres://user:password@localhost:5432/postgres
go test ./...                                 # each database test creates and drops its own database
```

The tests that need PostgreSQL **fail** with a clear message when the variable is not set; they never skip. Then run it against an empty database:

```sh
export FORGEBASE_DATABASE_URL=postgres://user:password@localhost:5432/crm
go build -o crm ./cmd/crm
./crm migrate                                 # framework tables, schema sync, app migrations
echo 'a long password' | ./crm user create ann --role "Crm User"
FORGEBASE_DEV_INSECURE_COOKIES=1 ./crm serve  # http://127.0.0.1:8000/api/v1
```

Log in with `POST /api/v1/login` and `{"username": "ann", "password": "..."}`; the reply sets the session cookie. `GET /api/v1/meta/crm_note` then returns the DocType with `"app": "crm"`. `serve` does not migrate: run `migrate` after every change to DocTypes or migrations. `./crm --help` lists the other commands. The binary of an app has all the commands of the stock `forgebase` binary, and does not need `--doctypes`.

## 3. DocTypes are embedded

`crm.go` embeds the directory:

```go
//go:embed doctypes/*.toml
var doctypes embed.FS

func App() *forgebase.App {
	return forgebase.NewApp("crm").DocTypes(doctypes). // ...
}
```

`DocTypes` reads every `.toml` file of the file system, at any depth, in lexical order, and the path in it (`doctypes/crm_note.toml`) is the name of the file in messages. The files are read at **build time** (that is what `//go:embed` does), so the binary does not depend on the files at run time and a change to a file needs a rebuild. The embed pattern is the whole list, with nothing scanned at run time, so a file the pattern does not reach (a subdirectory, say) would silently be missing; the test `TestEveryDocTypeFileIsEmbedded` (it calls `fbtest.AssertDocTypesListed(t, crm.App(), "doctypes")`) fails instead. The file format is in [the architecture](../blueprint/architecture.md#doctype-file-format). Name every DocType with the app name as prefix: DocType names are global, and a name used by two apps is a startup error.

Change DocTypes additively. Adding a field or a DocType is applied by `migrate`; changing a field's type or length is refused (nothing is changed); reshaping existing data is a job for a migration.

## 4. Hooks

A hook set is a type that implements `forgebase.DocTypeHooks`, and is registered with `.Hooks("crm_note", NoteRules{})`. Embed `forgebase.BaseHooks`, whose methods do nothing, and override the points you need: `BeforeInsert`, `Validate`, `BeforeDelete`, `BeforeSubmit`, `BeforeCancel` and `BeforeTransition`. The sample trims the title and rejects a blank one:

```go
type NoteRules struct{ forgebase.BaseHooks }

func (NoteRules) Validate(_ forgebase.HookContext, doc *forgebase.Document) error {
	if title, ok := doc.Values["title"]; ok && title.Kind == forgebase.KindText {
		trimmed := strings.TrimSpace(title.Text)
		if trimmed == "" {
			return forgebase.NewHookError("the title must not be blank")
		}
		doc.Values["title"] = forgebase.Text(trimmed)
	}
	return nil
}
```

Hooks receive the document (`forgebase.Document`: `Name`, `Values`, `Tables`; a value is a `forgebase.Value` with a `Kind`) and a `forgebase.HookContext`, run inside the operation's transaction, and have **no database access**. They run before the framework's own validation, so a value may be missing or of another kind: look before you change. Returning an error rejects the whole operation; a hook set is shared by every request, so it must be safe for concurrent use. The order of the hooks in each operation is in [ADR 0009](../adr/0009-lifecycle-pipeline-and-hooks.md) and in the godoc of `DocTypeHooks` (`go doc github.com/tvstore/forgebase-go.DocTypeHooks`). A hook is tested without a database by calling it with `forgebase.NewHookContext(..)`, as the sample test does.

A DocType can also have an **approval workflow**: states and transitions with roles, written in the DocType file (`[workflow]`), and a `BeforeTransition` hook for conditions. A transition needs a role of the transition and the right it stands for, and plain `submit` and `cancel` are refused on such a DocType. The full guide, with the `sales` example, is [workflow, saved views and reports](workflow-and-reports.md); the short version is in [Getting started](getting-started.md#workflow) and the rules are in [ADR 0015](../adr/0015-workflow-states-and-transitions.md). (The transition routes and commands are ported in P6.)

## 5. Extend another app

Two complete, tested examples live in the Forgebase checkout, each a Go module of its own: [`examples/sales`](../../examples/sales/README.md) (DocTypes, hooks, a data migration) and [`examples/sales_extension`](../../examples/sales_extension/README.md), which depends on `sales`, links to its `customer` and adds a hook to its `sales_order`. Read them next to this section; their tests are the model for yours.

Make a second app, `tasks`, that links to the notes of `crm` and adds a rule to them.

```sh
bin/forgebase new-app tasks --forgebase-path . --dir ~/apps   # in the Forgebase checkout
```

`tasks` starts with the same sample as `crm`, which it does not need. Remove its sample DocType, require `crm`, and then replace four files.

```sh
cd ~/apps/tasks
rm doctypes/tasks_note.toml cmd/tasks/main_test.go
go mod edit -require=crm@v0.0.0 -replace=crm=../crm   # crm is a module next to this one
```

(With a module that has a proper path and a version, `go get` it instead; the `replace` is for modules that live on your disk.)

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

`tasks.go` (replaces the sample, including its `NoteRules` hook):

```go
package tasks

import (
	"embed"
	"strings"

	"github.com/tvstore/forgebase-go"
)

//go:embed doctypes/*.toml
var doctypes embed.FS

func App() *forgebase.App {
	return forgebase.NewApp("tasks").
		Version("0.1.0").
		DependsOn("crm").
		DocTypes(doctypes).
		Hooks("crm_note", NoDrafts{}) // a hook on a DocType of crm
}

// NoDrafts rejects a note whose title starts with "DRAFT:".
type NoDrafts struct{ forgebase.BaseHooks }

func (NoDrafts) Validate(_ forgebase.HookContext, doc *forgebase.Document) error {
	if title := doc.Values["title"]; title.Kind == forgebase.KindText && strings.HasPrefix(title.Text, "DRAFT:") {
		return forgebase.NewHookError("a draft note cannot be saved")
	}
	return nil
}
```

`cmd/tasks/main.go` lists the dependency first:

```go
package main

import (
	"os"

	"crm"
	"github.com/tvstore/forgebase-go"

	"tasks"
)

func main() {
	os.Exit(forgebase.New().
		App(crm.App()).
		App(tasks.App()).
		Run())
}
```

`tasks_test.go` (replaces the sample tests, which test the sample DocType and hook that are gone). It builds the same binary as `main.go`, checks the hook without a database, and runs both apps end to end with a user that has both roles:

```go
package tasks_test

import (
	"testing"

	"crm"
	"github.com/tvstore/forgebase-go"
	fbtest "github.com/tvstore/forgebase-go/testing"

	"tasks"
)

// binary is the same apps, in the same order, as cmd/tasks/main.go.
func binary() *forgebase.Binary {
	return forgebase.New().App(crm.App()).App(tasks.App())
}

func TestEveryDocTypeFileIsEmbedded(t *testing.T) {
	fbtest.AssertDocTypesListed(t, tasks.App(), "doctypes")
}

func TestTheAppsFitTogether(t *testing.T) {
	if err := binary().Check(); err != nil {
		t.Fatal(err)
	}
}

func TestADraftNoteIsRejectedByTheHook(t *testing.T) {
	ctx := forgebase.NewHookContext("crm_note", "ann", forgebase.OpInsert)
	doc := &forgebase.Document{Values: map[string]forgebase.Value{"title": forgebase.Text("DRAFT: idea")}}
	if err := (tasks.NoDrafts{}).Validate(ctx, doc); err == nil {
		t.Error("a draft must be rejected")
	}
	doc.Values["title"] = forgebase.Text("Plan")
	if err := (tasks.NoDrafts{}).Validate(ctx, doc); err != nil {
		t.Error(err)
	}
}

func TestTasksLinkToNotesAndDraftsAreRefused(t *testing.T) {
	b := binary()
	db := fbtest.NewDB(t)
	if _, err := db.Migrate(b, false); err != nil {
		t.Fatal(err)
	}
	db.CreateUser("ann", "Crm User", "Tasks User")
	ann := db.Client(b).Login("ann")

	// The hook of tasks runs on the DocType of crm.
	if draft := ann.Post("/api/v1/resource/crm_note", map[string]any{"title": "DRAFT: idea"}); draft.Status != 422 {
		t.Fatalf("%d %s", draft.Status, draft.Text)
	}
	note := ann.Post("/api/v1/resource/crm_note", map[string]any{"title": "Plan"})
	if note.Status != 201 {
		t.Fatalf("%d %s", note.Status, note.Text)
	}
	noteName := note.DataObject()["name"].(string)

	// A task links to the note of the other app.
	task := ann.Post("/api/v1/resource/tasks_task", map[string]any{"subject": "Call", "note": noteName})
	if task.Status != 201 {
		t.Fatalf("%d %s", task.Status, task.Text)
	}
}
```

Run `go test ./...` in both modules (`crm` still passes on its own). The test package of `crm` imports nothing of `tasks`. The rules of the model:

- `DependsOn` names apps that must be added to the binary **earlier**. Nothing is sorted; a wrong order is an error.
- An app may link to, and register hooks for, DocTypes of **itself and its dependencies** only.
- An app **cannot redefine another app's DocType**, but it can **add fields** to it with an overlay (the next subsection). Everything else is extended by hooks and by DocTypes of your own.
- Hooks of one DocType run in app order, then in registration order: the hook of `tasks` runs after the hooks of `crm`, and can reject what `crm` accepted.
- Every DocType reports its owner in the metadata API (`"app"`).

### Add a field to another app's DocType (overlays)

> Go port: arrives in P7 (`App.ExtendDocType`, [ADR 0028](../adr/0028-go-public-api-of-the-app-model.md)). The behaviour below is that of the Rust implementation and does not change; until then the Go `App` has no way to add the overlay file.

A worked example with `sales_extension`, and the site custom fields that complete it, is in [Customizing and restricting data](customizing.md#extending-another-apps-doctype).

`tasks` depends on `crm`, so it may add fields to `crm_note` without changing the DocType or the `crm` module ([ADR 0023](../adr/0023-metadata-overlays.md)). An overlay file holds only `[[fields]]` entries, written exactly like those of a DocType file, with every property a field has except the type `table`:

```toml
# overlays/crm_note.toml
[[fields]]
name = "priority"
type = "select"
options = ["low", "normal", "high"]
```

The overlay files are embedded like the DocType files and added to the app with a call that names the target DocType. The fields are appended after the fields of `crm_note` (apps in order, files in order), `migrate` adds the column, the hooks of every app see the field, and the `[[permissions]]` of `crm_note` apply to it (an overlay field may carry a `perm_level` as well; the rules of that level are the `level` rules of `crm_note`'s `[[permissions]]`, because an overlay file holds only `[[fields]]`, see [field permission levels](getting-started.md#field-permission-levels)). The DocType is still owned by `crm` (`"app": "crm"` in the metadata API).

Startup errors, each naming the app and the file: an unknown target; a target that none of your dependencies owns (your **own** DocTypes too: put the field in the DocType's file); a child DocType; a `table` field; a field name that the DocType or another overlay already has (the message names both apps); and a name that starts with `custom_`, a prefix reserved for [site custom fields](cli.md#customize-field-commands-m101) (an app's own DocType fields may not start with it either).

Two things are yours to watch: a `required` overlay field on a DocType that already has documents makes the next update of each of them fail until the field is filled in, and a `unique` field creates a unique index over the existing rows (they hold NULL for it, and NULLs never collide). The overlay of the Rust example is `examples/sales_extension/overlays/customer.toml` (`loyalty_tier`); the Go example gets it in P7.

## 6. Upgrade with migrations

`migrate` applies the schema changes of your DocTypes by itself. For changes to *data* (a backfill, a rename of a value) add a SQL migration: a file `migrations/0001_trim_subjects.sql`, embedded like the DocTypes:

```go
//go:embed migrations/*.sql
var migrations embed.FS

func App() *forgebase.App {
	return forgebase.NewApp("tasks").Migrations(migrations) // ...
}
```

The file name is the version and the name: `0001_trim_subjects.sql` is migration 1, `trim_subjects`. (`embed` needs at least one file for the pattern, which is why the generated app has the call commented out until its first migration.) Any other `.sql` name is a startup error; other files, such as `README.md`, are ignored.

`forgebase migrate` runs them after the schema sync, in app order and version order, **once per database**, each in its own transaction together with the row in `fb_app_migration` that records it. A failing migration is rolled back and stops the run; the ones before it stay applied. The SQL must not contain `BEGIN` or `COMMIT`.

Migrations are **append-only**. The checksum of every applied migration is verified at every `migrate` (also with `--dry-run`), before anything but the framework migrations is changed:

- an applied migration that was **edited** is refused (`was changed after it was applied`);
- an applied migration that you **removed** from the list is refused;
- a **new** migration with a version lower than one already applied is refused (`a new migration must have a version above every applied one`), so number new migrations above the highest one. The numbers need not be consecutive.

`forgebase migrate --dry-run` shows what would be applied and changes nothing. Two `migrate` runs at the same time wait for each other (`--lock-timeout-ms`). `fb_app` records each app and the version it declared at its last migrate.

## 7. Background jobs and schedules

> Go port: ported in P5 ([ADR 0028](../adr/0028-go-public-api-of-the-app-model.md)): `App.Job`, `App.JobWith`, `App.Schedule`, `JobHandler`, `JobContext`, `JobError`, `JobOptions`, `Documents`, `testing.RunJobs`, `testing.RunSchedulesAt` and the `worker` and `job` commands. The workflow transition of the documents handle arrives with P6.

Work that should not happen inside a request, or that must happen later or on a timer, is a **job**. The full design is [ADR 0013](../adr/0013-jobs-scheduler-and-transactional-outbox.md); the running examples are `sales` and `sales_extension` (section 5).

**Register a job.** A job is a handler registered on the app under a name; it is known as `<app>.<name>` (`sales.log_order_event`). A handler is a function that gets a context and the job:

```go
func App() *forgebase.App {
	return forgebase.NewApp("crm").
		DocTypes(doctypes).
		Job("welcome", welcome).
		JobWith("sync", syncCustomers, forgebase.NewJobOptions().MaxAttempts(3).Timeout(time.Minute))
}

func welcome(ctx context.Context, job *forgebase.JobContext) error {
	var payload struct {
		Customer string `json:"customer"`
	}
	if err := job.DecodePayload(&payload); err != nil {
		return forgebase.NewPermanentJobError("the payload must be {\"customer\": <name>}")
	}
	customer, err := job.Documents().Get(ctx, "customer", payload.Customer)
	if err != nil {
		return err // retried if the database is down, permanent for a refusal
	}
	if customer == nil {
		return forgebase.NewPermanentJobError("the customer is gone")
	}
	return nil
}
```

The options (5 attempts, 30 s backoff doubling up to 1 h, 5 min timeout) are the defaults; `JobWith` takes job options built from `NewJobOptions()`. A handler that needs state is a closure over it. **Pass the `ctx` to every call and return when it is done**: it is cancelled when the attempt times out or the worker stops, and a handler that ignores it keeps running (it cannot commit, but it holds its connection until it returns).

**Enqueue from a hook, with the outbox.** A hook calls `ctx.Enqueue(name, payload)` (the payload is anything `encoding/json` can marshal); a job calls `job.Enqueue(ctx, name, payload)` and the child exists only if the parent completes. The job is written in the **same transaction** as the document change, after all hooks have run, so it exists only if the operation commits: a refused submit leaves no job.

Never put a secret in a payload: it is stored in clear text. In a test, a hook needs a collector, or `Enqueue` fails: `forgebase.NewHookContext(..).WithJobs(jobs)` with `jobs := forgebase.NewEnqueuedJobs()`, then `jobs.Take()` lists what was enqueued (the `sales` example's `TestSubmitAndCancelEnqueueTheLogJob`).

**Be idempotent.** Delivery is **at least once**: a job can run twice (a worker crash, an operator's `job enqueue`). Everything the handler does to the database is atomic with the job's completion, but make the handler safe to repeat anyway, and any effect outside the database (an email) must be. Two ways from the examples: a row whose name is the idempotency key, looked up first (a `get` of `sales_order_log` named `<order>-<event>`, which keeps the normal path quiet; for two runs at the same moment the unique name refuses the second insert, a failed insert runs in a savepoint so the transaction stays usable, and the handler looks again after it and returns `Ok` if the row now exists: a unique violation is a permanent `JobError`, so check for the row rather than for the error), and a field that remembers the source (`customer_visit.source_order`, looked up with a `list` filter). The job's id (the same for every attempt) and its attempt number are there for effects that need a key or a log line.

**Read and write documents.** The job's documents handle (`job.Documents()`) offers `Get`, `List`, `Insert`, `Update`, `Delete`, `Submit`, `Cancel` and `Amend` (and, from P6, `Transition`, a workflow action). `Get` returns `nil, nil` when there is no such document; `List` takes a query built with `forgebase.NewListQuery().Filter("order_date", forgebase.FilterEq, value).OrderBy("name", true).Limit(50)`. Its errors are a `*forgebase.DocError`, turned into the job errors below. It runs as the **system context** (no permission rules) with the actor `job:<app>.<job>`, which becomes the `owner` and `modified_by` of what the job writes. Validation and hooks run as usual, each write is a savepoint, and everything commits or rolls back with the job. `list` returns one page of at most 500 documents and has no offset.

**Errors.** An ordinary error returned by the handler (`errors.New`, a failed HTTP call) is retried with backoff, then the job is `dead` until `forgebase job retry`; so is a panic. A permanent job error (`forgebase.NewPermanentJobError`) skips the retries: use it for what retrying cannot fix, such as a payload that is wrong or an order that no longer exists. Errors of the documents handle already follow this rule: a database or connection error is retried, every refusal (validation, permission, a hook that rejected, not found, a version conflict, a unique violation) is permanent.

**Run on a schedule.** A schedule enqueues one of the app's own jobs on a timer: every N minutes, every day or every week, **UTC only** (no cron, no time zones). The `sales` example runs `daily_summary` every day at 02:00:

```go
Schedule("daily_summary", forgebase.DailyAt(2, 0)) // also forgebase.EveryMinutes(15), forgebase.WeeklyOn(time.Monday, 7, 0)
```


The job gets `{"scheduled_for": "<the tick, RFC 3339 UTC>"}`: the time that was due, not the time the worker noticed it, so use it as the idempotency key (`sales.daily_summary` summarizes the day before it and names the row by that day). A tick is enqueued once however many workers run. Ticks missed while no worker ran are **not caught up**: one job runs, then the next future time. `forgebase migrate` stores the schedules, so run it after you add, change or remove one (it deletes schedules the binary does not declare).

**Run it.** Jobs need a worker; schedules need a worker and `migrate`:

```sh
./sales migrate
./sales worker                  # until Ctrl-C or SIGTERM; --concurrency N
./sales job list --status dead  # job show <id>, job retry <id>, job enqueue <name> --payload '{..}'
```

**Test it.** `forgebase/testing` has `RunJobs` and `RunSchedulesAt`: `RunJobs(binary, db)` runs every due job with the worker's own code and returns how many ended done, retried or dead (`run.Done`, `run.Retried`, `run.Dead`); jobs that hooks and other jobs enqueue run too. `RunSchedulesAt(binary, db, at)` enqueues the schedules that are due at a given time (read `next_run_at` from `fb_schedule` with `DB.Rows`), and `RunJobs` then runs them. To test delivery twice, copy a job row with `DB.Exec` (`INSERT INTO fb_job ... SELECT ... FROM fb_job`) and run again. A job that is retried is queued again after its backoff, so `RunJobs` does not run it again in the same call.

```go
db := fbtest.NewDB(t)
db.Migrate(binary, false)
db.Exec(`INSERT INTO fb_job (id, name, payload, status, enqueued_by) VALUES (gen_random_uuid(), 'crm.welcome', '{"customer": "ada"}', 'queued', 'test')`)
run := fbtest.RunJobs(binary, db) // run.Done == 1
```

The tests of the `sales` and `sales_extension` examples show each of these (`TestAJobDeliveredTwiceLogsOnce`, `TestTheDailySummaryCountsTheSubmittedOrdersOfTheDayBeforeTheTick`).

**Common errors.** A schedule for a job the app did not register is a startup error, reported with the other mistakes of the app before any command runs:

```
error: invalid app setup:
  - app `sales`: schedule for job `daily_sumary`, which `sales` did not register (register it with App.Job first; an app schedules only its own jobs)
```

An unknown job name in `ctx.Enqueue` is found at run time, not at startup (it is a string): it fails the **operation** that enqueued it when the job is written, with a message like ``unknown job `sales.log_order_evnt` (jobs are registered by the apps of the binary as `<app>.<job>`)``. A job of an app that the worker's binary does not contain stays `queued` until a binary that has it runs a worker.

**Query reports.** An app can also define a read-only SQL report (P6) that is run by the roles it names; the SQL is compiled in, its parameters are `$1..$n` and always bound, and `forgebase migrate` checks it against the database. Row-level rules do not apply inside a report. The guide with the `sales` report is [workflow, saved views and reports](workflow-and-reports.md); the design, the limits and what to watch for are in [ADR 0016](../adr/0016-query-reports-read-only-execution.md).

## 8. App methods

> Go port: the method API (`App.Method`, its handler and context) arrives in P7. The behaviour below is that of the Rust implementation and does not change; no Go code is shown because the exact Go shape is fixed with that phase ([ADR 0028](../adr/0028-go-public-api-of-the-app-model.md)).

An app can add its own endpoint, `POST /api/v1/method/<app>.<method>`, for an operation that is not a plain create, read or change of one document. A method is a handler registered on the app with **one** of a list of roles or "any user" (neither, both or an empty list is a startup error: there are no guest methods). It runs **as the person who called** and in **one transaction**. The design is [ADR 0019](../adr/0019-app-method-endpoints.md).

- The context has the request's JSON object (`{}` when there is none), the user's name and roles and a documents handle, the same one a job has, but **it acts as the caller**: the permission rules of the DocTypes (`if_owner`, read rules, read-only fields, workflow roles) decide every call, so a method can do no more than its caller could.
- Enqueueing a job writes it in the method's transaction: it exists only if the method succeeds, and it runs later as the system context.
- Returning a value commits and answers `200 {"data": value}`. Every error rolls the whole call back: a bad-request error (400) and an unprocessable error (422) answer `method_error` with **your message, which the client sees**, so write it for users; an error of the documents handle keeps the document API's status and code (`permission_denied` 403, `conflict` 409, `validation_failed` 422, ...); anything else, and a panic, is a generic 500.
- A person without a role of the method, an unknown method and a method of an app that the site does not install all get the same 404.
- **A real one:** the Rust `sales` example has `sales.order_summary`, walked through with curl in [Integrating with other systems](integration.md#1-app-methods-another-system-asks-a-question) (the Go example gets it in P7).
- **Test it** with `forgebase/testing`: `db.Client(binary).Login("ann").Post("/api/v1/method/sales.orders_of", map[string]any{"customer": "ada@example.com"})`, and `db.CreateToken(user)` with `client.WithToken(..)` for a token call. The route, its statuses and a curl call are in the [HTTP API guide](http-api.md#app-methods).

## 9. Fixtures

> Go port: fixtures (`App.Fixtures`, embedded like the DocTypes) arrive in P7. The behaviour below is that of the Rust implementation and does not change.

Some records should exist on every site that installs your app: the customer groups, the territories, a default price list. Ship them as a **fixture**: a JSON file with the records of one DocType, embedded in the app.

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

Each record has the shape of the body of `POST /api/v1/resource/customer_group`: field values by name, a Link as the name of the target, a Table field as an array of row objects.

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

**Tests.** `DB.Migrate` applies fixtures too, and `Migrated.Fixtures` has the counts of each app, so you can test that your records exist after `db.Migrate(binary, false)`. Each site applies its own fixtures: the fixtures of an app that a site does not install are not applied there.

The Rust `sales` example ships a fixture next to its SQL migration, so one `migrate` shows an insert and an adoption (the Go example gets it in P7): see [Integrating with other systems](integration.md#4-fixtures-records-the-app-ships). The design and the alternatives are in [ADR 0022](../adr/0022-fixtures.md).

Running your app for real (several sites, a reverse proxy, workers, upgrades, backups and restores) is in the [operations guide](operations.md).


## The public API boundary

Apps import the root package `github.com/tvstore/forgebase-go` and, in their tests, `github.com/tvstore/forgebase-go/testing` **only**. Everything else of the framework (`internal/core`, `internal/db`, `internal/httpapi`, `internal/app`) is under `internal/`, which the Go toolchain refuses to import from another module: the boundary is enforced by the compiler, and the example apps are separate modules for that reason. The root package re-exports everything an app needs (the list is in [ADR 0028](../adr/0028-go-public-api-of-the-app-model.md#the-root-package-forgebase)): `App` and `NewApp`, `Binary` and `New`, `DocTypeHooks` and `BaseHooks`, `HookContext`, `HookError`, `Operation`, `Document`, `Row`, `Value` and `Decimal`, and the helpers of `forgebase/testing`. The version is 0.x: the API may still change between minor versions, and every change is in the [changelog](../../CHANGELOG.md). If you need something that is not exported, ask for it; do not work around it.

## The Forgebase dependency

The generated `go.mod` points at your checkout: `require github.com/tvstore/forgebase-go v0.0.0` and `replace github.com/tvstore/forgebase-go => <checkout>`. A relative `--forgebase-path` is written as an absolute path, because it is relative to where you ran the command, not to the new module. To depend on a released version instead, delete the `replace` line and run `go get github.com/tvstore/forgebase-go@<version>`.

`new-app` copies the `go` and `toolchain` lines of the checkout's `go.mod`, its requirements (as `// indirect` lines) and its `go.sum` into the app, so your first build uses the dependency versions Forgebase is tested and license-checked with, not whatever is newest. `go mod tidy` leaves only what the app needs; commit `go.mod` and `go.sum`. Updating dependencies later is your decision (`go get -u`). The toolchain is the one the checkout pins; Go downloads it itself when your installed `go` is older (`GOTOOLCHAIN`, see <https://go.dev/doc/toolchain>).

## Common errors

Everything about how the apps fit together is checked **before any command runs**, and all problems are reported together (exit code 1). Real examples, from the apps of section 5:

```
error: invalid app setup:
  - app `tasks` depends on `crm`, which is registered after it; register `crm` first (apps are never sorted)
  - app `tasks`: doctypes/tasks_task.toml: field `note` of doctype `tasks_task` refers to `crm_note`, which belongs to app `crm`; app `tasks` does not depend on `crm` (add DependsOn("crm"), registered before `tasks`)
  - app `tasks`: hooks registered for DocType `crm_note` of app `crm`, which `tasks` does not depend on
```

The first line is the cause; the other two follow from it. This is the output of `tasks` with `App(tasks.App()).App(crm.App())` in `main.go`. Without `DependsOn("crm")` at all, only the last two lines appear (the second example below).

```
error: invalid app setup:
  - app `tasks`: doctypes/tasks_task.toml: field `note` of doctype `tasks_task` refers to `crm_note`, which belongs to app `crm`; app `tasks` does not depend on `crm` (add DependsOn("crm"), registered before `tasks`)
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

```
error: invalid app setup:
  - app `tasks`: migration file migrations/trim_subjects.sql must be named NNNN_name.sql (the digits are the version, for example 0001_backfill.sql)
```

A DocType file that is not embedded is caught by the test, not at startup:

```
app `tasks`: the embedded DocType set and doctypes differ; in the directory but not embedded: ["doctypes/tasks_other.toml"]; embedded but not in the directory: []
```

Migration errors (from `migrate`):

```
error: migration 1 (trim_subjects) of app `tasks` was changed after it was applied: expected checksum 1aded17b..., database has 80f843fe...
error: migration 2 (late) of app `tasks` is not applied, but migration 3 of the app is; a new migration must have a version above every applied one (renumber it above 3); nothing was applied
```

Other things you may meet:

- `FORGEBASE_TEST_DATABASE_URL is not set ... Tests never skip.`: set it to a superuser or `CREATEDB` URL (`postgres://user:password@localhost:5432/postgres`).
- `FORGEBASE_DATABASE_URL is not set`: the app's own database for `migrate`, `serve`, `user` and `doc`.
- `new-app`: `... is not a Forgebase checkout`: `--forgebase-path` must be the repository root (its `go.mod` names `github.com/tvstore/forgebase-go`); `... already exists; new-app never overwrites`: choose another name or `--dir`; `app name ... is a Go keyword` (or `cannot be used as a package name here`): the name is the Go package name, choose another.
- `pattern migrations/*.sql: no matching files found` at build time: the app calls `Migrations` with an `embed.FS` of a directory that has no `.sql` file yet; add the first migration or leave the call out.
- A hook cannot reach the database or the network on purpose ([ADR 0009](../adr/0009-lifecycle-pipeline-and-hooks.md)). Compute from the document, or reject the operation.
