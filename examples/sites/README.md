# examples/sites

Two sites on one server and a backup restored into a new database ([ADR 0014](../../docs/adr/0014-multi-site-database-per-site.md), M7.3). It uses the composed binary of [`sales_extension`](../sales_extension/README.md).

- [`forgebase.toml`](forgebase.toml): the site `shop` (the `sales` app only), the site `field` (every app) and `restore_check` (the target of the restore).
- [`ci.sh`](ci.sh): creates the sites, serves them, checks that each answers for its own host with its own users and apps (and that an unknown host is 404), then backs up `shop`, restores it into `restore_check` and compares the documents (and the login).

The `sites` job of CI runs the script.

**Not in the Go port yet.** The script of the Rust implementation also proves the approval workflow and a shared saved view (P6), the fixture of the sales app, a webhook delivered to a test receiver (`receiver.py`), a site custom field and user permissions (P7). Each part is added here, with `receiver.py`, by the phase that ports what it uses; the header of `ci.sh` lists them.

## Run it locally

You need PostgreSQL 16 with a role that has `CREATEDB`, the PostgreSQL client tools (`pg_dump` and `pg_restore`, of the server's major version, 16; set `FORGEBASE_PG_BIN` to their directory if another version is first on `PATH`), `curl` and `jq`. The three databases are created by the script and must not exist.

```sh
(cd examples/sales_extension && go build -o ../../bin/sales_extension ./cmd/sales_extension)
export SHOP_DATABASE_URL=postgres://user:password@localhost:5432/fb_sites_shop
export FIELD_DATABASE_URL=postgres://user:password@localhost:5432/fb_sites_field
export RESTORE_DATABASE_URL=postgres://user:password@localhost:5432/fb_sites_restore
examples/sites/ci.sh
```

The script never prints a URL. It leaves the three databases in place: drop them when you are done (`DROP DATABASE fb_sites_shop WITH (FORCE)`, and the same for `fb_sites_field` and `fb_sites_restore`). `SITES_PORT` changes the port (default 8000); `FORGEBASE_BIN` the binary.

The commands it runs are the ones of the [operations guide](../../docs/guide/operations.md).
