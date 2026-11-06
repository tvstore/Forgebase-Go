# The command-line tool

Walkthrough of the `forgebase` commands (or the binary of your app) for users and tokens, background jobs, webhooks and sites. The document commands (`doc`) are in [Getting started](getting-started.md#quick-start). Run `forgebase --help` for every option.

## Users and tokens

Passwords are read from the first line of **standard input**, never from an argument or an environment variable. API tokens are printed once as `key:secret`; only a digest is stored, so the secret cannot be shown again. Reading a password from stdin does not hide it: on a terminal it is echoed as you type, so use `read -s` (see below) or a pipe. These commands have no authentication of their own: whoever can run `forgebase` with the database URL can create users and tokens.

```sh
# Create a user with two roles (usernames: 1-140 ASCII letters, digits and _ . @ -; passwords: 8-1024 bytes)
printf '%s\n' 'a long password' | cargo run -p sales -- user create ann --role Sales --role Accounts
# Without echo on a terminal: read -s -r PW && printf '%s\n' "$PW" | cargo run -p sales -- user create ann
cargo run -p sales -- user add-role ann Admin
cargo run -p sales -- user remove-role ann Admin
printf '%s\n' 'another password' | cargo run -p sales -- user set-password ann   # also ends all sessions
cargo run -p sales -- user disable ann    # sessions and tokens stop working; `user enable` undoes it

# An API token for a machine client: prints key:secret once
cargo run -p sales -- token create ann
cargo run -p sales -- token revoke <key>
```

Five consecutive failed logins for a username (each within 15 minutes of the previous one) lock it for 15 minutes; see [ADR 0010](../adr/0010-authentication-and-sessions.md) and the [security model](../blueprint/security-model.md).


### User permissions (M10.3)

Walkthrough: [Customizing and restricting data](customizing.md#user-permissions).

`user permission` restricts what a user sees: of a DocType the user is restricted on, only the documents you allow, and of every other DocType only the documents whose Links to it are empty or allowed ([ADR 0025](../adr/0025-user-permissions.md); the rule is explained in [getting started](getting-started.md#user-permissions)). The commands run as the system context like the other `user` commands, and the same operations are available to an Administrator over [HTTP](http-api.md#user-permissions-m103).

```sh
# Ann (role Sales) may see only the customer group Retail, and so only the customers and orders of that group
cargo run -p sales -- user permission add --user ann --doctype customer_group --value Retail --site main
# restricted user ann to customer_group Retail; it applies on the user's next request (at most 1000 per DocType)
cargo run -p sales -- user permission add --user ann --doctype customer_group --value Online --site main   # allow a second group
cargo run -p sales -- user permission list --site main                      # every row, sorted by user, DocType and value
cargo run -p sales -- user permission list --user ann --doctype customer_group --site main
# USER  DOCTYPE         VALUE   ADDED
# ann   customer_group  Online  2026-11-01T09:30:12.123456Z
# ann   customer_group  Retail  2026-11-01T09:30:05.654321Z
cargo run -p sales -- user permission remove --user ann --doctype customer_group --value Online --site main
```

- **`add`** needs the user, the DocType (one of the site's, custom fields included; not a child DocType: restrict the parent) and the document to exist when it is added (it is looked up as the system context). Adding a row that exists succeeds and says `already`. At most **1000** documents per user and DocType: the 1001st is refused with a message that says so. `--doctypes DIR` is accepted like for `webhook add`.
- **`remove`** of a row that is not there succeeds and says so; it does not check that the DocType or the document still exist, so a stale row can always be removed. **`list`** shows rows for a DocType the site no longer has too (they are ignored when reading).
- A user with **no** row for a DocType is not restricted on it. Users with the `Administrator` role and the system context (`forgebase doc`, jobs) are never restricted, whatever rows exist. Changes apply on the user's **next request**: no restart, no reload.
- A user restricted on a DocType cannot create (or amend) documents of it. Query reports, webhook payloads and the `doc` and `data` commands are not restricted by user permissions.

## Background jobs

An app registers a job with `App::job`; a hook, another job or the command line enqueues it, and `forgebase worker` runs it. A job runs in **one database transaction together with its completion**; a failure is retried (5 attempts, 30 s doubling up to 1 h, 5 min timeout) and then the job is `dead`. Delivery is at-least-once, so effects outside the database must be safe to repeat. A hook's `ctx.enqueue(..)` is written in the same transaction as the document change (the transactional outbox), so the job exists if and only if the change is committed. Never put a secret in a payload: it is stored in clear text. Design: [ADR 0013](../adr/0013-jobs-scheduler-and-transactional-outbox.md).

```rust
use forgebase::{App, JobContext, JobFuture, JobHandler};

struct Welcome;

impl JobHandler for Welcome {
    fn run<'a>(&'a self, ctx: &'a mut JobContext<'_>) -> JobFuture<'a> {
        Box::pin(async move {
            let name = ctx.payload()["customer"].as_str().unwrap_or_default().to_owned();
            // The system context, as `job:crm.welcome`, inside this job's transaction.
            let _customer = ctx.documents().get("customer", &name).await?;
            Ok(())
        })
    }
}

pub fn app() -> App {
    App::new("crm").job("welcome", Welcome) // the job is `crm.welcome`
}
// in a hook: ctx.enqueue("crm.welcome", forgebase::serde_json::json!({"customer": doc_name}))?;
```

```sh
forgebase migrate                                   # creates the job table (migration 0005)
forgebase worker --concurrency 4                    # runs jobs until Ctrl-C or SIGTERM
forgebase job enqueue crm.welcome --payload '{"customer":"c-1"}'   # or --payload - for stdin
forgebase job list --status dead                    # id, name, status, attempts, run_at, last error
forgebase job show <id>                             # every field as JSON, the payload included
forgebase job retry <id>                            # a dead job goes back to the queue
```

The first signal stops the worker from claiming and lets running jobs finish for 30 seconds (then they are given back, without counting an attempt); a second signal exits at once, and another worker recovers what was running when its lease expires. In an app's tests, `forgebase::testing::run_jobs(&binary, &db)` runs the jobs that are due. Two complete examples: `cargo run -p sales -- worker` runs the log and daily-summary jobs of [`examples/sales`](../../examples/sales/README.md) (and `cargo run -p sales_extension -- worker` those of both example apps), after `migrate`.

### Scheduled jobs

An app runs one of its own jobs on a schedule: every N minutes, every day at a time, or every week on a day at a time. **All times are UTC**; there is no cron and there are no time zones.

```rust
use forgebase::{App, Schedule, Weekday};

App::new("crm")
    .job("send_digest", SendDigest)
    .schedule("send_digest", Schedule::daily_at(6, 30))
    .job("weekly_report", WeeklyReport)
    .schedule("weekly_report", Schedule::weekly_on(Weekday::Monday, 7, 0))
```

`Schedule::every_minutes(n)` (1 to 1440) ticks at the times where the minutes since the Unix epoch are divisible by `n`: every 15 gives :00, :15, :30 and :45. Run `forgebase migrate` after you add, change or remove a schedule (it stores the schedules the binary declares and **deletes the others**), and keep `forgebase worker` running: it enqueues each tick once, however many workers run, with the payload `{"scheduled_for": "<the tick, RFC 3339 UTC>"}` (use it for idempotency). Ticks missed while no worker ran are not caught up: one job runs, then the next future time. In tests, `forgebase::testing::run_schedules_at(&binary, &db, at)` enqueues what is due at `at`, and `run_jobs` runs it.

The framework schedules its own clean-up in every binary (app name `forgebase`, reserved): finished jobs, done or dead, 7 days after they finished (daily at 03:00 UTC), stale login-failure rows and expired sessions (every 60 minutes). They need a running worker.

The framework's own job `forgebase.deliver_webhook` has no schedule: document operations enqueue it for the [webhooks](#webhook-commands-m92) of the site.

## Webhook commands (M9.2)

A webhook sends a signed JSON snapshot of a document to a URL after it was inserted, updated, deleted, submitted, cancelled or moved by a workflow transition ([ADR 0020](../adr/0020-outgoing-webhooks.md); the receiving side, the headers and a verification recipe are in the [webhooks guide](webhooks.md)). Webhooks belong to a **site** (they are rows of its database), so they take `--site` like the other database commands. The delivery is a job: **a site that has webhooks needs a running `forgebase worker`**.

```sh
# Add one. The secret is printed once; store it now.
cargo run -p sales -- webhook add orders \
  --doctype sales_order --events insert,submit \
  --url https://hooks.example.com/forgebase/orders --site main
# added webhook orders
# secret: 3f9c...   (64 hex characters; it signs every delivery)

cargo run -p sales -- webhook list
# NAME    DOCTYPE      EVENTS         URL                          ENABLED  ALLOW_PRIVATE  INSECURE_HTTP
# orders  sales_order  insert,submit  https://hooks.example.com:443  yes    no             no

cargo run -p sales -- webhook test orders       # one signed test delivery, now, without the queue
cargo run -p sales -- webhook disable orders    # no new deliveries; queued ones are dropped when they run
cargo run -p sales -- webhook enable orders
cargo run -p sales -- webhook rotate-secret orders   # prints a new secret once
cargo run -p sales -- webhook remove orders
```

- **`add NAME --doctype D --events E1,E2 --url URL`.** The name follows the rules of a DocType name (lower-case letters, digits, underscores). The DocType must exist on the site and must not be a child DocType. The events are `insert`, `update`, `delete`, `submit`, `cancel` and `transition`; a workflow transition that submits or cancels the document also fires `submit` or `cancel`, and an amendment fires `insert`. The URL must be `https://`, have a host and **no user info** (`user:password@` is refused: put a token in the path or, better, verify the signature). Like the `doc` commands it needs `--doctypes DIR` when the binary has no apps. Every error repeats nothing of the URL.
- **`--secret-stdin`** (on `add` and `rotate-secret`) reads the secret from the first line of standard input instead of generating one (32 random bytes as hex). It must be **at least 16 bytes** (a shorter one is refused and nothing changes). It is not printed back. The secret is **stored in plain text** in the site's database, because the signature needs it: anyone who can read the database or a backup of it can read it.
- **`--allow-private`** lets the URL resolve to a loopback, private, link-local or reserved address, **the cloud metadata address included**. Without it such a delivery is refused for good (the SSRF guard). Use it only for a receiver inside your own network, and know that it widens what the worker can reach.
- **`--insecure-http`** allows `http://`, for development only: the payload and the signature travel unencrypted. `add` prints a warning and `list` shows the flag.
- **`list`** prints the host of the URL (scheme, host and port) but never its path or query, which often hold a token, and never the secret.
- **`test NAME`** sends one delivery with `event` `test` and the same signature, address check and time limit as a real one, **synchronously** and without the queue, and works for a disabled webhook. It prints `HTTP <status>` and exits 0 for a 2xx answer; for any other answer, a refused address or a network error it exits 1 and says why. Use it after `add` to check the receiver and the secret.
- **`disable`, `enable`, `remove`** change the webhook at once. A delivery that is already in the queue finds the webhook gone or disabled when it runs, **completes without sending** and is logged.

To see what happened to deliveries use the job commands: `forgebase job list --name forgebase.deliver_webhook` (a failing receiver shows `HTTP 500` or a connection error in `LAST_ERROR`, never the URL path), `job show ID` and `job retry ID` for a dead one (5 attempts, 30 s doubling up to 1 h).


## Data commands: export and import (M9.3)

`data export` writes the documents of a DocType to a CSV or JSON file, and `data import` reads such a file into documents ([ADR 0021](../adr/0021-import-and-export.md); the HTTP routes and the desk pages are in the [HTTP API](http-api.md#import-and-export-m93) and [desk](desk.md) guides). Like the `doc` commands they run as the trusted system context: no permission rules apply, and `--actor` names who is recorded as owner on import.

```sh
# Export the gold customers, newest first, as CSV to standard input of another tool or to a new file
cargo run -p sales -- data export customer --format csv \
  --filter '[{"field":"tier","op":"=","value":"gold"}]' --sort modified_at:desc \
  --fields name,full_name,tier --out gold-customers.csv
# wrote 12 row(s) to gold-customers.csv

# Check a file without keeping anything, then import it
cargo run -p sales -- data import customer new-customers.csv --format csv --dry-run
cargo run -p sales -- data import customer new-customers.csv --format csv --actor jane
# {"dry_run": false, "error_count": 0, "errors": [], "inserted": 250, "mode": "insert", "rows": 250, "updated": 0}
```

- **`export DOCTYPE [--format csv|json] [--filter JSON] [--sort FIELD[:desc]] [--fields a,b] [--out FILE] [--limit-override N]`.** `--filter` and `--sort` are those of `doc list`; there is no paging. The default format is CSV; the default output is standard output. `--out FILE` creates a **new** file (an existing one is an error) and a failed export removes its partial file. The export is read in one consistent snapshot and written page by page, so it needs little memory. More rows than the limit (100 000) are refused before anything is written, with the count; `--limit-override N` raises the limit for this run.
- **The CSV file** is UTF-8 without a byte order mark, CRLF line ends, a header of the keys (`name` and the fields, or `--fields`), no child tables. Dates and datetimes are ISO 8601, decimals exact strings, booleans `true`/`false`, a link the target's name, null an empty cell. **Formula protection:** text that starts with `=`, `+`, `-`, `@`, a tab or a carriage return gets a leading `'`, so a spreadsheet does not run it. Numbers are never changed (`-5` stays `-5`). Import does not strip the `'`: **to keep exact text, use JSON**. The JSON file is an array of documents with their child rows, the shape of `doc get`, and can be imported again.
- **`import DOCTYPE FILE --format csv|json [--upsert] [--dry-run] [--actor NAME]`.** The whole file is **one transaction** and every row goes through the normal pipeline (hooks, validation, naming, webhooks); each row has its own savepoint, so all errors are found in one run. If any row has an error **nothing is imported**. The summary is printed as JSON (the same as the API's `data`) with the first 100 row errors, each with `row` (1-based among the data rows), `line` (CSV), `name`, `code`, `message` and `details`; the exit code is 1 when any row failed, also with `--dry-run`. At most 10 000 rows (for JSON, documents and child rows together). A file that cannot be used at all (an unknown, duplicate or table column, a bad encoding) is refused before anything runs.
- **CSV import.** The first row names the fields; a `name` column is allowed. Table fields cannot be CSV columns (use JSON). A cell is read by its field's type: a date `YYYY-MM-DD`, a datetime RFC 3339, an integer, a decimal, a boolean `true`, `false`, `1` or `0`, a link by the target's name. **An empty cell is "not given":** on insert the field keeps its default (or null); on an upsert of an existing document it is **set to null**. A UTF-8 byte order mark at the start is accepted, and so are the standard columns of an export (`owner`, `modified_at`, ...), which are ignored. JSON import takes an array of objects as `doc insert --data` does; the standard keys an export adds (`id`, `owner`, `version`, ...) are ignored.
- **`--upsert`** updates the document that a row's `name` names and inserts the rows whose name does not exist, when the DocType's naming allows a given name (a DocType named by a field takes the name that equals the field's value; random and series naming give their own names, so such a row is an error). A DocType named by a field cannot be renamed: the row's naming field must equal the name.
- **Time.** The transaction is as long as the file: about 37 seconds for 10 000 rows of a simple DocType (measured with a debug build on a local PostgreSQL; the export of the same rows takes half a second). Run big imports when the site is quiet.

## Customize field commands (M10.1)

Walkthrough: [Customizing and restricting data](customizing.md#site-custom-fields).

A **site custom field** is a field one site adds to a DocType in its own database, without a rebuild ([ADR 0023](../adr/0023-metadata-overlays.md); an app adds fields with an [overlay](first-app.md#add-a-field-to-another-apps-doctype-overlays) instead). The command line is the tool for the operator; the same operations are available to an Administrator over [HTTP](http-api.md#customize-fields-m101).

```sh
# One field is one TOML table body, written like a [[fields]] entry of a DocType file.
cat > vip.toml <<'EOF2'
name = "custom_vip"
label = "VIP"
type = "check"
EOF2
cargo run -p sales -- customize field add --doctype customer --field vip.toml --site main
# added custom field customer.custom_vip (check); its column exists now, and a running server or worker uses it within 10 seconds
printf 'name = "custom_rank"\ntype = "int"\nunique = true\n' | cargo run -p sales -- customize field add --doctype customer --field - --site main

cargo run -p sales -- customize field list --site main                      # every custom field
cargo run -p sales -- customize field list --doctype customer --site main   # one DocType's
# DOCTYPE   NAME         TYPE   LABEL        ENABLED  MODIFIED                     BY
# customer  custom_vip   check  VIP          yes      2026-11-01T09:30:12.123456Z  cli
cargo run -p sales -- customize field disable --doctype customer custom_vip --site main
cargo run -p sales -- customize field enable --doctype customer custom_vip --site main
```

- **Rules.** The name starts with `custom_`. Any type except `table`. `required` and `read_only` are refused (the documents that exist have no value for a new field, and nothing could write a read-only site field); `unique`, `allow_on_submit` (on a submittable DocType) and `perm_level` (0 to 9, M10.2: callers without a `[[permissions]]` rule of that `level` on the DocType do not see the field; see [field permission levels](getting-started.md#field-permission-levels)) are allowed; `required` stays refused with a level too. The DocType must exist, must not be a child DocType and must have been migrated; a Link must target a DocType that exists. A name that the DocType, an app overlay or an earlier custom field has (**a disabled one too**: use `enable`) is refused. Every refusal prints the reason and exits with code 1, and changes nothing.
- **What happens.** In one transaction under the lock of `migrate`: the field is stored, its column (and the foreign key and index of a Link, or the unique index) is added with the statements of the schema sync, and the metadata version is incremented. A running `serve` and `worker` look at the version every 10 seconds and swap in the new metadata; the API and the desk show the field from then on. A request in progress finishes with the metadata it started with.
- **Disable and enable.** Disabling hides the field everywhere (the metadata API, input, filters, sort, summary, export, import, webhook snapshots); its **column and data are kept**, and enabling brings both back. Nothing drops a custom field's column.
- **`migrate`** creates the column of every stored custom field (enabled or disabled) if it is missing, for example after a restored backup. A stored field that no longer validates (say its Link target left the apps) is skipped with a warning; the site still starts.
- **`doc` and `data`** use the custom fields too: an enabled field is accepted in `doc insert` and `update`, `doc list --filter`, `data export` (the CSV columns, `--fields`, `--filter`, `--sort`) and `data import`, and a disabled one is refused as an unknown field. An unreadable file or an unknown DocType still fails before the database is opened.
- **A disabled Link field keeps its foreign key.** Deleting a document that is referenced only by a hidden custom Link field fails with `409 linked_document_exists` (the message does not name the field); enable the field and clear it first.
- **Large tables.** A `unique` field (and a Link) builds its index in the same transaction, under the table lock and within the 5 second `lock_timeout` of the command; on a very large table add the field in a quiet period. A migrate in progress holds the same lock: the command then fails with the lock-timeout error (the API answers `503 lock_timeout`); run it again.

## Migrate and fixtures (M9.4)

`forgebase migrate` applies the framework migrations, the schema sync, the app migrations and then the **fixtures** of the apps that have any ([first-app guide, section 9](first-app.md#9-fixtures)). It prints one line per app with fixtures:

```
fixtures: sales: 2 inserted, 1 updated, 0 kept, 5 unchanged
```

*inserted* are records written as new documents (also a record the site deleted that the app has since changed), *updated* are records whose new version was applied to the existing document, *kept* are records whose document the site had made before the app shipped it (left as it is, tracked from now on), and *unchanged* are records that did not change since they were last applied (nothing is read or written for them). `migrate --dry-run` prints the plan and writes nothing: `fixtures: sales: would insert 2, update 1, keep 0, 5 unchanged`. While framework migrations are pending (a fresh database) it cannot plan them and says `fixtures: sales: 8 record(s), not planned yet`.

A record the document pipeline refuses (a hook, a missing link, a value that is too long) fails `migrate` with `fixtures of app sales were not applied: fixtures/customer_group.json: record 2 (Wholesale): ...`; the fixtures of that app are rolled back as a whole, what was applied before stays, and `migrate` can be run again after the fix. A fixture insert or update fires the webhooks of its DocType, as any write does. Each site applies its own fixtures.

## Sites

One deployment can serve several **sites**, each with its own database (ADR 0014). A single-site setup needs nothing new: without a config file there is one site, `default`, from `FORGEBASE_DATABASE_URL`. For several sites, write a `forgebase.toml` (no secrets: the database URLs come from environment variables) and pass it with `--config forgebase.toml` or `FORGEBASE_CONFIG=forgebase.toml` (it is never searched for in the current directory):

```toml
[[sites]]
name = "acme"
hosts = ["acme.example.com", "acme.localhost"]   # chosen by the Host header, no port
database_url_env = "ACME_DATABASE_URL"
pool_size = 8                                    # optional
apps = ["sales", "sales_extension"]              # optional: the apps installed on this site

[[sites]]
name = "beta"
hosts = ["beta.example.com"]
database_url_env = "BETA_DATABASE_URL"
```

```bash
export ACME_DATABASE_URL=postgres://user:password@localhost:5432/acme
export BETA_DATABASE_URL=postgres://user:password@localhost:5432/beta
forgebase --config forgebase.toml site list                                           # sites, hosts, apps; never the URL
forgebase --config forgebase.toml site create acme                                    # CREATE DATABASE, then migrate
forgebase --config forgebase.toml migrate --site acme --doctypes examples/sales/doctypes
forgebase --config forgebase.toml user create root --role Administrator --site acme   # password on stdin
forgebase --config forgebase.toml serve --doctypes examples/sales/doctypes            # serves all sites by Host
forgebase --config forgebase.toml worker --site acme --doctypes examples/sales/doctypes
```

`--site NAME` is required on every database command except `serve` when the file has several sites, and optional with one site. `serve` without `--site` serves all sites, one pool each; with `--site` only that site. A well-formed `Host` that no site owns is `404`, a malformed one `400`; `X-Forwarded-Host` is ignored, so the reverse proxy must pass the original `Host`. **There is one worker process per site** (`forgebase worker --site NAME`; the scheduler runs in it), and `migrate` migrates one site at a time. Running a deployment (reverse proxy, workers, upgrades, roles) is in the [operations guide](operations.md).

**Apps per site.** `apps` lists the apps of the binary that are installed on a site; absent means all of them. The list must be closed under `depends_on` (a site that lists `sales_extension` must list `sales`: dependencies are never added for you), may be empty (only auth, users and the built-in jobs) and names only apps the binary has; every mistake is reported at once, naming the site. A site's registry has only its apps' DocTypes, hooks, jobs and schedules, so `migrate`, `doc`, `job enqueue`, the meta API, the resources and the worker of a site all see the same subset: a DocType of an app that is not installed is unknown (`404` over HTTP). The `--doctypes DIR` app `local` is in every site and depends on that site's apps. **Removing an app from a site's list keeps its tables and data** (there is no uninstall): `migrate` warns once per such app, its schedules are deleted, and its queued jobs stay queued (the worker does not claim names it does not know) until the app is installed again. **The kept rows of a removed app still hold their links:** a document of an installed app that they link to cannot be deleted (the error says it is linked from another document, without naming the DocType, which the site no longer knows) until the app is installed again and its rows are removed or changed.

**`site list` and `site create`.** `forgebase site list` prints each site's name, hosts, apps and the **name** of its database variable (never the URL) and needs no database. `forgebase site create NAME` runs `CREATE DATABASE` for the database named in the site's URL, connecting to the maintenance database `postgres` of the same server (`--maintenance-db NAME` to use another), and then migrates it like `migrate --site NAME`. The role in the URL needs the `CREATEDB` privilege. It refuses, with exit 1, a database that exists already (run `migrate --site NAME` instead: an existing database is never migrated implicitly). If the migrate fails after the create, the database is kept: fix the cause and run `migrate --site NAME`. With an existing database and a role without `CREATEDB`, `site create` reports "permission denied" (PostgreSQL checks the privilege first). `site create NAME --empty` creates the database and does not migrate it (for a restore); it uses no apps.

**Backup and restore.** They use the PostgreSQL client tools `pg_dump` and `pg_restore` (not bundled; the server's major version, on `PATH` or in `FORGEBASE_PG_BIN`). The connection reaches them through `PG*` environment variables, never on the command line.

```bash
forgebase --config forgebase.toml site backup acme --out acme.dump          # never overwrites; mode 0600
forgebase --config forgebase.toml site create acme2 --empty                 # acme2: another site of the file; a new, empty database
forgebase --config forgebase.toml site restore acme2 --from acme.dump       # only into an empty database; one transaction
forgebase --config forgebase.toml migrate --site acme2 --doctypes examples/sales/doctypes   # if the binary is newer than the backup
```

A backup holds everything in the database, **password hashes, sessions and API token digests included**: protect the file. A restore refuses a database that is not empty (and `site create` without `--empty` migrates, so its database is not empty); it never migrates by itself. Details, scheduling and retention: [operations guide](operations.md#backups).
