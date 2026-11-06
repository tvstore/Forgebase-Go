<p align="center"><img src="docs/assets/logo-banner.svg" alt="Forgebase" width="480"></p>

<p align="center"><strong>The foundation for business applications.</strong></p>

<p align="center">
  <img src="docs/assets/badge-rust-powered.svg" alt="Rust-powered">
  <img src="docs/assets/badge-metadata-driven.svg" alt="Metadata-driven">
  <img src="docs/assets/badge-full-stack.svg" alt="Full-stack">
</p>

# Forgebase

Forgebase is a Rust framework for building business applications, inspired by the concepts and capabilities of the Frappe Framework. It is an independent implementation: no Frappe code is copied.

> Build for longevity, not complexity. Maintenance is the most expensive part of software ownership.

You describe a business entity (a customer, an order, an invoice) in a small TOML file, a **DocType**. Forgebase creates its PostgreSQL table, its REST API, its permission checks, its lifecycle (submit, cancel, amend, workflow) and a web desk with list and form views. Your own rules go in plain Rust hooks. It ships as one binary and one built desk, runs on one PostgreSQL database per site, and is built for a low long-term maintenance cost: a modular monolith with few dependencies, and every decision recorded in an [ADR](docs/adr/README.md).

<p align="center"><img src="docs/assets/desk-list.png" alt="The Forgebase desk: the list view of the Sales Order DocType with its search box, Filter, Views, Columns and Summary buttons, and a Draft, a Submitted and a Cancelled order with their status badges" width="800"></p>

## Key features

- **Metadata DocTypes**: fields, links, child tables, naming series and validation from one TOML file ([quick start](docs/guide/getting-started.md#quick-start), [architecture](docs/blueprint/architecture.md#3-metadata-doctype-model-proposed)).
- **Document lifecycle and workflow**: insert, update, delete, submit, cancel and amend, optimistic concurrency, and approval workflows with states and role-checked transitions ([workflow guide](docs/guide/workflow-and-reports.md)).
- **Permissions**: roles with per-right grants, owner-only rules and query enforcement ([security model](docs/blueprint/security-model.md#authorization-implemented-in-m32-adr-0011)).
- **REST API**: sessions with CSRF protection, API tokens, filters, sorting and paging, stable error codes ([HTTP API](docs/guide/http-api.md)).
- **Desk**: a React web interface generated from the metadata, with saved views and a command bar ([desk guide](docs/guide/desk.md)).
- **Jobs and schedules**: a transactional job queue with retries and a scheduler ([jobs and schedules](docs/guide/cli.md#background-jobs)).
- **Multi-site**: several sites on one server, one database each, chosen by the Host header ([sites](docs/guide/cli.md#sites)).
- **Backups**: `site backup` and `site restore` with the PostgreSQL client tools ([operations guide](docs/guide/operations.md#backups)).
- **Reports**: SQL query reports that run by role in a read-only transaction ([reports](docs/guide/workflow-and-reports.md#3-query-reports)).
- **Apps**: your DocTypes, hooks, jobs and migrations in a Rust crate that depends on the public API only ([first app](docs/guide/first-app.md)).
- **Webhooks**: signed JSON deliveries of document events to other systems, sent after the commit, with an SSRF guard ([webhooks guide](docs/guide/webhooks.md)).

## Quick start in 5 minutes

You need Rust (the version in [rust-toolchain.toml](rust-toolchain.toml)), Node.js 22 and a PostgreSQL 16 server with an empty database. This runs the `sales` example app. Connections to PostgreSQL are not encrypted unless the URL says `sslmode=require` (TLS with certificate verification, [operations guide](docs/guide/operations.md#tls-to-postgresql)); on a local or private network that is fine.

```sh
git clone https://github.com/tvstore/Forgebase && cd Forgebase
export FORGEBASE_DATABASE_URL=postgres://user:password@localhost:5432/mydb

(cd desk && npm ci && npm run build)                  # the desk, written to desk/dist
cargo run -p sales -- migrate                         # tables and example data
printf '%s\n' 'a long password' | cargo run -p sales -- user create admin --role Administrator
FORGEBASE_DEV_INSECURE_COOKIES=1 FORGEBASE_DESK_DIR=desk/dist cargo run -p sales -- serve
```

Open <http://localhost:8000/desk/> and log in as `admin`. (`FORGEBASE_DEV_INSECURE_COOKIES=1` is for plain HTTP on localhost only.)

**With Docker** (the same app in a container; `docker build` takes several minutes the first time):

```sh
docker build -t forgebase-sales .
docker network create fb
docker run -d --name fbpg --network fb -e POSTGRES_USER=forgebase -e POSTGRES_PASSWORD=forgebase -e POSTGRES_DB=forgebase postgres:16
until docker exec fbpg pg_isready -h 127.0.0.1 -U forgebase -d forgebase; do sleep 1; done   # wait for PostgreSQL
docker run -d --name fbapp --network fb -p 8000:8000 --restart on-failure -e FORGEBASE_DATABASE_URL=postgres://forgebase:forgebase@fbpg:5432/forgebase \
  -e FORGEBASE_MIGRATE_ON_START=1 -e FORGEBASE_ALLOWED_HOSTS=localhost -e FORGEBASE_DEV_INSECURE_COOKIES=1 forgebase-sales all
until curl -fsS -o /dev/null http://localhost:8000/desk/; do sleep 1; done                   # wait for migrate and the server
printf '%s\n' 'a long password' | docker exec -i fbapp forgebase-app user create admin --role Administrator
```

To run it on a server with HTTPS and backups, use the compose file in [deploy/](deploy/compose.yaml) and follow the [deploy guide](docs/guide/deploy.md); the same guide has a free Render + Neon path (TLS to PostgreSQL). The full walkthrough (documents, submit and cancel, permissions, workflow) is [Getting started](docs/guide/getting-started.md).

## Documentation

All guides are indexed in [docs/README.md](docs/README.md). The main ones:

- [Getting started](docs/guide/getting-started.md), [Your first app](docs/guide/first-app.md) and [the command-line tool](docs/guide/cli.md)
- [HTTP API](docs/guide/http-api.md), [Desk](docs/guide/desk.md) and [Workflow, saved views and reports](docs/guide/workflow-and-reports.md)
- [Customizing and restricting data](docs/guide/customizing.md) (overlays, site custom fields, field permission levels, user permissions)
- [Webhooks](docs/guide/webhooks.md), [Integrating with other systems](docs/guide/integration.md), [Operating Forgebase](docs/guide/operations.md) and [Deploy](docs/guide/deploy.md)
- The blueprint: [vision](docs/blueprint/vision.md), [architecture](docs/blueprint/architecture.md), [security model](docs/blueprint/security-model.md), [dependency evaluation](docs/blueprint/dependency-evaluation.md) and the [ADRs](docs/adr/README.md)

## Status and roadmap

Milestones M0 to M10 are done: documents, auth and permissions, the REST API, the desk, apps, jobs, multi-site, workflow and reports, and integration (TLS to PostgreSQL, app method endpoints, outgoing webhooks, import and export, fixtures, and the example apps with the [integration guide](docs/guide/integration.md)). Customization (M10) adds fields to another app's DocType (overlays) and to one site (custom fields, live), field permission levels and user permissions, with the walkthrough [Customizing and restricting data](docs/guide/customizing.md). No next milestone is approved yet: the next one is to be planned. The details and the roadmap are in [docs/blueprint/milestones.md](docs/blueprint/milestones.md); the full status is in [docs/README.md](docs/README.md#project-status). The repository is public and the project is open source.

## Contributing

Read [CONTRIBUTING.md](CONTRIBUTING.md) for the local checks, and [AGENTS.md](AGENTS.md), which every contributor and agent must follow (the workflow, the engineering principles, git safety).

## License

MIT. See [LICENSE](LICENSE).
