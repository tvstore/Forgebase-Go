# Contributing

Workflow, approval gates, git safety and review rules are defined only in [AGENTS.md](AGENTS.md). Read it first.

## Prerequisites

- [rustup](https://rustup.rs/). The toolchain (1.99.1, with rustfmt and clippy) is selected automatically from `rust-toolchain.toml`.
- For the MSRV check: `rustup toolchain install 1.97 --profile minimal`.
- For the dependency check: `cargo install --locked cargo-deny --version 0.20.2`.
- For the desk (`desk/`): Node.js `^20.19.0 || >=22.12.0` (Vite 8) with npm. `desk/.npmrc` sets `ignore-scripts=true`, so `npm ci` runs no install scripts of dependencies.

## Local checks

Run the same commands as CI ([.github/workflows/ci.yml](.github/workflows/ci.yml)) before opening a PR:

```sh
cargo fmt --all --check
cargo clippy --workspace --all-targets --all-features --locked -- -D warnings
cargo test --workspace --all-features --locked         # needs FORGEBASE_TEST_DATABASE_URL, see below
cargo build --workspace --locked
RUSTDOCFLAGS="-D warnings" cargo doc --workspace --no-deps --all-features --locked
cargo +1.97 check --workspace --all-targets --all-features --locked   # MSRV
cargo deny --locked check                              # needs network for advisories
```

Two small scripts back the `changes` and `docs` jobs of CI. Both need only `python3` (standard library):

```sh
python3 .github/ci/test_classify_paths.py   # unit test of the path classifier (which CI jobs a change needs)
python3 .github/ci/linkcheck.py             # relative links and anchors in *.md, outside node_modules and target
git diff --name-only --no-renames origin/main...HEAD | python3 .github/ci/classify-paths.py --event pull_request   # what CI would run for your branch
```

The workspace includes the example apps in `examples/` (`sales`, `sales_extension`), so these commands also build, lint and test them. They may depend on the `forgebase` crate only, and a change to them must not need a change under `crates/`.

The desk has its own checks (the `desk` job of CI). Run them in `desk/` when you change it:

```sh
cd desk
npm ci
npm run lint
npm run typecheck
npm test
npm run build       # writes desk/dist
npm run licenses    # license allow-list over package-lock.json
npm audit --audit-level=high
```

The Playwright smoke test (CI job `e2e`) needs a real server, so it is not part of `npm test`. To run it locally you need a PostgreSQL 16 server, an **empty** scratch database (never one you care about: the seed script creates users and documents), the built `forgebase` binary and the built desk:

```sh
export FORGEBASE_DATABASE_URL=postgres://forgebase:forgebase@localhost:5432/fb_e2e   # an empty scratch database
cargo build -p forgebase --example e2e_app && (cd desk && npm ci && npm run build)
desk/e2e/seed.sh                                   # DocTypes in target/e2e/doctypes, users e2e_manager and e2e_clerk, one customer
FORGEBASE_DEV_INSECURE_COOKIES=1 FORGEBASE_DESK_DIR=desk/dist target/debug/examples/e2e_app serve --doctypes target/e2e/doctypes &
cd desk
npx playwright install chromium                    # downloads the Chromium of this Playwright version (CI does this too)
npx playwright test                                # E2E_BASE_URL (default http://localhost:8000), E2E_PASSWORD
```

To take screenshots of the desk (for a pull request that changes how it looks), run the screenshot script against the same server. It is skipped unless `SCREENSHOTS=1`, adds sample documents to the scratch database, and does not commit anything:

```sh
cd desk && SCREENSHOTS=1 SCREENSHOT_DIR=/tmp/desk-shots npx playwright test screenshots   # login, home, list and form at 1280, 800 and 375 px
```

If you already have a Chromium, skip the install and set `PLAYWRIGHT_CHROMIUM_EXECUTABLE=/path/to/chromium`. The test can be run again on the same database (it creates new orders each time). Stop the server and drop the scratch database afterwards.

## Docker files

The `Dockerfile`, `.dockerignore` and `deploy/` (compose, Caddyfile, `entrypoint.sh`) are described in the [deploy guide](docs/guide/deploy.md); CI builds the image only when they change (`docker build .`). Run `bash deploy/test-entrypoint.sh` to test the entrypoint locally (a fake binary, no Docker needed).

## PostgreSQL for tests

The `forgebase-db`, `forgebase-http` and `forgebase` integration tests need a PostgreSQL 16 server and a superuser (or `CREATEDB`) URL in `FORGEBASE_TEST_DATABASE_URL`. They fail with a clear message when it is not set; they never skip. Each test creates its own `fb_test_*` database and drops it at the end. The `forgebase` tests run the built `forgebase` binary (`tests/cli.rs`) and a test binary with apps (`tests/app_binary.rs`) against such a database (they set `FORGEBASE_DATABASE_URL` for it themselves). `--all-features` turns on the `testing` feature of the `forgebase` crate (the helpers for the tests of apps, [ADR 0012](docs/adr/0012-app-model-and-public-api-boundary.md)); the crate's own tests enable it themselves, so a plain `cargo test` runs them too.

The backup tests (`crates/forgebase/tests/backup.rs`) also run the real `pg_dump` and `pg_restore`: install the PostgreSQL client tools of the server's major version, 16 (Debian and Ubuntu: `postgresql-client-16`, in `/usr/lib/postgresql/16/bin`; set `FORGEBASE_PG_BIN` to that directory if another version is first on `PATH`; CI does this). Without them those tests fail with `pg_dump not found`; they never skip. The CI job `sites` (`examples/sites/ci.sh`, see [examples/sites](examples/sites/README.md)) runs two sites, a backup and a restore end to end and can be run locally.

```sh
docker run -d --name forgebase-pg -p 5432:5432 -e POSTGRES_USER=forgebase -e POSTGRES_PASSWORD=forgebase postgres:16
# or install PostgreSQL 16 from your system package manager and create a superuser role
export FORGEBASE_TEST_DATABASE_URL=postgres://forgebase:forgebase@localhost:5432/postgres
```

CI runs the same tests against a `postgres:16` service container.

**Webhook tests (M9.2, [ADR 0020](docs/adr/0020-outgoing-webhooks.md)).** `crates/forgebase/tests/webhooks.rs` starts receivers on `127.0.0.1` and, for the `https://` cases, makes a throwaway CA with the `openssl` command-line tool (nothing else to set up; the tests fail loudly if `openssl` is missing). They use `forgebase::testing::webhook_test_settings` to trust that CA and to shorten the delivery time limit.

**TLS tests (M9.0, [ADR 0018](docs/adr/0018-postgres-tls.md)).** `crates/forgebase-db/tests/tls.rs` and `crates/forgebase/tests/backup_tls.rs` need a second PostgreSQL 16 with TLS. `.github/ci/pg-tls.sh` makes one: it needs `openssl` and Docker, generates a throwaway CA, a server certificate (names `localhost` and `127.0.0.1`) and a second, unrelated CA into a directory you give, and starts the container `forgebase-pg-tls` on `127.0.0.1:5433`. It prints the variables the tests read (`FORGEBASE_TEST_TLS_URL`, `FORGEBASE_TEST_TLS_CA`, `FORGEBASE_TEST_TLS_WRONG_CA`); CI appends them to `GITHUB_ENV` in the `check` job, so the TLS tests run in the same `cargo test`. Locally:

```sh
.github/ci/pg-tls.sh up /tmp/forgebase-tls > /tmp/forgebase-tls.env
export $(cat /tmp/forgebase-tls.env)       # the three variables
cargo test --workspace --all-features --locked
.github/ci/pg-tls.sh down                  # removes the container; delete /tmp/forgebase-tls too (it holds throwaway keys)
```

Without these variables the TLS tests print `SKIPPED` and pass, except when `CI=true`: then they fail. If Docker Hub rate-limits the pull, pull another mirror of the image and `docker tag` it as `postgres:16-bookworm` (the script's default; `PG_TLS_IMAGE` overrides it).

## The app template

`forgebase new-app` writes the files of `crates/forgebase/templates/app/`. They are not compiled with the workspace, so only the CI job `app-template` (and your own run) proves that the generated crate builds, passes its tests and runs. Run what the job runs after changing the template or anything an app uses (needs the network for crates.io, a few minutes and the PostgreSQL of the section above):

```sh
export RUSTUP_TOOLCHAIN=$(rustup show active-toolchain | cut -d' ' -f1)   # in the checkout: the toolchain of rust-toolchain.toml
cargo build -p forgebase
target/debug/forgebase new-app demo --forgebase-path "$PWD" --dir /tmp/forgebase-app   # outside the checkout
cd /tmp/forgebase-app/demo && cargo test
# then: create a scratch database, export FORGEBASE_DATABASE_URL, cargo build, run target/debug/demo with
# migrate, user create (password on stdin), serve; log in with curl and GET /api/v1/meta/demo_note.
# These are the steps of the job `app-template` in .github/workflows/ci.yml.
```

The generated crate is outside the checkout, so `rust-toolchain.toml` does not apply to it; the job sets `RUSTUP_TOOLCHAIN` for that reason. `tests/new_app.rs` (no database needed) tests what the command writes and refuses.

## Dependencies

Adding a dependency needs the justification described in [maintenance-policy](docs/blueprint/maintenance-policy.md#dependency-admission). It must pass cargo-deny. An npm dependency of the desk is pinned to an exact version, must pass `npm run licenses` (see [the frontend rules](docs/blueprint/maintenance-policy.md#frontend-npm-dependencies)) and, if it is a runtime dependency, needs an ADR.

## Commits and PRs

Open a pull request as a **draft**: CI does not run on drafts (it is billed per minute). Run the local checks above first, and mark the pull request ready for review when you want CI to run; every push to a ready pull request runs it again. CI runs only the jobs that the changed paths need; the details are in [the maintenance policy](docs/blueprint/maintenance-policy.md#ci).

Keep commits small and focused. Update docs and `CHANGELOG.md` in the same PR as the behavior change.
