# Dependency evaluation

Condensed from research done on 2026-10-28; sources are given inline. **Versions, licenses and MSRVs are as of 2026-10-28**; re-run `cargo info` at decision time. Recommendations are **Proposed** unless marked Accepted (the `time` and `rust_decimal` choices below were Accepted by the owner on 2026-10-28): the axum, tokio and tokio-postgres + deadpool-postgres (with the custom migration runner) directions are Accepted by the owner (ADRs 0004 and 0005, 2026-10-28; ADR 0004 confirmed by the M1 spike, see below), as are React + TypeScript for the desk (ADR 0002) and TLS termination at a reverse proxy (ADR 0005); everything not marked Accepted is not decided. Admission rules: [maintenance-policy](maintenance-policy.md). Related decisions: [ADR 0004](../adr/0004-database-access-layer.md), [ADR 0005](../adr/0005-http-stack.md).

Selection criteria, in priority order: long-term maintenance cost, simplicity, small dependency count, then fit with runtime-metadata DocTypes (tables and columns created from metadata at runtime; documents cross the boundary as validated maps/JSON). Popularity is not a criterion.

## Method, evidence labels, limitations

| Label | Meaning |
|---|---|
| `Verified (cargo info)` | Read from crates.io metadata with `cargo info` (cargo 1.97.0): version, license field, `rust-version`, features. |
| `Verified (local measurement)` | Measured in throwaway crates with `cargo add` + `cargo tree -e normal` (unique packages, root included, host target) and `cargo metadata` (licenses of all resolved packages, all targets). Nothing was compiled. |
| `Source-indexed` | Backed by a web-search summary of the page at the URL given inline next to the claim. Summaries are second-hand; confirm on the live page. |
| `Unverified` | General knowledge; confirm before relying on it. |

Limitations:

- crates.io, docs.rs, GitHub and rustsec.org were not directly reachable. Release dates, activity and advisories come from search summaries or are Unverified.
- `cargo info` reports the license *field*, not license files; crates with bundled code (ring, aws-lc-sys) may carry extra terms.
- Package counts are a rough proxy for weight (not compile time, `unsafe` usage or C build needs) and depend on the features chosen.

## Summary table

All rows `Verified (cargo info)`. "n/d" = no `rust-version` declared.

| Crate | Version | License | MSRV |
|---|---|---|---|
| axum | 0.8.9 | MIT | 1.80 |
| actix-web | 4.18.1 | MIT OR Apache-2.0 | 1.89 |
| poem | 3.1.12 | MIT OR Apache-2.0 | 1.85 |
| rocket | 0.5.2 | MIT OR Apache-2.0 | 1.89 |
| tower | 0.5.3 | MIT | 1.64 |
| tower-http | 0.7.1 | MIT | 1.65 |
| tokio | 1.54.1 | MIT | 1.85 |
| clap | 4.6.7 | MIT OR Apache-2.0 | 1.85 |
| argh | 0.1.19 | BSD-3-Clause | n/d |
| lexopt | 0.3.2 | MIT | n/d |
| sqlx | 0.9.0 | MIT OR Apache-2.0 | 1.94.0 |
| sea-orm | 2.0.4 | MIT OR Apache-2.0 | 1.94.0 |
| sea-query | 1.0.2 | MIT OR Apache-2.0 | 1.88.0 |
| diesel | 2.3.14 | MIT OR Apache-2.0 | 1.86.0 |
| diesel-async | 0.9.2 | MIT OR Apache-2.0 | 1.84.0 |
| tokio-postgres | 0.7.18 | MIT OR Apache-2.0 | 1.85 |
| postgres-types | 0.2.14 | MIT OR Apache-2.0 | 1.85 |
| deadpool-postgres | 0.14.2 | MIT OR Apache-2.0 | 1.85 |
| refinery | 0.10.0 | MIT | 1.92 |
| serde | 1.0.229 | MIT OR Apache-2.0 | 1.56 |
| serde_json | 1.0.154 | MIT OR Apache-2.0 | 1.71 |
| validator | 0.21.0 | MIT | 1.88 |
| garde | 0.23.0 | MIT OR Apache-2.0 | 1.87 |
| thiserror | 2.0.21 | MIT OR Apache-2.0 | 1.77 |
| anyhow | 1.0.104 | MIT OR Apache-2.0 | 1.68 |
| tracing | 0.1.44 | MIT | 1.65.0 |
| tracing-subscriber | 0.3.23 | MIT | 1.65.0 |
| argon2 | 0.6.0 | MIT OR Apache-2.0 | 1.85 |
| password-hash | 0.6.1 | MIT OR Apache-2.0 | 1.85 |
| tower-sessions | 0.15.0 | MIT | n/d |
| tower-sessions-sqlx-store | 0.15.0 | MIT | n/d |
| axum-login | 0.18.0 | MIT | n/d |
| apalis | 1.0.0 | MIT OR Apache-2.0 | n/d |
| apalis-postgres | 1.0.0 | **unknown (no license field)** | n/d |
| figment | 0.10.19 | MIT OR Apache-2.0 | n/d |
| config (config-rs) | 0.15.27 | MIT OR Apache-2.0 | 1.88.0 |
| toml | 1.1.8+spec-1.1.0 | MIT OR Apache-2.0 | 1.85 |
| cargo-deny | 0.20.2 | MIT OR Apache-2.0 | 1.88.0 |
| cargo-audit | 0.22.2 | Apache-2.0 OR MIT | 1.88 |
| rustls | 0.23.46 (0.24.0-dev.2 is pre-release) | Apache-2.0 OR ISC OR MIT | 1.71 |
| tokio-postgres-rustls | 0.14.0 | MIT | n/d |
| postgres-native-tls | 0.5.3 | MIT OR Apache-2.0 | n/d |
| native-tls | 0.2.18 | MIT OR Apache-2.0 | 1.80 |
| ring | 0.17.14 | Apache-2.0 AND ISC | 1.66.0 |
| aws-lc-rs | 1.18.2 | ISC AND (Apache-2.0 OR ISC) | 1.71.0 |
| uuid | 1.29.0 | Apache-2.0 OR MIT | **1.89.0** |
| chrono | 0.4.45 | MIT OR Apache-2.0 | 1.62.0 |
| time | 0.3.55 | MIT OR Apache-2.0 | 1.88.0 |
| jiff | 0.2.38 | Unlicense OR MIT | 1.70 |
| rust_decimal | 1.43.0 | MIT | 1.67.1 |

Frontend (not `cargo info`): React MIT (Unverified); TypeScript Apache-2.0 (Source-indexed, https://licenses.dev/npm/typescript/2.4.1); Vite MIT, its published artifact also bundles Apache-2.0, BSD-2-Clause, CC0-1.0, ISC and MIT code (Source-indexed, https://arches.lcc.ufmg.br/static/vitest/node_modules/vite/LICENSE.md). Both frontend sources are non-authoritative (the TypeScript page is for the old 2.4.1 version; the Vite page is a third-party mirror): re-verify against the official repositories before relying on them.

### Transitive weight (`Verified (local measurement)`, unique packages)

| Option (features) | Packages |
|---|---|
| axum (default) + tokio (rt-multi-thread, macros, net) | 48 |
| actix-web (default) | 139 |
| axum + tower-sessions | 82 |
| sqlx (no defaults; postgres, runtime-tokio, tls-rustls, migrate, json, uuid, chrono) | 132 |
| tokio-postgres (serde_json, uuid, chrono) + deadpool-postgres | 74 |
| diesel-async (postgres, deadpool) | 78 |
| sea-orm (sqlx-postgres, runtime-tokio-rustls, macros) | 152 |
| sea-query (no defaults; backend-postgres) | 2 |
| refinery (tokio-postgres) | 110 |
| apalis + apalis-postgres (defaults) | 155 |
| clap (derive) / argh / lexopt | 18 / 12 / 2 |
| figment (toml, env) / config (default) / toml + serde(derive) | 23 / 51 / 14 |
| validator (derive) / garde (derive) | 53 / 15 |
| argon2 (default) | 17 |
| tracing + tracing-subscriber (env-filter, json) | 29 |
| **Candidate stack A** (axum, tokio, tokio-postgres, deadpool-postgres, serde, serde_json, thiserror, anyhow, tracing, tracing-subscriber, clap, argon2, toml, uuid, chrono) | **136** |
| **Candidate stack B** (stack A with sqlx instead of tokio-postgres + deadpool; no TLS) | **179** |

## Per-category comparison

### 1. HTTP server

| | axum | actix-web |
|---|---|---|
| Version / MSRV | 0.8.9 / 1.80 | 4.18.1 / 1.89 |
| Weight | 48 | 139 |
| Versioning | 0.x; 0.6 to 0.8 had breaking steps (expect a migration every 1-2 years) | Stable 4.x line (date Unverified) |
| Middleware | tower / tower-http ecosystem (same tokio-rs org) | Own model, separate from tower (Unverified) |

poem (3.1.12) and rocket (0.5.2) offer nothing Forgebase needs that axum lacks.

**Recommendation (Accepted, [ADR 0005](../adr/0005-http-stack.md)): axum**, plus tower-http only as needed.
Trade-off: 0.x breaking upgrades; mitigate by keeping axum types inside `forgebase-http`. Alternative: actix-web if a 1.0+-style stability promise outweighs about 90 extra packages.

#### Admission notes for M3.3 (2026-10-29)

Approved by the owner with the M3 plan ([ADR 0005](../adr/0005-http-stack.md)); licenses checked with `cargo deny --locked check` (licenses ok, no advisory); every new package has an MSRV at or below 1.85 (`hyper-util` 1.85 is the highest; project MSRV 1.97; `cargo +1.97 check --workspace --all-targets --locked` passes).

| Dependency | Where | Need | Alternatives considered | Maintenance, weight |
|---|---|---|---|---|
| `axum` 0.8.9 (MIT, MSRV 1.80), `default-features = false`, features `http1`, `json`, `query`, `tokio` | `forgebase-http` | Routing, extractors, `DefaultBodyLimit`, `axum::serve` with graceful shutdown. `http2`, `form`, `matched-path`, `original-uri`, `tracing`, `macros`, `multipart`, `ws` and `tower-log` are off. | actix-web, poem, rocket (see above). | tokio-rs organisation; 0.x (expect a breaking step every 1-2 years; axum types stay inside the crate). |
| `tower` 0.5.3 (MIT, MSRV 1.64), feature `util` only | `forgebase-http` **dev-dependency** | `ServiceExt::oneshot` to call the router in tests without a socket. | `axum::serve` on a port in every test: slower, port handling. | Already in the tree as an axum dependency (default-off features there). `deadpool-postgres` (a workspace dependency of `forgebase-db`) is also a `forgebase-http` dev-dependency, for a pool that never connects in one unit test; it adds no package. |
| `tokio` features `net`, `sync` | `forgebase-http` | `TcpListener` (`serve`), `Semaphore` (login bound). | none. | Already in the tree. |
| `tokio` features `net`, `signal`, `sync`, `time` | `forgebase` (named `forgebase-cli` until M5.1) | Bind the listener, Ctrl-C and SIGTERM, the stop channels and the grace period for `serve`. `signal` adds `signal-hook-registry` and `errno`. | none. | Already in the tree. |

New packages in `Cargo.lock` (22): `axum` 0.8.9, `axum-core` 0.5.6, `tower` 0.5.3, `tower-layer` 0.3.3, `tower-service` 0.3.3, `http` 1.5.0, `http-body` 1.1.0, `http-body-util` 0.1.5, `hyper` 1.12.0 (MIT, MSRV 1.63), `hyper-util` 0.1.21 (MIT, MSRV 1.85), `httparse` 1.10.1, `httpdate` 1.0.3, `matchit` 0.8.4 (MIT AND BSD-3-Clause), `mime` 0.3.17, `form_urlencoded` 1.2.2, `serde_urlencoded` 0.7.1, `serde_path_to_error` 0.1.20, `sync_wrapper` 1.0.2 (Apache-2.0), `atomic-waker` 1.1.2, `ryu` 1.0.23 (Apache-2.0 OR BSL-1.0), `signal-hook-registry` 1.4.8 and `errno` 0.3.14 (the last two from tokio's `signal`). All licenses are in the `deny.toml` allow list. `forgebase-http` has 144 packages in its normal dependency tree (includes `forgebase-db` and its dependencies). **No `tower-http` and no `cookie` crate**: the body limit is axum's own, CORS is deliberately absent (owner decision 8), and the session cookie is one formatted string and a small parser (the token is lowercase hex, so no escaping is needed; resolves open question 12). `http-body-util` is not a dependency of the crate: tests read bodies with `axum::body::to_bytes`.

### 2. Async runtime

tokio 1.54.1 (MIT, MSRV 1.85). axum, tokio-postgres, deadpool, sqlx and tower-sessions all target tokio; async-std is deprecated (Source-indexed, https://github.com/launchbadge/sqlx/discussions/4271: sqlx 0.9 added smol / async-global-executor support as successors to it); smol/glommio would force their own stacks.
**Recommendation (Accepted, [ADR 0005](../adr/0005-http-stack.md): tokio with explicit features, not `full`; the exact feature list `rt-multi-thread, macros, net, signal, time` is Proposed):** tokio.
Trade-off: the whole stack is tied to the tokio ecosystem and its release cadence; there is no realistic alternative among the chosen libraries, so the cost is accepted rather than mitigated.

### 3. CLI

| Crate | Weight | Notes |
|---|---|---|
| clap (derive) | 18 | Subcommands, help, env fallback; stable 4.x. |
| argh | 12 | BSD-3-Clause; 0.1.x for years (Unverified). |
| lexopt | 2 | Minimal; every subcommand and help text is hand-written. |

**Recommendation (Accepted, owner decision 2026-10-28): clap (derive).** Hand-written parsing would cost more than 17 maintained transitive crates for a many-subcommand CLI. Alternative: lexopt if the CLI stays under about 5 commands.

### 4. Database access

DocType tables exist only at runtime, so compile-time-schema approaches (diesel `table!`, sea-orm entities, sqlx `query!`) fit only Forgebase's own fixed tables. Document CRUD must build SQL from metadata with quoted identifiers and bound parameters.

| Concern | sqlx (no macros) | tokio-postgres + deadpool | diesel(-async) | sea-orm |
|---|---|---|---|---|
| Dynamic DDL / columns | Raw SQL: OK | Raw SQL: OK | Off main path | Off main path |
| Rows as JSON maps | Dynamic row API | Natural (column metadata + `FromSql`) | Awkward | Awkward |
| Compile-time checks | Optional; need DB or `.sqlx` cache (recurring CI cost) | None | Schema file kept in sync | Entities kept in sync |
| Built-in migrations | `sqlx::migrate!` | No | diesel_migrations | sea-orm-migration |
| MSRV | 1.94 | 1.85 | 1.86 / 1.84 | 1.94 |
| Weight | 132 | 74 | 78 | 152 |

Other facts:

- sqlx 0.9.0 (2026-05-06 release notes) states it has not been maintained by LaunchBadge for a few years and is moving to the `transact-rs` GitHub org (Source-indexed, https://github.com/launchbadge/sqlx/discussions/4271): watch the governance change. RUSTSEC-2024-0363 (Postgres binary protocol truncating casts) was fixed in 0.8.1 (Source-indexed, https://rustsec.org/advisories/RUSTSEC-2024-0363).
- tokio-postgres development is concentrated in one maintainer (Unverified bus-factor assessment).
- sea-query 1.0.2 as a query/DDL builder only (backend-postgres) adds 1 package; optional later behind an internal trait.

**Recommendation (Accepted direction, [ADR 0004](../adr/0004-database-access-layer.md)): tokio-postgres + deadpool-postgres + small in-house SQL/DDL layer** (strict identifier pattern such as `^[a-z][a-z0-9_]{0,58}$` for the DocType part (the 63-byte PostgreSQL limit applies to the full name including the 4-byte `doc_` prefix, so at most 59 bytes remain; derived index and constraint names such as `doc_<name>_parent_id_idx` can exceed 63 bytes and need their own bounded naming scheme, decided in M1) and always double-quoted; values only as bound parameters; Postgres-to-field-type mapping).
Close alternative: sqlx with `default-features = false`, features `postgres, runtime-tokio, migrate, json`, macros off: better if built-in migrations and pooling in one crate are worth about 40-60 extra packages, MSRV 1.94 and the governance transition. Not recommended: diesel, sea-orm. The M1 spike (the required checkpoint for [ADR 0004](../adr/0004-database-access-layer.md)) confirmed this direction; see the spike result below.

#### M1 spike result (ADR 0004 checkpoint)

Evidence from a throwaway spike that is not kept in the repository (single run on PostgreSQL 16.15, 4 cores). Both stacks implemented the same scenario end to end: create a table from metadata, insert/get/update/delete with a dynamic `Value` enum, and an injection string stored literally.

| Metric | A: tokio-postgres 0.7.18 + deadpool-postgres 0.14.2 | B: sqlx 0.9.0 (no macros, no TLS) |
|---|---|---|
| Unique packages (`cargo tree -e normal`, incl. spike crate) | 93 | 154 |
| Clean release build | ~30 s | ~54 s |
| Clean debug build | ~19 s | ~33 s |
| Spike LOC (excl. metadata) | 169 | 167 |
| Builds on MSRV 1.97 | yes | yes |
| cargo-deny licenses | ok | ok |

- Dynamic parameters: A implements `ToSql for Value` once and passes `&[&(dyn ToSql + Sync)]`; NULL needs no type. B needs a by-value bind chain, a typed `None` per field type, and `sqlx::AssertSqlSafe` around every runtime query.
- Transactions: A `let mut c = pool.get().await?; let tx = c.transaction().await?; ... tx.commit().await?` (needs a `mut` pooled client; dropping `tx` without commit rolls back). B `pool.begin().await?` then `.execute(&mut *tx)` and `tx.commit()`. Both are acceptable.
- Decoding and type mappings (numeric to `rust_decimal`, date to `time::Date`, timestamptz to `OffsetDateTime`) worked in both. A's error `Display` is only "db error" (the server message is in `as_db_error()`); B includes the server message.

**Decision: the ADR 0004 direction is confirmed; no superseding ADR.** B's advantages (error text, built-in pool and migrations) do not outweigh +61 packages, about 1.8x build time, per-query `AssertSqlSafe`, typed-NULL binding, MSRV 1.94 and the governance transition.

Lessons carried into M1:

- Map Int to PostgreSQL `bigint` (decode as `i64`); an `int` column decodes as `i32`.
- Generated SQL uses explicit parameter casts (`$n::bigint`, `$n::numeric`, ...).
- Wrap database errors in a Forgebase error type that carries the server message and SQLSTATE code.
- `impl ToSql for Value` needs the `bytes` crate (`BytesMut` in the trait signature).

Limitations: one timing run; runtime performance, statement caching and pool contention were not measured.

#### Decimal and date/time types (Accepted, owner decision 2026-10-28)

- **Decimal:** `rust_decimal` (1.43.0, MIT), mapped to PostgreSQL `numeric`. In `forgebase-core` it is used without database features. Precision is limited to 28 digits, which bounds the Decimal field `precision`.
- **Date/time:** `time` (0.3.55, MIT OR Apache-2.0, MSRV 1.88) with only the features needed. `Date` maps to `date`, `OffsetDateTime` to `timestamptz`. chrono and jiff were not chosen.

#### Admission notes for M1.3 (2026-10-28)

Added in the M1.3 PR; licenses were checked with `cargo deny` (all MIT or Apache-2.0).

| Dependency | Where | Need | Alternatives considered | Maintenance, weight |
|---|---|---|---|---|
| `uuid` 1.29 (`std`, `v7`), plus tokio-postgres `with-uuid-1` | `forgebase-db` | UUID v7 `id` generated in the application (owner decision 2026-10-28) and the `uuid` column type. MSRV 1.89 (below the project MSRV 1.97). | Hand-written UUID v7: more code to own, no gain. | Widely used, rust-random organisation. Adds `uuid` to `Cargo.lock`; its random source (`getrandom`) was already in the tree. |
| `bytes` 1 | `forgebase-db` | The signature of `ToSql::to_sql` (the `SqlValue` newtype) names `BytesMut`. Already in the tree through tokio-postgres. | None (the trait requires it). | Already a transitive dependency; no new package. |
| `serde_json` 1.0.154 (`std`) | `forgebase` (named `forgebase-cli` until M5.1) | Parsing `--data` and printing documents as JSON. The evaluation above already recommended serde + serde_json. | Hand-written JSON: not reasonable. | Very widely used, maintained by the serde authors. Already in `Cargo.lock`; now also a direct dependency of the CLI. |
| `clap` 4.6.7 (`std`, `derive`, `help`, `usage`, `error-context`) | `forgebase` (named `forgebase-cli` until M5.1) | Subcommands, help and usage errors (exit code 2). Accepted by the owner in the M1 plan (clap derive). | `lexopt` (hand-written help and subcommands), `argh`; see section 3. | Stable 4.x. Default features such as colour and suggestions are off. Adds `clap`, `clap_builder`, `clap_derive`, `clap_lex`, `anstyle` and `heck` to `Cargo.lock`. |
| `tokio` (CLI) | `forgebase` (named `forgebase-cli` until M5.1) | `#[tokio::main]` for the async database calls; features `rt-multi-thread`, `macros` only. | A current-thread runtime would need fewer features but the owner plan names these. | Already used by `forgebase-db` tests and tokio-postgres. |

Feature changes without new packages: `tokio-postgres` `with-time-0_3` (`time` values as `date` and `timestamptz`), `rust_decimal` `db-tokio-postgres` (in `forgebase-db` only; `forgebase-core` still has no database features), and `time` `parsing` and `formatting` in the CLI manifest only (RFC 3339 and `YYYY-MM-DD`). Packages new to `Cargo.lock` in this PR: `anstyle`, `clap`, `clap_builder`, `clap_derive`, `clap_lex`, `heck`, `uuid` and `time-macros` (a lock entry only, not built). `cargo tree -p forgebase-cli -e normal` (the crate is now `forgebase`) listed 98 packages (94 external, 4 workspace crates).

### 5. Migrations

Two problems: (1) framework-internal ordered SQL migrations; (2) DocType schema sync, a declarative diff of metadata vs `pg_catalog`, which no migration tool solves.

| Option | Notes |
|---|---|
| sqlx migrate | Only if sqlx is chosen. |
| refinery 0.10.0 | MSRV 1.92; 110 packages; only 0.8.x from 2024 seen in search, so 0.10 history unconfirmed. |
| Custom runner | `forgebase_migrations(id, checksum, applied_at)`, `pg_advisory_xact_lock`, embedded SQL via `include_str!`, checksum verification; about 100-200 lines (Unverified estimate). |

**Recommendation (custom runner Accepted with [ADR 0004](../adr/0004-database-access-layer.md), confirmed by the M1 spike, see the spike result above; the rest Proposed):** custom runner if tokio-postgres; `sqlx::migrate!` if sqlx. Schema sync is always framework code, transactional, destructive changes explicit. Avoid refinery.

### 6. Serialization and validation

- serde 1.0.229 and serde_json 1.0.154: no real alternative. Decide `preserve_order` and `arbitrary_precision` features early; they affect the whole workspace (semantics Unverified).
- validator (53 packages) and garde (15) validate static structs; DocType rules are runtime metadata, so neither helps for documents.

**Recommendation (Proposed):** serde + serde_json, one in-house runtime validator returning structured per-field errors; no validation crate for now.

### 7. Errors and logging

**Recommendation (Proposed):** thiserror in libraries, anyhow only in binaries; tracing + tracing-subscriber (`fmt`, `env-filter`; 29 packages); defer OpenTelemetry.

Alternatives (not evaluated in the research; all Unverified, from general knowledge): `snafu` or hand-written error enums instead of thiserror; `eyre`/`color-eyre` instead of anyhow; `log` + `env_logger` instead of tracing. Trade-off: tracing adds weight (29 packages, measured) and an extra concept (spans), but fits the tokio/axum ecosystem and structured logs; `log` + `env_logger` would likely be lighter (weight not measured) with no spans. Revisit only if tracing's weight or complexity proves a problem.

### 8. Authentication

- argon2 0.6.0 (RustCrypto, pure Rust, 17 packages). Argon2id as OWASP default and absence of an audit are Unverified; 0.5 to 0.6 implies API churn.
- tower-sessions 0.15.0: +34 packages over axum; its Postgres store (`tower-sessions-sqlx-store`) pulls sqlx; 0.x with frequent breaking releases and a past session-ID cycling fix (Source-indexed, https://github.com/maxcountryman/tower-sessions/releases); single maintainer (Unverified). axum-login adds more abstraction than needed.
- Custom opaque sessions: 256-bit random token, SHA-256 hash stored with expiry/user/CSRF secret, `HttpOnly; Secure; SameSite=Lax` cookie; about 200-300 lines (Unverified estimate). `sha2` was not evaluated. JWT is avoided for first-party sessions (revocation complexity).

**Recommendation (Proposed):** argon2 (Argon2id) + custom DB-backed opaque session tokens and hashed API tokens. Alternative: tower-sessions with a self-written store.

#### Admission notes for M3.1 (2026-10-29)

Approved by the owner with the M3 plan; recorded in [ADR 0010](../adr/0010-authentication-and-sessions.md). Licenses checked with `cargo deny --locked check` (licenses ok); MSRV from `cargo metadata`, all at or below 1.85 (project MSRV 1.97; `cargo +1.97 check` passes).

| Dependency | Where | Need | Alternatives considered | Maintenance, weight |
|---|---|---|---|---|
| `argon2` 0.6.0 (MIT OR Apache-2.0, MSRV 1.85), features `alloc` and `password-hash` only | `forgebase-db` | Argon2id password hashing and verification of PHC strings. | bcrypt (72-byte input limit, not memory-hard), scrypt, hand-written: no. Argon2id is the default recommendation (Unverified, see below). | RustCrypto, pure Rust. New packages in `Cargo.lock`: `argon2`, `password-hash` 0.6.1, `phc` 0.6.1, `base64ct` 1.8.3, `blake2` 0.11.0 (all MIT or Apache-2.0, MSRV 1.85); `cpufeatures` was already there. The `getrandom` and `parallel` features are off: the salt comes from our own `getrandom` call and hashing is single-lane. `password-hash` comes only through `argon2`'s re-exports, not as a direct dependency. |
| `getrandom` 0.4.3 (MIT OR Apache-2.0, MSRV 1.85), default features | `forgebase-db` | 32-byte tokens and the 16-byte salt from the operating system's random source (`getrandom::fill`). | `uuid`'s internal use (not an API), `rand` (more weight, not needed). | rust-random organisation. Version 0.4.3 was already in the tree (`cargo tree -i getrandom@0.4.3 -e normal` shows `rand` 0.10, which `postgres-protocol` and so tokio-postgres use, and `uuid`); it is now also a direct dependency. No new package. |
| `tokio` (normal dependency of `forgebase-db`; feature `rt` only; the workspace entry has no default features, `macros` and `rt-multi-thread` are enabled in `forgebase` (formerly `forgebase-cli`) and in the dev-dependencies of `forgebase-db`) | `forgebase-db` | `tokio::task::spawn_blocking` so Argon2 does not block the async executor. | A dedicated thread pool: more code. | Already in the tree (tokio-postgres, and a dev-dependency of this crate before). |

Hashing of tokens needs no crate: SHA-256 is computed by PostgreSQL (`sha256(convert_to($1::text, 'UTF8'))`), as for migration checksums. Parameters: m = 19456 KiB, t = 2, p = 1; they match the OWASP Password Storage Cheat Sheet's minimum Argon2id configuration as far as known (Unverified: the cheat sheet was not re-read for this change). The crate's API change between 0.5 and 0.6 does not matter here: no 0.5 code existed. No independent audit of the crate was checked (Unverified).

### 9. Background jobs

- apalis 1.0.0 is very new (still at 1.0.0-rc.4 in Feb 2026; Source-indexed, https://lib.rs/crates/apalis), apalis-postgres has **no license field**, 155 packages, and is built on sqlx.
- Redis queues add a second stateful service, against the PostgreSQL-only direction.
- Custom queue: atomic claim with `FOR UPDATE SKIP LOCKED`; optional `LISTEN/NOTIFY` wake-up; enqueue in the same transaction as the document write gives a transactional outbox.

**Recommendation (Proposed):** custom Postgres `SKIP LOCKED` queue (few hundred lines, no new dependencies) with retries, backoff and a lease timeout.

**Decision (M6.1, [ADR 0013](../adr/0013-jobs-scheduler-and-transactional-outbox.md)): done as recommended, with no new package.** `serde_json` 1.0.154 (`std`, the workspace entry), already a dependency of `forgebase`, is now also a direct dependency of `forgebase-core` (the payload of `HookContext::enqueue`) and `forgebase-db` (reading and writing payloads); it is the same package and version, so `Cargo.lock` gains only the dependency edges, and `forgebase` re-exports it as `forgebase::serde_json` so apps use that version. The payload is bound to PostgreSQL as text and cast to `jsonb` in SQL, so the `with-serde_json-1` feature of `tokio-postgres` is not needed. The `tokio` features `signal`, `sync` and `time` of `forgebase` were already enabled; `tokio::task` and `watch` (the worker) need no further feature. `deadpool-postgres` is used for `Object::take` (closing a connection instead of pooling it) and a pool size, as `migrate_apps` already does.

### 10. Configuration

| Option | Weight | Notes |
|---|---|---|
| figment 0.10.19 | 23 | Good layering; upstream activity unconfirmed. |
| config 0.15.27 | 51 | Many formats; heavier. |
| toml + serde | 14 | Env overrides written by hand. |

**Recommendation (Proposed):** toml + serde with a small overlay: defaults, then `forgebase.toml`, then explicitly listed `FORGEBASE_*` env vars; secrets only from env or a file path. Alternative: figment.

### 11. Supply-chain tooling and MSRV

- cargo-deny (licenses, advisories, bans, sources) in CI; cargo-audit mostly redundant. Both are CI tools, not workspace dependencies. Details: [maintenance-policy](maintenance-policy.md).
- Measured floors: stack A **1.89** (set by `uuid` 1.29.0); stack B **1.94** (set by sqlx).

**CI actions (owner decision 2026-10-30, [maintenance policy](maintenance-policy.md#ci)).** `actions/checkout` v7.0.1 and `actions/cache` v6.1.0 (licence MIT, owned by GitHub), each pinned by full commit SHA in `ci.yml` with a version comment, and upgraded like each other. `actions/cache` is used for the Cargo and npm caches; it is the only reason a third action was admitted (cost of the private repository). The SHA of `v6.1.0` is `55cc8345863c7cc4c66a329aec7e433d2d1c52a9` (a lightweight tag; read with `git ls-remote --tags`). No third-party action is used.

### 12. Frontend

React + TypeScript for `desk/` is Accepted ([ADR 0002](../adr/0002-modular-monolith-and-workspace-layout.md)). Alternatives, listed briefly and not evaluated (Unverified): Vue, Svelte or Solid as the UI framework; server-rendered HTML (for example with htmx) instead of a SPA.

#### Owner decisions for M4 (2026-10-29, Accepted)

- **No UI component library, no router library, no data-fetching library.** Native inputs and a small own stylesheet; an own hash router (`desk/src/router.ts`); plain `fetch` (`desk/src/api.ts`). Reason: every runtime dependency is a long-term maintenance cost; the desk needs a login, a list and (M4.2) a form. Revisit only if a real need appears (an ADR then).
- **Toolchain: Vite + TypeScript + Vitest**, with ESLint (`@eslint/js`, `typescript-eslint`, `eslint-plugin-react-hooks`: the smallest set that lints TypeScript and the React hooks rules; the project's own rules forbid browser storage, `dangerouslySetInnerHTML` and inline `style`). Tests are pure-module tests in Vitest's `node` environment with a stubbed `fetch`; no `jsdom`, no testing library (not needed in M4.1 or M4.2: the form view's logic is in pure modules, and the browser behavior is covered by the one Playwright smoke test).
- **Only `react` and `react-dom` are runtime dependencies.** `npm run licenses` fails otherwise. *Amended by UX.4 (2026-10-30, owner decision U2): the self-hosted font `@fontsource-variable/inter` is the third runtime package, see the admission note for UX.4 below.*

#### Admission notes for M4.1 (2026-10-29)

Versions are pinned exactly in `desk/package.json` (no `^` or `~`) and the lockfile (`desk/package-lock.json`, lockfile version 3) is committed. Licenses are the `license` field of the lockfile (`Verified (local measurement)`, npm 10.9.4, Node 22.22.0); versions from `npm view` on 2026-10-29.

| Package | Version | License | Use |
|---|---|---|---|
| `react`, `react-dom` | 19.3.1 | MIT | runtime (shipped) |
| `vite` | 8.3.5 | MIT | build and dev server |
| `@vitejs/plugin-react` | 6.1.2 | MIT | JSX transform for Vite |
| `typescript` | 6.0.3 | Apache-2.0 | `tsc --noEmit` (type check only; Vite transpiles) |
| `vitest` | 5.0.5 | MIT | tests |
| `eslint`, `@eslint/js` | 10.13.0, 10.0.1 | MIT | lint |
| `typescript-eslint` | 8.72.1 | MIT | lint rules for TypeScript |
| `eslint-plugin-react-hooks` | 7.1.1 | MIT | hooks rules |
| `@types/react`, `@types/react-dom` | 19.3.0 | MIT | types |

**TypeScript 6.0.3, not 7.0.2.** TypeScript 7.0.2 exists and the pre-research named it, but `typescript-eslint` 8.72.1 declares the peer range `typescript >=4.8.4 <6.1.0`, so `npm install` refuses the combination (`ERESOLVE`; `--legacy-peer-deps` was not used). The newest TypeScript inside that range, 6.0.3, is pinned. Move to 7 when `typescript-eslint` supports it.

**Package count (`Verified (local measurement)`).** The lockfile lists **197** packages (172 are installed on Linux x64; the rest are optional platform binaries of `rolldown`, `lightningcss` and similar that other platforms install). Only **3** are shipped in the built desk (`react`, `react-dom`, `scheduler`, all MIT); the other 194 are development tooling. By license: MIT 149 (3 shipped), Apache-2.0 16, ISC 10, BSD-2-Clause 6, BSD-3-Clause 2, **MPL-2.0 12** (all dev only: `lightningcss` 1.33.0 and its 11 platform packages, used by Vite's CSS tooling), BlueOak-1.0.0 1 (`minimatch`), CC-BY-4.0 1 (`caniuse-lite`, browser data).

**Frontend license allow-list (Accepted).** Enforced by `desk/scripts/check-licenses.mjs` (`npm run licenses`, plain Node, run in CI):

- every package in `package-lock.json` needs a `license` that is exactly one of these strings (a missing license, an SPDX expression or anything else fails);
- packages that are **shipped** (not `dev` in the lockfile): MIT, ISC, Apache-2.0, BSD-2-Clause, BSD-3-Clause; and, since UX.4, **OFL-1.1 for font packages only** (the `@fontsource` and `@fontsource-variable` scopes; never for code);
- packages that are **development only**: the same plus MPL-2.0, BlueOak-1.0.0 and CC-BY-4.0.

**MPL-2.0 is accepted for development tooling only.** MPL-2.0 is file-level copyleft; nothing of `lightningcss` is part of the built desk, so it places no obligation on the MIT-licensed result. If an MPL-2.0 package ever becomes a shipped dependency (not `dev` in the lockfile), the check fails and the owner decides. The same check keeps the runtime dependency list to `react` and `react-dom`. **Limit of the "dev" flag.** The lockfile's `dev` flag says whether a package is reachable from `dependencies` only; it is as good as the imports of `desk/src/`. A development-only package (for example an MPL-2.0 one) that is `import`ed from `src/` would be bundled into the shipped desk although the lockfile calls it dev. Reviewers must check imports in `src/` for that; there is deliberately no scanner of `desk/dist`. Install scripts of dependencies are off: `desk/.npmrc` sets `ignore-scripts=true`, so `npm ci` (locally and in CI) runs none. Node: `^20.19.0 || >=22.12.0` (the range Vite 8 needs). The CI job also runs `npm audit --audit-level=high` (high and critical advisories fail the job; the daily schedule runs it too). `typescript` at 6.x is Apache-2.0 (NOTICE files are kept in `node_modules`, which is not shipped).

#### Admission note for M4.2 (2026-10-29): Playwright

One development dependency is added for the smoke test of owner decision 5 (one Playwright test, Chromium only):

| Package | Version | License | Use |
|---|---|---|---|
| `@playwright/test` | 1.64.1 | Apache-2.0 | test runner and API for `desk/e2e/smoke.spec.ts` (dev only) |
| `playwright`, `playwright-core` | 1.64.1 | Apache-2.0 | pulled in by `@playwright/test` (dev only; no other dependencies; `fsevents` is an optional macOS dependency that was already in the tree) |

`Verified (local measurement)`: licenses are the `license` field of the lockfile (`npm run licenses` passes: the lockfile now lists 200 packages, 3 of them shipped as before); the version is exact-pinned and 1.64.1 is the `latest` dist-tag of `npm view @playwright/test` on 2026-10-29. Nothing of Playwright is imported from `desk/src`; it is used by `desk/e2e` and `desk/playwright.config.ts` only (reviewers check that, like for any dev package). The Chromium that Playwright drives is **not** an npm package: CI downloads it with `npx playwright install --with-deps chromium` (an explicit command; `ignore-scripts=true` still holds for `npm ci`), it is never stored in the repository or shipped, and a developer who already has a Chromium points the test at it with `PLAYWRIGHT_CHROMIUM_EXECUTABLE`. Cost to keep in mind: the browser download is a CI-time network dependency and Playwright releases often (a new Chromium per release); the single test keeps the surface small, and the version is bumped by hand like any other pin. `@types/node` was **not** added: the two e2e files declare the one global they use (`desk/e2e/env.d.ts`).

The earlier remark that "UI library, router, data-fetching and form libraries are deferred" is answered by the decisions above.

#### Admission note for UX.3 (2026-10-30): Tailwind CSS

Owner decision T1 ([ADR 0017](../adr/0017-desk-styling-tailwind.md)): the desk is styled with Tailwind CSS v4 at build time. Two direct development dependencies, exact-pinned; licenses are the `license` field of the lockfile (`Verified (local measurement)`, npm 10.9.4, Node 22.22.0); 4.3.3 was verified on npm by the orchestrator on 2026-10-30.

| Package | Version | License | Use |
|---|---|---|---|
| `tailwindcss` | 4.3.3 | MIT | the compiler (utilities, `@theme`, `@apply`), dev only |
| `@tailwindcss/vite` | 4.3.3 | MIT | the Vite plugin that runs it, dev only |
| `@tailwindcss/node`, `@tailwindcss/oxide` | 4.3.3 | MIT | pulled in; `oxide` is the Rust scanner and parser (a native binary) |
| `@tailwindcss/oxide-<platform>` (12 packages: android, darwin x2, freebsd, linux x5, wasm32-wasi, win32 x2) | 4.3.3 | MIT | optional: npm installs the one for the machine (`linux-x64-gnu` on CI) |
| `lightningcss` 1.32.0 and its 11 platform packages (nested under `@tailwindcss/node`) | 1.32.0 | MPL-2.0 | optional platform binaries as before; dev only |
| `enhanced-resolve`, `tapable`, `graceful-fs`, `jiti`, `magic-string` | 5.26.0, 2.3.3, 4.2.11, 2.7.0, 0.30.21 | MIT, ISC (`graceful-fs`) | pulled in; dev only |

**Package count (`Verified (local measurement)`, lockfile diff).** The lockfile grows from 200 to **233** packages (**+33**, no existing package changed version): 2 direct, 31 transitive. All 33 are development only (none is shipped; the shipped set is still `react`, `react-dom` and `scheduler`). On Linux x64, `npm ci` installs 191 packages. By license the new ones are MIT 20 and ISC 1, plus **MPL-2.0 12**: `lightningcss` 1.32.0 and its 11 platform packages. Vite already carries `lightningcss` 1.33.0 (the MPL-2.0 dev tool of the M4.1 admission); Tailwind's own range needs a copy of its own, so there are now two versions side by side (24 MPL-2.0 entries in total, all optional or dev). MPL-2.0 stays allowed for development tooling only, exactly as decided in M4.1; nothing of it is in the built desk. `npm run licenses` passes with the allow-list unchanged, and `npm audit --audit-level=high` finds nothing.

**Install scripts.** `@tailwindcss/oxide` is a native module delivered as optional platform packages; no Tailwind package has `hasInstallScript` in the lockfile (the only one is `fsevents`, as before): the binary comes from the platform package, not from a script, and `npm ci` with `ignore-scripts=true` (`desk/.npmrc`) builds and runs the desk correctly (verified: `npm ci && npm run build` and the Playwright smoke test). No change to `.npmrc` or to CI is needed. Cost to keep in mind: a platform without a published `@tailwindcss/oxide` binary cannot build the desk (the WebAssembly fallback `oxide-wasm32-wasi` exists).

**Maintenance.** Tailwind Labs, MIT, active releases; a build tool with no runtime in the browser. **Why not plain CSS:** owner decision T1 (a shared scale and vocabulary for the M8 desk work); the cost is the toolchain above. The CSP is unchanged (verified on the built files: no inline script or style, no `@import`, no `url(...)`).

#### Admission note for UX.4 (2026-10-30): a self-hosted font and copied icons

Owner decisions U2 and U3 ([ADR 0017](../adr/0017-desk-styling-tailwind.md), section "UX.4"). **One new npm package and one copied asset set; no new crate.**

| Package | Version | License | Use |
|---|---|---|---|
| `@fontsource-variable/inter` | 5.3.0 | **OFL-1.1** (SIL Open Font License 1.1) | the Inter font, shipped as CSS and `woff2` files only (**runtime**, exact pin) |

`Verified (local measurement)`: the version is the `latest` dist-tag of `npm view @fontsource-variable/inter` on 2026-10-30 and its `license` field is `OFL-1.1`; it has no dependencies and no install script; `npm audit --audit-level=high` finds nothing. **Shipped as fonts only:** the desk uses one file of the package, `files/inter-latin-wght-normal.woff2` (the Latin variable-weight subset, 48 KB), through one hand-written `@font-face` in `desk/src/styles.css` (the package's default CSS, which lists every subset, is not imported); Vite copies that file to `assets/`. The package contains no JavaScript that the desk imports, so the JavaScript bundle is unchanged in kind (checked on the build: nothing under `assets/*.js` refers to the package). **Licence decision (OFL-1.1).** The OFL permits using, embedding and redistributing the font, also commercially and inside software, provided the licence and copyright notice stay with the font and the font is not sold on its own; it is a font licence and is not a copyleft licence for the software that uses the font. It is **not** on the allow-list for code. The check enforces that: OFL-1.1 passes only for packages in the `@fontsource` and `@fontsource-variable` scopes, and the runtime dependency list is exactly `@fontsource-variable/inter`, `react` and `react-dom`. Inter's own licence file ships in the package (`node_modules`); the built desk carries it too: `desk/public/THIRD-PARTY-LICENSES.txt` (Inter's OFL-1.1 text verbatim, with the Lucide notice) is copied by Vite to `desk/dist/THIRD-PARTY-LICENSES.txt`, and a desk test (`src/licenses.test.ts`) fails when it differs from the installed package's `LICENSE` (owner decision of 2026-10-30, *"setujui rekomendasi putusanmu"*). The notice is in `dist/` as part of the artefact; `forgebase serve` serves only `index.html` and `assets/{name}`, so it is not served over HTTP. **Maintenance.** Fontsource is an active, widely used packaging of open fonts (Inter is by Rasmus Andersson, OFL); the cost is a version bump of one package, and the family can be replaced by changing one `@font-face` and the `--font-sans` token. **Why a web font at all:** owner decision U2 (a consistent, elegant look on every machine, which the system stack cannot give); the cost is 48 KB per first load, within the 300 KB budget.

**Lucide icons (copied, not a dependency).** The desk draws 34 icons from [Lucide](https://lucide.dev) 1.65.0 (`lucide-static`, ISC; the Feather-derived ones also carry the MIT notice of Cole Bemis), written as inline React components in `desk/src/icons.tsx`. The SVG source was read from `npm pack lucide-static@1.65.0` into a scratch directory and **never added to `package.json` or the lockfile**; `lucide`, `lucide-react` and `lucide-static` are not dependencies. The licence text is kept next to the code in `desk/src/icons/LICENSE-lucide.txt`, with the version and the list of icons. ISC and MIT are on the allow-list; because nothing is installed, the lockfile check has nothing to check, so the notice file is the record; it is also part of `desk/public/THIRD-PARTY-LICENSES.txt`, shipped in `dist/`. **Maintenance:** a new or changed icon is copied by hand with the notice kept; the set is small and fixed on purpose. The DocType icon names are one list shared with the Rust crate (`crates/forgebase-core/src/doctype_icons.txt`) and a desk test fails when the two disagree.

**Package count (`Verified (local measurement)`, lockfile diff).** The lockfile grows from 233 to **234** packages (+1: the font; it is the only shipped package beyond `react`, `react-dom` and `scheduler`). By license: MIT 166 dev + 3 shipped, ISC 11, Apache-2.0 19, BSD-2-Clause 6, BSD-3-Clause 2, MPL-2.0 24 (dev only), BlueOak-1.0.0 1, CC-BY-4.0 1, **OFL-1.1 1 (shipped, a font)**.

### Admission note for M5.1 (2026-10-29): no new package

M5.1 adds **no external package** and no new version to `Cargo.lock`. The crate `forgebase-cli` is renamed `forgebase` and gets a library target; `axum` and `tower` (already direct dependencies of `forgebase-http`, same version and features) become **optional normal dependencies** of `forgebase`, enabled only by its cargo feature `testing` (the in-process HTTP client of `forgebase::testing` needs `axum::Router` and `tower::ServiceExt::oneshot`, as the `forgebase-http` tests do). The `testing` feature is off by default, so the stock binary and an app's release build do not carry the helpers; an app enables it in its `[dev-dependencies]`. The crate depends on itself as a dev-dependency with that feature, so that its own integration tests (which use the helpers) run in a plain `cargo test`. `tokio` also gets the `rt-multi-thread` runtime built by hand in the library (`Forgebase::run`) instead of `#[tokio::main]`; the features are the ones it already had. The public API re-exports `rust_decimal::Decimal` and `time::{Date, OffsetDateTime}` (the payloads of `Value`) from the versions already in the tree. Licenses: unchanged.

### 13. TLS to PostgreSQL (M9.0)

Owner decision of 2026-10-31, *"setuju ci-result dan setuju M9.0"* ([ADR 0018](../adr/0018-postgres-tls.md)): **`tokio-postgres-rustls` is approved**, with rustls and the **`ring`** provider. `rustls-native-certs` was already approved on 2026-10-30 for the M9.2 webhooks; the two share **one TLS stack** (M9.2 adds `hyper-rustls` on the same `rustls`).

| Crate | Version | Licence | Why it is in the tree |
|---|---|---|---|
| **`tokio-postgres-rustls`** | 0.14.0 | MIT | **new direct dependency** (workspace, `default-features = false`, feature `ring`); the `MakeTlsConnect` for `tokio-postgres`, one file, also does SCRAM channel binding |
| `rustls` | 0.23.46 | Apache-2.0 OR ISC OR MIT | direct dependency of `forgebase-db` (builds the client config); `default-features = false`, features `ring`, `std`, `tls12` |
| `rustls-native-certs` | 0.8.5 | Apache-2.0 OR ISC OR MIT | direct dependency of `forgebase-db` (the operating system's roots) |
| `ring` | 0.17.14 | Apache-2.0 AND ISC | the crypto provider (both licences are on the allow-list; builds C and assembly with `cc`, no CMake) |
| `rustls-webpki` | 0.103.16 | ISC | certificate and name verification (IP names included) |
| `rustls-pki-types` | 1.15.1 | MIT OR Apache-2.0 | types, PEM reading of `sslrootcert` |
| `tokio-rustls` | 0.26.6 | MIT OR Apache-2.0 | the async TLS stream |
| `x509-cert` 0.2.5, `der` 0.7.10, `der_derive`, `spki` 0.7.3, `const-oid` 0.9.6, `flagset`, `tls_codec` 0.4.2 (+ `_derive`) | | Apache-2.0 OR MIT (`flagset`: Apache-2.0) | `tokio-postgres-rustls` reads the server certificate for SCRAM channel binding |
| `subtle` 2.6.1 | | BSD-3-Clause | constant-time compares (`ring`, `rustls`) |
| `untrusted` 0.9.0 | | ISC | `ring`, `rustls-webpki` |
| `zeroize` 1.9.1 (+ `_derive`) | | Apache-2.0 OR MIT | `rustls` |
| `openssl-probe` 0.2.2 | | MIT OR Apache-2.0 | `rustls-native-certs`: where Linux keeps the CA store |
| `cc` 1.10.0, `shlex` 2.0.1, `find-msvc-tools` | | MIT OR Apache-2.0 | build scripts of `ring` |
| `core-foundation` (+ `-sys`), `security-framework` (+ `-sys`) | | MIT OR Apache-2.0 | macOS root store (not built on Linux) |
| `schannel` 0.1.30, `windows-sys` 0.52.6, `windows-targets` and the `windows_*` crates | | MIT | Windows root store (not built on Linux) |

`Verified (local measurement)`, 2026-10-31, from `Cargo.lock` and `cargo metadata`:

- **The lockfile grows from 171 to 209 package entries (+38)**, 15 of which are only built on Windows or macOS. `sha2` stays at the one version in the tree (0.11.1; `tokio-postgres-rustls` 0.14 asks for `^0.11`), so there is **no second `sha2`**.
- **Absent from the tree:** `cargo tree --workspace --all-features -i aws-lc-rs`, `-i openssl-sys` and `-i webpki-roots` each report that the package is not in the graph. No OpenSSL, no `aws-lc-rs` (a C library and CMake), no `webpki-roots` (its data is CDLA-Permissive-2.0, avoided by decision).
- **Licences:** `cargo deny --locked check` passes with the existing allow-list; no new entry and no `clarify` for `ring`. (Whether the `ring` 0.17.14 source tree still carries OpenSSL- or BoringSSL-derived text, as older releases did, was not inspected: the crate's declared licence is `Apache-2.0 AND ISC` and `cargo deny` accepts it. Inspect before a release.)
- **New duplicate versions** (`cargo deny` warns, as for the other duplicates of the tree): `const-oid` 0.9.6 beside 0.10.2, and `windows-sys` 0.52.6 beside 0.61.2 (Windows only).
- **MSRV:** `cargo +1.97 check --workspace --all-targets --all-features --locked` passes.
- **TLS is not behind a cargo feature** (the older "Proposed" text said it would be): one code path is easier to keep correct than two builds, and the stack is shared with M9.2.

### 14. Outgoing webhooks (M9.2)

Owner decision 6 of the M9 plan, 2026-10-30 (*"setuju semua rencana mu"*; [ADR 0020](../adr/0020-outgoing-webhooks.md)): **`hyper-util` (client) + `hyper-rustls` (`ring`) + `rustls-native-certs`**. `rustls-native-certs` and the `ring` TLS stack came with M9.0 ([section 13](#13-tls-to-postgresql-m90)). Rejected: `reqwest` 0.13 (its default rustls pulls `aws-lc-rs`, which builds a C library with CMake; the other feature set pulls `webpki-roots`, CDLA-Permissive-2.0) and `ureq` 3 (about 46 new crates).

| Crate | Version | Licence | How |
|---|---|---|---|
| **`hyper-rustls`** | 0.27.10 | Apache-2.0 OR ISC OR MIT | **new direct dependency** of `forgebase` (workspace, `default-features = false`, features `http1`, `tls12`, `ring`; **not** `aws-lc-rs`, `native-tokio`, `webpki-tokio` or `http2`: the roots come from `forgebase_db::tls`, which is the code of ADR 0018). MSRV 1.85. |
| `try-lock` 0.2.5 | | MIT | new transitive crate (`want`) |
| `want` 0.3.2 | | MIT | new transitive crate (`hyper-util`'s legacy client, pulled in by the `client-legacy` feature) |
| `hyper-util` | 0.1.21 | MIT | already in the tree (axum); now a **direct** dependency of `forgebase` with `default-features = false` and features `client-legacy`, `http1`, `tokio` |
| `http-body-util` | 0.1.5 | MIT | already in the tree; now direct (reads and discards the response body up to 64 KiB) |
| `hmac` 0.13.0, `sha2` 0.11.1 | | MIT OR Apache-2.0 | already in the tree (PostgreSQL SCRAM); now direct (the signature). The same single versions: **no second `sha2` or `hmac`**. |
| `rustls` | 0.23.46 | Apache-2.0 OR ISC OR MIT | now also a direct dependency of `forgebase` (the test seam takes a root store); still **one** version |
| `axum`, `tower` | 0.8.9, 0.5.3 | MIT | were optional in `forgebase` (feature `testing`); now unconditional there. The client builds its requests with `axum::http` (no direct `http` or `hyper` dependency is added) and implements `tower::Service` for the pinned resolver. Both are compiled for `forgebase-http` already. |

`Verified (local measurement)`, 2026-10-31, from `Cargo.lock`, `cargo tree` and `cargo deny`:

- **The lockfile grows from 209 to 212 package entries (+3):** `hyper-rustls`, `try-lock` and `want` (the plan expected about 15; M9.0 had already added `rustls`, `ring`, `tokio-rustls` and the root-store crates).
- **Absent from the tree:** `cargo tree --workspace --all-features --locked -i aws-lc-rs`, `-i openssl-sys` and `-i webpki-roots` each report that the package is not in the graph.
- **One `rustls`:** `Cargo.lock` has a single `rustls` entry (0.23.46), shared by `tokio-postgres-rustls`, `hyper-rustls` and `forgebase-db`.
- **Licences:** `cargo deny --locked check` passes with the existing allow-list (`MIT`, `Apache-2.0`, `ISC` cover the new crates); `deny.toml` is unchanged.
- **MSRV:** `cargo +1.97 check --workspace --all-targets --all-features --locked` passes (`hyper-rustls` 0.27.10 declares 1.85).
- **Test tooling:** the TLS tests need the `openssl` command-line tool (to make a throwaway CA); it is not a dependency of the build. CI images have it.

### 15. Import and export (M9.3)

Owner decision 9 of the M9 plan, 2026-10-30 (*"setuju semua rencana mu"*; [ADR 0021](../adr/0021-import-and-export.md)): **the `csv` crate reads CSV** (reading only; the writer is hand-written, about thirty lines, because the formula mitigation and the quoting rule need control over each cell).

| Crate | Version | Licence | How |
|---|---|---|---|
| **`csv`** | 1.4.0 | Unlicense OR MIT | **new direct dependency** of `forgebase-http` (workspace, default features; it has none that matter). MSRV 1.73. By BurntSushi; the de facto Rust CSV reader. |
| `csv-core` | 0.1.13 | Unlicense OR MIT | new transitive crate (the state machine under `csv`) |
| `itoa`, `ryu`, `memchr`, `serde_core` | | MIT OR Apache-2.0 (`memchr`: Unlicense OR MIT) | **already in the tree** (serde_json, axum): no new entry |
| `http-body-util` | 0.1.5 | MIT | already in the tree; now also a direct dependency of `forgebase-http`, with its **`channel` feature** (it adds `tokio` to that crate's own edges; no new crate): the body of a streamed export |

`Verified (local measurement)`, 2026-10-31, from `Cargo.lock`, `cargo tree` and `cargo deny`:

- **The lockfile grows by two package entries (+2):** `csv` and `csv-core` (the plan expected `csv` with `csv-core`, `itoa` and `ryu`; the last two were already there).
- **Licences:** `cargo deny --locked check` passes with the existing allow-list: the licences of the new crates are `MIT` or `Unlicense OR MIT`, so the MIT arm is used and `deny.toml` is unchanged.
- **MSRV:** `cargo +1.97 check --workspace --all-targets --all-features --locked` passes (`csv` declares 1.73).
- **Desk:** no new npm dependency.

### 16. Fixtures (M9.4): no new package

Owner decision 10 of the M9 plan, 2026-10-30 ([ADR 0022](../adr/0022-fixtures.md)). **No dependency is added and the lockfile does not change.** The checksum of a fixture record is SHA-256 of its canonical JSON (the compact form with sorted keys), computed with `sha2`, which `forgebase` has depended on directly since M9.2 (the webhook signature); the JSON is read with `serde_json`, already a dependency of every crate. The canonical text is produced entirely by a small function in `forgebase`: sorted keys, its own string escapes, integers by `Display`, floats from Rust's `{:e}` digits in a fixed form. It depends neither on `serde_json`'s features (`preserve_order`, `arbitrary_precision`, `float_roundtrip`; open question 11) nor on its version (its float formatter changed from `ryu` to `zmij`). The parser is still `serde_json`'s, so numbers are **restricted at startup** (no negative zero, no integer outside `i64`/`u64`, floats with at most 15 significant digits and a decimal exponent within ±15, below 2^63): if two builds both accept a number its canonical text is the same, and a number one build accepts may be a startup error in another (fail-closed, never a silent overwrite). `forgebase-db` does not need `sha2`: it gets the checksum as text.

## Recommended baseline

Rows or parts marked (Accepted) are owner-accepted directions (ADRs 0002, 0004, 0005); everything else is Proposed.

| Concern | Recommendation | Main alternative |
|---|---|---|
| HTTP | axum (+ tower-http as needed) (Accepted); admitted in M3.3 without tower-http | actix-web |
| Runtime | tokio (explicit features) (Accepted) | none realistic (ecosystem tie-in accepted) |
| CLI | clap (derive) (Accepted 2026-10-28) | lexopt |
| DB | tokio-postgres + deadpool-postgres + in-house SQL/DDL builder (Accepted; confirmed by the M1 spike, see the spike result subsection) | sqlx (macros off); sea-query as builder |
| Framework migrations | custom embedded runner (Accepted with the DB direction) | sqlx migrate (if sqlx) |
| Serialization / validation | serde + serde_json; runtime metadata validator | garde for fixed structs |
| Metadata file parsing | toml + serde (parse only, in `forgebase-core`) (format Accepted 2026-10-28) | JSON |
| Decimal, date/time | `rust_decimal`, `time` (Accepted 2026-10-28) | chrono, jiff |
| Errors / logging | thiserror, anyhow, tracing | snafu, eyre, log + env_logger (not evaluated) |
| Auth | argon2 + opaque DB-backed sessions | tower-sessions with own store |
| Jobs | custom Postgres SKIP LOCKED queue | apalis (if sqlx, after maturing) |
| Config | toml + serde + env overlay | figment |
| TLS | none in app, TLS terminated at a reverse proxy (Accepted, ADR 0005); rustls+ring for Postgres through `tokio-postgres-rustls` (Accepted 2026-10-31, [ADR 0018](../adr/0018-postgres-tls.md), not behind a feature) and for outgoing webhooks through `hyper-rustls` (Accepted, [ADR 0020](../adr/0020-outgoing-webhooks.md), [section 14](#14-outgoing-webhooks-m92)) | native-tls |
| Outgoing HTTP | `hyper-util` legacy client + `hyper-rustls` (ring), for webhooks only (Accepted, owner decision 6 of the M9 plan) | reqwest (pulls aws-lc-rs), ureq |
| Frontend | React + TypeScript (Accepted, ADR 0002) + Vite + Vitest, no UI, router or data library (Accepted 2026-10-29, M4.1) | Vue/Svelte/Solid, htmx (not evaluated) |

Approximate core-stack weight: 136 packages (A) vs 179 (B); excludes TLS, sessions and sha2.

## License compatibility with MIT

From `cargo metadata` on stacks A and B (all targets), `Verified (local measurement)`:

- Dominant: `MIT OR Apache-2.0` and variants. Fine.
- Permissive, list in `deny.toml`: `Unlicense OR MIT` (choose MIT), `Zlib`, `Apache-2.0 OR BSL-1.0`, `MIT AND BSD-3-Clause` (matchit, axum's router; keep both notices), `(MIT OR Apache-2.0) AND Unicode-3.0`, `Unicode-3.0` alone (18 ICU4X crates, only in the sqlx stack; needs attribution), `BSD-3-Clause` (argh, if chosen).
- Copyleft seen: only `r-efi` (`MIT OR Apache-2.0 OR LGPL-2.1-or-later`), an OR expression so MIT applies. No MPL, GPL or AGPL-only crates appeared.
- Missing license metadata: apalis-postgres 1.0.0.
- ring 0.17.14: field `Apache-2.0 AND ISC`. Older releases carried OpenSSL/BoringSSL-derived text and other projects needed `clarify` entries (Source-indexed, https://gitlab.torproject.org/tpo/core/arti/-/issues/493 and https://apache.googlesource.com/opendal/+/HEAD/deny.toml). Whether 0.17.14's LICENSE file still has such text is Unverified; inspect before release. The "ring is unmaintained" advisory (RUSTSEC-2025-0007) was withdrawn (Source-indexed, https://rustsec.org/advisories/RUSTSEC-2025-0007).
- aws-lc-rs 1.18.2: `ISC AND (Apache-2.0 OR ISC)`; its C library likely carries OpenSSL-derived notices (Unverified).

Proposed `deny.toml` allow-list is in [maintenance-policy](maintenance-policy.md#cargo-deny).

## Container images

The Dockerfile and `deploy/compose.yaml` use base images. They are runtime and build images, not crate or npm dependencies, and they ship nothing into the Forgebase binary. Each is pinned by its multi-arch (amd64 and arm64) index digest and updated like the toolchain ([maintenance policy](maintenance-policy.md#container-images)). Licences are those of the images' contents; the images themselves are not redistributed by this repository.

| Image | Use | Licence (of the software; verify per release) |
|---|---|---|
| `debian:bookworm-slim` | not used in the final image; listed because `postgres` and `rust` are Debian based | various free-software licences (Debian main) |
| `rust:1.99.1-slim-bookworm` | build stage, the toolchain of `rust-toolchain.toml` | MIT OR Apache-2.0 (Rust), Debian licences |
| `node:22-bookworm-slim` | build stage of the desk | MIT (Node.js) with bundled components under their own permissive licences |
| `postgres:16-bookworm` | runtime base (the PostgreSQL 16 client tools `pg_dump` and `pg_restore`) and the database in `deploy/compose.yaml` | PostgreSQL Licence (permissive), Debian licences |
| `caddy:2-alpine` | HTTPS reverse proxy in `deploy/compose.yaml` | Apache-2.0 (Caddy), Alpine licences |

## TLS note

- **HTTP:** terminate TLS at a reverse proxy; the app then needs no TLS crate.
- **Postgres:** optional on private networks, required for most managed databases.
  - rustls 0.23.46: pure Rust, no system OpenSSL. Default provider aws-lc-rs needs a C compiler and CMake (NASM on Windows); the `ring` provider avoids CMake. sqlx `tls-rustls` currently resolves to ring (verified locally).
  - native-tls 0.2.18: uses system OpenSSL on Linux; adds a system dependency and version drift.
- **Decision (2026-10-31, M9.0):** reverse proxy for HTTPS (Accepted, [ADR 0005](../adr/0005-http-stack.md)); Postgres TLS is **Accepted** with rustls and `ring` through `tokio-postgres-rustls` 0.14.0 (third-party, MIT), not behind a cargo feature, with the operating system's roots (`rustls-native-certs`) and no `webpki-roots` ([ADR 0018](../adr/0018-postgres-tls.md), [section 13](#13-tls-to-postgresql-m90)). This replaces the earlier recommendation, which was Proposed.

## Open questions

1. ~~DB layer: is sqlx convenience worth +43 packages, MSRV 1.94 and the governance transition?~~ Resolved by the M1 spike (2026-10-28): no; ADR 0004 direction confirmed.
2. tokio-postgres bus factor: contributor distribution and PR latency.
3. sqlx under transact-rs: maintainer list and post-move cadence.
4. ring 0.17.14 license file: need for a `deny.toml` clarify entry.
5. refinery: actual 0.10.0 release date and activity.
6. figment: upstream activity.
7. apalis-postgres: missing license metadata and maturity.
8. axum: any announced 0.9 plans; tokio's current LTS line.
9. ~~argon2 0.6: API changes, audit status, current OWASP Argon2id parameters.~~ Partly resolved in M3.1: admitted at 0.6.0 (features `alloc`, `password-hash`), parameters m = 19456 KiB, t = 2, p = 1 (owner decision, ADR 0010). Audit status and a re-check of the current OWASP numbers stay Unverified.
10. ~~Decimal and date/time types~~ Resolved (Accepted, owner decision 2026-10-28): `rust_decimal` and `time`, see the section in 4. Database access.
11. serde_json `preserve_order` / `arbitrary_precision` workspace-wide implications.
12. ~~Session token hashing: `sha2` weight and license; cookie handling via `cookie`/`axum-extra` vs hand-written.~~ Resolved: hashing in PostgreSQL (M3.1), cookie hand-written (M3.3, see the admission notes).
13. MSRV-aware resolver (resolver "3") behavior with the pinned toolchain.
14. ~~React license confirmation; frontend audit tooling.~~ Resolved in M4.1: React 19.3.1 is MIT; `npm audit` and the license allow-list run in CI (see section 12). Still open: bus factor and release cadence of the Vite and Vitest toolchain, and the move to TypeScript 7 once `typescript-eslint` supports it.
15. Re-run every `cargo info` check at decision time.
