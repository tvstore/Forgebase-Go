# 0014 Multi-site: database per site

- Status: Accepted (owner decisions M7-1 to M7-7 of the M7 plan, 2026-10-30, "setuju semua, lanjutkan"); the implementation details of M7.1, M7.2 and M7.3 are separated below and are not owner decisions. Replaces the "lean" of [architecture, section 9](../blueprint/architecture.md#9-multi-site-accepted-adr-0014); extends [ADR 0010](0010-authentication-and-sessions.md) (sessions and tokens live in the site's own database) without changing it.
- Date: 2026-10-30

## Context

One deployment of Forgebase should be able to serve several customers (sites), each with its own data and users. The risk to design against is a **cross-site leak**: one request, command or job that runs with the wrong site's data. [Architecture, section 9](../blueprint/architecture.md#9-multi-site-accepted-adr-0014) had a day-one constraint, "every request or command carries an explicit site context; there is no global database handle", and a lean towards a database per site. M7 is split into M7.1 (the config file and the site context everywhere, this ADR), M7.2 (per-site app install, `site list`, `site create`, the worker per site with its apps) and M7.3 (backup and restore, the operations guide, a CI example).

## Decision

The decisions below are the owner's (M7 plan, 2026-10-30).

- **M7-1. One database per site.** Architecture section 9 moves from "lean" to Accepted.
- **M7-2. A config file, `forgebase.toml`, holds the sites.** Database URLs come from environment variables, never from the file. Without a config file, Forgebase behaves exactly as before.
- **M7-3. The site is chosen by the `Host` header.** An unknown host is refused.
- **M7-4. The apps of a site are a subset of the binary's apps** (M7.2). Removing an app keeps its data.
- **M7-5. One worker process per site** in M7.
- **M7-6. Backup and restore use `pg_dump` and `pg_restore`** (M7.3).
- **M7-7. The work is split into M7.1, M7.2 and M7.3.**

## Alternatives considered

- **Schema per site in one database.** Cheaper in connections, but one `search_path` mistake or one missing schema qualifier reads another site's tables; the roles, sequences and extensions are still shared; a per-site backup needs `pg_dump --schema`. Rejected: the isolation is weaker for no gain that matters at the scale of M7.
- **A tenant column on every row.** The weakest isolation: every query, every hook and every future feature must remember the filter, and one permission bug leaks data. Rejected (as in architecture section 9 before).
- **A shared worker for all sites.** One process polling every site's database. Possible later; M7 runs one worker process per site (M7-5), which keeps a site's jobs, pool and failures separate and needs no new code path.

## Consequences

- **More pools and connections.** Each site has its own pool; a deployment with N sites can hold N times the connections. The config has an optional per-site `pool_size`, so a small site can have a small pool. PostgreSQL's `max_connections` must cover the sum (the [operations guide](../guide/operations.md) says so).
- **`CREATE DATABASE` is not transactional** (architecture section 5): creating a site is a separate step from migrating it (M7.2).
- **Backup and restore are per site**, with the standard PostgreSQL tools (M7.3).
- **A missed site context is the main risk.** The design answer is one explicit `Site` type, a site context passed down (no global pool, no global URL), and isolation tests at every layer.

## Implementation details (M7.1)

These choices were made while implementing M7.1 and are reviewed with that change; they are not owner decisions.

**The config file.** `forgebase.toml` is found only through `--config PATH` or the environment variable `FORGEBASE_CONFIG` (the option wins). **There is no discovery in the current directory**, so a stray file never changes behaviour. Format:

```toml
[[sites]]
name = "acme"                                  # [a-z][a-z0-9_]*, at most 63 bytes
hosts = ["acme.example.com", "acme.localhost"] # host names without a port, lower-cased on load
database_url_env = "ACME_DATABASE_URL"         # the environment variable that holds the URL
# pool_size = 16                               # optional, 1 to 256 (default: the pool default)
```

Unknown keys are an error (`deny_unknown_fields`; `apps` is added in M7.2, see below). Validation errors are collected and reported together with exit code 1: no sites, an invalid or duplicate site name, a site with no hosts or an invalid host (the rules of `FORGEBASE_ALLOWED_HOSTS`: no port, IPv6 in brackets), a host listed by two sites, an invalid variable name, a `pool_size` out of range. A site's variable that is unset or empty is an error **when that site is used**; the message names the site and the variable, and **the value is never printed**. With a config file, `FORGEBASE_ALLOWED_HOSTS` is ignored, and `serve` warns once if it is set.

**The `Site` type and the site context.** `Site` (in `forgebase`, `config.rs`) holds the name, the hosts, the name of the URL variable and the pool size. `Site::context` reads the URL, opens the site's pool and returns a `SiteContext { site, pool, registry }`; **that is the only way a command gets a database connection**. The registry was one `Arc<Registry>` shared by all sites in M7.1; M7.2 builds one per site (its apps), changing only how the context is built. There is no process-global pool or URL: only the builder of the implicit site names `FORGEBASE_DATABASE_URL` (a unit test checks the sources). `forgebase-http` has no notion of a configuration: it is given one `AppState` (pool, registry, login limit) per site.

**The implicit site.** Without a config file there is one site, `default`: its URL is `FORGEBASE_DATABASE_URL`, its hosts are `FORGEBASE_ALLOWED_HOSTS` (default `localhost,127.0.0.1`). `--site default` is accepted; any other name is a usage error. Every existing command, test and document stays valid.

**`--site`.** A global option. With a config file of several sites it is required on every database command except `serve` (exit 2, with the site names); with one site it is optional; `new-app` ignores it. `serve` without `--site` serves every site (one pool per site, routing by `Host`); with `--site` it serves that site only. `worker` runs for one site. `migrate` migrates one site: **there is no "all sites" mode** (a possible later convenience). A worker's pool is sized by its concurrency (`concurrency + 2`), not by the site's `pool_size`.

**Host to site, and the status codes.** The `Host` is still checked first, on every route and every error. A missing, repeated or malformed `Host` is `400 invalid_host` (unchanged). Then:

- **With a config file** a well-formed host that no site owns is `404 not_found` (`"no such site"`, the generic envelope): the host names no site, and the 404 does not say whether the name is a typo, a site that is not served here or no site at all. The match ignores case and port.
- **Without a config file** (the implicit site) a host outside the allow-list stays `400 invalid_host`, so the protection against DNS rebinding and every existing test are unchanged.

`X-Forwarded-Host` and `Forwarded` are never read: a reverse proxy must pass the original `Host`. A host with a trailing dot is not normalised and gets `404`; an absolute-form request target must agree with `Host` (see M7.2 below). The dispatcher is the outermost layer: it picks the site's router (each site has a complete router with its own state) and adds the common response headers. Desk serving, security headers, CSRF, the session cookie and the body limit are the same for all sites.

**Cookies and sessions.** The session cookie has no `Domain` attribute, so a browser sends it only to the host that set it. Sessions, API tokens and the login-failure counters live in the site's own database, so a cookie or token of site A cannot be validated by site B even if a request were routed to it by mistake (tested). The login semaphore and the pool are per site: `max_concurrent_logins` applies per site.

**Tests.** Config parsing and validation (units); the CLI with two real databases (`--site` required, optional, unknown, `migrate`, `doc`, `user`, `token` and `job enqueue` touch one database); HTTP isolation with one router over two databases (passwords, sessions, tokens, documents, meta, unknown and malformed hosts, forwarding headers); `serve` end to end with a config file; `worker --site a` claims only from database A.

## Implementation details (M7.2)

These choices were made while implementing M7.2 and are reviewed with that change; they are not owner decisions (M7-4 and M7-5 are).

**`apps` per site.** An optional key of a `[[sites]]` entry: `apps = ["sales", "sales_extension"]`. Absent means every app of the binary (the behaviour of M7.1). The list must be a subset of the binary's apps and **closed under `depends_on`**: the closure is explicit, **dependencies are never added automatically** ("site `a`: app `sales_extension` needs `sales`, which is not in `apps`"). An unknown name and a duplicate are errors too; all are collected with the other config problems, and each names the site. An empty list is allowed: the site then has only the framework itself (auth, users and the built-in jobs). The binary's order of apps decides the order in a site, not the order of the list.

**`local`.** The ad-hoc app of `--doctypes DIR` is part of **every** site's registry, and its implicit dependencies are that site's apps (a `local` DocType may link to the DocTypes of the apps installed on the site, not to others; a link to an app the site lacks is a startup error naming the site).

**One registry per site.** `site_composed` (in `run.rs`) calls the existing `compose` on the binary's apps first, so a problem of the binary or of the directory is reported once, as for every command; when the site lists `apps` it then calls `compose` again on the installed apps only (in the binary's order). Nothing in the composition is duplicated. `SiteContext.registry` is that registry, and **every command uses it**:

- `migrate` syncs the schema of the site's DocTypes only, runs the migrations of the site's apps only and records `fb_app` for them, and syncs the site's schedules only. As in M6.2, the schedules a binary does not declare are deleted, so **the schedules of an app that is not installed on the site are deleted** (the report lists them).
- `doc`: a DocType of an app that is not installed is an unknown DocType. `job enqueue`: only the jobs of the installed apps (and the built-in ones) are known.
- `serve`: each site's router has its own registry, so the meta API, the resources and the desk show only the installed apps (a DocType of another app is `404`).
- `worker --site`: only the jobs and schedules of the site's apps, plus the built-in ones. One worker process serves one site (M7-5); the scheduler runs in it.

**A removed app.** An app that is dropped from a site's list keeps its tables and data (M7-4); there is no uninstall. `migrate` prints one warning line per such app on standard error ("warning: site `a`: app `x` is recorded in the database but not installed; its tables and data are kept") and exits 0. Nothing is dropped: the schema sync is additive and never looks at tables of DocTypes that are not in the registry. **The kept rows of a removed app still hold their links:** a document of an installed app that they link to cannot be deleted (the error says it is linked from another document, without naming the DocType, which the site no longer knows) until the app is installed again and its rows are removed or changed. Its schedules are deleted (above). **Jobs of the app that are already queued stay queued**: the worker claims only names it knows, so they wait; adding the app back to the list lets them run. Its `fb_app` row and migration history stay, so installing it again finds everything as it was.

**`forgebase site list`.** Prints a table: the site name, its hosts, its apps (`all` when the key is absent, `(none)` for an empty list) and the **name** of its database variable, never the URL. It needs no database and works without a config file (the implicit `default` site).

**`forgebase site create NAME`.** The site must be in the config file (or be `default` without one). It parses the site's URL with `tokio_postgres::Config`, takes the database name (1 to 63 ASCII letters, digits, `_` or `-`, quoted with `quote_ident`), connects to the **maintenance database** (the same URL with the database `postgres`; `--maintenance-db NAME` overrides it) on a plain connection, since `CREATE DATABASE` cannot run in a transaction, and runs `CREATE DATABASE`. The role needs the `CREATEDB` privilege. If the database already exists it **refuses with exit 1** ("database for site `x` already exists; run `forgebase migrate --site x`"): an existing database is never migrated implicitly. After a create it migrates the new database as `migrate --site NAME` does and prints the report. If the migrate fails the database stays in place, and the error says so: fix the cause and run `migrate --site NAME`. Everything that can be checked without a database (the apps and the `--doctypes` directory) is checked before the database is created. The function is `forgebase_db::create_database` (an internal crate).

**Errors never contain the URL.** An error from opening a site's pool is reported as "site `NAME`: ..." (LOW-2 of the M7.1 review), so a `serve` with several sites says which one failed; an unusable URL given to `site create` is reported without its text.

**Follow-ups of the M7.1 review.**

- **LOW-1.** `user` (passwords on standard input) and `job enqueue` (the payload) read and check their input **before** the site's pool is opened, as before M7.1: a bad payload is reported even when the database is unreachable.
- **LOW-3.** If the request target is in absolute form (`GET http://host/path`), its host must equal the host of the `Host` header (case-insensitive, port ignored); otherwise the answer is `400 invalid_host`. The site is chosen by `Host` alone, so a disagreeing target would otherwise be routed by one host and answered as another.
- **INFO-1.** A host with a trailing dot (`a.localhost.`) is **not normalised**: it is not the host of any site and gets `404` (with a config file).
- **INFO-2.** The host rule of the config file is the rule of request routing (`forgebase_http::is_wellformed_host`: letters, digits, `-`, `.`, `_`, or IPv6 in brackets; no port), one function, so a host that could never match a request is refused when the file is loaded. It also applies to `FORGEBASE_ALLOWED_HOSTS`.

**Tests.** `crates/forgebase/tests/site_apps.rs` (a binary with two apps, run end to end against real databases: config validation, `site list`, `migrate` per site, `doc` and `job enqueue`, one `serve` with two sites, a worker per site, a removed app, `site create`, the follow-ups); config units; `crates/forgebase-http/tests/sites.rs` (absolute-form targets).

## Implementation details (M7.3)

These choices were made while implementing M7.3 and are reviewed with that change; they are not owner decisions (M7-6 is). The operator's view is the [operations guide](../guide/operations.md).

**`forgebase site backup NAME --out FILE`.** Runs `pg_dump --format=custom --no-owner --no-privileges` for the site's database, with its standard output being the open file `FILE.partial`. `pg_dump` is an external tool (not bundled, no crate dependency): `FORGEBASE_PG_BIN` (a directory) when set, else `PATH`; a missing tool is the error "pg_dump not found; install the PostgreSQL client tools of the server's major version (see docs/guide/operations.md)". An existing `FILE` is refused ("refusing to overwrite FILE", exit 1) and there is no `--force`: the dump is written to `FILE.partial`, created exclusively (mode 0600 on Unix, because a backup holds password hashes and token digests), and the open handle is the standard output of `pg_dump`, so the tool never opens a path by name. Only when `pg_dump` has succeeded is the file published under its final name with a hard link, which fails if `FILE` exists, so a race cannot overwrite it either; then the `.partial` name is removed. A failure removes `.partial`. A process killed in between (SIGINT, SIGTERM) can leave only `FILE.partial`, never a partial `FILE`; the next backup to that path refuses, says that an earlier backup was interrupted, and the leftover can be removed. The tool's standard error is passed on, each line prefixed `pg_dump: `. On success it prints the path and the size in bytes.

**No secret on a command line or in a message.** The site's URL is parsed with `tokio_postgres::Config` (`forgebase_db::pg_environment`) and reaches the child process only as `PGHOST`, `PGPORT`, `PGUSER`, `PGPASSWORD`, `PGDATABASE` and, when it is not the default `prefer`, `PGSSLMODE`. Every other `PG*` variable of the parent (`PGSERVICE`, `PGPASSFILE`, `PGOPTIONS`, ...) is removed from the child's environment, so the connection is the site's and nothing else. A URL option that these variables cannot express (more than one host, `hostaddr`, `options`, `application_name`, `sslnegotiation`, `connect_timeout`, the `keepalives*` and `tcp_user_timeout` settings, `target_session_attrs`, `channel_binding`, `load_balance_hosts`) is an error that names the option and never its value. The database name must satisfy the rule of `site create` (letters, digits, `_`, `-`), so it can never be read as a connection string. A test checks that no argument of either tool contains the URL, user, host or password, and that the child's environment has the connection and none of the inherited `PG*` variables.

**Version rules.** Before the dump, the server's major version (`SHOW server_version_num`) is compared with `pg_dump --version`: `pg_dump` must have the **same major version** as the server, else the command refuses and names both versions. A newer `pg_dump` is refused too: it writes an archive version that `pg_restore` of the server's version may not read, and newer tools send SQL (for example `SET transaction_timeout` from `pg_restore` 17 and later) that an older server rejects, so the failure would come late and half way (found by CI, whose runner image ships `pg_dump` 18 against the PostgreSQL 16 service). Before a restore, `pg_restore` must have the **server's** major version, and the `pg_dump` that wrote the file (read from the header that `pg_restore --list` prints, `Dumped by pg_dump version`) must not be newer than the server, so a dump of PostgreSQL 16 can be restored into a 17 server with the 17 tools (an upgrade) and never the other way round; the refusal names the versions of the tool, the dump and the server. A file without that header is refused as not a backup of `site backup`.

**`forgebase site restore NAME --from FILE`.** Restores into the site's database with `pg_restore --no-owner --no-privileges --exit-on-error --single-transaction --dbname=NAME FILE`: all or nothing. `pg_restore` restores into a database only when given one with `--dbname` (it does not read `PGDATABASE` for that), so the validated database name is the one connection value on its command line; it is not a secret. The database must exist and **be empty**, checked first on a plain connection of its own with one conservative SQL statement (`forgebase_db::EMPTY_CHECK_SQL`): no relation (`pg_class`) in any schema except `pg_catalog`, `information_schema` and the `pg_toast` and `pg_temp` ones, no schema other than `public`, no extension other than `plpgsql`, no function and no domain, enum, range or multirange type in those schemas (the kinds of objects checked; others, such as publications, are not looked at). Otherwise the command refuses (exit 1) and changes nothing. The check and the restore are not one transaction: nothing stops another session from writing in between, so restore into a database that only you know. A missing database gets its own message. On success it prints "run `forgebase migrate --site NAME` if this binary is newer than the backup" and **never migrates by itself**.

**`site create NAME --empty`.** Creates the database exactly as before and does not migrate it, so it is empty (a plain `site create` migrates, so its database is not). It uses no apps and no DocType files, so it needs no `--doctypes` with the stock binary. The restore flow is `site create x --empty`, `site restore x --from FILE`, `migrate --site x`.

**Carry-overs of the M7.2 review.** `serve` composes every site's registry before it opens any pool, so a configuration or composition error is reported without touching a database; the site tests no longer pin the number of framework migrations; and the worker isolation scenario uses a stale `fb_schedule` row of an app that is not installed (plus a control that a due schedule of an installed app does tick).

**Tests.** `crates/forgebase/tests/backup.rs` (the real `pg_dump` and `pg_restore` against real databases: a backup restored into an empty database gives the same documents, child rows and users; a non-empty and a missing database, a file that is not a backup, no overwrite, a missing tool, tools of another major version, a dump newer than the server, the environment, the failed-dump clean-up and a killed backup with fake tools), unit tests in `forgebase/src/backup.rs` and `forgebase-db/src/backup.rs` (the `PG*` mapping, the versions, the arguments, the file rules) and `forgebase-db/tests/inspect_database.rs` (the emptiness check). The CI job `sites` runs `examples/sites/ci.sh`: two sites with different apps, an unknown host, then a backup of one site restored into a new database and the documents compared.
