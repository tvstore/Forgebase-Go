# {{name}}

A [Forgebase](https://github.com/tvstore/forgebase-go) app: the package (`{{name}}.go`, the function `App()`) and a binary that runs it (`cmd/{{name}}`). Agents and contributors: read [AGENTS.md](AGENTS.md).

## Build, test, run

Needs Go {{go_version}} or newer and, for the tests and for running, a PostgreSQL 16 server.

```sh
go build -o {{name}} ./cmd/{{name}}
# Tests: a superuser (or CREATEDB) URL; every test creates and drops its own database.
export FORGEBASE_TEST_DATABASE_URL=postgres://user:password@localhost:5432/postgres
go test ./...

# Run: an empty database of your own.
export FORGEBASE_DATABASE_URL=postgres://user:password@localhost:5432/{{name}}
./{{name}} migrate                                         # schema, then data migrations
echo 'a long password' | ./{{name}} user create ann --role "{{title}} User"
FORGEBASE_DEV_INSECURE_COOKIES=1 ./{{name}} serve          # http://127.0.0.1:8000/api/v1
```

`./{{name}} --help` lists all commands (`doc`, `user`, `token`, ...). `FORGEBASE_DEV_INSECURE_COOKIES=1` is for plain-HTTP development only. `serve` does not migrate: run `migrate` after every change to DocTypes or migrations.

## What is where

| Path | Purpose |
|---|---|
| `{{name}}.go` | `App()`: the app's name, version, DocTypes, hooks and migrations |
| `cmd/{{name}}/main.go` | The binary: the list of apps, in dependency order |
| `doctypes/` | One TOML file per DocType, with its permissions |
| `migrations/` | SQL data migrations |
| `{{name}}_test.go` | Tests, using `forgebase/testing` |

## Extend it

- **A DocType**: add `doctypes/<app>_<thing>.toml`. Every `.toml` file under `doctypes/` is embedded; change an existing one additively (new fields). A test fails if the directory and the embedded set differ.
- **A hook**: implement `forgebase.DocTypeHooks` for a type (embed `forgebase.BaseHooks`) and register it with `.Hooks("<doctype>", Rules{})`. Hooks have no database access.
- **A migration**: see `migrations/README.md`.
- **Build on another app**: `go get` its module, name it in `.DependsOn("other")`, and add it before this app in `cmd/{{name}}/main.go`. You may then link to its DocTypes and register hooks for them.

## Forgebase dependency

`go.mod` points at a local Forgebase checkout (`replace`). To use a released version instead, delete the `replace` line and run `go get github.com/tvstore/forgebase-go@<version>`. The `require` lines and `go.sum` were copied from that checkout when this module was created, so you start with the versions Forgebase was tested with; commit both, and run `go mod tidy` when you want only what the app needs.

The full guide is `docs/guide/first-app.md` in the Forgebase checkout.
