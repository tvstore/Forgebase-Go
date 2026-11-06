# Owner decisions and thinking

This file records the project owner's decisions, comments and way of thinking. Every agent and contributor
should read it **before planning**. It helps shape options and recommendations; it never replaces the owner's
approval for owner-level decisions ([AGENTS.md §8 and §10.4](../AGENTS.md#8-final-rule)). It does not replace the detailed records: each entry links to the ADR or blueprint section that
holds the details. If an entry and a linked Accepted record disagree, the linked record wins, and the
mismatch is reported to the owner.

**How to keep it current.** Add every new owner decision or comment here, in the same pull request that
records it elsewhere. Rules for entries:

- quote the owner's words (Indonesian) where they matter;
- give the date;
- link to the detailed record;
- never rewrite an old entry. When the owner changes a decision, add a new entry that names the old one as
  superseded, and mark the old one "(superseded)".

The information session keeps this file. A working session adds decisions the owner gives it directly and
reports them to the information session ([AGENTS.md, section 10](../AGENTS.md#10-operating-model)).

## 1. How the owner thinks

Distilled from the decisions below. Use these to choose within an approved plan and to shape recommendations, and say
that you did. An owner-level decision ([AGENTS.md §10.4](../AGENTS.md#104-deviations-pdca)) still goes to the owner.

1. **Longevity over speed.** A framework that the owner, other people and other agents can maintain for
   years. The owner said, when approving M5: *"pikirkan matang-matang ... framework akan digunakan jangka
   panjang, harus mudah di maintenance olehmu, dan jika agents lain maintenance"*. So:
   - prefer the simplest design that will still hold;
   - keep a small public API;
   - write explicit code, with no magic;
   - write down every decision.
2. **A base that others extend, like Frappe and ERPNext.** *"open to extension dengan mudah, jadi ini
   sebagai base nya, jadi nanti bisa ditambahkan apps lain, persis pola frappe bisa menambahkan erpnext"*.
   So:
   - apps are separate crates stacked on the framework and on each other;
   - an app never needs a change in `crates/`;
   - example apps prove this.
3. **Frappe is a behavioural reference, never a code source.** Intentional differences are fine when they
   are simpler, safer or easier to maintain.
4. **Trusts well-argued recommendations and decides fast.** The owner usually answers "setuju semua" when
   each option comes with a clear recommendation and reason. *"untuk keputusan saya mengikuti plan anda,
   klo menurut anda sesuai lanjutkan"*. So present options with a recommendation first. That trust is not
   permission to skip the approval gates: plans and owner-level decisions still need an explicit "yes".
5. **Quality gates are not negotiable.**
   - Independent review is required.
   - CI must be green.
   - Dependencies need licence checks.
   - Owner-level choices (new requirements, architecture, significant dependencies, Accepted ADRs, starting
     a milestone) come back to the owner.
6. **Wants to see real results.** The owner asked how to see Forgebase in the browser, and then asked for UI
   work. Show working software (screenshots, a runnable quick start), not only reports.
7. **The UI must suit business users.** It must be simple, clean, elegant, corporate and user friendly:
   *"UI nya simple and clean, lalu UX yg user friendly"*; *"clean, simpel, elegan dan cocok untuk
   corporate"*. From UX.3 on, navy #1B2A5C (the logo background) is the brand colour of the UI and yellow stays only in the logo
   (owner decision 2026-10-30, below; recorded in [ADR 0017](adr/0017-desk-styling-tailwind.md)).
8. **Communication: results only.** Use Indonesian with the owner and English in code and docs. In the
   information session, report only finished results (merged pull requests, milestones Done) and decisions
   that are needed. Never send "waiting" or "no new results" messages. The history must read cleanly.
   Push notifications go to Android for results.
9. **Changes decisions openly.** A later decision supersedes an earlier one explicitly. An example is
   deleting `dead` jobs after 7 days, which superseded keeping them.

## 2. Standing instructions

| Date | Instruction | Recorded in |
|---|---|---|
| 2026-10-28 | Four-stage workflow (Plan, Doing, Review, Confirmation) with approval gates. Agent separation. Git safety: no direct changes to `main`, no merge or release without approval, no force push, no deleting without permission. Do not claim the project is open source while the repository is private (superseded 2026-10-31: the repository is public, see [Repository public](#repository-public-2026-10-31-information-session) below). Do not copy Frappe code. | [AGENTS.md](../AGENTS.md) §3–5 |
| 2026-10-28 | Model Selection Policy: Opus plans and reviews, Sonnet implements, Sonnet or Haiku confirms, Fable is for escalation. | [AGENTS.md §9](../AGENTS.md#9-model-selection-policy), [ADR 0008](adr/0008-model-selection-policy.md) |
| 2026-10-28/29 | Operating model: the information session holds plans and results, and a working session executes with standing merge authorization. Deviations follow PDCA. *"conversation ini hanya untuk information, hasil plan dan hasil task ... untuk pengerjaan, merge, dll PR, pindahkan di session working, dan allow otomatis, dan otomasi merge ... jika tidak sesuai plan, selalu PDCA"* | [AGENTS.md §10](../AGENTS.md#10-operating-model) |
| 2026-10-29 | Results-only reporting in the information session. Android push notifications for results. *"hanya hasil yang kamu update jgn ada informasi menunggu kerjaan"*; *"jangan muncul kan tidak ada hasil baru"* | this file, §1.8 |
| 2026-10-30 | Record every owner decision and comment here, tidied up, so that other agents can share the owner's thinking. *"setiap putusan saya atau yang saya berikan informasi simpan ke memory ... agar nanti bisa share ke agent lain"* | this file |
| 2026-10-30 | Keep at most the 10 newest GitHub Actions workflow runs: *"kamu bisa jika sudah selesai hapus github actions yg sudah selesai maksimal 10 github actions aktif"*. The information session prunes completed runs to the newest 10 after each merge (never queued or running runs, never a run still needed as CI evidence); the working session reports each merge and does not delete runs. (superseded 2026-10-31: the repository is public, so Actions minutes are free and pruning is no longer needed; see [Docs front page and deploy](#docs-front-page-and-deploy-2026-10-31-setuju-r1-d1-d2-information-session)) | this file |

## 3. Decision log

Most plan approvals were "setuju semua" (accepting every recommendation). Each row lists the decision and
where its details live.

### Blueprint (2026-10-28)

| Decision | Recorded in |
|---|---|
| Approve the research and blueprint plan. Network access to the documentation sites was allowed. Frappe claims stay labelled *Source-indexed* or *Unverified* until they are read directly. | [feature matrix](blueprint/feature-matrix.md) |
| Licence: keep **MIT**, intended to be open source (superseded 2026-10-31: the repository is public and the project is open source under MIT, see [Repository public](#repository-public-2026-10-31-information-session)). | [ADR 0007](adr/0007-license-mit.md), [LICENSE](../LICENSE) |
| Documents in English; communication with the owner in Indonesian. | [AGENTS.md §7](../AGENTS.md#7-communication-style) |
| Research and decisions by Opus; writing documents by Sonnet. | [AGENTS.md §9](../AGENTS.md#9-model-selection-policy) |
| Fix the optional review findings; accept ADRs 0002–0006 (modular monolith, PostgreSQL only, database access layer, and the others); re-verify Frappe later; open a pull request and start M0. | [ADR index](adr/README.md) |

### M0 Foundations (2026-10-28)

| Decision | Recorded in |
|---|---|
| A new branch and pull request per piece of work (option a). | information session (not recorded elsewhere); see [AGENTS.md §5](../AGENTS.md#5-git-and-repository-safety) |
| MSRV = pinned toolchain minus 2 minor versions (1.97). | [maintenance policy](blueprint/maintenance-policy.md) |
| Fix the M0 minor findings; add CONTRIBUTING and CHANGELOG to AGENTS.md §6; Frappe verification later. | [AGENTS.md §6](../AGENTS.md#6-documentation-structure) |

### M1 Metadata and storage (2026-10-28, "setuju semua")

| Decision | Recorded in |
|---|---|
| Three pull requests: M1.1, M1.2 and M1.3. | [milestones](blueprint/milestones.md) |
| DocType files in **TOML**. | [architecture §3](blueprint/architecture.md#3-metadata-doctype-model-proposed) |
| `time` for dates and times. `rust_decimal` for decimals. | [dependency evaluation](blueprint/dependency-evaluation.md) |
| PostgreSQL **16** is the minimum. | [maintenance policy](blueprint/maintenance-policy.md), [ADR 0003](adr/0003-postgresql-only.md) |
| Integration tests fail clearly without a database and are never skipped. | [CONTRIBUTING](../CONTRIBUTING.md) |
| `clap` (derive) for the command line. | [dependency evaluation](blueprint/dependency-evaluation.md) |
| UUID v7 primary keys, generated in the application. | [architecture §3](blueprint/architecture.md#3-metadata-doctype-model-proposed) |
| tokio-postgres and deadpool were confirmed by the spike. | [ADR 0004](adr/0004-database-access-layer.md) |

### M2 Relationships and lifecycle (2026-10-28, "setuju semua")

| Decision | Recorded in |
|---|---|
| A Link is a foreign key to `id`. | [architecture §4](blueprint/architecture.md) |
| Hooks are synchronous and have no database access. | [ADR 0009](adr/0009-lifecycle-pipeline-and-hooks.md) |
| A save replaces all child rows. | [architecture §4](blueprint/architecture.md) |
| An amended document is named `<name>-<n>`. | [architecture §6](blueprint/architecture.md) |
| Naming series are gapless and counted in the same transaction. DocTypes that share a prefix share the counter (option a, 2026-10-29: *"untuk keputusan saya mengikuti plan anda"*). | [architecture §6](blueprint/architecture.md) |
| Optimistic concurrency with `version`. | [architecture §6](blueprint/architecture.md) |

### M3 Auth, permissions, REST API (2026-10-29, "keputusan 1-12 setuju semua")

| Decision | Recorded in |
|---|---|
| Users and roles; Argon2id passwords; opaque session cookies; API tokens; lockout. | [ADR 0010](adr/0010-authentication-and-sessions.md) |
| Permissions in the DocType file, deny by default, `if_owner`. A document the caller cannot read behaves exactly like a missing one. | [ADR 0011](adr/0011-permission-model-phase-1.md) |
| CSRF header; `Host` validation; no CORS. | [security model](blueprint/security-model.md) |

### Project Goal and roadmap (2026-10-29)

| Decision | Recorded in |
|---|---|
| Project Goal: a full-stack framework comparable to Frappe, with 10 capabilities and 7 success criteria. | [vision](blueprint/vision.md) |
| New roadmap from M4 to M10. | [milestones](blueprint/milestones.md) |
| Logo: yellow "F" mark. Navy #1B2A5C banner. Badges: Rust-powered, Metadata-driven, Full-stack. No "open source" badge. | [docs/assets](assets/) |

### M4 Minimal Desk (2026-10-29, "setuju semua")

| Decision | Recorded in |
|---|---|
| React + TypeScript; **no UI component library**; own router and plain `fetch`. | [architecture §11](blueprint/architecture.md#11-desk-reports-workflow) |
| The built desk is served from a directory. | [architecture §11](blueprint/architecture.md#11-desk-reports-workflow) |
| Vite, Vitest and a Playwright smoke test. Dev-only MPL-2.0 packages are allowed. | [dependency evaluation §12](blueprint/dependency-evaluation.md#12-frontend) |

### M5 App model (2026-10-29)

| Decision | Recorded in |
|---|---|
| Design the app model for the long term (see §1.1 and §1.2). | [ADR 0012](adr/0012-app-model-and-public-api-boundary.md) |
| One crate, `forgebase`, is the public API. | [ADR 0012](adr/0012-app-model-and-public-api-boundary.md) |
| DocTypes are embedded in the binary. | [ADR 0012](adr/0012-app-model-and-public-api-boundary.md) |
| Several apps in an explicit order, with `depends_on`. | [ADR 0012](adr/0012-app-model-and-public-api-boundary.md) |
| App SQL migrations. | [ADR 0012](adr/0012-app-model-and-public-api-boundary.md) |
| `new-app` scaffold that includes an AGENTS.md. | [ADR 0012](adr/0012-app-model-and-public-api-boundary.md) |
| `forgebase::testing` helpers. | [ADR 0012](adr/0012-app-model-and-public-api-boundary.md) |
| Example apps that touch no `crates/`. | [ADR 0012](adr/0012-app-model-and-public-api-boundary.md) |

### M6 Jobs and scheduler (2026-10-30, "setuju semua")

| Decision | Recorded in |
|---|---|
| PostgreSQL queue with no Redis; the outbox is filled from hooks. | [ADR 0013](adr/0013-jobs-scheduler-and-transactional-outbox.md) |
| A job and its completion commit in one transaction. | [ADR 0013](adr/0013-jobs-scheduler-and-transactional-outbox.md) |
| Jobs run as the system context. | [ADR 0013](adr/0013-jobs-scheduler-and-transactional-outbox.md) |
| A scheduler without a leader; UTC schedules only, no cron. | [ADR 0013](adr/0013-jobs-scheduler-and-transactional-outbox.md) |
| Retry defaults. | [ADR 0013](adr/0013-jobs-scheduler-and-transactional-outbox.md) |
| Built-in clean-up jobs. | [ADR 0013](adr/0013-jobs-scheduler-and-transactional-outbox.md) |
| CLI-only operations. | [ADR 0013](adr/0013-jobs-scheduler-and-transactional-outbox.md) |
| ~~P-1: keep `dead` jobs until an operator retries them (the first reading of decision 8)~~ (superseded, row below). | [ADR 0013](adr/0013-jobs-scheduler-and-transactional-outbox.md#implementation-details-m62) |
| **Delete `dead` jobs 7 days after they finish**, like `done` jobs: *"hapus juga job dead setelah 7 hari"* (given in the working session; supersedes P-1). | [ADR 0013](adr/0013-jobs-scheduler-and-transactional-outbox.md#implementation-details-m62) |
| P-2: ADR 0010 gets a status note about the clean-up job (delivered with UX.1). | [ADR 0010](adr/0010-authentication-and-sessions.md) |

### Milestone UX (2026-10-30)

| Decision | Recorded in |
|---|---|
| UX-1: UX before M7: *"UI nya simple and clean, lalu UX yg user friendly"*. | [milestones](blueprint/milestones.md) |
| UX-2: a field without `label` gets the humanized name (`order_date` gives "Order Date"). The DocType `label` stays required (M1 decision). | [architecture §3](blueprint/architecture.md#3-metadata-doctype-model-proposed) |
| UX-2: `read_only`, `title_field` and `list_fields` in metadata. | [architecture §3](blueprint/architecture.md#3-metadata-doctype-model-proposed) |
| UX-3: sidebar grouped by app, plus breadcrumbs. | [architecture §11](blueprint/architecture.md#11-desk-reports-workflow) |
| UX-4: still no UI component library. | [architecture §11](blueprint/architecture.md#11-desk-reports-workflow) |
| ~~UX-5: the Forgebase yellow as the accent~~ (superseded by T2, the UX.3 navy decision of 2026-10-30, below). | [architecture §11](blueprint/architecture.md#11-desk-reports-workflow) (UX-5 marked superseded there by UX.3) |

### M7 Site management (2026-10-30, "setuju semua")

| Decision | Recorded in |
|---|---|
| A database per site. | [ADR 0014](adr/0014-multi-site-database-per-site.md) |
| `forgebase.toml`, with database URLs read from environment variables. | [ADR 0014](adr/0014-multi-site-database-per-site.md) |
| Site chosen by `Host`. | [ADR 0014](adr/0014-multi-site-database-per-site.md) |
| Apps per site. | [ADR 0014](adr/0014-multi-site-database-per-site.md) |
| One worker per site. | [ADR 0014](adr/0014-multi-site-database-per-site.md) |
| Backup and restore with `pg_dump` and `pg_restore`. | [ADR 0014](adr/0014-multi-site-database-per-site.md) |

### UX.3 Tailwind and corporate palette, and M8 Workflow and reports (2026-10-30, "oke setuju semua dengan plan mu"; information session)

These decisions were approved in the information session. Their ADRs and milestone entries are written by the UX.3 and
M8 pull requests. T1 to T3 are recorded in [ADR 0017](adr/0017-desk-styling-tailwind.md) (UX.3,
[#31](https://github.com/agilma/Forgebase/pull/31)); T4 (UX.3 before M8.1) is recorded in ADR 0017 and the
[milestones](blueprint/milestones.md). The workflow row is recorded in [ADR 0015](adr/0015-workflow-states-and-transitions.md)
(M8.1, [#32](https://github.com/agilma/Forgebase/pull/32)). The saved views and group-by summary row is recorded in
[architecture §11](blueprint/architecture.md#11-desk-reports-workflow) and [§7](blueprint/architecture.md#7-api-rest-resource-api-implemented-in-m33-metadata-and-session-routes-in-m41-the-rest-proposed)
(M8.3, [#35](https://github.com/agilma/Forgebase/pull/35)). The query reports row is recorded in
[ADR 0016](adr/0016-query-reports-read-only-execution.md) (M8.4, [#37](https://github.com/agilma/Forgebase/pull/37)). Decision 10 (the split into
M8.1 to M8.5) is recorded in the [milestones](blueprint/milestones.md#m8-workflow-and-reports) and delivered by M8.5, which
touches no file under `crates/`. **All M8 rows are recorded.**

| Decision | Recorded in |
|---|---|
| T1: **Tailwind CSS v4** as a build-time styling tool. Still no component kits. *"UI pakai tailwind agar tampilan ui clean, simpel, elegan dan cocok untuk corporate"* | [ADR 0017](adr/0017-desk-styling-tailwind.md) |
| T2: **Navy #1B2A5C** (the logo background) is the primary UI colour (supersedes UX-5). Yellow is used only in the logo: *"jangan warna kuning pakai warna background dari logo"*. | [ADR 0017](adr/0017-desk-styling-tailwind.md) |
| ~~T3: system font stack.~~ (superseded by U2, below: Inter is self-hosted; the system stack stays as the fallback.) | [ADR 0017](adr/0017-desk-styling-tailwind.md) |
| Workflow lives in the DocType file. A transition needs both the role and the underlying right. A DocType with a workflow changes state only through transitions. `before_transition` hook; transition log. | [ADR 0015](adr/0015-workflow-states-and-transitions.md) |
| Saved views: private per user; shared views only by Administrator. Group-by summary. | [architecture §11](blueprint/architecture.md#11-desk-reports-workflow), [§7](blueprint/architecture.md#7-api-rest-resource-api-implemented-in-m33-metadata-and-session-routes-in-m41-the-rest-proposed) (M8.3) |
| Query reports: app-defined SQL, limited by role, run in a read-only transaction with limits. No CSV export and no charts in M8. | [ADR 0016](adr/0016-query-reports-read-only-execution.md) |
| Decision 10: M8 is split into M8.1 to M8.5; M8.5 (the example apps and the guide, and closing M8) touches no file under `crates/`: the example uses the public `forgebase` API only, so an app never needs a change in the framework (owner thinking 2). Delivered 2026-10-30: M8 is Done. | [milestones, M8](blueprint/milestones.md#m8-workflow-and-reports), [guide](guide/workflow-and-reports.md), [ADR 0015](adr/0015-workflow-states-and-transitions.md) |

### UX.4 desk polish (2026-10-30, "setuju semua rencana mu")

The owner asked for a smoother, more elegant desk: *"apa bisa ui dibuat lebih smoothy agar terkesan elegan high kualitas, seperti frappe? tapi tetap pakai warna dasar yang sama"*, and approved the plan (and the M9 plan, below) in the information session with *"setuju semua rencana mu"*. The base colours do not change (navy `#1B2A5C`, yellow only in the logo: T2 holds). Frappe is a visual and behavioural reference only: no Frappe code or asset is copied. The decisions are recorded in [ADR 0017](adr/0017-desk-styling-tailwind.md) (section "UX.4") and in [architecture §11](blueprint/architecture.md#11-desk-reports-workflow) and [§7](blueprint/architecture.md#7-api-rest-resource-api-implemented-in-m33-metadata-and-session-routes-in-m41-the-rest-proposed); the pull request is the one that implements them.

| Decision | Recorded in |
|---|---|
| U1: UX.4 runs between M8.1 and M8.2, so that every M8 desk screen is built in the polished style. | [ADR 0017](adr/0017-desk-styling-tailwind.md), [milestones](blueprint/milestones.md) |
| U2: **Inter, self-hosted**, from `@fontsource-variable/inter` (OFL-1.1, Latin subset); the OFL-1.1 licence is allowed **for fonts only**. Supersedes T3 (the system stack stays as the fallback). | [ADR 0017](adr/0017-desk-styling-tailwind.md), [dependency evaluation §12](blueprint/dependency-evaluation.md#12-frontend) |
| U3: a curated set of **Lucide SVG icons copied into the repository** (ISC, with the licence notice; not a dependency), plus an optional `icon` key on DocTypes (validated against one list). | [ADR 0017](adr/0017-desk-styling-tailwind.md), [architecture §3](blueprint/architecture.md#3-metadata-doctype-model-proposed) |
| U4: the look and the motion of the mockup: tokens, filled inputs, pill badges, 12 px cards, 150 to 200 ms CSS motion that `prefers-reduced-motion` turns off. | [ADR 0017](adr/0017-desk-styling-tailwind.md), [architecture §11](blueprint/architecture.md#11-desk-reports-workflow) |
| U5: a **command bar** on Ctrl/Cmd+K: navigation only, on the client, no new API, no storage. | [architecture §11](blueprint/architecture.md#11-desk-reports-workflow) |
| U6: form in cards plus a right-hand info panel (the workflow history joins it in M8.2). | [architecture §11](blueprint/architecture.md#11-desk-reports-workflow) |
| U7: **link titles**: a permission-aware addition to the list and read routes (`?link_titles=1`); a target the caller cannot read gives no title. | [architecture §7](blueprint/architecture.md#7-api-rest-resource-api-implemented-in-m33-metadata-and-session-routes-in-m41-the-rest-proposed), [security model](blueprint/security-model.md#the-http-layer-implemented-in-m33) |

Order of the work, as the information session set it: M8.1 (done), then UX.4, then M8.2 to M8.5, then M9.1 to M9.5.

**Licence notices with the build (2026-10-30, information session).** The owner approved the recommendation with *"setujui rekomendasi putusanmu"*: ship Inter's OFL-1.1 text and the Lucide ISC notice with the built desk (`dist/THIRD-PARTY-LICENSES.txt`), included in the M8.3 pull request, with a test that keeps the copy current. The same approval covers a cosmetic fix (a list's primary column header shows one label, the title field's label) and confirms that the native date input format stays as it is (no custom date picker).

| Decision | Recorded in |
|---|---|
| Ship `THIRD-PARTY-LICENSES.txt` (Inter OFL-1.1, Lucide) in `desk/dist/`; refreshed on a font or icon upgrade. | [ADR 0017](adr/0017-desk-styling-tailwind.md) (UX.4, "Licence notices"), [maintenance policy](blueprint/maintenance-policy.md), [dependency evaluation §12](blueprint/dependency-evaluation.md#12-frontend) |

### M9 Integration (2026-10-30, "setuju semua rencana mu"; information session)

The owner approved the M9 plan in the information session (the same *"setuju semua rencana mu"* as UX.4). **M9 starts after M8 is Done**; this section is the only record of its decisions until the M9 pull requests land (the plan itself is in the information session). They build on M3 (auth, API tokens, permissions), M5 (apps, the public API), M6 (jobs and the outbox), M7 (sites) and M8. The owner's thinking applies: a small explicit public API, deny by default, no runtime code, extension through apps only, and a significant dependency is an owner decision (decisions 6 and 9 are that approval). Not in M9: GraphQL, RPC by dotted path, guest endpoints, inbound webhook receivers, OAuth or SSO, a webhook UI in the desk, Excel, CSV import of child tables, sync between sites. M10 needs a new plan.

| Decision | Recorded in |
|---|---|
| 1: app method endpoints `<app>.<method>`, `POST /api/v1/method/...` with JSON in and out; roles are required or the method says `any_user()`; no guest methods. | planned: the M9.1 pull request (ADR 0018) |
| 2: methods run as the caller; the documents handle enforces the caller's permissions; one transaction per call. | planned: the M9.1 pull request (ADR 0018) |
| 3: webhooks are configured per site at runtime (a table and the `forgebase webhook` commands). | planned: the M9.2 pull request (ADR 0019) |
| 4: webhook delivery through the outbox, signed with HMAC-SHA256, at least once. | planned: the M9.2 pull request (ADR 0019) |
| 5: an SSRF guard (https only, no loopback, private, link-local or metadata addresses unless allowed per webhook, a timeout, no redirects, secrets never printed). | planned: the M9.2 pull request (ADR 0019) |
| 6: the HTTP client is `hyper-util` plus `hyper-rustls` (ring) and `rustls-native-certs`: **the owner approved these dependencies and their licences**. | planned: the M9.2 pull request (dependency evaluation) |
| 7: new rights `export` and `import` (ADR 0011 extended). | planned: the M9.3 pull request |
| 8: import is all-or-nothing, with a dry run, per-row errors, insert or an explicit upsert, CSV and JSON. | planned: the M9.3 pull request |
| 9: the `csv` crate reads CSV: **the owner approved this dependency**. | planned: the M9.3 pull request (dependency evaluation) |
| 10: fixtures are inserted if missing, updated only when the fixture's record changed, and never deleted (the answer to the open question 11 of the feature matrix). | planned: the M9.4 pull request (ADR 0012 surface update) |
| 11: M9 is split into M9.1 to M9.5, and M9.5 (the example apps) touches no file under `crates/`. | planned: [milestones](blueprint/milestones.md) |

### UX.5 corporate refinement (2026-10-30, "setuju D"; information session)

The owner looked at the desk after UX.4 and was happy with the UX but not with the look of the text fields: *"coba check dan plan kan ui yang cocok agar keliatan untuk UI untuk corporate saya melihat masih kurang sreg UI nya, kalau UX saya sudah suka ... kurang suka dengan text field nya"*. Shown the options, the owner added *"coba kamu check frappe 16 UI nya bagus, abu² text nya lebih dibuat lebih soft"* and chose variant D ("soft neutral") with *"setuju D"*. The information session recorded the approval; the working session delivers it as a small pull request right after M8.3 (#35, which already held its desk part when UX.5 was approved; UX.5 restyles those screens too) and before M8.4. The decision **refines UX-4/U4 field styling** (it does not supersede U4: the look, the motion and the rest of U4 stand), and **T2 is unchanged: navy `#1B2A5C` stays the only accent**, yellow only in the logo. Visual only: no UX, backend or dependency change. Frappe (the public `frappe-ui` package, MIT) was a reference for token values only; no code or asset is copied. The details, the token set, the contrast table and the deviation are in [ADR 0017](adr/0017-desk-styling-tailwind.md) (section "UX.5") and [architecture §11](blueprint/architecture.md#11-desk-reports-workflow).

| Decision | Recorded in |
|---|---|
| F1: **field style D**: borderless `#F3F3F3` fills (hover `#EDEDED`, white with a `#C7C7C7` border on focus, a visible keyboard ring, a pink error state), labels above the fields. The owner **accepted the WCAG 1.4.11 trade-off**: the boundary of a borderless filled field is about 1.1:1 (3:1 is asked); mitigated by labels, the fill, hover and focus states and a visible keyboard focus; text contrast stays AA. | [ADR 0017](adr/0017-desk-styling-tailwind.md) (UX.5), [architecture §11](blueprint/architecture.md#11-desk-reports-workflow) |
| F2 to F4 (as numbered in the information session's approved plan): the **neutral grey scale** that replaces the bluish slate (page `#F8F8F8`, flat cards with an `#EDEDED` border, softer text: body `#383838`, labels `#6B6B6B`), the **child table** as one bordered grid with plain, text-like cells (the active cell white with a `#C7C7C7` outline), and **buttons** (32 px, 8 px radius, secondary white with a `#E2E2E2` border, no shadow) with **neutral badges**. The plan file held by the working session does not number these items, so they are listed together here rather than guessing the numbering. | [ADR 0017](adr/0017-desk-styling-tailwind.md) (UX.5) |

**Numbering confirmed (2026-10-30, information session).** The information session gave the numbering of the approved items, which the row above lists together: **F1** field style D (soft neutral, in the Frappe 16 direction) with the documented WCAG 1.4.11 trade-off; **F2** the child table as a soft, spreadsheet-like grid; **F3** the neutral grey palette and softer text colours across the desk (navy unchanged); **F4** delivering it as a small UX.5 pull request before further desk work ([#36](https://github.com/agilma/Forgebase/pull/36)). The information session also accepted the two deviations of the implementation: `#6B6B6B` for table captions and placeholders (the plan's lighter greys fail AA for text) and a navy focus ring at 60 % opacity. Details: [ADR 0017](adr/0017-desk-styling-tailwind.md) (UX.5).

### CI cost (2026-10-30, "setuju C-1 sampai C-5"; information session)

The private repository hit GitHub's usage limit. The owner: *"maaf saya suruh hapus karena repo private kena limit usage $12 lebih saya kena peringatan github"*, and approved the plan with *"setuju C-1 sampai C-5"*. Measured before the change: one run was 7 jobs and about 24 job-minutes (check 14.0, e2e 3.2, deny 2.3, app-template 2.1, sites 1.4, msrv 0.8, desk 0.5), about 121 runs in 2.2 days, no dependency cache. The checks themselves are unchanged; which ones run, and when, depends on the trigger and the changed paths.

| Decision | Recorded in |
|---|---|
| C-1: cache the Cargo registry, git db and `target/` with the GitHub-owned `actions/cache` (pinned by SHA; a new CI action approved by this decision), `CARGO_INCREMENTAL=0`; the weekly run and `workflow_dispatch` on main warm the cache that pull requests share. | [maintenance policy](blueprint/maintenance-policy.md#ci), [dependency evaluation](blueprint/dependency-evaluation.md#11-supply-chain-tooling-and-msrv), [`ci.yml`](../.github/workflows/ci.yml) |
| C-2: draft pull requests skip CI; the working session opens drafts, checks locally and marks ready only after review PASS (section 10.3 unchanged: CI green on the head before merge). | [AGENTS.md section 10.2](../AGENTS.md#102-roles-in-the-working-session), [maintenance policy](blueprint/maintenance-policy.md#ci), [`ci.yml`](../.github/workflows/ci.yml) |
| C-3: no run on push to main; the schedule is weekly; `workflow_dispatch` stays. | [maintenance policy](blueprint/maintenance-policy.md#ci), [`ci.yml`](../.github/workflows/ci.yml) |
| C-4: path filters without a third-party action: a `changes` job and a tested classifier script; unknown paths count as Rust; a `docs` job checks links offline. | [maintenance policy](blueprint/maintenance-policy.md#ci), [`ci.yml`](../.github/workflows/ci.yml), [CONTRIBUTING](../CONTRIBUTING.md#local-checks) |
| C-5: `msrv` and `deny` become one job, `msrv-deny`, with one cache. | [maintenance policy](blueprint/maintenance-policy.md#ci), [`ci.yml`](../.github/workflows/ci.yml) |

### Repository public (2026-10-31; information session)

The owner moved the repository and made it public: *"repo sudah saya ganti public"*. The owner keeps the `agilma` account for these sessions: *"saya tetap pakai agil"*.

| Decision | Recorded in |
|---|---|
| The repository was transferred to `tvstore/Forgebase` and made **public**. Repository URLs in `Cargo.toml`, the new-app templates, the README and the docs were updated; older entries of this file keep their `agilma/Forgebase` links, which redirect. | [README](../README.md), [`Cargo.toml`](../Cargo.toml) |
| Licence wording: the project is open source under the MIT licence and the repository is public since 2026-10-31. This supersedes the 2026-10-28 instruction not to claim the project is open source while the repository is private, and the "intended to be open source" licence row. | [AGENTS.md section 2](../AGENTS.md#2-core-engineering-principles), [ADR 0007](adr/0007-license-mit.md), [LICENSE](../LICENSE) |
| The sessions keep the `agilma` account. | this file |
| **Pause of 2026-10-30, cancelled 2026-10-31.** The owner had paused the project because of the billing block: *"oke project kita pending sampai awal bulan, jangan lupa kasih notifikasi di awal bulan waktu github actions reset"*. The transfer and the public decision cancel the pause: public repositories get free standard-runner minutes. | this file, CI cost section above |


### Docs front page and deploy (2026-10-31, "setuju R1, D1, D2"; information session)

The owner asked: *"kamu bisa bersihkan readme, buat readme seperti guidline atau documentation forgebase, lalu saya mau coba deploy yang gratis dimana?"*, and approved the plan with *"setuju R1, D1, D2"*.

| Decision | Recorded in |
|---|---|
| **R1**: the README becomes the documentation front page (what it is, key features, a desk screenshot, a quick start, an index, status, contributing, licence). The long walkthroughs move, unchanged, to `docs/guide/` and `docs/README.md` indexes all documents. | [README](../README.md), [docs/README.md](README.md), [CHANGELOG](../CHANGELOG.md) |
| **D1**: a multi-stage `Dockerfile`, `.dockerignore`, `deploy/` (compose with PostgreSQL, serve, worker and Caddy) and a copy-pasteable deploy guide for a free VM (Oracle Cloud Always Free or any Ubuntu VM). CI builds the image only when its files change. No new dependency. | [deploy guide](guide/deploy.md), [`Dockerfile`](../Dockerfile), [maintenance policy](blueprint/maintenance-policy.md#container-images) |
| **D2**: single-container mode (`serve` and `worker` in one container) as an alternative for small machines; separate services stay the recommended layout. `migrate` on start behind `FORGEBASE_MIGRATE_ON_START=1`, default off. | [deploy guide](guide/deploy.md#2-single-container-mode), [`deploy/entrypoint.sh`](../deploy/entrypoint.sh) |
| **Render + Neon is deferred (superseded 2026-10-31: M9.0 added TLS to PostgreSQL and the Render + Neon path, see the section "Postgres TLS and ci-result" below).** Forgebase connects to PostgreSQL with `NoTls`, and Neon (and Render's Postgres from outside Render) require TLS; Render's free tier also has no shell for the first Administrator. A TLS dependency for PostgreSQL needs an owner decision, which is **pending with the owner**. The guide describes no workaround. | [deploy guide](guide/deploy.md#3-render--neon-free-path), [dependency evaluation, TLS note](blueprint/dependency-evaluation.md#tls-note) |
| The standing instruction of 2026-10-30 to keep at most the 10 newest GitHub Actions runs is **superseded**: the repository is public, so Actions minutes are free and the information session stopped pruning. The old row is marked, not rewritten. | the CI cost section above |

### Postgres TLS and ci-result (2026-10-31, "setuju ci-result dan setuju M9.0"; information session)

The owner approved two proposals at once with *"setuju ci-result dan setuju M9.0"*: the aggregate CI job `ci-result`, and **M9.0**, a step before M9.1 that adds TLS to PostgreSQL so that the free Render + Neon path works (the pending question of the "Docs front page and deploy" section above).

| Decision | Recorded in |
|---|---|
| **M9.0: TLS to PostgreSQL per `sslmode`.** No `sslmode` or `disable`: no TLS; `require`, `verify-ca` and `verify-full`: TLS with full verification (chain and host name, stricter than libpq on purpose); `prefer` and `allow` refused; roots from the operating system or only the `sslrootcert` file. Applies to the pool, `migrate`, the maintenance and inspection connections, `site backup` and `site restore` (`PGSSLMODE`, `PGSSLROOTCERT`), and per-site URLs. | [ADR 0018](adr/0018-postgres-tls.md), [security model](blueprint/security-model.md), [operations guide](guide/operations.md#tls-to-postgresql) |
| **M9.0: the dependency `tokio-postgres-rustls` is approved**, with rustls and the `ring` provider; `rustls-native-certs` (approved on 2026-10-30 for M9.2) is shared: **one TLS stack** in the tree, with no `aws-lc-rs`, OpenSSL or `webpki-roots`. | [dependency evaluation, section 13](blueprint/dependency-evaluation.md#13-tls-to-postgresql-m90) |
| **M9.0: the Render + Neon free path** is documented: a Neon URL with `sslmode=require` (the direct endpoint), the first Administrator created by running `user create` **locally** against the Neon URL, a Render Web Service from the repository's `Dockerfile` in `all` mode. **No new bootstrap feature.** | [deploy guide](guide/deploy.md#3-render--neon-free-path) |
| **M9 ADR numbers shift by one.** M9.0 takes ADR 0018, so the methods ADR of M9.1 is 0019 and the webhooks ADR of M9.2 is 0020. The "planned" rows of the M9 section above still say 0018 and 0019: read them as 0019 and 0020. | [milestones](blueprint/milestones.md#m9-integration) |
| **`ci-result`: one aggregate job, the single required check.** Job name exactly `ci-result`; `if: always()`; `needs` every other job; it fails when any needed job's result is `failure` or `cancelled` (skipped is fine) and succeeds otherwise. Implemented fail-closed: any result other than `success` or `skipped` fails. | [`ci.yml`](../.github/workflows/ci.yml), [maintenance policy](blueprint/maintenance-policy.md#ci) |
| **The owner's plan for a branch ruleset on `main`** (the owner enables it in GitHub, it is not a repository file): deletions restricted and force pushes blocked; a pull request required, with 0 approvals; merge commits only, no linear history required; no "up to date" requirement; once `ci-result` exists on `main`, it becomes the single required status check. | [maintenance policy](blueprint/maintenance-policy.md#ci) |

### M9.1 app method endpoints (2026-10-31; working session)

M9.1 implements owner decisions 1, 2 and 11 of the M9 plan above (the owner's *"setuju semua rencana mu"* of 2026-10-30). The rows of that section for decisions 3 to 10 stay "planned" and are recorded by M9.2 to M9.5. **No new owner decision was taken by the working session**: the design choices below are implementation details of the approved plan, reported to the information session and reviewed with the pull request.

| Decision | Recorded in |
|---|---|
| **1 (recorded by this pull request):** *"app method endpoints `<app>.<method>`, `POST /api/v1/method/...` with JSON in and out; roles are required or the method says `any_user()`; no guest methods."* A method with neither, both or an empty role list is a startup error; a method the caller may not call is the same 404 as an unknown or uninstalled one. | [ADR 0019](adr/0019-app-method-endpoints.md) (the M9 row said 0018; M9.0 took that number), [milestones](blueprint/milestones.md#m9-integration) |
| **2 (recorded by this pull request):** *"methods run as the caller; the documents handle enforces the caller's permissions; one transaction per call."* The handle is a `Caller::User` and the one evaluator decides; every error and a panic roll the transaction back; a job enqueued by a method exists only with the commit. | [ADR 0019](adr/0019-app-method-endpoints.md), [security model](blueprint/security-model.md#app-methods-m91-adr-0019) |
| **11 (recorded by this pull request):** *"M9 is split into M9.1 to M9.5, and M9.5 (the example apps) touches no file under `crates/`."* M9 is In progress; M9.0 and M9.1 are delivered. | [milestones](blueprint/milestones.md#m9-integration) |
| 3 to 10: to be recorded by M9.2 to M9.5 (webhooks 3 to 6, import and export 7 to 9, fixtures 10); the "planned" rows above stand. | the M9.2 to M9.5 pull requests |
| **Report connections** (the carry-over *"after a report runs, reset the pooled connection's session state"*, within M8 decision 8): the working session chose to **close the report's connection instead of returning it to the pool**, because it cannot miss a kind of state, where `RESET ALL` and `pg_advisory_unlock_all()` list them; the cost is a new connection after a report. | [ADR 0016, amendment](adr/0016-query-reports-read-only-execution.md#amendment-2026-10-31-m91-a-reports-connection-is-not-pooled-again), [security model](blueprint/security-model.md#query-reports-m84-adr-0016) |
| **A breaking change of the 0.x API** inside the approved plan: the methods of `Documents` return `DocError` instead of `JobError`; `?` into `JobError` is unchanged. Approved as a design finding by the orchestrator of the working session, with `Documents::transition`. | [ADR 0012, public surface update (M9.1)](adr/0012-app-model-and-public-api-boundary.md#public-surface-update-m91), [CHANGELOG](../CHANGELOG.md) |


### M9.2 outgoing webhooks (2026-10-31; working session)

M9.2 implements owner decisions 3 to 6 of the M9 plan above (the owner's *"setuju semua rencana mu"* of 2026-10-30). The rows of that section for decisions 7 to 10 stay "planned" and are recorded by M9.3 and M9.4. **No new owner decision was taken by the working session**: the design choices below are implementation details of the approved plan (the specification of the orchestrator), reported to the information session and reviewed with the pull request.

| Decision | Recorded in |
|---|---|
| **3 (recorded by this pull request):** *"webhooks are configured per site at runtime (a table and the `forgebase webhook` commands)."* The table is `fb_webhook` (migration 0010) in the site's own database, with the commands `add`, `list`, `disable`, `enable`, `remove`, `rotate-secret` and `test`; there is no desk screen and no HTTP route. | [ADR 0020](adr/0020-outgoing-webhooks.md) (the M9 row said 0019), [CLI guide](guide/cli.md#webhook-commands-m92), [milestones](blueprint/milestones.md#m9-integration) |
| **4 (recorded by this pull request):** *"webhook delivery through the outbox, signed with HMAC-SHA256, at least once."* Every document operation of `forgebase-db` enqueues `forgebase.deliver_webhook` in its own transaction; the signature is `HMAC-SHA256(secret, "<timestamp>.<body>")`; the delivery id is the job id and is the same on every retry. | [ADR 0020](adr/0020-outgoing-webhooks.md), [webhooks guide](guide/webhooks.md), [architecture, section 7](blueprint/architecture.md#7-api-rest-resource-api-implemented-in-m33-metadata-and-session-routes-in-m41-the-rest-proposed) |
| **5 (recorded by this pull request):** *"an SSRF guard (https only, no loopback, private, link-local or metadata addresses unless allowed per webhook, a timeout, no redirects, secrets never printed)."* The host is resolved by Forgebase, every address is checked, the connection is pinned to the vetted addresses; `--allow-private` and `--insecure-http` are explicit per-webhook flags shown by `list`; 10 seconds; a 3xx is a permanent failure. | [ADR 0020](adr/0020-outgoing-webhooks.md), [security model](blueprint/security-model.md#outgoing-webhooks-m92-adr-0020) |
| **6 (recorded by this pull request):** *"the HTTP client is `hyper-util` plus `hyper-rustls` (ring) and `rustls-native-certs`: the owner approved these dependencies and their licences."* The lockfile grows by three crates (`hyper-rustls`, `try-lock`, `want`; Apache-2.0 OR ISC OR MIT, MIT, MIT); no `aws-lc-rs`, OpenSSL or `webpki-roots`; one `rustls`. | [dependency evaluation, section 14](blueprint/dependency-evaluation.md#14-outgoing-webhooks-m92) |
| **Carry-over of the M9.1 review: the swallowed read error.** The working session closed the known gap of ADR 0019 by running the reads of `Documents` in a savepoint (the option the specification preferred), at a measured cost of about 150 µs per read on the loopback interface. | [ADR 0019, amendment](adr/0019-app-method-endpoints.md#amendment-2026-10-31-m92-reads-run-in-a-savepoint) |
| **A choice inside the plan: the snapshot is the full document, not filtered by permissions** (a webhook is configured by an operator for a system the operator chose). It is documented as a risk, with `allow_private` and `insecure_http`. | [ADR 0020](adr/0020-outgoing-webhooks.md#the-snapshot), [security model](blueprint/security-model.md#outgoing-webhooks-m92-adr-0020) |

### M9.3 import and export (2026-10-31; working session)

M9.3 implements owner decisions 7 to 9 of the M9 plan above (the owner's *"setuju semua rencana mu"* of 2026-10-30). The rows of that section for decisions 7 to 9 stay "planned" (this file is append-only) and are recorded here; decision 10 is recorded by M9.4. **No new owner decision was taken by the working session**: the design choices below are implementation details of the approved plan (the specification of the orchestrator), reported to the information session and reviewed with the pull request.

| Decision | Recorded in |
|---|---|
| **7 (recorded by this pull request):** *"new rights `export` and `import` (ADR 0011 extended)."* `Right::Export` and `Right::Import`, keys of `[[permissions]]`, deny by default (**no role may export or import until an app grants it**; the migration note is in the CHANGELOG). Export needs `read` and `export`; import needs `import` and `create` (and `write` for an upsert); `if_owner` limits an export to the caller's own documents; every imported row still checks its own rights. `examples/sales` grants `export` on `customer` and `sales_order` to Sales User and Sales Manager and `import` on `customer` to Sales Manager. | [ADR 0011, amendment](adr/0011-permission-model-phase-1.md#amendment-2026-10-31-m93-the-export-and-import-rights), [ADR 0021](adr/0021-import-and-export.md), [security model](blueprint/security-model.md#import-and-export-m93-adr-0021) |
| **8 (recorded by this pull request):** *"import is all-or-nothing, with a dry run, per-row errors, insert or an explicit upsert, CSV and JSON."* One transaction, a savepoint per row, every row through the normal pipeline as the caller; any error rolls everything back (422 `import_failed` with the first 100 row errors); `dry_run=1` always rolls back and changes nothing, jobs and webhook deliveries included; upsert is an explicit mode by `name`; at most 10 000 rows and 16 MiB. Measured: about 37 s for 10 000 rows of a simple DocType (debug build). | [ADR 0021](adr/0021-import-and-export.md), [HTTP API guide](guide/http-api.md#import-and-export-m93), [CLI guide](guide/cli.md#data-commands-export-and-import-m93), [desk guide](guide/desk.md) |
| **9 (recorded by this pull request):** *"the `csv` crate reads CSV: the owner approved this dependency."* `csv` 1.4.0 and `csv-core` 0.1.13 (Unlicense OR MIT) are new; the CSV writer is hand-written. | [dependency evaluation, section 15](blueprint/dependency-evaluation.md#15-import-and-export-m93) |
| **A choice inside the plan: the formula mitigation covers text only.** A text value that starts with `=`, `+`, `-`, `@`, a tab or a carriage return gets a leading `'` in CSV; numbers, dates and booleans are never prefixed (`-5` stays `-5`). It is always on; CSV import does not strip the `'`, and JSON keeps exact text. | [ADR 0021](adr/0021-import-and-export.md#export-get-apiv1exportdoctype), [security model](blueprint/security-model.md#import-and-export-m93-adr-0021) |
| **A choice inside the plan: the CSV has no byte order mark** (Excel then needs "From Text/CSV" with UTF-8, or JSON); the importer accepts one. | [ADR 0021, alternatives](adr/0021-import-and-export.md#alternatives-considered) |
| **Carry-overs of the M9.2 review:** a refused `get` inside a handler is tested; a webhook secret given with `--secret-stdin` must have at least 16 bytes; a snapshot that cannot be written as JSON is sent as `"data": null, "unavailable": true` instead of failing the write; the Azure WireServer `168.63.129.16` is on the SSRF blocklist. | [ADR 0020, amendment](adr/0020-outgoing-webhooks.md#amendment-2026-10-31-m93-three-carry-overs-of-the-m92-review) |


### M9.4 fixtures (2026-11-01; working session)

M9.4 implements owner decision 10 of the M9 plan above (the owner's *"setuju semua rencana mu"* of 2026-10-30). The row of that section for decision 10 stays "planned" (this file is append-only) and is recorded here. Decision 11 is already recorded by M9.1; M9.5 touches no file under `crates/`. **No new owner decision was taken by the working session**: the design choices below are implementation details of the approved plan (the specification of the orchestrator), reported to the information session and reviewed with the pull request.

| Decision | Recorded in |
|---|---|
| **10 (recorded by this pull request):** *"fixtures are inserted if missing, updated only when the fixture's record changed, and never deleted (the answer to the open question 11 of the feature matrix)."* A checksum per record in `fb_fixture` (migration 0011); a record with no row is inserted, or kept if its document exists; the same checksum does nothing, even if the site edited or deleted the document; another checksum updates only the fields the record has, or inserts again if the site deleted the document; nothing is ever deleted. | [ADR 0022](adr/0022-fixtures.md), [feature matrix](blueprint/feature-matrix.md), [ADR 0012, public surface update (M9.4)](adr/0012-app-model-and-public-api-boundary.md#public-surface-update-m94) |
| **A choice inside the plan: only DocTypes with field naming**, because the name must be the same on every site; submittable DocTypes, DocTypes with a workflow and child DocTypes are refused (a fixture update must not fight the lifecycle or the workflow). | [ADR 0022](adr/0022-fixtures.md#only-field-naming-and-why) |
| **A choice inside the plan: one transaction per app**, through the document pipeline as `fixture:<app>`; hooks, validation, naming and webhooks run (a fixture write fires webhooks). A failing record rolls back that app's fixtures and fails `migrate`, after the migrations before it are applied. | [ADR 0022](adr/0022-fixtures.md#through-the-pipeline-in-one-transaction-per-app) |
| **A choice inside the plan: no new dependency.** The checksum is SHA-256 from `sha2` (a direct dependency since M9.2) of the canonical JSON (compact, sorted keys). | [ADR 0022](adr/0022-fixtures.md#the-checksum-and-the-canonical-json) |

### M9.5 the example apps and M9 Done (2026-11-01; working session)

M9.5 implements owner decision 11 of the M9 plan (*"M9 is split into M9.1 to M9.5, and M9.5 (the example apps) touches no file under `crates/`."*, the owner's *"setuju semua rencana mu"* of 2026-10-30) and completes M9. **No new owner decision was taken by the working session**: this pull request touches no file under `crates/` and adds no dependency. This file is append-only, so the "planned" rows of the section "M9 Integration (2026-10-30 ...)" above stand; the table below records where each decision of that plan was recorded and delivered. **M9 is Done (2026-11-01).**

| Decision | Recorded in |
|---|---|
| **1** (methods `<app>.<method>`, roles or `any_user`, no guest methods): recorded and delivered by M9.1, #43; shown by `sales.order_summary` in M9.5. | [M9.1 section](#m91-app-method-endpoints-2026-10-31-working-session), [ADR 0019](adr/0019-app-method-endpoints.md), [integration guide](guide/integration.md#1-app-methods-another-system-asks-a-question) |
| **2** (methods run as the caller, one transaction): recorded and delivered by M9.1, #43. | [M9.1 section](#m91-app-method-endpoints-2026-10-31-working-session), [ADR 0019](adr/0019-app-method-endpoints.md) |
| **3** (webhooks configured per site at runtime), **4** (delivery through the outbox, HMAC-SHA256) and **5** (the SSRF guard): recorded and delivered by M9.2, #44; M9.5 adds a webhook on the submit of a sales order delivered to a test receiver in CI. | [M9.2 section](#m92-outgoing-webhooks-2026-10-31-working-session), [ADR 0020](adr/0020-outgoing-webhooks.md), [integration guide](guide/integration.md#2-webhooks-forgebase-tells-another-system) |
| **6** (the HTTP client dependencies `hyper-util`, `hyper-rustls` with `ring` and `rustls-native-certs`, approved by the owner): recorded and delivered by M9.2, #44 (and M9.0, #42, for the shared TLS stack). | [M9.2 section](#m92-outgoing-webhooks-2026-10-31-working-session), [dependency evaluation](blueprint/dependency-evaluation.md) |
| **7** (the rights `export` and `import`), **8** (import all-or-nothing with a dry run, per-row errors, insert or upsert, CSV and JSON) and **9** (the `csv` crate, approved by the owner): recorded and delivered by M9.3, #45; M9.5 shows both rights on `customer` and a dry-run import in the guide. | [M9.3 section](#m93-import-and-export-2026-10-31-working-session), [ADR 0021](adr/0021-import-and-export.md), [integration guide](guide/integration.md#3-import-and-export-files-in-and-out) |
| **10** (fixtures: insert if missing, update only when the record changed, never delete): recorded and delivered by M9.4, #46; M9.5 ships `fixtures/customer_group.json` in the `sales` example. | [M9.4 section](#m94-fixtures-2026-11-01-working-session), [ADR 0022](adr/0022-fixtures.md), [integration guide](guide/integration.md#4-fixtures-records-the-app-ships) |
| **11** (M9 split into M9.1 to M9.5; M9.5 touches no file under `crates/`): recorded by M9.1, #43; kept by M9.5, whose diff has no file under `crates/`. | [M9.1 section](#m91-app-method-endpoints-2026-10-31-working-session), [milestones](blueprint/milestones.md#m9-integration) |
| **M9 is Done (2026-11-01).** Delivered by #42 (M9.0), #43, #44, #45, #46 and the M9.5 pull request. The exit criteria of the plan, with their evidence and what is only partly evidenced, are in the milestones. The next milestone, M10, has not started. | [milestones](blueprint/milestones.md#m9-exit-criteria-and-evidence) |

### M10 Customization (2026-10-31, "setuju semua rencana mu"; information session)

The owner approved the M10 plan in the information session on 2026-10-31: *"setuju semua rencana mu"* (all ten decisions). **M10 starts after M9 is Done** and has not started; this section is the only record of its decisions until the M10 pull requests land.

| Decision | Recorded in |
|---|---|
| 1: app overlays (an app adds fields to a DocType of an app it depends on) and site custom fields (per site, at runtime). | planned: the M10.1 pull request |
| 2: overlays only add fields (every type except Table); no property setters in M10. | planned: the M10.1 pull request |
| 3: site custom field names start with `custom_`. | planned: the M10.1 pull request |
| 4: disabling a site custom field hides it everywhere; its column and data are kept. | planned: the M10.1 pull request |
| 5: running `serve` and `worker` pick up site changes without a restart, within about 10 seconds (a per-site metadata version, an atomic swap of that site's registry). | planned: the M10.1 pull request |
| 6: field levels 0 to 9 (`perm_level`, a rule's `level = n`); fields above the caller's levels are omitted everywhere and cannot be filtered, sorted or grouped by; query reports are exempt. | planned: the M10.2 pull request |
| 7: user permissions: for a restricted user every non-empty Link to the target DocType must point to an allowed value (empty links stay visible), applied in SQL on every path; query reports are exempt. | planned: the M10.3 pull request |
| 8: customization through the CLI and an Administrator-only API; no admin UI. | planned: the M10.1 and M10.3 pull requests |
| 9: sharing is Later. | planned: [milestones](blueprint/milestones.md) |
| 10: M10 is split into M10.1 to M10.4, and M10.4 (desk checks, example apps and the guide) touches no file under `crates/`. | planned: [milestones](blueprint/milestones.md) |

### M10.1 metadata overlays (2026-11-01; working session)

M10.1 implements owner decisions 1 to 5 and 8 of the M10 plan above (the owner's *"setuju semua rencana mu"* of 2026-10-31). The rows of the section "M10 Customization (2026-10-31 ...)" stay "planned" (this file is append-only); where each decision is recorded and delivered is below. **No new owner decision was taken by the working session.** Two design details were chosen by the orchestrator of the working session **within the plan; they are not owner decisions**, are disclosed here and in the ADR, and can be changed by the owner.

| Decision | Recorded in |
|---|---|
| **1 (recorded by this pull request):** app overlays (`App::extend_doctype`, an overlay file holds only `[[fields]]`, only a dependency's DocType) and site custom fields (`fb_custom_field`, per site, at runtime). | [ADR 0023](adr/0023-metadata-overlays.md), [ADR 0012, public surface update (M10.1)](adr/0012-app-model-and-public-api-boundary.md#public-surface-update-m101), [architecture section 10](blueprint/architecture.md#10-customization-accepted-for-overlays-and-custom-fields-adr-0023-delivered-in-m101) |
| **2 (recorded by this pull request):** overlays only add fields (every type except Table); no property setters. | [ADR 0023](adr/0023-metadata-overlays.md#decision), [feature matrix](blueprint/feature-matrix.md) |
| **3 (recorded by this pull request):** site custom field names start with `custom_`; the prefix is reserved, so an app's own DocType and overlay fields may not start with it either (a startup error, in the CHANGELOG). | [ADR 0023](adr/0023-metadata-overlays.md#app-overlays), [CHANGELOG](../CHANGELOG.md) |
| **4 (recorded by this pull request):** disabling a site custom field hides it everywhere; its column and data are kept. | [ADR 0023](adr/0023-metadata-overlays.md#site-custom-fields), [security model](blueprint/security-model.md#customization-overlays-and-site-custom-fields-m101-adr-0023) |
| **5 (recorded by this pull request):** `serve` and `worker` pick up site changes without a restart, within about 10 seconds (a per-site `fb_meta_version`, an atomic swap of the site's registry, one snapshot per request). | [ADR 0023](adr/0023-metadata-overlays.md#live-reload) |
| **8 (recorded by this pull request, with M10.3):** customization through the command line and an Administrator-only API; no admin UI. M10.1 delivers `forgebase customize field` and the routes under `/api/v1/customize`; the user permission commands and routes follow in M10.3. | [ADR 0023](adr/0023-metadata-overlays.md#the-interface), [CLI guide](guide/cli.md), [HTTP API guide](guide/http-api.md) |
| **A choice inside the plan, not an owner decision: site custom fields cannot be `required` or `read_only` in M10.1.** `required`: the existing documents have no value, and there is no default concept. `read_only`: only hooks write such a field and a site field has no hooks. `unique` and `allow_on_submit` are allowed. | [ADR 0023](adr/0023-metadata-overlays.md#site-custom-fields) |
| **A choice inside the plan, not an owner decision: `enable` is added** (`forgebase customize field enable`, `POST .../enable`) next to the plan's `add|list|disable`, as the natural inverse; without it a disabled name could never be used again. | [ADR 0023](adr/0023-metadata-overlays.md#site-custom-fields) |
| **Other choices inside the plan:** overlays on child DocTypes are refused; no new dependency (`std::sync::RwLock<Arc<Registry>>`, not `arc-swap`); the `doc` and `data` commands do not accept a custom field as input. | [ADR 0023](adr/0023-metadata-overlays.md) |

**Correction (2026-11-01, after the Stage 3 review of the M10.1 pull request).** The last row above says the `doc` and `data` commands do not accept a custom field as input. That limit was removed before merge: they read their files first, then check the input against the site's effective registry, so enabled custom fields work and disabled ones are refused ([ADR 0023](adr/0023-metadata-overlays.md#the-interface)). The other rows stand.

### M10.2 field permission levels (2026-11-01; working session)

M10.2 implements owner decision 6 of the M10 plan above (the owner's *"setuju semua rencana mu"* of 2026-10-31). The row 6 of the section "M10 Customization (2026-10-31 ...)" stays "planned" (this file is append-only); where it is recorded and delivered is below. **No new owner decision was taken by the working session.** The design details below were chosen by the orchestrator of the working session **within the plan; they are not owner decisions**, are disclosed here and in the ADR, and can be changed by the owner.

| Decision | Recorded in |
|---|---|
| **6 (recorded by this pull request):** field levels 0 to 9 (`perm_level` on a field, `level = n` on a `[[permissions]]` rule); a field above the caller's levels is omitted everywhere (get, list, summary, export, saved views, the metadata API) and cannot be filtered, sorted or grouped by, with the error of an unknown field; write to an unwritable field is 422 (`field_not_permitted`); hooks see the whole document; Administrator and the system context bypass; query reports are exempt. | [ADR 0024](adr/0024-field-permission-levels.md), [ADR 0011, amendment (M10.2)](adr/0011-permission-model-phase-1.md#amendment-2026-11-01-m102-field-permission-levels), [security model](blueprint/security-model.md#authorization-implemented-in-m32-adr-0011), [getting started: field permission levels](guide/getting-started.md#field-permission-levels) |
| **A choice inside the plan, not an owner decision: a field of a child DocType cannot have a `perm_level`.** A table replaces its rows on an update, so a hidden child column would be wiped; the `perm_level` of the Table field of the parent protects the whole table. | [ADR 0024](adr/0024-field-permission-levels.md#definition-errors) |
| **A choice inside the plan, not an owner decision: a rule of a level grants `read` and `write` only, never with `if_owner`, and never `write` without `read`.** Document rights are decided at level 0; `if_owner` at level 0 already limits the documents. | [ADR 0024](adr/0024-field-permission-levels.md#definition-errors) |
| **A choice inside the plan, not an owner decision: the naming field, the `title_field`, a `list_fields` column and a `required` field stay at level 0.** They are shown to everybody who can read a document, and there is no default value, so a caller who cannot write a required field could never save a document. A level that no rule names is legitimate: only Administrator and the system context see it. | [ADR 0024](adr/0024-field-permission-levels.md#definition-errors) |
| **Details of the working session (not owner decisions):** input for a hidden field is an unknown field whatever the value (null too) and is reported by an early check, alone, before the hooks, so it cannot be told from a missing field; a new `ValidationError::Hidden` replaces a validation problem about a hidden field (a stored value that went invalid, a hook's value) so that no field name or value reaches the caller; a shared saved view that names a hidden field is not listed to a caller who cannot read it. | [ADR 0024](adr/0024-field-permission-levels.md#the-leak-review) |

### M10.3 user permissions (2026-11-01; working session)

M10.3 implements owner decisions 7 and 8 of the M10 plan above (the owner's *"setuju semua rencana mu"* of 2026-10-31). The rows 7 and 8 of the section "M10 Customization (2026-10-31 ...)" stay "planned" (this file is append-only); where each is recorded and delivered is below. **No new owner decision was taken by the working session.** The design details below were chosen by the orchestrator of the working session **within the plan; they are not owner decisions**, are disclosed here and in the ADR, and can be changed by the owner.

| Decision | Recorded in |
|---|---|
| **7 (recorded by this pull request):** user permissions: for a restricted user every non-empty Link to the target DocType must point to an allowed value (an empty Link stays visible), applied in SQL on every path (get, list, summary, export, the locking reads of update, delete, submit, cancel, amend and transitions, link resolution, link titles; import and methods inherit it); Administrator and the system context bypass; query reports are exempt. | [ADR 0025](adr/0025-user-permissions.md), [ADR 0011, amendment (M10.3)](adr/0011-permission-model-phase-1.md#amendment-2026-11-01-m103-user-permissions), [security model](blueprint/security-model.md#authorization-implemented-in-m32-adr-0011), [getting started: user permissions](guide/getting-started.md#user-permissions) |
| **8 (completed by this pull request; M10.1 delivered `customize`):** management through the command line (`forgebase user permission add|remove|list`) and an Administrator-only API (`/api/v1/user-permissions`); no admin UI. | [ADR 0025](adr/0025-user-permissions.md#who-can-change-it-and-when-it-applies), [CLI guide](guide/cli.md), [HTTP API guide](guide/http-api.md) |
| **A choice inside the plan, not an owner decision: child rows do not restrict their parent.** A Link in a row of a Table field does not decide whether the parent is visible (simple, predictable SQL: one condition per top-level field). The Link of a row is still resolved when written. | [ADR 0025](adr/0025-user-permissions.md#the-rule) |
| **A choice inside the plan, not an owner decision: a user restricted on a DocType cannot create or amend a document of it** (`PermissionDenied`). The new name could not be checked against the allowed names, and the user would not see the result. | [ADR 0025](adr/0025-user-permissions.md#every-path) |
| **A choice inside the plan, not an owner decision: only direct Links restrict, and a Link is resolved with the whole scope of its target.** A restriction does not propagate through a chain (an order is not hidden because its customer is hidden by its group), while linking to a hidden customer is refused like linking to a missing one. | [ADR 0025](adr/0025-user-permissions.md#the-rule) |
| **A choice inside the plan, not an owner decision: at most 1000 documents per user and DocType**, refused on `add` with a message (the names are bound as one array in every read). | [ADR 0025](adr/0025-user-permissions.md#who-can-change-it-and-when-it-applies) |
| **Details of the working session (not owner decisions):** a disabled site custom Link field restricts nothing (it is not in the effective registry); `add` of an existing row and `remove` of a missing row succeed and say so; the API answers 201 or 200 (`added`) and `POST .../remove` (`removed`), as this API has no `DELETE` with a body; `add` takes `--doctypes` like `webhook add`; the permissions are read with the roles of every request, and a database that has not run migration 0013 yet serves its users without restrictions; the allowed names are bound as one text array literal (the list builder binds typed values). | [ADR 0025](adr/0025-user-permissions.md) |

### M10.4 examples, desk checks, the guide and M10 Done (2026-11-01; working session)

M10.4 implements owner decisions 9 and 10 of the M10 plan above (the owner's *"setuju semua rencana mu"* of 2026-10-31) and completes M10. The rows of the section "M10 Customization (2026-10-31 ...)" stay "planned" (this file is append-only); where each decision is recorded and delivered is below. **No new owner decision was needed**, and no file under `crates/` changed.

| Decision | Recorded in |
|---|---|
| **1 to 5** (overlays and site custom fields, add only, the `custom_` prefix, disable keeps the data, live reload within about 10 s): recorded and delivered by M10.1, #48; shown end to end by M10.4 (`sales_extension` overlay, `examples/sites/ci.sh`, the guide). | [M10.1 section](#m101-metadata-overlays-2026-11-01-working-session), [ADR 0023](adr/0023-metadata-overlays.md), [guide](guide/customizing.md#site-custom-fields) |
| **6** (field levels, hidden fields omitted everywhere and not filterable, reports exempt): recorded and delivered by M10.2, #49; M10.4 makes `credit_limit` of the `sales` example a level 1 field. | [M10.2 section](#m102-field-permission-levels-2026-11-01-working-session), [ADR 0024](adr/0024-field-permission-levels.md), [guide](guide/customizing.md#field-permission-levels) |
| **7** (user permissions: every non-empty Link must match, empty visible, SQL on every path, reports exempt) and **8** (command line and Administrator API, no admin UI): recorded and delivered by M10.3, #50; M10.4 restricts a Sales User to one customer group in CI and in the desk specs. | [M10.3 section](#m103-user-permissions-2026-11-01-working-session), [ADR 0025](adr/0025-user-permissions.md), [guide](guide/customizing.md#user-permissions) |
| **9** (sharing is Later): recorded by M10.4; the guide says what is not covered and the feature matrix keeps sharing Later. | [guide](guide/customizing.md#what-is-not-covered), [feature matrix](blueprint/feature-matrix.md) |
| **10** (M10 split into M10.1 to M10.4; M10.4 touches no file under `crates/`): recorded by M10.1 to M10.3 and kept by M10.4, whose diff has no file under `crates/`. | [milestones](blueprint/milestones.md#m10-customization) |
| **M10 is Done (2026-11-01).** Delivered by #48 (M10.1), #49 (M10.2), #50 (M10.3) and the M10.4 pull request. The exit criteria of the plan, with their evidence and what is only partly evidenced, are in the milestones. No next milestone is approved; it is to be planned. | [milestones](blueprint/milestones.md#m10-exit-criteria-and-evidence) |
| **Details of the working session (not owner decisions):** the `sales` app has no role that writes level 0 and only reads level 1, so the read-only desk spec uses an e2e-only role (Credit Auditor) added by `seed.sh`, not by the example; the example's `credit_limit` is covered for a Sales User by the `403` that comes before the field (no write right on `customer`). | [milestones](blueprint/milestones.md#m10-exit-criteria-and-evidence) |
