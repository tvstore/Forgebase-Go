# Contributing

Workflow, approval gates, git safety and review rules are defined only in [AGENTS.md](AGENTS.md). Read it first. This repository is the Go implementation of Forgebase ([ADR 0026](docs/adr/0026-go-implementation.md)); the Rust implementation is a frozen reference.

## Prerequisites

- Go **1.27.3**, the version of the `toolchain` line of `go.mod` (the minimum, the `go` line, is 1.26.0). With a Go of 1.21 or newer installed, running `go` in this repository downloads the pinned toolchain automatically; to be explicit, set `GOTOOLCHAIN=go1.27.3`. CI installs the same version from <https://go.dev/dl/> with a verified checksum.
- A PostgreSQL 16 server for the integration tests (from P1), see [below](#postgresql-for-tests).
- Python 3 (standard library only) for the CI helper scripts.
- For the desk (`desk/`, from P3): Node.js `^20.19.0 || >=22.12.0` (Vite 8) with npm. `desk/.npmrc` sets `ignore-scripts=true`, so `npm ci` runs no install scripts of dependencies.
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

The jobs `docker`, `desk`, `e2e`, `sites` and `app-template` of the Rust CI come back with the phases that port what they check; the local commands for them are added to this file then.

## PostgreSQL for tests

From P1 the integration tests need a PostgreSQL 16 server and a superuser (or `CREATEDB`) URL in `FORGEBASE_TEST_DATABASE_URL`. They **fail with a clear message when it is not set; they never skip.** Each test creates its own `fb_test_*` database (helper package `internal/db/dbtest`) and drops it at the end. Later phases also need the PostgreSQL client tools of the server's major version, 16 (`pg_dump`, `pg_restore`; Debian and Ubuntu: `postgresql-client-16`, in `/usr/lib/postgresql/16/bin`).

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
