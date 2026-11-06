# 0026 Go implementation

- Status: Accepted by owner
- Date: 2026-11-06

## Context

Forgebase was written in Rust (ADRs 0001 to 0025, milestones M0 to M10, see the frozen repository <https://github.com/tvstore/Forgebase>). The owner decided on 2026-11-06 (information session), in Indonesian: *"ini bisa kamu ubah semua ke bahasa go, karena rust kesusahan untuk build kalau ada perubahan cepat untuk klien"* (convert all of this to Go, because Rust is hard to build when clients need fast changes). The reason is build speed and the cost of a build when a client asks for a quick change: Forgebase is a base that apps are built on, so an app developer rebuilds often. The owner chose *"Go, repo baru tvstore/forgebase-go"* and approved the plan with *"setuju G1–G8"* (the table is in [owner decisions](../owner-decisions.md#go-port-2026-11-06-setuju-g1g8-information-session)).

Everything that Forgebase does (the behaviour) is recorded in the ADRs, the blueprint and the guides. Only the implementation language and its libraries change.

## Decision

### Parity port (G1)

The Go implementation reproduces the Rust implementation: the same HTTP API, the same database schema (`fb_*` and `doc_*` tables; the framework migrations keep their numbers), the same metadata formats (DocType and overlay TOML, workflow, fixtures JSON including the canonical checksum of ADR 0022), the same command-line commands and flags, the same environment variables and `forgebase.toml`. **No new feature is added during the port.** A behavioural difference is a bug to fix, or, when the Rust behaviour is itself wrong, a finding that goes to the owner. The documents written for the Rust implementation (ADRs, blueprint, guides) stay normative for behaviour.

### Module and package layout, import direction (G3)

The module path is `github.com/tvstore/forgebase-go`. The layout mirrors the Rust crates:

| Go package | Directory | Rust crate | Responsibility |
|---|---|---|---|
| `forgebase` | repository root | the public part of `forgebase` | The public API: what an app imports (ADR 0012) |
| `forgebase/testing` | `testing/` | `forgebase::testing` | Test helpers for apps (from P4) |
| `internal/core` | `internal/core/` | `forgebase-core` | Pure metadata, validation, lifecycle definitions, permission evaluation: no I/O, no SQL, no network |
| `internal/db` | `internal/db/` | `forgebase-db` | PostgreSQL access, schema sync, migrations, documents, auth storage, jobs |
| `internal/httpapi` | `internal/httpapi/` | `forgebase-http` | HTTP routes, middleware, REST API, serving the desk |
| `internal/app` | `internal/app/` | internals of `forgebase` | App model, builder, CLI commands, worker, sites |
| `main` | `cmd/forgebase/` | the `forgebase` binary | The command-line tool |

Import direction: `cmd` -> `forgebase` (root) -> `internal/app` -> `internal/httpapi` -> `internal/db` -> `internal/core`. Internal packages never import the root package. `internal/core` imports no other Forgebase package. The root package and `forgebase/testing` are the **only** public Go API (the boundary of ADR 0012); everything else is under `internal/`, which the Go toolchain refuses to import from outside the module. DocTypes, overlays, migrations and fixtures of an app are embedded with `//go:embed`, replacing `include_str!` and the `doctypes!`, `migrations!`, `overlays!` and `fixtures!` macros. Apps are Go modules that import `github.com/tvstore/forgebase-go` and are listed in order in their `main`.

### Hooks as interfaces (G3)

Lifecycle hooks (ADR 0009) are Go interfaces. The framework provides a base struct whose methods do nothing; an app embeds it and overrides the hook points it needs. Hooks stay synchronous and pure, run in registration order and roll the whole operation back on an error, exactly as in ADR 0009. The same pattern applies to the other extension points (job handlers, method handlers, the `before_transition` hook).

### Dependencies (G2)

The standard library first. The approved dependencies are:

| Module | Licence | Use | Admitted |
|---|---|---|---|
| `github.com/jackc/pgx/v5` (with `pgxpool`) | MIT | PostgreSQL driver and pool | P1 |
| a decimal library: `github.com/shopspring/decimal` (MIT) or `github.com/cockroachdb/apd/v3` (Apache-2.0) | MIT or Apache-2.0 | `Decimal` fields | P1, decided in an ADR |
| `github.com/google/uuid` | BSD-3-Clause | UUID values | P1 |
| one TOML library: `github.com/BurntSushi/toml` or `github.com/pelletier/go-toml/v2` (it must reject unknown keys) | MIT | DocType, overlay and config files | P1 |
| `golang.org/x/crypto/argon2` | BSD-3-Clause | Argon2id passwords, m=19456, t=2, p=1 (ADR 0010) | P3 |

HTTP, TLS, HMAC, SHA-256, CSV, JSON, `embed` and the command line (the `flag` package) use the standard library only. There is **no web framework, no ORM and no command-line framework.** Every dependency and its licence goes into the [dependency evaluation](../blueprint/dependency-evaluation.md#go-dependencies-adr-0026) when it is first used. Any other dependency needs the owner's approval. CI tools (`staticcheck`, `govulncheck`) are run at pinned versions with `go install` and are not module dependencies.

### Toolchain and minimum version

The module pins Go **1.27.3** with the `toolchain go1.27.3` line of `go.mod`. The minimum version, the `go` line, is the previous minor release: **`go 1.26.0`**. It rises together with the pin and the change is noted in the changelog. CI installs exactly the pinned toolchain from <https://go.dev/dl/> and verifies its sha256 against the download index (`.github/ci/install-go.sh`); there is no third-party setup action.

### CI design

The CI of the Rust repository is carried over with the same structure and the same cost rules (draft pull requests skip CI, a weekly run, no run on push, caching, path classes with `changes`, an offline `docs` link check, `ci-result` as the one required check, GitHub-owned actions pinned by SHA). Jobs for P0: `changes`, `docs`, `check` (gofmt, `go vet`, staticcheck, govulncheck, `go test -race`, `go build`, `go mod verify`, `go mod tidy -diff`, against a PostgreSQL 16 service), `licenses` (a standard-library checker of the licence of every module in the build graph) and `ci-result`. The jobs that need later phases (`docker`, `desk`, `e2e`, `sites`, `app-template`) are added with those phases and each is added to `ci-result.needs`.

**`ci-result` fails on a draft pull request (G8).** In the Rust repository `ci-result` itself passed on a draft, because every other job was skipped and a skipped job counts as a success; the rule "CI green on the ready head" then rested on discipline alone. In Go, the first step of `ci-result` fails the job with a clear message when the event is a pull request and the pull request is a draft, so a draft run can never satisfy the required check. The workflow also runs on `converted_to_draft`, so that a run in progress is cancelled by the concurrency group when a pull request is turned back into a draft. Merging needs the `ci-result` of the run on the ready pull request head ([AGENTS.md section 10.3](../../AGENTS.md#103-standing-merge-authorization)).

### Phases

P0 foundations (this ADR, the skeleton, CI, the documentation); P1 metadata and storage (Rust M1); P2 relationships and lifecycle (M2); P3 auth, permissions and the REST API (M3), the minimal desk (M4) and UX, including serving the unchanged desk; P4 the app model (M5); P5 jobs and sites (M6, M7) and the container and deploy work (D1, D2); P6 workflow and reports (M8); P7 integration and customization (M9, M10); then a parity report. Amended by the owner on 2026-11-06 (*"setuju TLS di P5"*): PostgreSQL TLS (M9.0, ADR 0018) moves from P7 to P5, with the container and deploy work. The desk (React) is not rewritten and not restyled; the Go server serves it.

### Carried decisions (G8)

Child-row Links do not restrict user-permission visibility: only top-level fields decide (as Rust M10.3, [ADR 0025](0025-user-permissions.md)).

## Superseded parts of earlier ADRs

The earlier ADRs are not edited (ADR 0001). Their behavioural decisions stay in force. For this repository, the Rust-specific mechanisms below are replaced as stated; the Status column of the [ADR index](README.md) says so for each ADR.

| ADR | What is superseded for the Go implementation | What stays |
|---|---|---|
| 0002 Modular monolith and workspace layout | The Cargo workspace with four crates (and the rename in ADR 0012). Replaced by one Go module with the packages above. | One modular monolith; core free of I/O; the dependency direction (adapted to the import direction above). |
| 0003 PostgreSQL only | Nothing. | Everything. |
| 0004 Database access layer | `tokio-postgres` and `deadpool-postgres`. Replaced by `pgx/v5` and `pgxpool`. | A small in-house SQL, DDL and CRUD layer on validated, quoted identifiers and bound parameters; an embedded migration runner (advisory lock, checksums). The M1 spike condition is not repeated. Connection settings come from the database URL only, as with `tokio-postgres`: `internal/db` parses the URL itself and neutralizes what `pgx` would take from `PG*` variables, the password file or the service file; it uses a pool without connection lifetime limits, like `deadpool-postgres` (P1.2; details in [dependency evaluation](../blueprint/dependency-evaluation.md)). |
| 0005 HTTP stack | axum, tokio, tower-http. Replaced by `net/http` (Go 1.22+ `ServeMux` patterns and middleware as plain functions). | A thin HTTP layer; no CORS; TLS terminated at a reverse proxy; body limits. |
| 0006 Extension model | "A Rust crate compiled into the user's binary", the `DocTypeHooks` trait. Replaced by a Go module compiled into the user's binary and hook interfaces with a no-op base struct. | Compile-time apps, explicit registration list, no runtime plugins, hooks in registration order. |
| 0007 License: MIT | Nothing. | Everything. |
| 0008 Model selection policy | Nothing. | Everything. |
| 0009 Lifecycle pipeline and hooks | Traits, `Arc<dyn ...>`, `#[non_exhaustive]`, `forgebase_core::hooks`. Replaced by interfaces and a base struct. | The hook points, their order, the pipelines, one transaction per operation, rollback on a hook error. |
| 0010 Authentication and sessions | The `argon2` crate and `tokio::task::spawn_blocking`. Replaced by `golang.org/x/crypto/argon2` with the same parameters (m=19456, t=2, p=1) and a plain function call per request goroutine. | Argon2id, server-side sessions, hashed API tokens, lockout, the hash format. |
| 0011 Permission model | Nothing behavioural (type names are Go names). | Everything. |
| 0012 App model and public API boundary | The `forgebase` crate, `App` builder and the macros (`doctypes!`, `migrations!`, `overlays!`), `pub use`, the Cargo feature `testing`. Replaced by the root package `forgebase`, a Go builder, `//go:embed` and the package `forgebase/testing`. | The boundary idea: a small public API, everything else internal; apps never need a change in the framework. |
| 0013 Jobs, scheduler and transactional outbox | tokio tasks and `tokio::time::timeout`, the trait-object transaction. Replaced by goroutines, `context` deadlines and an interface. | The queue table, the outbox, claim and retry rules, schedules in UTC, clean-up jobs. |
| 0014 Multi-site: database per site | `tokio_postgres::Config` parsing, the `Arc<Registry>`. Replaced by pgx's configuration parsing and Go values. | A database per site, `Host` routing, `forgebase.toml`, backup and restore with `pg_dump` and `pg_restore` of the same major version. |
| 0015 Workflow | Conditions "written in Rust" become Go code; the hook is a Go interface. | States, transitions, the log. |
| 0016 Query reports | `include_str!` for the report SQL. Replaced by `//go:embed` or a Go string constant. | App-defined SQL, read-only transaction, timeout, row limit, run by role. |
| 0017 Desk styling | The path of the icon list under `crates/`. Replaced by a path in the Go module (decided when the desk is served, P3). | Tailwind, theme tokens, no component kits; the desk is not restyled. |
| 0018 TLS to PostgreSQL | `rustls`, `rustls-native-certs`, the connector type of `deadpool-postgres`. Replaced by `crypto/tls` and pgx's TLS configuration. | `sslmode` semantics (no TLS unless the URL asks for it; `prefer` and `allow` refused; `require`, `verify-ca` and `verify-full` all verify the certificate chain and the host name), the roots, `sslrootcert`, the refusal of ambiguous URLs. |
| 0019 App method endpoints | Handler types as boxed futures and `MethodDispatch`. Replaced by Go function types and an interface. | The endpoint, run as the caller in one transaction, errors and codes. |
| 0020 Outgoing webhooks | `hyper-util`, `hyper-rustls`, `tower::Service` in the HTTP client. Replaced by `net/http` and `crypto/tls`. | The signed delivery (HMAC-SHA256 over timestamp, a dot and the body), the outbox, the SSRF guard, retries. |
| 0021 Import and export | The `csv-core` crate. Replaced by `encoding/csv`. | The formats, rights, dry run, per-row errors, formula-injection mitigation. |
| 0022 Fixtures | `include_str!`, `serde_json` features, Rust's `{:e}` number formatting, the `sha2` crate. Replaced by `//go:embed` and a canonical encoder written for Go that must produce byte-identical output; `crypto/sha256`. | The fixture format and the canonical checksum (parity test against the Rust output), apply rules. |
| 0023 Metadata overlays | `include_str!` relative to `CARGO_MANIFEST_DIR`, serde `RawField`, `RwLock<Arc<Registry>>`. Replaced by `//go:embed`, the TOML library and an atomic pointer or `sync.RWMutex`. | App overlays, site custom fields, live reload, the rules. |
| 0024 Field permission levels | Nothing behavioural. | Everything. |
| 0025 User permissions | Nothing behavioural (`Arc` of a sorted name list becomes a shared slice). | Everything, including that child-row Links do not restrict (G8). |

## Alternatives considered

- **Stay in Rust.** Rejected by the owner: build time and the cost of a rebuild for quick client changes outweigh the strengths of the language for this project.
- **Port to Go with frameworks and an ORM** (a web framework, `sqlx`-style generators, an ORM, a command-line framework). Rejected (G2): it repeats the dependency discipline question of ADR 0004 and ADR 0005, adds maintenance surface, and DocType tables exist only at runtime, so an ORM does not fit. The standard library covers HTTP, TLS, JSON, CSV and `embed`.
- **A rewrite with a different design** instead of a parity port. Rejected (G1): the Rust implementation is the tested specification; a parity port has an objective acceptance test.
- **Rewrite the desk in Go templates.** Rejected: the desk is not changed (it is not restyled and has its own tests); the Go server serves it.

## Consequences

- A **parity report** at the end of P7 compares the Go implementation with the Rust one (API, schema, metadata formats, CLI, configuration) and is the gate for the owner to decide the archive of the Rust repository (G6). The Rust repository is frozen in the meantime and is the reference implementation.
- The owner reviews per phase: each phase is a plan approved by the owner and executed in the working session under [AGENTS.md section 10](../../AGENTS.md#10-operating-model).
- The guides are adapted to Go as each phase lands: until then they carry a note at the top that their code examples still show the Rust API.
- The blueprint documents keep their Rust wording; this ADR is the mapping of crates, libraries and tooling to Go.
- Go's `internal/` rule enforces the public API boundary of ADR 0012 at compile time.
- Behaviour that depended on Rust's type system (for example, `#[non_exhaustive]`, exhaustive `match`) is replaced by tests and review; the phase plans name each such case.
- `ci-result` can no longer be satisfied by a draft run, which closes a gap of the Rust CI.
