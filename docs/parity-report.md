# Parity report: the Go port against the Rust implementation

This is the report that the owner's plan asked for at the end of the Go port (G4, [owner decisions](owner-decisions.md#go-port-2026-11-06-setuju-g1g8-information-session), [ADR 0026](adr/0026-go-implementation.md#consequences)). It is the gate for one decision that stays with the owner: whether to archive the frozen Rust repository, <https://github.com/tvstore/Forgebase> (G6).

- **Compared:** this repository at `main` after P7.3 (commit `2626ee2`, merge of pull request #15) with the Rust repository at `451dc28` (merge of its pull request #51, M10.4).
- **Date:** 2026-11-07.
- **Rule of the report:** a statement has evidence, and what could not be verified is said with its reason (section 7). Nothing in the Rust repository was changed, and Cargo was not run: the environment's permission policy refused it, and the report does not work around that.

## Summary

| Area | Result | Evidence |
|---|---|---|
| Tests | Every one of the 1287 Rust tests and 12 scenarios of two test targets without a harness is accounted for: 1297 mapped to Go tests, 2 not ported with a reason, 0 unmatched. All Go tests pass with `go test -race`. | [section 1](#1-test-mapping), [appendix](parity/test-mapping.md) |
| Desk | Byte-identical to the Rust desk but for one import line. 320 Vitest tests pass; the Playwright suite passes against the Go server locally and in CI. | [section 2](#2-desk) |
| Same database | **Not verified with the Rust binary** (Cargo cannot run here). The framework migrations are byte-identical, and a schema built from the Rust SQL files is identical to the one the Go `migrate` builds. | [section 3](#3-same-database-check) |
| Build and CI times | Cold `go build ./...` 20.7 s; one pull request run of CI about 8 minutes end to end. No comparable Rust measurement exists in this environment. | [section 4](#4-build-and-ci-times) |
| Differences | 33 documented differences. Four need or await an owner decision. | [section 5](#5-differences-from-rust) |
| Dependencies | Four direct modules, all on the G2 list; the decimal library of G2 was not needed. | [section 6](#6-dependencies) |

## 1. Test mapping

**What is counted.** Every function that follows a line `#[test]` or `#[tokio::test]` in a `.rs` file of the Rust repository: `crates/*/src` (unit tests), `crates/*/tests` (integration tests), the tests of the app template `crates/forgebase/templates/app/tests`, and `examples/*/tests`. That is 1287 tests. Two integration tests, `crates/forgebase/tests/app_binary.rs` and `site_apps.rs`, have `harness = false` in `Cargo.toml`: each has its own `main` that runs scenario functions and has no `#[test]`. Their 12 scenario functions (4 in `app_binary`, 8 in `site_apps`) are counted next to the tests, so the units are 1299. Not counted: the code blocks in doc comments (21: 18 `ignore`, 2 compiled rustdoc examples, 1 `toml`).

**How a Go test is found.** The porting sessions wrote a comment `// Rust: file.rs::test` (or `file.rs::tests::test` for a unit test, or a bare name after the file in the same comment) on each Go test that ports a Rust test. `.github/ci/test-mapping.py` reads those comments in the `_test.go` files, attaches each to the function it belongs to, and tells two Rust files of the same name and test name apart by the crate named in the comment, by `tests::` and by the Go package (this happens for four Rust tests). A comment `Not ported: <reason>` records a Rust test with no Go counterpart. A short notes file, `.github/ci/test-mapping-notes.txt`, holds what comments cannot say (the four tests of the app template, and one helper test).

**Why a script in the repository.** It is about 300 lines of Python, standard library only, and has a unit test (`.github/ci/test_test_mapping.py`, seven tests), like the other scripts in `.github/ci/`. It needs a checkout of the Rust repository, which CI does not have, so CI runs only its unit test and the generated appendix is a snapshot: regenerate it with `python3 .github/ci/test-mapping.py RUST_CHECKOUT --out docs/parity/test-mapping.md`.

**Totals.**

| | Units |
|---|---|
| Rust tests | 1287 |
| Scenarios of the two targets without a harness | 12 |
| **Mapped to at least one Go test** | **1297** |
| Not ported, with the reason | 2 |
| **Unmatched** | **0** |

| Rust crate | Mapped | Not ported | Unmatched |
|---|---|---|---|
| `forgebase-core` (unit and integration tests) | 176 | 1 | 0 |
| `forgebase-db` | 540 | 0 | 0 |
| `forgebase-http` | 179 | 0 | 0 |
| `forgebase` (tests, scenarios and the app template) | 337 | 1 | 0 |
| `examples/sales` and `examples/sales_extension` | 65 | 0 | 0 |

The Go suite has 1527 top-level test functions, of which 1309 are named by a Rust test (one Go test can port several, and several Go tests can share one Rust test, for example a unit test and a test through the real binary); the other 218 are additions for behaviour that only Go has (the CSV reader, the decimal arithmetic, the checksum vectors, the name rules of `new-app`, the router) and carry a `Not in Rust:` note where it matters.

**Not ported, with the reason.**

| Rust test | Reason |
|---|---|
| `forgebase-core/src/meta.rs::registry_debug_shows_hook_counts` | It tests the Rust `Debug` text of `Registry`, which Go has no counterpart for (`%v` of a struct with unexported fields). The hook counts themselves are covered by `TestHooksAreKeptInRegistrationOrder`. |
| `forgebase/tests/common/mod.rs::the_independent_hmac_matches_a_known_vector` | It checks a hand-written HMAC-SHA256 that only the Rust test helper has (RFC 4231 test case 2). The Go tests use `crypto/hmac`, and `TestTheSignatureHasTheDocumentedForm` pins one signature computed independently with Python. |

**Unmatched (0).** The first version of this report had five unmatched tests, all in `forgebase-http/src/json.rs`, the output half of the module (a document written as JSON). They were covered in Go only inside larger tests, and were then ported as unit tests in `internal/httpapi/json_test.go` (`TestOutputMapping`, `TestTableOutputIsAnArrayOfRowObjectsInOrder`, `TestExtremeDatetimesNeverPanic`, `TestDocstatusAndAmendedFromAreOutputForSubmittableDoctypesOnly`, `TestWorkflowStateIsOutputOnlyWhenTheDocumentHasOne`), with the `i64` minimum and the round trip through the input parser, a row of nulls and an empty table, and the instant `9999-12-31T23:00:00-05:00`. They passed at once: the port of the code needed no change.

**What a mapping proves.** That a Go test exists, has the Rust test's name or says it ports it, and passes. It was not re-checked assertion by assertion here: the porting sessions and their reviews did that per phase. The check made for this report is mechanical: every Go function named in the appendix exists, every Rust test is accounted for, and the whole Go suite passes ([section 4](#4-build-and-ci-times) has the run).

The complete list, one table per Rust file, is generated in [parity/test-mapping.md](parity/test-mapping.md).

## 2. Desk

**Identical to the Rust desk.** The desk is not rewritten and not restyled (ADR 0026). `desk/` has 107 files tracked by git, the same list as `desk/` of the Rust repository, and a byte comparison of every file finds one difference, the one documented in the [changelog](../CHANGELOG.md): `desk/src/icons.test.ts` imports the icon list from a Go path.

```
-import list from '../../crates/forgebase-core/src/doctype_icons.txt?raw';
+import list from '../../internal/core/doctype_icons.txt?raw';
```

`internal/core/doctype_icons.txt` is itself byte-identical to the Rust file (`cmp`). `package-lock.json` is identical, so the tests ran with the same dependency versions.

**Vitest, lint and types** (`desk/`, `npm ci` then the scripts of `package.json`, Node 22.23.4):

| Command | Result |
|---|---|
| `npm test` (`vitest run`) | 26 test files and 320 tests passed in 1.68 s |
| `npm run typecheck` (`tsc --noEmit`) | no errors |
| `npm run lint` (`eslint .`) | no findings |

**Playwright, the whole suite, against the Go server.** The database was seeded by `desk/e2e/seed.sh`, unchanged (the Go binary and the directory come from `FORGEBASE_BIN` and `E2E_DIR`), the server was `examples/e2e_app serve` (the Go counterpart of the Rust example `e2e_app`: the stock commands plus the app that defines the report "Sales by customer") with the desk built by `npm run build`, and the browser was the Chromium of this machine (`PLAYWRIGHT_CHROMIUM_EXECUTABLE`; `playwright install` was not run). Result: **26 passed, 4 skipped, 0 failed** in 1.2 minutes. The four skipped are the tests of `screenshots.spec.ts`, which skip themselves unless `SCREENSHOTS=1`, as in Rust. The 30 tests are those of `restrict.spec.ts` (6), `screenshots.spec.ts` (4), `smoke.spec.ts` (16) and `transfer.spec.ts` (4).

**CI evidence.** CI runs on pull requests and on a weekly schedule, not on a push to `main`, so there is no CI run of `main` itself after the P7.3 merge. The last change before this report is pull request #15 (P7.3); its final run while ready for review, run [42404482794](https://github.com/tvstore/Forgebase-Go/actions/runs/42404482794) on head `4dfd714`, had every job green, including `desk` (the same Vitest, lint, typecheck, build, licence and audit steps) and `e2e` (the full `npx playwright test`, 47 s of tests, 2 min 8 s for the job).

## 3. Same-database check

**What the plan asked for.** Run the Rust binary: `migrate` and `serve` on a database, then the Go binary: `serve` (and `migrate`) on the same database, and the other way round, to show that a database of one implementation is accepted by the other.

**What could not be done.** Cargo cannot run in this environment (its permission policy refused it, and the Dockerfile of the Rust repository was not built to get around that), and there is no Rust release binary. So **no Rust binary ever touched a database in this report, and the plan's check is not complete.** The evidence below is the strongest available without Rust; the last paragraph says what would complete it.

**a. The framework migrations are byte-identical.** The 13 files of `internal/db/migrations/` and of `crates/forgebase-db/migrations/` compare equal with `cmp`. The version and the name of every migration in `migrate.rs` (read from the Rust source) equal the Go `Migrations`. The SHA-256 of each file is also pinned in `internal/db/migrate_test.go` (`expectedChecksums`, with `TestMigrationFilesAreByteIdenticalToRust` and `TestRecordedChecksumsAreTheRustChecksums`); the pinned values equal `sha256sum` of the Rust files computed for this report:

| Version | Name | SHA-256 (the checksum stored in `fb_migration`) |
|---|---|---|
| 1 | init | `026b1eff9bdcc03ac6bae10f6ee1375151a8ea6fa9c5503cf579a33ec73435d3` |
| 2 | series | `b76f24860c8e99ce350c7fb0216b210beb2bb187651be274929f97188ac0e232` |
| 3 | auth | `93d730328241373f4607f2a96537d396fbe35506487a29c952a05f86e2723b1b` |
| 4 | apps | `474103338ae7097aefead5bc0b82c5d0719707ff2ff56c961b579f5442a385a1` |
| 5 | jobs | `40c1075c0730e30c8c5c4f111a7e09150d811649e08013f289751f33deb4f3a3` |
| 6 | schedules | `bbfab1b47e90ec978185da803a0d7ba0e3fad43a40fef756069baa611df5329e` |
| 7 | finished_jobs_index | `28832e1f51ec460436202a03b56fe5d39a62a4d1c14aa118d8b2308d178439f6` |
| 8 | workflow_log | `53e8f97a0da2c42859c3fe6d72d5db09d1fb79510c37963afa2fb70e7f832b61` |
| 9 | saved_views | `653d6ddd6e770076a6a47820268f3548138a51ded8715edb3a4a310fd657db88` |
| 10 | webhooks | `33d72fc27b1dae069601b71a33e86865fb96bba039146da737d38394052702e8` |
| 11 | fixtures | `0edc024228bf72fe112a2aae1ed8f40350a354ca81133b3a0596c1532df3006b` |
| 12 | custom_fields | `28d28650605a210dceab465e2d49854c56235b1d5e5f7d01b6ef57c703274efe` |
| 13 | user_permissions | `cf9095eea0377d1ba93ef35de87263bf13a55f7bf06e3b39f1e0008a1a838193` |

**b. Schema equality (framework tables).** The Rust runner `migrate_framework` was read (`migrate.rs`) and reproduced by hand with `psql`: one transaction (`-1`), the bootstrap `CREATE TABLE IF NOT EXISTS fb_migration (...)`, then for each migration its Rust SQL file and `INSERT INTO fb_migration (version, name, checksum)` with the SHA-256 of the file (PostgreSQL computes the same value as `sha256sum` for a UTF-8 file; check c shows it accepts these rows). That gave database A. The Go binary, `forgebase migrate` on an empty database with an empty `--doctypes` directory, gave database B. Then `pg_dump --schema-only --no-owner --no-privileges` of both (PostgreSQL 16.15, 582 lines, 19 tables, 50 relations including indexes and sequences):

```
diff schema_a.sql schema_b.sql
5c5    < \restrict ux6xwcdaDSR...        > \restrict CjvPOKhlJL6...
581c581 < \unrestrict ux6xwcdaDSR...     > \unrestrict CjvPOKhlJL6...
```

The only differences are the two random `\restrict` token lines that `pg_dump` 16.15 writes into every dump; with those lines removed the dumps are **identical**. The rows of `fb_migration` (version, name, checksum) are identical too (same SHA-256 over the 13 rows). The limit of this check: the Go runner embeds the same files, so equality shows that the Go runner adds no DDL of its own and records the same rows; it does not show that the Rust binary does nothing else, which only running it would. (A search of `crates/*/src` found `CREATE` and `ALTER` statements only in the bootstrap of `migrate.rs`, in the DocType DDL of `plan.rs` and `sync.rs`, and in a comment or test of `ident.rs`; the framework tables are all in the SQL files.)

**Composed app (`examples/sales`): not feasible as a schema comparison.** The tables of the DocTypes are not in SQL files: the Rust sync generates their DDL (`plan.rs`), so a Rust-built schema for `examples/sales` cannot be made without running Rust. What was checked instead: the DocTypes, the fixture and the app migration of `examples/sales` are byte-identical to the Rust files (`cmp`); the Go `sales migrate` on an empty database applies the 13 framework migrations, 26 sync steps (6 DocTypes, one of them a child table, with their keys and indexes), the app migration and the fixture, prints `fixtures: sales: 1 inserted, 0 updated, 2 kept, 0 unchanged` and `reports: 1 checked`, and the app migration is recorded with the checksum `0b59a3249c79f045c5f1530da1d5c5c76d6b7d2b7538109824aa392d2f900017`, which is the `sha256sum` of the Rust file; a second run reports every part up to date and `fixtures: sales: 0 inserted, 0 updated, 0 kept, 3 unchanged`. The DDL itself is covered by the 29 unit tests of `plan.rs` (exact SQL strings) and the 38 tests of `schema_sync.rs`, all mapped in [section 1](#1-test-mapping) and passing.

**c. A Go `migrate` on database A (built from the Rust SQL files).** Output, exit code 0:

```
framework migrations: up to date
schema sync: up to date
schedules: changed 3
  add forgebase.purge_jobs (daily 03:00)
  add forgebase.purge_login_failures (every 60m)
  add forgebase.purge_sessions (every 60m)
```

So Go verified the checksum of all 13 rows (a wrong one is a refusal, `ErrChecksumMismatch`) and found nothing pending. The three `schedules` lines are data, not schema: the hand-made database A never ran the part of `migrate` that stores the built-in schedules, and the Go `migrate` on empty database B printed the same three lines. A second run on A, and on B, printed `framework migrations: up to date` and `schema sync: up to date` and nothing else.

**d. Fixture checksums.** `TestTheChecksumOfAFixtureRecordMatchesTheTestVectors` (`internal/httpapi/canonical_test.go`) pins 13 vectors (input, canonical text, SHA-256 computed with Python's `hashlib`: `1e21`, `0.1`, `-0.0`, a 30-digit integer, the escapes, the float edges). The first is the vector of the Rust test `the_checksum_is_a_known_sha256`; the canonical texts of the others were derived by hand from the rules of ADR 0022 and checked against Rust's `{:e}` digits, **not produced by running Rust**. So the evidence is: one value pinned by a Rust test, the rest are the rules applied independently. A database fixtured by Rust is therefore expected, not shown, to stay unchanged under a Go `migrate`.

**e. Argon2 strings.** `internal/db/auth_unit_test.go` shows that Go verifies strings written by other implementations with the parameters inside the string (m, t, p and the output length): the example of the Argon2 authors (`$argon2i$v=19$m=65536,t=2,p=4$c29tZXNhbHQ$...`), and strings written by the reference C implementation through argon2-cffi 25.1, one with the Forgebase parameters (`$argon2id$v=19$m=19456,t=2,p=1$...`) and one with the library's defaults. It also shows that the string Go writes is the plain construction of the PHC format (`TestOurHashIsTheStringTheRustCodeWouldWrite`). The comment of the tests says argon2-cffi writes what the Rust `argon2` and `password-hash` crates write and read; that is a claim about the crates, **not verified here**, and no string written by the Rust binary was available.

**What would complete the check.** A Rust release binary of the final Rust commit (`451dc28`), or permission to run `cargo build --locked` for it. Then: Rust `migrate`, `user create` and `serve` on a database X; Go `serve` on X (log in, list, create a document) and Go `migrate` on X (expected: `up to date`, fixtures `unchanged`); the same the other way round on a database Y that Go migrated first; a `pg_dump --schema-only` comparison for `examples/sales` built by each binary; and a password hash written by each binary verified by the other. Until that is run, parity of the database is evidenced by identical migration files, identical framework schema and recorded rows, the pinned vectors and the ported tests, and is **not proved**.

## 4. Build and CI times

**Local measurements** (this machine: 4 vCPU, 16 GB, Linux 6.18, Go 1.27.3, PostgreSQL 16.15 on the same machine, module cache populated, run on 2026-11-07; one measurement each, so the numbers are indications, not benchmarks):

| Step | Wall time |
|---|---|
| `go build ./...`, cold (`GOCACHE` an empty temporary directory) | 20.7 s |
| `go build ./...`, warm (nothing changed) | 0.83 s |
| Rebuild of `cmd/forgebase` after adding an exported variable to `internal/core` (recompiles the packages above it) | 3.2 s |
| the same after a change in `internal/db` | 2.9 s |
| the same after a change in `internal/app` | 1.5 s |
| `go test -race -count=1 ./...` (root module, all tests, PostgreSQL local; the TLS tests that need `pg-tls.sh` skip with a notice) | 290 s (the longest packages: `internal/httpapi` 265 s, `internal/db` 190 s, the root package 139 s, `internal/app` 84 s, which run in parallel) |
| `go test -race -count=1 ./...` in `examples/sales` / `examples/sales_extension` | 31.7 s / 14.3 s |
| `npm test` (Vitest, 320 tests) | 1.7 s |
| `npm run build` (the desk) | 0.4 s |
| `npx playwright test` (26 tests, 4 skipped) | 72 s |

The reason for the port was the cost of a rebuild when a client asks for a quick change (ADR 0026). The measurements above are that cost: a change in a package of the framework is rebuilt and linked in 1.5 to 3.2 seconds, and a cold build of everything takes 21 seconds.

**CI durations.** The CI runs on pull requests and weekly, not on a push to `main`, so there are no runs of `main` to average. The figures are the final ready-for-review run of pull request #15, the last change before this report: run [42404482794](https://github.com/tvstore/Forgebase-Go/actions/runs/42404482794), head `4dfd714`, all jobs green, from the GitHub API (earlier pull request runs had similar times):

| Job | Wall time |
|---|---|
| `check` (gofmt, vet, staticcheck, govulncheck, `go test -race` with PostgreSQL 16 and TLS, builds, the example modules) | 7 min 50 s (the `go test -race` step 5 min 25 s) |
| `e2e` (the full Playwright suite) | 2 min 08 s (tests 47 s) |
| `docker` (build and smoke test) | 1 min 30 s (image build 61 s) |
| `sites` | 1 min 15 s |
| `desk` | 1 min 04 s |
| `app-template` | 48 s |
| `licenses` | 18 s |
| `docs`, `changes` | about 5 s each |
| `ci-result`, end to end (19:05:19 to 19:13:30) | about 8 min 11 s |

The jobs add up to about 15 job-minutes; the run takes 8 minutes because the jobs run in parallel.

**Rust numbers.** A search of the documents of the Rust repository (the README, CONTRIBUTING, the guides, the blueprint and the owner decisions) finds two figures: (1) the CI of 2026-10-30, measured before the cache was added (C-1 in the owner's CI cost decision, recorded in [owner decisions](owner-decisions.md#ci-cost-2026-10-30-setuju-c-1-sampai-c-5-information-session)): one run was 7 jobs and about 24 job-minutes (`check` 14.0, `e2e` 3.2, `deny` 2.3, `app-template` 2.1, `sites` 1.4, `msrv` 0.8, `desk` 0.5); (2) its deploy guide says the first Docker build takes about 10 to 20 minutes on a small VM because it compiles every dependency (the Go [deploy guide](guide/deploy.md) says about one minute on a development machine with a warm network and 5 to 10 minutes on a small VM). No Rust build or test time could be measured here, because Cargo cannot run, so **there is no like-for-like comparison**: the Rust CI figures are older, from a different set of jobs, without the cache, and the Go run has jobs that the Rust list of that day lacks (`docker`, `licenses`). What the figures do show is the order of magnitude behind the owner's reason: the slowest Rust job was 14 minutes, the slowest Go job is under 8.

## 5. Differences from Rust

Every difference that the phases recorded, in one table. Sources: the "Differences from Rust" lists of the [changelog](../CHANGELOG.md) (P1.1 to P7.3), [ADR 0026](adr/0026-go-implementation.md), [ADR 0027](adr/0027-decimal-and-toml.md), [ADR 0028](adr/0028-go-public-api-of-the-app-model.md), the [dependency evaluation](blueprint/dependency-evaluation.md#go-dependencies-adr-0026) and the [CLI guide](guide/cli.md#parity-with-the-rust-command-line). The deviations that the working sessions reported to the owner during the port are the ones in those documents; no other deviation record was available for this report.

**Observable** says where a user can see the difference: **API** (the HTTP API), **CLI** (the command line, its output and startup messages), **DB** (the database schema or stored data), or **none** (an internal or Go-API difference only). **Owner** is **decide** where the difference needs or awaits an owner decision (the four are also listed in [section 7](#7-what-could-not-be-verified-and-decisions-for-the-owner)), **inform** where it is a deliberate or fail-closed deviation that the owner can overrule, and **-** otherwise.

| # | Difference | Reason | Observable | Documented | Owner |
|---|---|---|---|---|---|
| 1 | The text of a TOML parse error in a config, DocType or overlay file is the Go library's; when a file has errors of several kinds, the one reported first may differ from serde's order. The same files are refused. | `BurntSushi/toml`; it matches keys case-insensitively, so a strict decoder replaces struct decoding | CLI (startup and `migrate` messages) | ADR 0027; CLI guide; changelog P1.1, P2.2 | - |
| 2 | JSON errors of the core (a field given as JSON) name the key in serde's wording without the line excerpt. The syntax errors of `--data` and of HTTP bodies have serde_json's wording and position, but a float with more than 15 significant digits in the "not an integer" message may differ in its last digit. A field must be decoded with `UseNumber` or Go integers. | serde_json's float printing is not `strconv`'s | API and CLI (error text) | changelog P1.1, P2.2; CLI guide | - |
| 3 | TLS error texts are those of `crypto/tls` (`x509: certificate signed by unknown authority` where rustls says `UnknownIssuer`), in connection errors and in the error of a failed webhook delivery. | `crypto/tls` replaces rustls | CLI, DB (the text of a failed delivery) | changelog P5.2, P7.1 | - |
| 4 | The `sslrootcert` reader matches rustls-pki-types for ordinary PEM files only. For unusual files Go refuses unpadded base64, spaces in a body line and bad base64 in an unknown section, skips a BEGIN line without exactly five dashes, accepts markers with leading whitespace, and refuses an END line with trailing text, a nested BEGIN and lone `\r` line ends, where Rust does the opposite. The error details after the `tls.rs` prefix are Go's. | The PEM parser of the standard library is stricter in other places | CLI (a connection with an unusual root file) | changelog P5.2 | - |
| 5 | A set `PGSERVICE` makes every connection fail (also `site backup` and `site restore`); Rust ignores it. | pgx would read the service file and cannot be told not to; it fails closed | CLI (an environment variable) | changelog P1.2, P5.2; dependency evaluation | inform |
| 6 | `hostaddr`, `tcp_user_timeout` above 0 and `load_balance_hosts=random` in a database URL are refused. Rust supports them; Forgebase never used them. | pgx has no equivalent | CLI (a URL option) | dependency evaluation | - |
| 7 | URL edge cases: an empty host or a host list with an empty entry fails before any host is tried; `keepalives_retries=0` and an oversized `keepalives_idle` are handled differently; a URL with no password against a server that asks for one fails with the server's `28P01` instead of "password missing"; connection errors read "failed to connect to ..." instead of "error connecting to server". | pgx against tokio-postgres | CLI | dependency evaluation | - |
| 8 | `pgxpool` pings a connection that was idle for more than a second when it is taken; deadpool only checks that it is not closed. | pgx behaviour | none | dependency evaluation | - |
| 9 | A request that `net/http` refuses before the handler gets a plain-text `400`, not the JSON envelope: no `Host`, two `Host` headers, a malformed `Host`, a target its parser refuses (for example `/api/v1/resource/%zz`, where Rust answers the envelope, 401 or 404). `OPTIONS *` goes through the handler. | The standard server rejects these first | API | changelog P3.2 | - |
| 10 | Response header names are in canonical case (`Set-Cookie`); Rust writes lower case. `Content-Length` and `Date` come from `net/http`. | HTTP header names are case-insensitive | API (wire form only) | changelog P3.2 | - |
| 11 | The server limits reading the request header to 30 seconds (counted from the accept or the start of the next request on a kept-alive connection). Rust has no such timer. | A deliberate deviation (protection against slow clients) | API (a client that sends its header slowly) | changelog P3.2 | inform |
| 12 | A panic in an HTTP handler is recovered and answered with the generic 500, or the connection is dropped when the answer has begun. | The Rust handlers do not panic | API | changelog P3.2 | - |
| 13 | The version check of `serve` ends with the context of `serve` (Rust: when the last clone of the state is dropped). The holder's warnings go to the command's standard error. A failed rollback of the read-only transaction of a registry rebuild is ignored and the registry is still swapped (Rust returns that error and keeps the old registry). | Go cannot see the last clone; the rollback of a read-only transaction has no effect | none | changelog P7.3 | - |
| 14 | DocTypes, migrations, fixtures and overlays are an embedded directory (`fs.FS`), not an explicit list: every `.toml`, `.sql` or `.json` file at any depth in lexical order, hidden names skipped. A `.sql` file must be named `NNNN_name.sql`. The order of overlay files decides only the order of the fields they add and of the problems. | `//go:embed` replaces `include_str!` and the macros | none (startup errors only) | ADR 0028; changelog P4, P7.3 | - |
| 15 | **Fixture file order.** The Rust list fixed the order in which fixture files, and so DocTypes, are applied; Go applies the files in lexical order of their paths. It matters when a record links to a record of another file of the same app: such an app must name its files `1_groups.json`, `2_lists.json`. Within a file and between apps the order is Rust's. | An embedded directory has no list | DB (which record exists when a link is checked) | ADR 0028 (Fixtures); changelog P7.2 | **decide** |
| 16 | **Public API as aliases.** `App`, `Binary`, `DocTypeHooks`, `HookContext` and the other types of the table in ADR 0028 are aliases of internal types. `go doc` and pkg.go.dev do not list their methods, and every exported member of an aliased type is public API. Wrapper types (about sixty lines, no change to any call in an app) would list the methods. | No wrapper code; the ADR leaves the wrapper to the owner | none (documentation and API surface) | ADR 0028 (the rule for aliased types, consequences, alternatives) | **decide** |
| 17 | **Decimal arithmetic.** `Add`, `Mul` and `Rescale` were added to `internal/decimal` (Rust apps use `rust_decimal`'s operators). They report whether the result fits and never scale it to fit, which is stricter than `rust_decimal` (it rounds a product to fit 96 bits). `Rescale` to fewer digits rounds half away from zero. This amends ADR 0027, which said the type has no arithmetic. | The `sales` example computes with decimals; stricter is safer than silent rounding | none for the framework; an app hook that computes at the 28-digit limit | ADR 0028; changelog P4 | **decide** |
| 18 | Go spelling of the API where Go cannot express the Rust one: handlers return `(any, error)` or take a `context.Context`; the getters of `StoredDocument`, `JobError` and the rest are exported fields; `Documents.Get` returns `nil, nil` for a missing document; a `Document` is copied before the hooks see it; `RunArgs` takes the arguments without the program name; the testing helpers call `t.Fatalf` and have no `cleanup()`; `AssertDocTypesListed` reads the directory recursively; a schedule hour and minute are `int` and out of range is a startup error; a `nil` handler, report, `Param` or `Column` is a startup error that Rust has no case for. | The language | none | ADR 0028; changelog P2.1, P4, P5.1, P6 | - |
| 19 | Startup messages that name a function name the Go one: the hint of the app-rule message and of the overlay message about a missing dependency (`add DependsOn("crm"), registered before ...`), the `EveryMinutes` message, the help and name errors of `new-app`. | The text must be actionable in Go | CLI | changelog P4, P5.1, P7.3 | - |
| 20 | `forgebase new-app` writes a Go module (the package is imported as `apppkg`, files `app.go` and `app_test.go`, a `replace` to the checkout, the checkout's `go.sum`). The name rules refuse Go keywords, predeclared identifiers, top-level packages of the standard library and names the go command treats specially (`std`, `all`, `mod`, `testdata`, Windows device names and others) in place of the Rust keywords and reserved crate names. | The language | CLI | ADR 0028; changelog P4 | - |
| 21 | The container image has the three stages of the Rust one, with Go in place of Rust; every base image is pinned by digest (newer digests than Rust's); the build fails if the image's Go differs from the `toolchain` line. The build argument `BIN` (a workspace crate) is `APP_DIR` and `APP_PKG` (a Go module directory and its main package), compose reads `FORGEBASE_APP_DIR` and `FORGEBASE_APP_PKG`. | The examples are Go modules of their own | none (build and operations) | changelog P5.3 | - |
| 22 | `examples/sales`, `examples/sales_extension` and `examples/sites` are the Rust examples with their DocTypes, fixtures, overlays and migrations byte-identical. Each Go example is a module of its own, so the compiler enforces the public-API boundary; `examples/e2e_app` is in the root module. The READMEs, the header comment of the report SQL and the comments of `examples/sites/ci.sh` differ (Go commands and paths). | The boundary of ADR 0012 needs a module of its own | none | ADR 0028 | - |
| 23 | A job handler is stopped cooperatively: Go cannot stop a goroutine, so on a timeout or an abort the context is cancelled, the transaction is never committed, the connection is closed and the failure is recorded. A handler that ignores its context keeps its goroutine until it returns. | The language | DB and operations (the job state is the same; a stuck handler lingers) | changelog P5.1 | - |
| 24 | A method route runs its handler with `context.WithoutCancel` instead of a task of its own; a panic is recovered in the call and its connection is closed. | The language | none | changelog P7.1 | - |
| 25 | The webhook URL parser is a port of the rules of the `http` crate's `Uri` for the cases that matter. A port above 65535 is refused (Rust falls back to the default port) and an IPv6 literal with a zone is looked up as a name. | Different parser | CLI, DB (a delivery that fails permanently) | changelog P7.1 | - |
| 26 | **Webhook delivery body.** It forwards `truncated` and not `unavailable`, as the Rust code does, although ADR 0020 says both. This is a difference inside the Rust repository (code against ADR) that the port reproduces. | A parity port reproduces the code; ADR 0026 sends a Rust behaviour that is itself wrong to the owner | API (the payload a receiver gets) | changelog P7.1; ADR 0020 | **decide** |
| 27 | A report's row limit uses the extended protocol with a row cap on a dedicated connection (pgx has no portal API); the export reads its pages with `DECLARE ... NO SCROLL CURSOR` and `FETCH 1000` in the simple protocol where Rust uses a portal; the CSV reader is written for the project to keep the `csv` crate's behaviour (CRLF inside a quoted cell, no failure on a stray quote, one error text). | pgx and `encoding/csv` differ from the Rust crates | none (the changelog records that nothing observable changes) | changelog P6, P7.2 | - |
| 28 | A stored Argon2 hash that the Go implementation cannot run (version `v=16`, more than 255 lanes) is not verified, so the login fails like a wrong password. Forgebase writes none of those. Argon2 runs in the calling goroutine (Rust: `spawn_blocking`). | `golang.org/x/crypto/argon2` | DB (a hash imported from elsewhere) | changelog P3.1 | - |
| 29 | Some database error texts are hand-written where tokio-postgres has its own (`ErrDecode`, a NULL or infinite timestamp, the bind error "cannot bind a ... value as ..."); validation makes the bind error unreachable in practice. `ErrUniqueViolation` and `ErrLinkedDocumentExists` keep no driver error, so a hidden field or DocType cannot leak through `errors.As`. | Different driver | CLI and API (only if reached) | changelog P2.1 | - |
| 30 | The tests of TLS are skipped locally with a notice when their variables are missing (Rust prints a notice and passes); with `CI=true` they fail. The host name test runs against a fake server that speaks the TLS start of the protocol, because `hostaddr` is not supported. | pgx; item 6 | none (tests) | changelog P5.2 | - |
| 31 | `ci-result` fails on a draft pull request, and the workflow also runs on `converted_to_draft`; in Rust `ci-result` passed on a draft. | Owner decision G8 | none (CI) | ADR 0026; owner decisions | - |
| 32 | Rust features without a Go counterpart (`#[non_exhaustive]`, exhaustive `match`, the `Debug` text) are replaced by tests and review; one Rust test is not ported for it (section 1). | The language | none | ADR 0026 (consequences) | - |
| 33 | The libraries that ADR 0026 replaces: `tokio-postgres` and `deadpool-postgres` by pgx, axum by `net/http` with a router written for the project, rustls by `crypto/tls`, clap by a parser that is a port of the rules of clap 4.6.7, `csv-core` by an own reader, the `argon2` crate by `x/crypto/argon2`, `rust_decimal` by `internal/decimal`, `serde_json` by `encoding/json` with a port of its parser for the messages. Every observable consequence is in the rows above. | G2 and G3 | see the rows | ADR 0026, 0027 | - |

**The four decisions.** Rows 15, 16, 17 and 26 need or await the owner: the order of fixture files (15), aliases or wrappers for the public API (16), the strictness of the decimal arithmetic (17), and what to do with the webhook body that disagrees with its ADR in the Rust code itself (26, to be settled in both repositories or in the document). The working sessions took the choices written in the table within G2 and G3 and recorded them as overrulable; none changes the HTTP API, the CLI or the database schema.

## 6. Dependencies

The final `go.mod` (module `github.com/tvstore/forgebase-go`, `go 1.26.0`, `toolchain go1.27.3`) against the list of G2 ([ADR 0026](adr/0026-go-implementation.md#dependencies-g2)). The licences are the output of `go run ./.github/ci/licensecheck` (the standard-library checker of the CI job `licenses`, allow-list MIT, Apache-2.0, BSD-2-Clause, BSD-3-Clause, ISC) over the build graph; `go mod verify` reports all modules verified and `go mod tidy -diff` reports nothing.

| G2 item | Module in `go.mod` | Version | Licence | Direct |
|---|---|---|---|---|
| PostgreSQL driver and pool | `github.com/jackc/pgx/v5` | v5.12.0 | MIT | yes |
| UUID values | `github.com/google/uuid` | v1.6.0 | BSD-3-Clause | yes |
| One TOML library | `github.com/BurntSushi/toml` | v1.6.0 | MIT | yes |
| Argon2 | `golang.org/x/crypto` (only `argon2` and `blake2b` are linked) | v0.58.0 | BSD-3-Clause | yes |
| A decimal library | **none**: an own `rust_decimal`-compatible type in `internal/decimal` ([ADR 0027](adr/0027-decimal-and-toml.md)) | - | part of this module | - |
| (transitive of pgx) | `github.com/jackc/pgpassfile` v1.0.0, `github.com/jackc/pgservicefile` v0.0.0-20240606120523-5a60cdf6a761, `github.com/jackc/puddle/v2` v2.2.3 | | MIT | no |
| (transitive of pgx, puddle, crypto) | `golang.org/x/sync` v0.24.0, `golang.org/x/sys` v0.49.0, `golang.org/x/text` v0.43.0 | | BSD-3-Clause | no |

Four direct modules, all named by G2; the decimal library that G2 allowed was not needed, so the port has one dependency fewer than the plan permitted. Nothing outside the G2 list is in the build graph, so no dependency needed a separate owner approval. HTTP, TLS, HMAC, SHA-256, CSV, JSON, `embed` and the command line are the standard library; there is no web framework, ORM or CLI framework. The CI tools (`staticcheck` v0.8.2, `govulncheck` v1.9.0) are installed at pinned versions and are not module dependencies. The rest of the module graph that `go list -m all` prints (testify and others) comes from the test files of pgx and is not in the build graph; `go.sum` lists it for `go mod tidy`. `govulncheck` reports GO-2026-5932 (the unmaintained `golang.org/x/crypto/openpgp`) in a required module that no code calls ([dependency evaluation](blueprint/dependency-evaluation.md#go-dependencies-adr-0026)); it ran green in the CI run of [section 4](#4-build-and-ci-times) and was not re-run here. The desk's npm dependencies are those of the Rust desk (`package-lock.json` is identical), checked by the job `desk`.

## 7. What could not be verified, and decisions for the owner

**Not verified, and why.**

| What | Why it could not be verified | What would verify it |
|---|---|---|
| A database shared by the Rust and the Go binary (Rust `migrate` and `serve`, then Go, and the reverse) | Cargo cannot run here (refused by the environment's permission policy) and there is no Rust binary | A Rust release binary of `451dc28` or permission to run `cargo build --locked`; the steps are at the end of [section 3](#3-same-database-check) |
| The schema of a composed app (`examples/sales`) built by Rust | The DocType tables come from Rust code (`plan.rs`), not from SQL files | The same Rust binary and a `pg_dump --schema-only` comparison |
| That the 1287 Rust tests pass | Not run: no Cargo. The count is a count of test functions, not a run | The Rust CI of its last pull request (not re-read here), or a run of `cargo test` |
| The Rust desk against the Rust server | Not run: no Cargo. The desk files are identical, and the suite passes against the Go server | The same Rust binary |
| A password hash and a fixture checksum written by the Rust binary | No Rust output available. The Go side is tested against the reference C implementation, the PHC rules and one checksum pinned by a Rust test | The same Rust binary |
| Assertion-by-assertion equality of every ported test | The mapping is by the porting sessions' comments and names; it was checked mechanically, not re-read test by test | A new independent review of a sample, if the owner wants one |
| Rust build and CI times for comparison | No measurement possible, and the Rust repository records only two old figures | The same Rust binary and a clean build |
| The TLS tests and `govulncheck` locally | The TLS tests need `.github/ci/pg-tls.sh` and skip with a notice without it; `govulncheck` and `staticcheck` are not installed here. Both ran green in CI (run 42404482794) | Running them locally |
| The `docker` and `sites` jobs locally | Docker is not used here; both were green in the CI run above | Running them |

**Decisions for the owner.**

1. **Archive the Rust repository (G6).** This report is its gate. The evidence above is that every behaviour the Rust tests describe has a passing Go test, the desk and its whole end-to-end suite pass on the Go server, the migration files and the framework schema are identical, and the dependency set is within G2. The same-database run with the Rust binary is the one thing missing. Archiving a GitHub repository makes it read-only, but it stays clonable and buildable, so that check can still be done after an archive; it needs only permission to build the Rust binary with Cargo, or a release binary. The decision stays with the owner; nothing here archives anything.
2. **The four open differences** of [section 5](#5-differences-from-rust): the order of fixture files (row 15), aliases or wrappers for the public API (16), the strictness of the decimal arithmetic (17), and the webhook body in which the Rust code disagrees with its ADR (26).
