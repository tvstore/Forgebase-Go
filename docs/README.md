# Forgebase documentation

The documentation of Forgebase, a Go framework for building business applications. Start with the [README](../README.md) for what it is and its status; this page indexes everything else. The guides below describe the behaviour that the Go port reproduces from the Rust implementation; their code examples still show the Rust API until the phase that ports them ([ADR 0026](adr/0026-go-implementation.md)).

## Guides

For people who build on Forgebase or run it.

| Guide | Contents |
|---|---|
| [Getting started](guide/getting-started.md) | Install, the full quick start with the example app (documents, submit/cancel/amend, permissions, workflow) and how apps are built |
| [Your first app](guide/first-app.md) | Generate an app, run it, extend another app, upgrade with migrations, common errors |
| [The command-line tool](guide/cli.md) | Users and tokens, background and scheduled jobs, sites, backup and restore commands |
| [HTTP API](guide/http-api.md) | Logging in, CSRF, creating, listing, changing and deleting documents, API tokens, errors |
| [Desk](guide/desk.md) | The web interface: building and serving it, what it shows, the smoke test and screenshots |
| [Workflow, saved views and reports](guide/workflow-and-reports.md) | The approval workflow, saved list views and SQL query reports |
| [Webhooks](guide/webhooks.md) | Sending document events to other systems, and verifying a delivery |
| [Integrating with other systems](guide/integration.md) | A walkthrough on the sales example: an app method, a webhook, CSV import and export, fixtures, TLS to PostgreSQL |
| [Operating Forgebase](guide/operations.md) | Configuration, sites, reverse proxy, workers, upgrades, backups and restores |
| [Customizing and restricting data](guide/customizing.md) | A walkthrough on the sales example: an overlay field on another app's DocType, a site custom field, a field only managers see, a user restricted to one customer group |
| [Deploy](guide/deploy.md) | Run Forgebase on a free or small VM with Docker, HTTPS and backups |

## Reference, records and examples

| Document | Contents |
|---|---|
| [AGENTS.md](../AGENTS.md) | Permanent project instructions: engineering principles, four-stage workflow, git safety, model selection policy |
| [docs/blueprint/vision.md](blueprint/vision.md) | Purpose, target users, goals, non-goals, relation to Frappe |
| [docs/blueprint/feature-matrix.md](blueprint/feature-matrix.md) | Frappe capabilities vs Forgebase status, with evidence labels |
| [docs/blueprint/architecture.md](blueprint/architecture.md) | Architecture (partly Accepted, mostly Proposed): crates (Go packages in the port), metadata, lifecycle, API, jobs, multi-site |
| [docs/blueprint/dependency-evaluation.md](blueprint/dependency-evaluation.md) | The approved Go dependencies and CI tools; the historical Rust dependency comparison, licenses, recommendations |
| [docs/blueprint/security-model.md](blueprint/security-model.md) | Authentication, authorization, injection defense, isolation |
| [docs/blueprint/milestones.md](blueprint/milestones.md) | M0 to M10 (roadmap) and later work, with exit criteria |
| [docs/blueprint/maintenance-policy.md](blueprint/maintenance-policy.md) | Toolchain, dependency admission, licence check, CI, testing, docs and review rules |
| [CONTRIBUTING.md](../CONTRIBUTING.md) | Prerequisites and local checks |
| `examples/` | The example apps, each a Go module of its own that imports the public API only: [`sales`](../examples/sales/README.md) and [`sales_extension`](../examples/sales_extension/README.md) (their M5 parts, P4, and their jobs and schedule, P5.1; the workflow and report parts, P6; the method part, P7.1; the fixture part, P7.2; the overlay part, P7.3). [`sites`](../examples/sites/README.md) (P5.2: two sites on one server and a backup restored into a new database, run by the CI job `sites`; the workflow and shared saved view part, P6; the webhook part, P7.1; the fixture part, P7.2; the custom field and user permission parts, P7.3) |
| [docs/adr/](adr/README.md) | Architecture Decision Records, including [ADR 0026](adr/0026-go-implementation.md), the Go implementation |
| [docs/parity-report.md](parity-report.md) | The Go port against the Rust implementation: the tests mapped one by one ([appendix](parity/test-mapping.md)), the desk suite, the database check, build and CI times, every difference, the dependencies |
| [docs/owner-decisions.md](owner-decisions.md) | The owner's decisions and comments, with links to the detailed records |
| [CHANGELOG.md](../CHANGELOG.md) | Notable changes, Keep a Changelog format |

## Project status

**The Go port is complete: P0 to P7 are implemented and merged, and the [parity report](parity-report.md) is written.** Forgebase was implemented in Rust up to milestone M10 (documents, auth and permissions, the REST API, the desk, apps, jobs, multi-site, workflow and reports, integration, customization); that repository, <https://github.com/tvstore/Forgebase>, is frozen and is the reference implementation. On 2026-11-06 the owner decided to port it to Go with identical behaviour (HTTP API, database schema, metadata formats, command line, configuration) and no new features, in the phases P0 to P7 followed by a parity report ([ADR 0026](adr/0026-go-implementation.md), [owner decisions](owner-decisions.md#go-port-2026-11-06-setuju-g1g8-information-session)). The parity report links to the one decision that stays with the owner, whether to archive the frozen Rust repository (G6).

What exists now: every command and route of the Rust implementation, in the `forgebase` binary and in the binary of an app: the core, the database layer, the document lifecycle with workflow, the HTTP API and the unchanged desk, authentication and permissions (field levels and user permissions included), apps with hooks, jobs, schedules, reports, methods, webhooks, fixtures and overlays, sites with backup and restore, TLS to PostgreSQL, the container image, the generator `new-app`, the example modules `examples/sales`, `examples/sales_extension` and `examples/sites`, the public API (`forgebase`, `forgebase/testing`, [ADR 0028](adr/0028-go-public-api-of-the-app-model.md)) and the CI. The phase table is in the [README](../README.md#status) and the comparison with Rust is in the [parity report](parity-report.md). The blueprint documents (see the table above) record research, architecture and decisions; they were written for the Rust implementation and their behaviour is normative for the port. ADRs 0001 to 0025 are unchanged; [ADR 0026](adr/0026-go-implementation.md) lists the Rust-specific parts that it supersedes for this repository.

The project is open source under the MIT license (see [LICENSE](../LICENSE)).
