# Operating Forgebase

For the person who runs a Forgebase deployment: one server, one binary, one or more sites. Every command here was run against the repository at the time of writing (the two-site flow is `examples/sites/ci.sh`, run by the CI job `sites`). Design and decisions: [ADR 0014](../adr/0014-multi-site-database-per-site.md). Security notes: [security model](../blueprint/security-model.md#multi-site-isolation-m71-accepted-adr-0014).

The examples use a binary called `myapp` (your apps compiled together, see [the first-app guide](first-app.md)). The stock `forgebase` binary works the same, except that database commands then also need `--doctypes DIR`.

## What you deploy

To run all of this on one VM with Docker, HTTPS and backups, follow the [deploy guide](deploy.md).

- **One binary** with its apps. There is no plugin loading: to change the apps you build and deploy a new binary.
- **The built desk**, a directory (`desk/dist` after `npm run build`), if you want the web interface. `serve` reads it from `FORGEBASE_DESK_DIR`.
- **PostgreSQL 16 or newer**, one database per site.
- **The PostgreSQL client tools** (`pg_dump`, `pg_restore`) of **the same major version as the server** (newer tools can write archives or send SQL that the server's version cannot handle), on the machine that runs the backups. Forgebase does not bundle them. On Debian and Ubuntu the package is `postgresql-client-16` (or the one of your server's major version).
- A reverse proxy for TLS (see [Running](#running)). Forgebase itself speaks plain HTTP. Its connection to PostgreSQL has TLS when the database URL asks for it ([TLS to PostgreSQL](#tls-to-postgresql)); without that it is not encrypted, so keep the link on a local or private network.

## Configuration

Without a configuration file there is one site, `default`, whose URL is in `FORGEBASE_DATABASE_URL`. For several sites write a `forgebase.toml` and name it with `--config PATH` or `FORGEBASE_CONFIG` (it is never looked for in the current directory):

```toml
[[sites]]
name = "acme"                                   # lower-case letters, digits, underscore
hosts = ["acme.example.com"]                    # chosen by the Host header, no port
database_url_env = "ACME_DATABASE_URL"          # the variable that holds the URL
apps = ["sales"]                                # optional: absent means every app of the binary

[[sites]]
name = "beta"
hosts = ["beta.example.com"]
database_url_env = "BETA_DATABASE_URL"
pool_size = 8                                   # optional, 1 to 256
apps = ["sales", "sales_extension"]             # the list must be closed under depends_on
```

**No secrets go in this file.** It names the environment variables; put their values in a file that only the service user can read (for example `/etc/forgebase/forgebase.env`, mode 0600, loaded by systemd with `EnvironmentFile=`):

```sh
ACME_DATABASE_URL=postgres://forgebase_app:long-random-password@127.0.0.1:5432/acme
BETA_DATABASE_URL=postgres://forgebase_app:long-random-password@127.0.0.1:5432/beta
```

| Variable | Meaning |
|---|---|
| `FORGEBASE_CONFIG` | The path of `forgebase.toml`. `--config` wins. |
| the variable named by each site's `database_url_env` | The PostgreSQL URL of that site. Unset or empty is an error that names the site and the variable, never the value. |
| `FORGEBASE_DATABASE_URL` | The URL of the implicit site `default`, when there is no configuration file. |
| `FORGEBASE_LISTEN` | The address `serve` listens on (default `127.0.0.1:8000`). |
| `FORGEBASE_DESK_DIR` | The built desk, served under `/desk/`. Unset: no desk. |
| `FORGEBASE_ALLOWED_HOSTS` | Only without a configuration file (with one, the hosts come from the file and this variable is ignored with a warning). |
| `FORGEBASE_PG_BIN` | The directory of `pg_dump` and `pg_restore`, if they are not on `PATH`. |

`site list` shows what is configured (names, hosts, apps and the variable names, never a URL):

```sh
myapp --config /etc/forgebase/forgebase.toml site list
```

### TLS to PostgreSQL

The connection to PostgreSQL follows the `sslmode` in the database URL, for every site (the URLs in the environment variables that `forgebase.toml` names work the same way), and for everything that connects: the pool, `migrate`, `site create`, the worker, `site backup` and `site restore`. Details and reasons: [ADR 0018](../adr/0018-postgres-tls.md).

| In the URL | What happens |
|---|---|
| no `sslmode`, or `sslmode=disable` | no TLS (the default; right for a local or private network) |
| `sslmode=require` (also `verify-ca`, `verify-full`) | TLS, and the certificate is **always fully verified**: the chain against the trusted roots **and** the host name in the URL. This is stricter than libpq's `require`. |
| `sslmode=prefer` or `allow` | refused with `` `sslmode=prefer` is not supported: use `disable` or `require` ``: a failed handshake never falls back to plain text |
| `sslrootcert=/path/ca.pem` | trust **only** the certificates in that PEM file (a private CA, or a server's own certificate) |
| `sslrootcert=system`, or nothing | trust the operating system's CA store (a container image needs the `ca-certificates` package or a copy of its bundle; the image of the [Dockerfile](../../Dockerfile) carries the Debian bundle) |

```sh
# the URLs, one per site, in the environment (never in forgebase.toml itself)
ACME_DATABASE_URL=postgres://forgebase_app:long-random-password@db.example.com:5432/acme?sslmode=require
BETA_DATABASE_URL=postgres://forgebase_app:long-random-password@db.internal:5432/beta?sslmode=require&sslrootcert=/etc/forgebase/db-ca.pem
```

- A **managed database** (Neon, a cloud PostgreSQL) uses a certificate from a public CA: `sslmode=require` is all you need. A **self-hosted server with its own CA** needs `sslrootcert` with the CA certificate, and the server certificate must contain the host name (or IP address) you connect to.
- **Percent-encode special characters in the user name and password**: `/` as `%2F`, `?` as `%3F`, `#` as `%23`, `@` as `%40` and `%` as `%25` (an `@` in a query value too). A raw `/`, `?` or `#` before the first `@` is refused with a clear error at startup, never silently, because the URL parsers of libpq and Forgebase could read it differently and a `sslmode` could be lost. If you have a generated password with such characters, encode it in the URL (an upgrade step; the password itself does not change).
- The host name is sent for routing (SNI). A Unix-socket host with TLS is refused; use TCP.
- `sslcert`, `sslkey` (client certificates), `sslcrl` and other `ssl*` parameters are refused, not ignored. Only `postgres://` and `postgresql://` URLs are accepted.
- `site backup` and `site restore` give `pg_dump` and `pg_restore` the same choice in their environment: `PGSSLMODE=verify-full` with `PGSSLROOTCERT` (the path, or `system`, which needs the version 16 client tools). With no TLS they get `PGSSLMODE=disable`.
- A failure says what failed, for example `error performing TLS handshake: invalid peer certificate: UnknownIssuer` (the server's certificate is not signed by a trusted root: use `sslrootcert`) or `certificate not valid for name "..."` (the URL's host is not in the certificate). It is not a password error, and no message contains the URL.

**Connections.** Every site has its own pool (by default four connections per CPU; `pool_size` sets it) and every worker has one of its own. PostgreSQL's `max_connections` must cover the sum for all sites, workers and your own tools.

## Database roles

Forgebase uses one URL per site for everything, so the role in it creates and owns the tables. The role needs the `CREATEDB` privilege **only** for `site create`. Two ways to run it:

- **Simple:** one role with `CREATEDB` for everything.
- **Separated (recommended when you can):** a human administrator creates the empty database by hand, owned by the application role, and the application role has no `CREATEDB`:

  ```sql
  CREATE ROLE forgebase_app LOGIN PASSWORD '...';
  CREATE DATABASE acme OWNER forgebase_app;
  ```

  Then run `myapp migrate --site acme` instead of `site create` (for a restore, the database is the empty one you just created, see [Restoring](#restoring)).

## Creating a site

Add the site to `forgebase.toml`, set its URL variable, then:

```sh
myapp --config /etc/forgebase/forgebase.toml site create acme      # CREATE DATABASE, then migrate
echo 'a long password' | myapp --config /etc/forgebase/forgebase.toml user create root --role Administrator --site acme
```

`site create` connects to the maintenance database `postgres` of the same server (`--maintenance-db NAME` to use another), creates the database named in the site's URL and migrates it. It refuses a database that exists already (exit 1): an existing database is never migrated implicitly; use `migrate --site acme`. A role without `CREATEDB` gets "permission denied"; when the database exists already and the role also lacks `CREATEDB`, PostgreSQL checks the privilege first, so that message wins over "already exists". Passwords are read from standard input, never from an argument.

## Running

**The web server.** One process serves every site, one pool each, and routes by the `Host` header:

```sh
FORGEBASE_LISTEN=127.0.0.1:8000 FORGEBASE_DESK_DIR=/srv/forgebase/desk \
  myapp --config /etc/forgebase/forgebase.toml serve
```

Put a reverse proxy in front for TLS. It **must pass the original `Host`**: Forgebase picks the site from `Host` and never reads `X-Forwarded-Host`. With nginx:

```nginx
server {
    listen 443 ssl;
    server_name acme.example.com beta.example.com;
    # ssl_certificate ... (TLS is terminated here)
    location / {
        proxy_pass http://127.0.0.1:8000;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-Proto $scheme;
    }
}
```

A request for a host that no site owns gets `404`; a malformed `Host` gets `400`. `serve` stops gracefully on SIGTERM (running requests get 10 seconds).

**Workers.** Run **one worker process per site** (it also runs that site's scheduler):

```sh
myapp --config /etc/forgebase/forgebase.toml worker --site acme
```

A systemd template unit, `/etc/systemd/system/forgebase-worker@.service`, starts one per site (`systemctl enable --now forgebase-worker@acme forgebase-worker@beta`):

```ini
[Unit]
Description=Forgebase worker for site %i
After=network-online.target postgresql.service

[Service]
User=forgebase
EnvironmentFile=/etc/forgebase/forgebase.env
ExecStart=/usr/local/bin/myapp --config /etc/forgebase/forgebase.toml worker --site %i
Restart=on-failure
# SIGTERM (the default) is the graceful stop: running jobs get 30 seconds.
KillSignal=SIGTERM
TimeoutStopSec=45

[Install]
WantedBy=multi-user.target
```

A unit for `serve` is the same without `%i` and `--site`. The worker does not migrate: run `migrate` first. The worker also sends the **webhooks** of its site ([webhooks guide](webhooks.md)): it opens the outgoing connections, so it needs the network access to the receivers and the operating system's CA certificates (the Docker image has them), and a site with webhooks has no deliveries without one.

## Upgrading

1. **Back up** every site (see [Backups](#backups)).
2. Deploy the new binary (and the new desk directory).
3. Stop the web server and the workers of the old binary.
4. Run `myapp migrate --site NAME` **for each site** (there is no "all sites" mode). `migrate --dry-run` prints what would change. `migrate` also applies the **fixtures** of the apps (records the app ships; [first-app guide, section 9](first-app.md#9-fixtures)) and prints one line per app, for example `fixtures: sales: 2 inserted, 1 updated, 0 kept, 5 unchanged`: a record is inserted if missing, updated only when the new binary ships a new version of it (so edits made on the site survive), and never deleted. They go through the normal write path, so **webhooks fire** for them: disable a webhook around the upgrade if you do not want deliveries for the records `migrate` writes. If a record is refused, `migrate` fails after everything else was applied, naming the app, file and record; fix the file and run `migrate` again.
5. Start the web server and the workers.

Schema changes are additive and refused when they would lose data; the command lists the conflicts and changes nothing.

Framework migration 0013 (M10.3) adds `fb_user_permission`, the user permissions of a site ([ADR 0025](../adr/0025-user-permissions.md)). Until a site has run `migrate`, a newer binary serves its users **without** restrictions (there can be none yet). The rows are in the site's database, so [backups](#backups) and [restores](#restoring) carry them, and `forgebase user permission list --site NAME` shows them; see [the CLI guide](cli.md#user-permissions-m103). They are read with the roles of every request, so a change needs no restart.

## Backups

```sh
myapp --config /etc/forgebase/forgebase.toml site backup acme --out /var/backups/forgebase/acme-$(date +%Y%m%d).dump
```

It runs `pg_dump --format=custom --no-owner --no-privileges` against the site's database and prints the file and its size. Things to know:

- **What it contains:** everything in the site's database, including **password hashes, session records and API token digests**. Treat every backup file like the database itself: it is created with mode 0600; keep the directory private, encrypt it where it is stored off the machine (Forgebase does not encrypt it), and delete old copies.
- It **never overwrites** a file: an existing path is an error (exit 1, "refusing to overwrite"; there is no `--force`). Use a new name each time. The dump is written to `FILE.partial` and given its final name only when it is complete, so a failed backup leaves nothing, and a backup that is killed (SIGINT, SIGTERM) can leave only `FILE.partial`, never a partial `FILE`. The next backup to that path says that an earlier backup was interrupted; remove the `.partial` file.

  The backup directory must be on a filesystem that supports hard links (a finished dump is published with one; FAT/exFAT and some network mounts cannot), otherwise the backup fails at the end with an error and leaves no file. If `forgebase` is killed, the `pg_dump` it started may keep running until it finishes writing `FILE.partial`; `FILE` never appears.
- The connection reaches `pg_dump` through `PG*` environment variables (`PGHOST`, `PGPORT`, `PGUSER`, `PGPASSWORD`, `PGDATABASE`, and `PGSSLMODE` when the URL sets one), never on the command line, where `ps` would show the password. Other `PG*` variables of your environment (`PGSERVICE`, `PGPASSFILE`, `PGOPTIONS`, ...) are not passed on. A URL option that these variables cannot express (more than one host, `options`, `application_name`, `connect_timeout`, ...) is an error that names the option.
- `pg_dump` must have the same major version as the server; otherwise (older or newer) the command refuses and names both versions. It works while the site is running (a consistent snapshot).

Schedule it outside Forgebase, with cron or a systemd timer, and keep what you need. **Retention is your job.** A minimal script:

```sh
#!/bin/sh
set -eu
umask 077
dir=/var/backups/forgebase
for site in acme beta; do
  myapp --config /etc/forgebase/forgebase.toml site backup "$site" --out "$dir/$site-$(date +%Y%m%d-%H%M%S).dump"
done
find "$dir" -name '*.dump' -mtime +14 -delete
```

Run it as the service user with its environment (the URL variables) loaded. Copy the files off the machine.

## Restoring

A restore goes into a **new, empty database**, never over existing data:

```sh
myapp --config /etc/forgebase/forgebase.toml site create acme --empty      # CREATE DATABASE only, no migrate
myapp --config /etc/forgebase/forgebase.toml site restore acme --from /var/backups/forgebase/acme-20261030.dump
myapp --config /etc/forgebase/forgebase.toml migrate --site acme
```

(With the separated roles above, create the empty database with `CREATE DATABASE acme OWNER forgebase_app` instead of `site create --empty`.) To replace a site's data, restore into a database with a new name, point the site's URL variable at it, and drop the old one when you are sure.

- **What "empty" means.** The database must exist, and have no relation (table, index, sequence, view) in any schema except the system ones, no schema other than `public`, no extension other than `plpgsql`, and no function or domain, enum or range type (the kinds of objects checked). If the check finds any of them the command refuses (exit 1) and changes nothing. A database made by `site create` without `--empty` has been migrated, so it is **not** empty.
- The restore runs `pg_restore --no-owner --no-privileges --exit-on-error --single-transaction`: all or nothing. The objects belong to the role of the site's URL. The database name is the one argument that is not in the environment (`pg_restore` needs it to restore into a database at all); it is not a secret.
- `pg_restore` must have the same major version as the target server, **and** the `pg_dump` that wrote the backup must not be newer than the server; otherwise the command refuses and names the versions. A backup of a 16 server can therefore be restored into a 17 server with the 17 tools (an upgrade), not the other way round.
- **Nothing is migrated automatically.** Run `migrate --site acme` if the binary is newer than the backup; it does nothing when they match.
- `site create --empty`, `site backup` and `site restore` use no apps and no DocType files, so they work with the stock binary without `--doctypes`.

**Test your restores regularly.** A backup that was never restored is a hope. The CI job `sites` does exactly this on every change: it backs up one site, restores it into a new database and compares the documents.

## Removing an app from a site

Delete the app from the site's `apps` list (and from the list of every site that has it) and run `migrate --site NAME`. The app's tables and data **are kept** (there is no uninstall): `migrate` prints one warning per such app, deletes the app's schedules, and the app's queued jobs stay queued. The kept rows still hold their links, so a document of an installed app that they link to cannot be deleted until the app is installed again and the rows are removed or changed. Put the app back in the list and everything is where you left it. See [ADR 0014](../adr/0014-multi-site-database-per-site.md#implementation-details-m72).

## Troubleshooting

| What you see | Cause and fix |
|---|---|
| `404 not_found` for every request | The `Host` matches no site. Check `site list`, and that the proxy passes the original `Host` (`proxy_set_header Host $host;`). `400 invalid_host` means a malformed or missing `Host`. |
| `site `acme`: the environment variable ACME_DATABASE_URL is not set or is empty` | The variable named in `forgebase.toml` is missing in this process's environment (systemd: `EnvironmentFile=`; shell: `export`). |
| `error performing TLS handshake: invalid peer certificate: UnknownIssuer` | The server's certificate is not signed by a root that Forgebase trusts. For a private CA add `sslrootcert=/path/ca.pem` to the URL ([TLS to PostgreSQL](#tls-to-postgresql)). |
| `certificate not valid for name "..."` | The host in the URL is not in the server certificate. Connect with a name that is (or fix the certificate). |
| `` `sslmode=prefer` is not supported: use `disable` or `require` `` | Change the URL: `sslmode=disable` for no TLS, `sslmode=require` for TLS. |
| `no root certificate in the operating system's trust store` | The machine or image has no CA certificates. Install `ca-certificates` or set `sslrootcert`. |
| `pg_dump not found; install the PostgreSQL client tools ...` | Install `postgresql-client-NN` or set `FORGEBASE_PG_BIN` to the directory that has the tools. |
| `pg_dump is version 18 but the server is version 16` | The client tools do not have the server's major version (older or newer). Install the package of the server's version, or point `FORGEBASE_PG_BIN` at it (on Debian and Ubuntu: `/usr/lib/postgresql/16/bin`). |
| `the database of site `acme` is not empty` | A restore needs an empty database: use `site create acme --empty` (not plain `site create`, which migrates), or a freshly created database. |
| `the database of site `acme` does not exist` | Create it first with `site create acme --empty`. |
| `refusing to overwrite FILE` | A backup never overwrites. Choose a new file name. |
| `permission denied to create database` | The role in the URL lacks `CREATEDB`. Create the database as an administrator (see [Database roles](#database-roles)) and use `migrate --site`. |
| `the database URL uses the option `...`, which cannot be passed to pg_dump` | Remove that option from the URL, or take the backup with `pg_dump` yourself. |
| `fixtures of app `sales` were not applied: fixtures/customer_group.json: record 2 (...)` | A fixture record was refused by validation, a hook or a link. The migrations before it stay applied and none of that app's fixtures was kept; fix the file in the app and run `migrate` again. |
| `database for site `acme` already exists; run `forgebase migrate --site acme`` | `site create` never migrates an existing database. Run `migrate --site acme`. |
