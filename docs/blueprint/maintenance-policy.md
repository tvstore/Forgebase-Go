# Maintenance policy

Status: **Proposed** (awaiting owner acceptance). Workflow, approval gates, git safety and the model selection policy are defined only in [AGENTS.md](../../AGENTS.md); they are not repeated here. Guiding rule: prefer deleting code over adding configuration options.

## Toolchain and MSRV

Status: **Accepted** (owner decision, 2026-10-28).

- The toolchain is pinned to 1.99.1 in `rust-toolchain.toml`.
- MSRV = pinned stable minus 2 minor releases (currently 1.97). It is declared as the workspace `rust-version` and checked in CI. MSRV rises together with pin bumps and is noted in `CHANGELOG.md`. The dependency set sets a floor (measured: about 1.89 for recommended stack A, 1.94 if sqlx is chosen; see [dependency-evaluation](dependency-evaluation.md#11-supply-chain-tooling-and-msrv)); the window must stay above it.
- Edition 2024 is confirmed. `Cargo.lock` is committed (the workspace ships binaries).

*Implementation note (M7.3, not an owner decision):* the CI jobs `check` and `sites` run on `ubuntu-24.04`, not `ubuntu-latest`: they need `pg_dump` and `pg_restore` of the `postgres:16` service's major version, and 24.04 ships `postgresql-client-16` in its own archive (26.04 does not). Re-evaluate when the PostgreSQL floor or the service version changes, or when 24.04 runners are retired.

## Dependency admission

Adding a dependency requires a short justification in the PR:

- need, and alternatives considered;
- license (must pass cargo-deny);
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

Runs in CI (job `msrv-deny`; advisories also on the weekly schedule). Covers licenses, advisories, bans (duplicates where practical) and sources. Proposed license allow-list (to be reviewed): MIT, Apache-2.0, Apache-2.0 WITH LLVM-exception, BSD-2-Clause, BSD-3-Clause, ISC, Zlib, Unicode-3.0, Unlicense, BSL-1.0, CC0-1.0. Copyleft is denied by default; a per-crate `clarify` entry for `ring` is added if needed. cargo-deny and cargo-audit are CI tools, not workspace dependencies. CI installs cargo-deny with `cargo install --locked` (no third-party action).

## CI

Status: **Accepted** (owner decision of 2026-10-30, "setuju C-1 sampai C-5"; cost control for the private repository, see [owner decisions](../owner-decisions.md); the repository has been public since 2026-10-31, and these controls are kept as good practice). The workflow is [`.github/workflows/ci.yml`](../../.github/workflows/ci.yml).

- **Triggers.** A pull request that is not a draft (`opened`, `synchronize`, `reopened`, `ready_for_review`), a weekly schedule (Monday) and `workflow_dispatch`. There is no run on push to main: a merge needs a green CI on the pull request head ([AGENTS.md section 10.3](../../AGENTS.md#103-standing-merge-authorization)). The weekly run and a dispatch run every job.
- **Drafts.** A draft pull request runs no job. The working session opens pull requests as drafts, runs all checks locally during implementation and review, and marks the pull request ready only after the review reports PASS, so CI runs once (plus fix-ups). See [AGENTS.md section 10.2](../../AGENTS.md#102-roles-in-the-working-session).
- **Path classes.** The first job `changes` diffs against the merge base (`base...head`) and runs [`.github/ci/classify-paths.py`](../../.github/ci/classify-paths.py) (unit test: `test_classify_paths.py`, run in the same job). `rust` (`crates/`, `examples/`, `.cargo/`, `Cargo.toml`, `Cargo.lock`, `rust-toolchain.toml`, `deny.toml`, and **any path not otherwise classified**, Markdown under `crates/` included because templates are compiled in) runs `check`, `sites`, `msrv-deny` and `app-template`; `docker` (`Dockerfile`, `.dockerignore` and everything under `deploy/`, Markdown included; `deploy/` is not Rust because nothing under it is compiled) runs the job `docker` (the image build and a smoke test, no push), and the weekly run covers changes of the crates or the desk that would break the image; `desk` (`desk/`, except Markdown files) runs `desk` (so does the one file outside `desk/` that the desk reads, `crates/forgebase-core/src/doctype_icons.txt`, listed in `DESK_ALSO` in the classifier; add a path there when a desk file imports from outside `desk/`); `rust` or `desk` runs `e2e`; a change under `.github/`, a schedule or a dispatch runs everything (the classifier prints six keys: `rust`, `desk`, `e2e`, `docker`, `docs_only`, `all`, and the job validates all six, failing closed); documentation-only changes (`docs/`, `*.md`, `AGENTS.md`, `CHANGELOG.md`, `LICENSE`) run only the `docs` job (offline link and anchor check, always run). A skipped job counts as a success for required checks, and `ci-result` (below) is the one check that is required.
- **Entrypoint test.** The job `docker` also runs `bash deploy/test-entrypoint.sh` (the signal and exit-status handling of `deploy/entrypoint.sh` against a fake binary); run it locally after changing the entrypoint.
- **Cache.** `actions/cache` keeps the Cargo registry index and crate cache, the git db and `target/` per job, keyed on `rust-toolchain.toml` and `Cargo.lock` (`restore-keys` give a partial hit). `CARGO_INCREMENTAL=0` keeps it small. The cargo-deny binary and the npm download cache have their own keys. A cache saved on a pull request ref is visible to that pull request only; a cache saved on the default branch is visible to all. Because nothing runs on push to main, **the weekly schedule and `workflow_dispatch` on main warm the shared cache**: after a change of a cache key (a new toolchain, a changed `Cargo.lock` that matters, a changed job name) run the workflow manually on main once. GitHub allows 10 GB of caches per repository and evicts the least recently used first; if CI gets slow again, check the cache list first. Bumping a key prefix (for example the `msrv1.97` part of the `msrv-deny` `target/` key) starts a fresh cache and so resets one that has grown too large.
- **Risk: no CI on push to main.** Two pull requests that are each green can conflict after both merge, and nothing notices until the weekly run. After a batch of merges, run `workflow_dispatch` on main.
- **`ci-result`, the one required check (owner decision of 2026-10-31, *"setuju ci-result dan setuju M9.0"*).** The last job of `ci.yml`. It has `if: always()` and `needs` every other job, and it fails if any needed job's result is anything other than `success` or `skipped` (so `failure`, `cancelled` and any unknown value fail; the script prints every result). The owner's branch ruleset on `main` requires **only `ci-result`**: deletions restricted and force pushes blocked; a pull request required with 0 approvals; merge commits only, no linear history; no "up to date" requirement; once `ci-result` exists on `main` it is the single required status check. A job added to the workflow must be added to the `needs` list of `ci-result`, or the ruleset does not guard it. On a **draft** pull request `changes` is skipped, so is everything after it, and `ci-result` still runs and passes without meaning anything: a draft has no meaningful `ci-result`. It is never merged, and marking it ready runs CI again. (`ci-result` itself is not skipped on drafts: it is one short job, and the approved definition is kept as it is.)
- **TLS test setup (M9.0, [ADR 0018](../adr/0018-postgres-tls.md)).** The `check` job runs [`.github/ci/pg-tls.sh`](../../.github/ci/pg-tls.sh) before `cargo test`: a second `postgres:16` container on port 5433 with `ssl=on`, a throwaway CA, a server certificate and a second, unrelated CA, created by `openssl` into `$RUNNER_TEMP`. The script appends `FORGEBASE_TEST_TLS_URL`, `FORGEBASE_TEST_TLS_CA` and `FORGEBASE_TEST_TLS_WRONG_CA` to `GITHUB_ENV`. The TLS tests fail, not skip, when `CI=true` and these are missing. The same script runs locally ([CONTRIBUTING](../../CONTRIBUTING.md#postgresql-for-tests)). The script lives under `.github/`, so a change to it runs every job. No new action is used (the script uses `docker` and `openssl`, which the runner has).
- **Actions.** Only GitHub-owned actions (`actions/checkout`, `actions/cache`), pinned by full commit SHA with a version comment and upgraded together. `actions/cache` was added by the same owner decision. See [dependency evaluation](dependency-evaluation.md#11-supply-chain-tooling-and-msrv).

## Container images

The `Dockerfile` and `deploy/compose.yaml` pin every base image by its multi-arch index digest (`image:tag@sha256:...`); the tag says what the digest is. The images are listed with their licences in the [dependency evaluation](dependency-evaluation.md#container-images). Update them like the toolchain, on purpose and not by accident:

1. `docker pull IMAGE:TAG`, then `docker inspect --format '{{index .RepoDigests 0}}' IMAGE:TAG`.
2. Replace the digest in the `Dockerfile` (`node`, `rust`, `postgres`) and in `deploy/compose.yaml` (`postgres`, `caddy`). The `postgres` digest must be the same in both files. A new Rust toolchain also changes `rust-toolchain.toml` and the `rust` tag and `RUSTUP_TOOLCHAIN` in the `Dockerfile` (the build stage fails if they differ).
3. The `postgres` image must stay major version 16 while the server is 16: `pg_dump` and `pg_restore` must match it.
4. The final image carries the CA bundle of the `rust` build stage (`COPY --from=build /etc/ssl/certs/ca-certificates.crt`, for TLS to PostgreSQL, [ADR 0018](../adr/0018-postgres-tls.md)): updating the `rust` digest also refreshes the roots, so update it regularly even without a toolchain change.
5. The CI job `docker` (it runs because `Dockerfile` or `deploy/` changed) builds the image and runs the smoke test; run `docker build .` locally first.

The weekly CI run also builds the image, so a change in the crates or the desk that breaks it is noticed within a week.

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
