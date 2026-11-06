# 0023 Metadata overlays

- Status: Accepted (owner decisions 1 to 5 and 8 of the M10 plan, 2026-10-31, *"setuju semua rencana mu"*; the design below is the specification of the M10.1 working session and is reviewed with its pull request).
- Date: 2026-11-01

## Context

A DocType is owned by exactly one app, and no app may redefine another app's DocType ([ADR 0012](0012-app-model-and-public-api-boundary.md)). That keeps a definition in one place, and it leaves two real needs unmet:

- **An app that extends another app** (a custom app on top of `sales`) wants a field on `sales`' `customer`. Today it can only add a DocType of its own that links to `customer`.
- **A site** (one customer's database) wants a field the apps do not have, without a rebuild or a new app: a checkbox "VIP", a reference number. In Frappe this is the Custom Field.

The owner decided ([M10 plan](../owner-decisions.md#m10-customization-2026-10-31-setuju-semua-rencana-mu-information-session), decisions 1 to 5 and 8): both, as **overlays that only add fields** on top of the apps' definitions; site fields start with `custom_`; disabling a field hides it and keeps its data; a running server and worker pick up a change within about 10 seconds; the interface is the command line and an Administrator-only API, with no admin screen. Property setters (changing a property of an existing field), Table custom fields, a Customize Form and scripts are not part of this change.

## Decision

### App overlays

```rust
App::new("sales_extension")
    .depends_on(&["sales"])
    .extend_doctype("customer", forgebase::overlays!["overlays/customer.toml"])
```

`overlays!` is `doctypes!` for overlay files (`include_str!` relative to `CARGO_MANIFEST_DIR`) and gives a `&'static [OverlayFile]`; `OverlayFile` is public, `#[non_exhaustive]`, with a hidden `__new` for the macro and a `new` for tests, like `FixtureFile`.

**An overlay file holds only `[[fields]]` entries**, written exactly like those of a DocType file. They are parsed by the same raw structure (`RawField` in `forgebase-core/src/load.rs`) and converted by the same function as a DocType's fields, so a field means the same thing everywhere and there is no second parser. Any other top-level key, and a file without a field, is an error. Every property of a field is allowed (`required`, `unique`, `read_only`, `allow_on_submit`, the type parameters), under the same rules as in a DocType.

**Applying.** `compose` applies the overlays after every app's DocTypes (and the `--doctypes` directory) are in the registry, in **app order, then file order**; each overlay appends its fields, in file order, after the DocType's own fields and the fields of the overlays before it. The resulting DocType is validated again by the DocType validation that already exists (names, `allow_on_submit`, `read_only`, naming), and the reference checks that follow (Link targets, the app rule) see the new fields. The hooks of every app (the owner's too) see the fields like any other field, because they read the same DocType.

**Startup errors**, collected with the other app problems, each naming the app and the file. An overlay is refused when:

- its target DocType is unknown;
- the target is **not owned by one of the app's dependencies** (the transitive set that Link fields and fixtures use). The app's **own** DocTypes are refused too: define the field in the DocType's file;
- the target is a **child** DocType (kept out of M10.1 to stay simple: a child's rows have no document, name or hooks of their own);
- a field has the type **Table** (the rows of a child DocType cannot be added afterwards);
- a field name is already taken by the DocType, by an earlier overlay (of this app or another) or twice in the file. The message names **both apps** and the file that defined the field first;
- a field name starts with **`custom_`**.

**The reserved prefix** is for site custom fields. It is also a startup error for an app's **own DocType** field (and for a `--doctypes` file). The prefix keeps the two name spaces apart for good: a site field can never collide with a field an app adds in a later version. (No existing example, template or test field used it.)

**Owners.** The registry records, for each field that was added after the DocType's definition, who added it: an app (its name) or the site (`Registry::field_source`, `Registry::field_owner`). It is used by the errors above and is available to later work; the metadata API does not expose it.

**Two cautions for the author of an overlay, documented where `App::extend_doctype` is:** a `required` field added to a DocType that already has documents makes the next update of each of them fail until the field is filled in (the framework has no default value concept; this is the app author's responsibility, as it is for a DocType's own new required field); a `unique` field creates a unique index over the existing rows, which hold NULL for it, and NULLs never collide.

**Schema.** `migrate` adds the columns with the schema sync it already has: the overlay fields are in the registry, so the planner sees a missing column like any other.

### Site custom fields

**Storage** (framework migration 0012):

```sql
CREATE TABLE fb_custom_field (
  doctype text NOT NULL, name text NOT NULL,
  definition jsonb NOT NULL,        -- the field as JSON, with the keys of a TOML [[fields]] entry
  enabled boolean NOT NULL DEFAULT true,
  created_at timestamptz NOT NULL DEFAULT now(), modified_at timestamptz NOT NULL DEFAULT now(),
  modified_by text NOT NULL,
  PRIMARY KEY (doctype, name));
CREATE TABLE fb_meta_version (id int PRIMARY KEY CHECK (id = 1), version bigint NOT NULL);
INSERT INTO fb_meta_version VALUES (1, 0);
```

A row is never deleted. The stored definition is parsed by the **same `RawField` deserialisation** (serde from JSON) and validated by the same code as an overlay field.

**The rules of a site field** (`Registry::add_custom_field`), on top of those of an overlay:

- the name starts with `custom_` and is a valid field name;
- the type is any type except Table; the DocType exists and is **not a child DocType**;
- a Link target must exist and not be a child DocType (a site field is added to a complete registry, so this is checked at once);
- **`required` and `read_only` are refused in M10.1.** `required`: the existing documents have no value and there is no default concept. `read_only`: only hooks write such a field, and a site field has no hooks. These two limits are a **design detail of M10.1** chosen by the working session within the approved plan, not an owner decision and not property setters; they can be lifted by a later decision (a default value for `required`; a hook capability for `read_only`). `unique`, `allow_on_submit` (submittable DocTypes only, as for any field), labels and the type parameters are allowed.

**Changing fields** is done by `forgebase-db` functions that both the CLI and the API call:

- `add_custom_field`: in **one transaction**, (1) take the **advisory lock of `migrate`** (the same key, so schema changes are serialised with it) with a `lock_timeout` (5 s, the default of `migrate`; the commands have no option for it in M10.1); (2) build the site's effective registry inside the transaction and validate the field against it (a name used by an enabled or a **disabled** field is a clear "exists already" error: bring a disabled field back with `enable`); (3) insert the row; (4) `ALTER TABLE ... ADD COLUMN` through the **statements of the schema sync** (`plan::field_steps` uses the same `add_column_sql`, foreign key, index and unique-index builders, so the column type mapping, the Link foreign key `ON DELETE RESTRICT` and its index and a unique index are exactly what `migrate` would create); (5) increment `fb_meta_version`. A DocType whose table does not exist yet is refused (run `migrate` first).
- `set_custom_field_enabled(doctype, name, enabled)` flips the flag, sets `modified_*` and increments the version. It **never drops a column or data**.
- `list_custom_fields(doctype?)` returns enabled and disabled fields.

**Disable and enable.** (A disabled **Link** field keeps its foreign key `ON DELETE RESTRICT`: deleting a document that is referenced only by a hidden field fails with `409 linked_document_exists`, and the message does not name the hidden field. Enable the field and clear it, or clear the column with SQL.) A disabled field is not in the effective registry, so it is hidden everywhere (the metadata API, input, filters, sort, group-by, export, import, the documents' JSON, webhook snapshots): the framework has no code that knows about "disabled". Its column and data stay in the table. **Enabling** brings the field back with its data. `enable` is **not in the plan's command list** (`add|list|disable`); it is the natural inverse, avoids a dead end (a name can never be used again) and is a disclosed small addition of the working session.

**`migrate` and custom columns.** `migrate` syncs the schema from the registry of the apps **plus every stored custom field, enabled or disabled**, so the column of each exists again after a restored backup or a field added while an old binary was running; it is idempotent and uses the same planner. A stored field that no longer validates is skipped with a warning.

### The effective metadata of a site

The registry of the apps (with their overlays) plus the site's **enabled** custom fields, in `(doctype, created_at, name)` order, appended after the fields of the app and its overlays. `Registry` is `Clone` and its hooks are shared `Arc`s, so building it is "clone the base registry and append the fields".

**A stored field that no longer validates** (its Link target left the apps, its DocType is gone, a rule of a later version) is **skipped with a warning** on standard error that names the site, the DocType and the field. The site still starts: a bad custom field must never take a site down. A site that an older binary migrated (no migration 0012 yet) is served with the registry of the apps alone, until `migrate` runs.

### Live reload

- **A holder per site** (`forgebase_db::RegistryHolder`): the **base registry**, a `std::sync::RwLock<Arc<Registry>>` with the current registry and the version it was built from. `AppState::registry()` (HTTP) and the worker return an **`Arc<Registry>` snapshot**. **A request, a job attempt or a command takes one snapshot at its start and uses it to the end**, so it never sees a half-built registry or two different ones. A registry that was swapped out lives until the last holder of its `Arc` is gone.
- **The swap is atomic and never blocks readers for a rebuild**: the new registry is built completely, then the `Arc` is replaced under the write lock, which is held for one assignment. Rebuilds are serialised by an async mutex (so a slow older build can never replace a newer one), and the version and the fields are read in one `REPEATABLE READ` read-only transaction (so they belong together).
- **A poll per site**, in `serve` and in `worker`, every `meta_reload_interval` (10 s by default; an `AppConfig` field and a `WorkerConfig` field, short in tests, zero turns it off): `SELECT version FROM fb_meta_version`; if it differs from the last seen version, rebuild and swap. The `serve` poll is one task per site that ends when the site's state is dropped; the worker polls from its slots, one slot per interval. A failed poll (the database is down) is logged and the current registry is kept. **Nothing happens on the request path.**
- **The process that made a change reloads at once**: the customize routes call `reload` after the commit, so the next request on that server sees the change; other servers and workers see it within the interval.
- **Reports, methods, jobs and schedules** come from the apps and do not change; only the DocTypes' fields do.

### The interface

- **CLI**: `forgebase customize field add --doctype D --field FILE|-`, `list [--doctype D]`, `disable --doctype D NAME`, `enable --doctype D NAME`. A field is **one TOML table body**, written like a `[[fields]]` entry (`name = "custom_vip"`, `type = "check"`): one syntax, the same as in the DocType files, instead of a flag for every property. The commands run as the system context like `webhook` and `user`; `modified_by` is `cli`. The file is read and parsed before the database is opened.
- **API** (Administrator role only; any other authenticated user gets `403 permission_denied`, a cookie call without CSRF is `403 csrf_failed`): `GET` and `POST /api/v1/customize/{doctype}/fields`, `POST .../fields/{name}/disable` and `/enable`. The body is the JSON definition with the keys of the TOML. Statuses: unknown DocType `404`, a rule broken `422 validation_failed`, a taken name `409 conflict`, a created field `201`. `modified_by` is the user. No admin screen: the desk reads the metadata API and needs no change to show a field.
- **`doc` and `data` see custom fields.** These commands read their input files and look the DocType up in the apps' registry before the database is opened (an unreadable file or an unknown DocType never touches the database), then open the site and check the input, `--fields`, `--filter`, `--sort`, the CSV columns and the import against the **effective registry** of the site (`SiteContext::registry`). An enabled custom field is accepted everywhere, and a disabled one is refused as unknown. The exporter takes its columns from the DocType it is given, which is the one of the registry snapshot, so HTTP and the command line agree.

## Alternatives considered

- **`LISTEN/NOTIFY` to push changes.** Immediate, but it needs a long-lived connection per process, reconnect logic, and a fallback poll anyway (a notification is lost while a listener is disconnected). A version poll every 10 s is a few lines, has one failure mode (the database is down, and then nothing works anyway), and meets the "about 10 seconds" the owner accepted.
- **Reload on every request.** Always current, but a query on every request for a change that happens a few times a year; the poll costs one query per site per 10 s.
- **`arc-swap`.** A lock-free pointer swap; `std::sync::RwLock<Arc<Registry>>` has the same observable behaviour here (the lock is held for one assignment and a read clones an `Arc`), and no new dependency was approved.
- **A restart after a change.** The simplest, and the Frappe-like experience is "change it and it is there"; a restart of every server and worker per change is operations work for the owner of a site. Rejected by the owner's decision 5.
- **A shared registry with per-field checks** instead of a per-site registry: every path would have to know about sites and about disabled fields. A registry that is simply the truth for one site keeps every path unchanged.
- **Letting an app overlay property changes** (property setters): a larger rule set (which property, in which order, what wins) for a need nobody has yet. A non-goal.

## Non-goals

A Customize Form or any admin screen; property setters; Table custom fields; overlays on child DocTypes; `required` or `read_only` site fields (above); scripts of any kind; renaming or deleting a custom field or its column; per-field permissions (M10.2) and user permissions (M10.3), which come on top of this change.

## Consequences

- A DocType can still be defined by one app only, and now extended in two controlled ways. Every other path (insert, update, get, list, filter, sort, summary, export, import, webhooks, hooks, jobs, the metadata API and the desk) sees a field because it reads the registry; the tests prove it for an overlay and for a site field.
- `AppState::registry()` now returns a snapshot (`Arc<Registry>`), and a request holds it. These are internal crates ([ADR 0012](0012-app-model-and-public-api-boundary.md)); the public surface gains `App::extend_doctype`, `overlays!`, `OverlayFile` and a few `testing` helpers (see the amendment in ADR 0012).
- Framework migration 0012 adds two small tables. A running site costs one extra query every 10 s per process.
- Adding a field through a running server writes under the migrate lock, so it waits for a `migrate` in progress (and the reverse), up to the `lock_timeout`. The API answers a lock that stays busy with `503 lock_timeout` and `Retry-After: 5` (nothing was written); the command line prints the timeout error. If reloading the registry fails after a change committed, the server logs it and still answers `201` or `200`: the version poll catches up. A `unique` field (and a Link) builds its index in the same transaction, under the table lock and within the 5 s `lock_timeout`, which can be too short on a very large table: add such a field in a quiet period, or add the index by hand first.
- A column is never dropped by Forgebase: removing a custom field's data is a manual, deliberate SQL job.
