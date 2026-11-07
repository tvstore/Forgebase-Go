# Contributing

Workflow, approval gates, git safety and review rules are defined only in [AGENTS.md](AGENTS.md). Read it first. This repository is the Go implementation of Forgebase ([ADR 0026](docs/adr/0026-go-implementation.md)); the Rust implementation is a frozen reference.

## Prerequisites

- Go **1.27.3**, the version of the `toolchain` line of `go.mod` (the minimum, the `go` line, is 1.26.0). With a Go of 1.21 or newer installed, running `go` in this repository downloads the pinned toolchain automatically; to be explicit, set `GOTOOLCHAIN=go1.27.3`. CI installs the same version from <https://go.dev/dl/> with a verified checksum.
- A PostgreSQL 16 server for the integration tests (from P1), see [below](#postgresql-for-tests).
- Python 3 (standard library only) for the CI helper scripts.
- For the desk (`desk/`, from P3.2): Node.js `^20.19.0 || >=22.12.0` (Vite 8) with npm. `desk/.npmrc` sets `ignore-scripts=true`, so `npm ci` runs no install scripts of dependencies.
- The CI tools are not module dependencies; `go run` fetches them at pinned versions: `honnef.co/go/tools/cmd/staticcheck@v0.8.2` and `golang.org/x/vuln/cmd/govulncheck@v1.9.0`.

## Local checks

Run the same commands as CI ([.github/workflows/ci.yml](.github/workflows/ci.yml)) before opening a PR:

```sh
test -z "$(gofmt -l .)" || { gofmt -l .; false; }       # formatting; gofmt -w . fixes it
go vet ./... ./.github/ci/licensecheck
go run honnef.co/go/tools/cmd/staticcheck@v0.8.2 ./... ./.github/ci/licensecheck
go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...
go test -race ./...                                    # needs FORGEBASE_TEST_DATABASE_URL, see below
go build ./...
go mod verify
go mod tidy -diff                                      # fails when go.mod or go.sum is not tidy
```

`./...` skips directories that start with a dot, so the licence checker under `.github/ci/` is named explicitly where it matters.

The helper scripts back the `changes`, `docs` and `licenses` jobs of CI. They need only `python3` and Go (standard library only):

```sh
python3 .github/ci/test_classify_paths.py   # unit test of the path classifier (which CI jobs a change needs)
python3 .github/ci/linkcheck.py             # relative links and anchors in *.md (links to files of later phases are listed in .github/ci/linkcheck-pending.txt)
python3 .github/ci/test_test_mapping.py     # unit test of the generator of docs/parity/test-mapping.md (the generator itself needs a checkout of the frozen Rust repository, see docs/parity-report.md)
go test ./.github/ci/licensecheck           # unit test of the licence classifier
go mod download && go run ./.github/ci/licensecheck   # licence of every module in the build graph ("no dependencies" for now)
git diff --name-only --no-renames origin/main...HEAD | python3 .github/ci/classify-paths.py --event pull_request   # what CI would run for your branch
```

The desk (from P3) has its own checks (the `desk` job of CI). Run them in `desk/` when you change it:

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

The `e2e` job runs Playwright against a real `forgebase serve` with the built desk. To run it locally (Playwright and a Chromium must be installed: `cd desk && npx playwright install chromium`, or point `PLAYWRIGHT_CHROMIUM_EXECUTABLE` at a Chromium binary), with a scratch database that nothing else uses:

```sh
go build -o bin/e2e_app ./examples/e2e_app               # the stock command plus the app of the report
(cd desk && npm ci && npm run build)
export FORGEBASE_DATABASE_URL=postgres://forgebase:forgebase@localhost:5432/forgebase_e2e   # an EMPTY database
FORGEBASE_BIN=bin/e2e_app E2E_DIR=bin/e2e desk/e2e/seed.sh   # writes bin/e2e/doctypes, migrates, creates the users, the restrictions and the custom field
FORGEBASE_DESK_DIR=desk/dist FORGEBASE_DEV_INSECURE_COOKIES=1 bin/e2e_app serve --doctypes bin/e2e/doctypes &
(cd desk && E2E_BASE_URL=http://127.0.0.1:8000 npx playwright test)
```

The seed runs against an empty database each time (drop and create it again for a second run). Every spec runs, as in the end-to-end job of the Rust implementation (`screenshots.spec.ts` skips itself unless `SCREENSHOTS=1`).

The example apps (P4) are modules of their own (`examples/*/go.mod`, a `replace` to this checkout), so `./...` at the root does not enter them; CI runs the same checks in each (the step "Example modules" of the job `check`). Run them with the same database:

```sh
for dir in examples/*/; do
  (cd "$dir" && go vet ./... && go test -race ./... && go build ./... && go mod tidy -diff)
done
```

The job `app-template` generates an app outside the checkout and checks it as a user would. To run it locally (it needs an empty database called `demo` next to the test database):

```sh
go build -o bin/forgebase ./cmd/forgebase
bin/forgebase new-app demo --forgebase-path "$PWD" --dir /tmp/forgebase-app-template
cd /tmp/forgebase-app-template/demo && test -z "$(gofmt -l .)" && go vet ./... && go mod tidy -diff && go test ./... && go build -o bin/demo ./cmd/demo
export FORGEBASE_DATABASE_URL=postgres://forgebase:forgebase@localhost:5432/demo
bin/demo migrate && echo 'a long password' | bin/demo user create ann --role "Demo User"
FORGEBASE_DEV_INSECURE_COOKIES=1 bin/demo serve &   # then log in at /api/v1/login; GET /api/v1/meta/demo_note says "app":"demo"
```

The job `sites` (P5.2) runs `examples/sites/ci.sh`: two sites on one server, a backup restored into a new database. Run it locally the same way (see [examples/sites](examples/sites/README.md)): build the composed binary, set the three database URLs, and run the script. The job `docker` of the Rust CI comes back with the phase that ports the container work; its local commands are added to this file then.

## PostgreSQL for tests

From P1 the integration tests need a PostgreSQL 16 server and a superuser (or `CREATEDB`) URL in `FORGEBASE_TEST_DATABASE_URL`. They **fail with a clear message when it is not set; they never skip.** Each test creates its own `fb_test_*` database (helper package `internal/db/dbtest`) and drops it at the end. The `site backup` and `site restore` tests also need the PostgreSQL client tools of the server's major version, 16 (`pg_dump`, `pg_restore`; Debian and Ubuntu: `postgresql-client-16`, in `/usr/lib/postgresql/16/bin`; set `FORGEBASE_PG_BIN` to that directory when another version is first on `PATH`); they fail with the tool's name when it is missing.

```sh
docker run -d --name forgebase-pg -p 5432:5432 -e POSTGRES_USER=forgebase -e POSTGRES_PASSWORD=forgebase postgres:16
# or install PostgreSQL 16 from your system package manager and create a superuser role
export FORGEBASE_TEST_DATABASE_URL=postgres://forgebase:forgebase@localhost:5432/postgres
```

CI runs the same tests against a `postgres:16` service container.

## Dependencies

Adding a dependency needs the justification described in [maintenance-policy](docs/blueprint/maintenance-policy.md#dependency-admission). The Go dependency set is limited to the list approved in [ADR 0026](docs/adr/0026-go-implementation.md#dependencies-g2) (the standard library first); any other module needs the owner's approval. It must pass the licence check (MIT, Apache-2.0, BSD-2-Clause, BSD-3-Clause or ISC; `go run ./.github/ci/licensecheck`) and `govulncheck`, and goes into the [dependency evaluation](docs/blueprint/dependency-evaluation.md#go-dependencies-adr-0026). An npm dependency of the desk is pinned to an exact version, must pass `npm run licenses` (see [the frontend rules](docs/blueprint/maintenance-policy.md#frontend-npm-dependencies)) and, if it is a runtime dependency, needs an ADR.

## Commits and PRs

Open a pull request as a **draft**: CI does not run on drafts (it is billed per minute), and `ci-result` fails on a draft on purpose, so a draft run can never satisfy the required check. Run the local checks above first, and mark the pull request ready for review when you want CI to run; every push to a ready pull request runs it again. A merge needs the `ci-result` of the run on the **ready** pull request head to be green. CI runs only the jobs that the changed paths need; the details are in [the maintenance policy](docs/blueprint/maintenance-policy.md#ci).

Keep commits small and focused. Update docs and `CHANGELOG.md` in the same PR as the behavior change.
