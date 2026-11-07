# Maintenance policy

> This document was written for the Rust implementation; the Go mapping of crates, libraries and tooling is in [ADR 0026](../adr/0026-go-implementation.md). Behaviour described here is normative for the Go port (G1). Sections marked "(Go port, ADR 0026)" replace the Rust text.

Status: **Proposed** (awaiting owner acceptance). Workflow, approval gates, git safety and the model selection policy are defined only in [AGENTS.md](../../AGENTS.md); they are not repeated here. Guiding rule: prefer deleting code over adding configuration options.

## Toolchain and MSRV

**(Go port, ADR 0026.)** "MSRV" is the minimum supported Go version here.

Status: **Accepted by owner** (owner decision of 2026-11-06, G2, [ADR 0026](../adr/0026-go-implementation.md)). This section replaces "Toolchain and MSRV" of the Rust implementation (the Rust text and its 2026-10-28 owner decision are history; they still apply to the frozen Rust repository).

- The toolchain is pinned to Go **1.27.3** by the `toolchain go1.27.3` line of `go.mod`. CI installs exactly that version from <https://go.dev/dl/> and verifies its sha256 against the download index (`.github/ci/install-go.sh`; no setup action) and sets `GOTOOLCHAIN=local`, so that no job silently uses another toolchain.
- The minimum version, the `go` line of `go.mod`, is the **previous minor release**: `go 1.26.0`. It rises together with the pin (when the pin moves to a new minor release, the minimum moves to the minor release before it) and the change is noted in `CHANGELOG.md`. The dependency set sets a floor (the `go` line of a dependency must not be above the minimum); the window must stay above it.
- `go.sum` is committed once there is a dependency. CI runs `go mod verify` and `go mod tidy -diff`, which fails when `go.mod` or `go.sum` is not tidy.

*Implementation note (Go port, not an owner decision):* the build must work with `CGO_ENABLED=0` (one static binary, no cgo); the CI job `check` runs `CGO_ENABLED=0 go build ./...` to check it.

*Implementation note (carried from M7.3, not an owner decision):* the CI job `check` runs on `ubuntu-24.04`, not `ubuntu-latest`: later phases need `pg_dump` and `pg_restore` of the `postgres:16` service's major version, and 24.04 ships `postgresql-client-16` in its own archive (26.04 does not). Re-evaluate when the PostgreSQL floor or the service version changes, or when 24.04 runners are retired.

## Dependency admission

Adding a dependency requires a short justification in the PR:

- need, and alternatives considered;
- license (must pass the licence check, see "Licence check and vulnerabilities" below; in the Go port, ADR 0026 lists the approved dependencies and any other needs the owner's approval);
- maintenance status;
- transitive weight.

Significant dependencies get an ADR ([docs/adr](../adr/README.md)). The same rules apply to frontend (npm) dependencies, as made concrete below.

### Frontend (npm) dependencies

Status: **Accepted** (owner decisions of 2026-10-29, M4). Details and the package list: [dependency-evaluation, section 12](dependency-evaluation.md#12-frontend).

- Exact versions in `desk/package.json` (no `^` or `~`); `desk/package-lock.json` is committed and CI installs with `npm ci`.
- Runtime dependencies are `react`, `react-dom` and, since UX.4, the self-hosted font `@fontsource-variable/inter` (CSS and `woff2` only, no JavaScript; [ADR 0017](../adr/0017-desk-styling-tailwind.md), UX.4). No UI component library, router or data-fetching library without an ADR. Icons are copied into `desk/src/icons.tsx` with their licence notice, never installed as a package. The built desk ships `THIRD-PARTY-LICENSES.txt` (Inter's OFL-1.1 text and the Lucide notice; source `desk/public/THIRD-PARTY-LICENSES.txt`, copied to `desk/dist/`): **an upgrade of the font, or a change to the icons, refreshes that file in the same pull request**; the desk test `src/licenses.test.ts` fails when the Inter text is stale.
- **License allow-list**, checked by `npm run licenses` in CI: shipped packages MIT, ISC, Apache-2.0, BSD-2-Clause, BSD-3-Clause; development-only packages additionally MPL-2.0, BlueOak-1.0.0 and CC-BY-4.0. **OFL-1.1 is allowed for fonts only** (packages of the `@fontsource` and `@fontsource-variable` scopes; owner decision U2, UX.4): the check rejects it for any other package, so it never covers code. **MPL-2.0 is allowed only for packages marked `dev` in the lockfile** (build tooling that is not shipped). A missing, unknown or expression license fails the check; extending the list is an owner decision recorded here and in the dependency evaluation.
- `npm audit --audit-level=high` runs in CI (also on the weekly schedule): high and critical advisories fail the job; low and moderate ones are reported and handled when convenient.
- **Tailwind CSS (UX.3, [ADR 0017](../adr/0017-desk-styling-tailwind.md)) is a pinned development tool**, not a runtime dependency: `tailwindcss` and `@tailwindcss/vite` at the same exact version, no plugins and no component kits. An upgrade is its own change: pin both packages together, run the whole desk check and the screenshot script, and compare before and after screenshots in the review. The license allow-list stays as it is (the Tailwind tree adds only MIT, ISC and MPL-2.0 development packages); if a Tailwind release needs another license, the check fails and the owner decides.
- `desk/vite.config.ts` uses `experimental.renderBuiltUrl` to keep the font URL relative in the built CSS. Because that option is experimental, a Vite upgrade must verify the font URL in the built CSS (and the CSP check of `dist/index.html`).
- The desk build must not need a network, a CDN or an inline script or style: the Content-Security-Policy of [the security model](security-model.md#the-http-layer-implemented-in-m33) forbids them.

## cargo-deny

**(Go port, ADR 0026.)** cargo-deny is not used in the Go port. The licence check and the vulnerability check below replace it (the Rust text stays in the history of the Rust repository).

## Licence check and vulnerabilities (Go port, ADR 0026)

Two CI jobs cover what cargo-deny and cargo-audit covered:

- **Licences.** The job `licenses` runs the standard-library program [`.github/ci/licensecheck`](../../.github/ci/licensecheck/main.go). It lists every module in the build graph of the main module (`go list -deps -test -json ./...`, the non-standard, non-main modules), reads the licence file in the module directory (`LICENSE`, `LICENSE.txt`, `LICENSE.md` or `COPYING`), identifies the licence by its distinctive text and fails when a licence is missing, not recognised or not on the allow-list: **MIT, Apache-2.0, BSD-2-Clause, BSD-3-Clause, ISC** (the Rust list without its Rust-specific entries). Copyleft is not allowed. With no dependency it prints "no dependencies" and passes. Extending the allow-list is an owner decision recorded here and in the dependency evaluation. Limits of the checker: it recognises a licence by its distinctive phrases, so it would not notice an extra restrictive clause added to otherwise standard text; dependencies that appear only under other GOOS or build tags, and modules of a `tool` directive, are not covered; so every new dependency needs the owner's approval and a manual reading of its licence in the dependency evaluation.
- **Vulnerabilities.** The job `check` runs `govulncheck` (pinned version, installed with `go install`) on every pull request run and on the weekly schedule. It replaces `cargo audit` and the advisories part of `cargo deny`: it reports only vulnerabilities in code that the module actually calls.
- `staticcheck` (pinned version) and `go vet` run in `check`. staticcheck and govulncheck are CI tools, not module dependencies.

## CI

Status: **Accepted** (owner decision of 2026-10-30, "setuju C-1 sampai C-5"; cost control for the private repository, see [owner decisions](../owner-decisions.md); the repository has been public since 2026-10-31, and these controls are kept as good practice). **Adapted for Go (Go port, ADR 0026; owner decision G8 of 2026-11-06).** The workflow is [`.github/workflows/ci.yml`](../../.github/workflows/ci.yml). The jobs of P0 are `changes`, `docs`, `check`, `licenses` and `ci-result`; the jobs `docker`, `desk`, `e2e`, `sites` and `app-template` are added with the phase that needs them, and each is added to the `needs` of `ci-result`.

- **Triggers.** A pull request that is not a draft (`opened`, `synchronize`, `reopened`, `ready_for_review`, and since the Go port `converted_to_draft`, see "Drafts" and `ci-result`), a weekly schedule (Monday) and `workflow_dispatch`. There is no run on push to main: a merge needs a green CI on the pull request head ([AGENTS.md section 10.3](../../AGENTS.md#103-standing-merge-authorization)). The weekly run and a dispatch run every job.
- **Drafts.** A draft pull request runs no job (every job except `ci-result` has the draft guard, directly or through `needs: changes`). The working session opens pull requests as drafts, runs all checks locally during implementation and review, and marks the pull request ready only after the review reports PASS, so CI runs once (plus fix-ups). See [AGENTS.md section 10.2](../../AGENTS.md#102-roles-in-the-working-session).
- **Path classes.** The first job `changes` diffs against the merge base (`base...head`) and runs [`.github/ci/classify-paths.py`](../../.github/ci/classify-paths.py) (unit test: `test_classify_paths.py`, run in the same job). `go` (`*.go` anywhere outside `desk/`, `go.mod`, `go.sum`, `cmd/`, `internal/`, `testing/`, `examples/`, and **any path not otherwise classified**, Markdown under those directories included because files there are embedded with `//go:embed`) runs `check` and `licenses` (and the later Go jobs); `docker` (`Dockerfile`, `.dockerignore` and everything under `deploy/`, Markdown included; `deploy/` is not Go because nothing under it is compiled) runs the job `docker` (P5.3); `desk` (`desk/`, except Markdown files) will run `desk`; a file outside `desk/` that the desk reads is listed in `DESK_ALSO` in the classifier (empty for now; add a path there when a desk file imports from outside `desk/`); `go` or `desk` will run `e2e`; a change under `.github/`, a schedule or a dispatch runs everything (the classifier prints six keys: `go`, `desk`, `e2e`, `docker`, `docs_only`, `all`, and the job validates all six, failing closed); documentation-only changes (`docs/`, `*.md`, `AGENTS.md`, `CHANGELOG.md`, `LICENSE`) run only the `docs` job (offline link and anchor check, and the unit test of the test-mapping generator of the [parity report](../parity-report.md), always run). A skipped job counts as a success for required checks, and `ci-result` (below) is the one check that is required.
- **The `check` job.** On `ubuntu-24.04` with a PostgreSQL 16 service (`FORGEBASE_TEST_DATABASE_URL` set): `gofmt -l .` (fails and lists the files), `go vet`, `staticcheck`, `govulncheck`, `go test -race ./...`, `go build ./...`, `CGO_ENABLED=0 go build ./...`, `go mod verify` and `go mod tidy -diff`. Integration tests need the database, fail clearly without it and are never skipped.
- **The `docker` job (P5.3).** On `ubuntu-24.04` with a PostgreSQL 16 service: `bash deploy/test-entrypoint.sh`, `docker build` (no push), the trust-store checks of the image (CA bundle present, `SSL_CERT_FILE` set, the snakeoil certificate and key gone, `/etc/ssl/certs` holding only the bundle) and a smoke test of the single-container mode (`all`: migrate on start, desk and login page, login, an authenticated API call, `worker: started` in the log, graceful stop with exit code 0). It runs when `Dockerfile`, `.dockerignore`, `deploy/` or `.github/` change, and on the weekly and manual runs, and is in `needs` of `ci-result`.
- **Entrypoint test (P5.3).** The job `docker` also runs `bash deploy/test-entrypoint.sh` (the signal and exit-status handling of `deploy/entrypoint.sh` against a fake binary); run it locally after changing the entrypoint.
- **Cache.** `actions/cache` keeps the Go module cache (`~/go/pkg/mod`), the build cache (`~/.cache/go-build`) (the CI tools are installed with `go install` on every run, from the build cache), keyed on `go.mod`, `go.sum` and the tool versions (`restore-keys` give a partial hit). A cache saved on a pull request ref is visible to that pull request only; a cache saved on the default branch is visible to all. Because nothing runs on push to main, **the weekly schedule and `workflow_dispatch` on main warm the shared cache**: after a change of a cache key (a new Go version, a changed `go.sum` that matters, a changed job name) run the workflow manually on main once. GitHub allows 10 GB of caches per repository and evicts the least recently used first; if CI gets slow again, check the cache list first. The npm download cache of the desk jobs has its own key (from P3).
- **Risk: no CI on push to main.** Two pull requests that are each green can conflict after both merge, and nothing notices until the weekly run. After a batch of merges, run `workflow_dispatch` on main.
- **`ci-result`, the one required check (owner decision of 2026-10-31, *"setuju ci-result dan setuju M9.0"*).** The last job of `ci.yml`. It has `if: always()` and `needs` every other job, and it fails if any needed job's result is anything other than `success` or `skipped` (so `failure`, `cancelled` and any unknown value fail; the script prints every result). The owner's branch ruleset on `main` requires **only `ci-result`**: deletions restricted and force pushes blocked; a pull request required with 0 approvals; merge commits only, no linear history; no "up to date" requirement; once `ci-result` exists on `main` it is the single required status check. A job added to the workflow must be added to the `needs` list of `ci-result`, or the ruleset does not guard it.
- **`ci-result` fails on a draft (Go port, ADR 0026; owner decision G8).** This supersedes the Rust text that `ci-result` itself passes on drafts. On a draft pull request `changes` is skipped, so is everything after it, and a skipped job counts as a success; to make sure that a draft run can never satisfy the required check, the **first step of `ci-result` fails the job** when the event is `pull_request` and the pull request is a draft, with the message: "ci-result: this pull request is a draft; CI does not run on drafts. Mark it ready for review to run CI. A draft run never satisfies the required check." CI runs again when the pull request is marked ready (`ready_for_review`), and the `ci-result` of **that** run, on the ready head, is the one that counts for the merge ([AGENTS.md section 10.3](../../AGENTS.md#103-standing-merge-authorization)). The workflow also runs on `converted_to_draft`: the concurrency group cancels a run in progress for the pull request, and the new run fails at once.
- **TLS test setup (P5.2; owner: "setuju TLS di P5"; [ADR 0018](../adr/0018-postgres-tls.md)).** The `check` job runs `.github/ci/pg-tls.sh` before `go test` (a second `postgres:16` container on port 5433 with `ssl=on`, a throwaway CA, a server certificate and a second, unrelated CA, created by `openssl` into `$RUNNER_TEMP`; the script is the one of the Rust repository, unchanged). The `check` and `sites` jobs also install the PostgreSQL 16 client tools (`postgresql-client-16`, `FORGEBASE_PG_BIN`) that `site backup` and `site restore` need, and fail if `pg_dump` or `pg_restore` is not version 16. The `sites` job runs `examples/sites/ci.sh` and is in `needs` of `ci-result`. The TLS tests fail, not skip, when `CI=true` and `FORGEBASE_TEST_TLS_URL`, `FORGEBASE_TEST_TLS_CA` and `FORGEBASE_TEST_TLS_WRONG_CA` are missing.
- **Actions.** Only GitHub-owned actions (`actions/checkout`, `actions/cache`), pinned by full commit SHA with a version comment and upgraded together; no `actions/setup-go`: Go is installed by `.github/ci/install-go.sh`. `actions/cache` was added by the C-1 owner decision. See [dependency evaluation](dependency-evaluation.md#11-supply-chain-tooling-and-msrv) (the Rust evaluation) and [Go dependencies](dependency-evaluation.md#go-dependencies-adr-0026).

## Container images

The `Dockerfile` and `deploy/compose.yaml` pin every base image by its multi-arch index digest (`image:tag@sha256:...`); the tag says what the digest is. The images are listed with their licences in the [dependency evaluation](dependency-evaluation.md#container-images). Update them like the toolchain, on purpose and not by accident:

1. `docker pull IMAGE:TAG`, then `docker inspect --format '{{index .RepoDigests 0}}' IMAGE:TAG`.
2. Replace the digest in the `Dockerfile` (`node`, `golang`, `postgres`) and in `deploy/compose.yaml` (`postgres`, `caddy`). The `postgres` digest must be the same in both files. A new Go toolchain also changes the `toolchain` line of `go.mod` (and the `toolchain` line of `examples/*/go.mod`, and `GOTOOLCHAIN` in the docs) and the `golang` tag in the `Dockerfile` (the build stage fails if the image's Go and the `toolchain` line differ).
3. The `postgres` image must stay major version 16 while the server is 16: `pg_dump` and `pg_restore` must match it.
4. The final image carries the CA bundle of the `golang` build stage (`COPY --from=build /etc/ssl/certs/ca-certificates.crt`, for TLS to PostgreSQL, [ADR 0018](../adr/0018-postgres-tls.md)): updating the `golang` digest also refreshes the roots, so update it regularly even without a toolchain change.
5. The CI job `docker` (it runs because `Dockerfile` or `deploy/` changed) builds the image and runs the smoke test; run `docker build .` locally first.

The weekly CI run also builds the image, so a change in the Go code or the desk that breaks it is noticed within a week.

## Versioning and changelog

SemVer once published; 0.x until the API stabilizes. `CHANGELOG.md` in Keep a Changelog format.

## Testing strategy

| Area | Approach |
|---|---|
| `forgebase-core` | Unit tests (pure), table-driven; property tests only where cheap value (for example identifier validation). |
| `forgebase-db`, `forgebase-http` | Integration tests against a real PostgreSQL (no mocks); each test in its own transaction or fresh database; CI uses a Postgres service container. Supported PostgreSQL floor: 16 (Accepted, owner decision 2026-10-28). |
| Security | Explicit negative tests for every permission rule and for injection attempts in filters and identifiers ([security-model](security-model.md#required-negative-tests)). |
| Frontend (from M4) | Vitest on pure modules (router, API client, filter builder, list helpers, form field conversion, access rules, error mapping) with a stubbed `fetch`, ESLint rules that forbid browser storage, raw HTML and inline styles, `tsc --noEmit`, and one Playwright smoke test (`desk/e2e/smoke.spec.ts`, Chromium only, CI job `e2e`, implemented in M4.2). Component tests with `jsdom` only when a pure-module test cannot cover the risk. Decided in M4 (owner decision 2026-10-29). |
| Coverage | No percentage targets; risk-based coverage. |

## Documentation and ADR rules

- Docs are updated in the same PR as behavior changes.
- ADRs are immutable once Accepted: they are superseded, not edited. See [docs/adr/README.md](../adr/README.md).
- Do not duplicate content across documents; link instead.

## Review expectations

Every change gets an independent review. The Stage 3 checklist is defined only in [AGENTS.md](../../AGENTS.md#stage-3--agent-review). Passing CI alone is not a review.
