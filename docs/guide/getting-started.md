# Getting started

Install Forgebase and try it end to end with the example app: the full quick start, the document commands, permissions and workflow, and how apps are built. The short version is in the [README](../../README.md#quick-start-in-5-minutes).

```sh
cargo build
cargo run -p forgebase -- --version
```

## Quick start

Needs a PostgreSQL 16 server and an empty database. Connections are not encrypted unless the URL says `sslmode=require` ([TLS to PostgreSQL](operations.md#tls-to-postgresql)), so on a network that is not private add it.

The quick start runs the binary of the example app [`examples/sales`](../../examples/sales/README.md), so its DocTypes, hooks and data migration are built in and there is no `--doctypes` option. (The stock `forgebase` binary has no apps: it takes the DocTypes of a directory with `--doctypes DIR`, see [Apps](#apps).) Every command here was run against a fresh database.

```sh
export FORGEBASE_DATABASE_URL=postgres://user:password@localhost:5432/mydb

# Create the framework tables and the DocType tables, then apply the app's data migrations (add --dry-run to only print the plan; on a fresh database it shows the pending framework migrations only, the DocType plan appears once they are applied)
cargo run -p sales -- migrate

# The migration `default_customer_groups` of the app created two customer groups, Retail and Wholesale
cargo run -p sales -- doc list customer_group

# Insert another customer group, then a customer that links to it by name (the name comes from the DocType's naming field)
cargo run -p sales -- doc insert customer_group \
  --data '{"title": "Partner"}'
cargo run -p sales -- doc insert customer \
  --data '{"email": "ann@example.com", "full_name": "Ann", "credit_limit": "1250.50", "customer_group": "Partner"}'

# Read it (note its "version"), change it, then delete it (below). update and delete need --version, the version you last read;
# if someone changed the document meanwhile they fail with a conflict that shows the current version.
cargo run -p sales -- doc get customer ann@example.com
cargo run -p sales -- doc update customer ann@example.com --version 1 \
  --data '{"loyalty_points": 10}'

# A customer group that a customer still links to cannot be deleted: this command fails (exit code 1, the error names `customer`)
cargo run -p sales -- doc delete customer_group Partner --version 1
# Delete the customer first, then the group can be deleted
cargo run -p sales -- doc delete customer ann@example.com --version 2
cargo run -p sales -- doc delete customer_group Partner --version 1

# A sales order has a child table, `items`: the rows are an array in the same JSON and are saved with the order in one transaction.
# Its name comes from a naming series (SO-<year>-00001, ...); the output shows it, the rows in order and the version. The sed keeps just the name for the next commands.
# The `validate` hook of the app computes each row's `amount` (qty * rate, 2 places) and the order's `total`. A row without a rate has no amount.
cargo run -p sales -- doc insert customer \
  --data '{"email": "bob@example.com", "full_name": "Bob"}'
ORDER=$(cargo run -q -p sales -- doc insert sales_order \
  --data '{"customer": "bob@example.com", "order_date": "2026-10-28", "items": [
    {"description": "Widget", "qty": 2, "rate": "9.50"},
    {"description": "Gadget", "qty": 1}]}' | sed -n 's/^  "name": "\(.*\)",$/\1/p')

# An update that names `items` replaces its rows when they differ from the stored ones (a table with identical rows keeps its row ids); one that leaves it out keeps them.
# The hook runs again: the amount becomes 47.50 and the total 47.50.
cargo run -p sales -- doc update sales_order "$ORDER" --version 1 \
  --data '{"items": [{"description": "Widget", "qty": 5, "rate": "9.50"}]}'
cargo run -p sales -- doc delete sales_order "$ORDER" --version 2
```

## Submit, cancel and amend

`sales_order` is `submittable` and uses a naming series ([sales_order.toml](../../examples/sales/doctypes/sales_order.toml)): a new order is a **draft** (`docstatus` 0, named `SO-<year>-00001`, ...; it may have no items yet), **submitting** needs at least one item and a positive total (the `before_submit` hook of the app refuses otherwise) and locks it (`docstatus` 1; only fields marked `allow_on_submit`, here `remarks`, can still change), **cancelling** makes it `docstatus` 2, and `amend` creates a new draft from a cancelled order, named `<original>-<n>` with `amended_from` set. In the `sales` app an order also has an approval workflow (see [Workflow](#workflow)), so it is submitted and cancelled with **transitions**: `doc submit` and `doc cancel` are refused on it, and the commands below use `doc transition` (the CLI does not check the roles of a transition, the HTTP API does). `doc submit` and `doc cancel` work as before on a submittable DocType that has no workflow. Every step needs `--version`, the version you last read (each one adds 1).

```sh
# Continues the example above (the `bob@example.com` customer exists). A new order is a draft in the state "Draft": "docstatus": 0, named by the series.
NAME=$(cargo run -q -p sales -- doc insert sales_order \
  --data '{"customer": "bob@example.com", "items": [{"description": "Widget", "qty": 2, "rate": "9.50"}]}' | sed -n 's/^  "name": "\(.*\)",$/\1/p')

# An order without items can be saved and sent for approval, but not approved (approving submits it): this fails (exit code 1)
# with the hook's message "a sales order needs at least one item to be submitted". (Delete the draft again; it is version 2.)
EMPTY=$(cargo run -q -p sales -- doc insert sales_order \
  --data '{"customer": "bob@example.com"}' | sed -n 's/^  "name": "\(.*\)",$/\1/p')
cargo run -p sales -- doc transition sales_order "$EMPTY" "Submit for Approval" --version 1
cargo run -p sales -- doc transition sales_order "$EMPTY" "Approve" --version 2
cargo run -p sales -- doc delete sales_order "$EMPTY" --version 2

# Send it for approval (version 1 -> 2: "workflow_state": "Pending Approval"), then approve it (2 -> 3): "docstatus": 1.
# A submitted order is locked: this update fails (exit code 1) ...
cargo run -p sales -- doc transition sales_order "$NAME" "Submit for Approval" --version 1
cargo run -p sales -- doc transition sales_order "$NAME" "Approve" --version 2
cargo run -p sales -- doc update sales_order "$NAME" --version 3 \
  --data '{"order_date": "2026-11-01"}'
# ... and this one, on the allow_on_submit field, works (version 3 -> 4). A submitted order cannot be deleted either.
cargo run -p sales -- doc update sales_order "$NAME" --version 3 \
  --data '{"remarks": "rush delivery"}'

# Cancel it (4 -> 5: "docstatus": 2), then amend it: a new draft with the same values and rows, named "<NAME>-1", "amended_from": "<NAME>".
cargo run -p sales -- doc transition sales_order "$NAME" "Cancel" --version 4
cargo run -p sales -- doc amend sales_order "$NAME" --version 5
# The amendment is a draft again: edit it, send it for approval, and so on. A cancelled order can be amended once (the second try fails), or deleted.
cargo run -p sales -- doc delete sales_order "$NAME" --version 5
```

`doc submit`, `doc cancel` and `doc amend` take `--actor` like the other commands and no data (`doc transition` takes the action and `--version`); the output of a submittable DocType includes `docstatus` (0, 1 or 2) and `amended_from`. `docstatus` and `amended_from` cannot be set through `--data`.

A DocType file can also say how the desk should show it (UX.1). A field without `label` gets its humanized name (`order_date` is "Order Date"; write `label = "Quantity"` where that is not good English), `read_only = true` marks a field that only hooks may write (a client that sends a value for it is refused with a 422; it cannot be `required` or a `table`), and `title_field` and `list_fields` name the field that names a document and the columns of the list:

```toml
name = "sales_order"
label = "Sales Order"
title_field = "customer"
list_fields = ["customer", "order_date", "total"]

[[fields]]
name = "total"
type = "decimal"
precision = 12
scale = 2
read_only = true   # computed by a hook; a client must not send it
```

DocTypes are TOML files, one per DocType, in the `doctypes/` directory of an app (embedded in its binary) or in the directory given by `--doctypes` of the stock binary (see [examples/sales/doctypes/customer.toml](../../examples/sales/doctypes/customer.toml) and the [file format](../blueprint/architecture.md#doctype-file-format)). A `link` field names its target DocType (`target = "customer_group"`); in JSON a link is the target document's name, and the database enforces it with a foreign key. A `table` field names a child DocType (`is_child = true`, see [sales_order.toml](../../examples/sales/doctypes/sales_order.toml) and [sales_order_item.toml](../../examples/sales/doctypes/sales_order_item.toml)); in JSON it is an array of row objects. A child DocType belongs to exactly one parent and has no commands of its own. A `submittable = true` DocType also has a docstatus and `doc submit`, `doc cancel` and `doc amend` (next section); `naming = { series = "SO-.YYYY.-.#####" }` names documents `SO-2026-00001`, `SO-2026-00002`, ... from a counter that is incremented in the same transaction as the insert, so numbers have no gaps per rendered prefix (date parts are the UTC date of the transaction; DocTypes whose patterns render the same prefix share one counter and see gaps, so give each DocType a distinct prefix). `--data -` reads the JSON from standard input. Decimal values are JSON strings (any form `rust_decimal` parses, such as `"1e3"` or `".5"`), dates are `YYYY-MM-DD`, datetimes are RFC 3339 with microsecond precision (finer digits are truncated). Duplicate JSON keys: the last one wins. Output keys are sorted alphabetically. DocType files: `*.toml` directly in the directory, hidden files skipped, not recursive. The tool prints the stored document as JSON; errors go to standard error as `error: ...` with exit code 1 (2 for a usage error). The `--actor` option (default `cli`) names the trusted **system context** the `doc` commands run as: it is recorded as owner / modified_by on insert, update, submit, cancel and amend, and given to the hooks (on delete it is only given to hooks, not stored); the tool has no authentication, and no permission rule applies to it (see "Permissions" below). The stock `forgebase` binary registers no lifecycle hooks; hooks are Rust code of an app built with the `forgebase` crate, which is why the `sales` binary computes `amount` and `total` and checks submits (see [Apps](#apps) and [ADR 0009](../adr/0009-lifecycle-pipeline-and-hooks.md)).

## List documents

`doc list` prints a page of documents as a JSON array (default 20, at most 500; child table rows are not listed). `--filter` is a JSON array of conditions combined with AND, `--sort` can be repeated (`FIELD` or `FIELD:desc`; `name` is always the last key, so paging is stable), `--limit` and `--offset` page. Operators: `=`, `!=`, `<`, `<=`, `>`, `>=`, `in` (the value is an array), `like` (text fields only; a SQL pattern with `%` and `_`), `is null` and `is not null`. Values are written as in `--data`; a link is filtered by the target's name; the standard columns `name`, `owner`, `modified_by`, `created_at`, `modified_at`, `version` and (submittable DocTypes) `docstatus` can be used too.

```sh
cargo run -p sales -- doc list customer \
  --filter '[{"field":"tier","op":"=","value":"gold"},{"field":"loyalty_points","op":">=","value":10}]' \
  --sort loyalty_points:desc --limit 10
```

## Permissions

A DocType file can list `[[permissions]]` (see [sales_order.toml](../../examples/sales/doctypes/sales_order.toml)): a `role`, the rights it grants (`read`, `write`, `create`, `delete`, `submit`, `cancel`, `amend`) and optionally `if_owner = true` (the rights then apply to documents the user owns only). A user may do what any of their roles grants; the role `Administrator` may do everything; **a DocType without rules is closed to everyone else**. Child rows follow their parent. The rules are enforced by `forgebase-db` in every operation (`insert`, `get`, `update`, `delete`, `submit`, `cancel`, `amend`, `list`), which take a `Caller`: a user with roles (`AuthUser::caller()`), or the trusted system context. A document a user is not allowed to read is treated as if it did not exist (`get`, `list`, updates, links, errors), with the known limits listed in [ADR 0011](../adr/0011-permission-model-phase-1.md). The `forgebase doc` commands run as the system context; the rules apply to real users of the [HTTP API](http-api.md#http-api) (and to Rust code that calls `forgebase_db` with `Caller::user(...)`). See [ADR 0011](../adr/0011-permission-model-phase-1.md) and the [security model](../blueprint/security-model.md).

### Field permission levels

A walkthrough on the sales example, with the requests and what each user sees, is [Customizing and restricting data](customizing.md#field-permission-levels).

Rights are per DocType, but a few fields are not for everybody: a credit limit, a cost price. Give such a field a `perm_level` from 1 to 9 (0 is the default) and let a rule with the same `level` grant read and write on it ([ADR 0024](../adr/0024-field-permission-levels.md)):

```toml
[[fields]]
name = "credit_limit"
type = "decimal"
precision = 12
scale = 2
perm_level = 1

[[permissions]]
role = "Sales User"
read = true
write = true
create = true

[[permissions]]
role = "Sales Manager"
read = true
write = true

# Level 1: the manager sees and changes credit_limit, the sales user does not even know it exists.
[[permissions]]
role = "Sales Manager"
level = 1
read = true
write = true
```

- A rule at level 0 (no `level` key) is the document rule you already know. A rule at `level = n` grants `read` and `write` on the fields at level `n` and **nothing else**: it never grants a right on documents, never with `if_owner` (the level 0 rule already limits the documents), and not `write` without `read`. A caller needs the right at level 0 as well (a `level = 1` rule alone gives nothing).
- **A field the caller cannot read does not exist for them.** It is left out of the document, the list, the summary, an export and the metadata API; a filter, sort or group-by on it is an `unknown field` error, the same as for a name that is not a field; sending it in a document is an unknown field too. A caller who can read a level but not write it gets `422 field_not_permitted` when a value would change the field (sending back the stored value is fine), and the metadata API shows the field as `read_only`.
- **An update by a user who cannot see a field keeps its value.** Hooks see every field. The Administrator role and the system context (the `forgebase doc` commands, jobs) see and write every level. Two things are **not** filtered by levels: webhook payloads (they carry the whole document) and query reports.
- A field at a level that no rule names is visible to Administrator only: a good place for something only a hook or an operator uses.
- Not allowed (the file does not load): a level outside 0 to 9; a `perm_level` on a field of a child DocType (give the `table` field of the parent a `perm_level` to protect the whole table); on the naming field, the `title_field` or a `list_fields` column (they are shown to everybody who can read a document); or together with `required` (a caller who cannot write the field could never save the document).
- Overlay fields and [site custom fields](cli.md#customize-field-commands-m101) may carry a `perm_level` too; the rules for the level are the `[[permissions]]` of the DocType they are added to.

### User permissions

A walkthrough on the sales example: [Customizing and restricting data](customizing.md#user-permissions).

Roles say what a user may do; **user permissions** say which documents. Restrict a user to some documents of a DocType and they see only those, on every path: lists, a single document, summaries, exports, links and titles, and every write ([ADR 0025](../adr/0025-user-permissions.md)). An Administrator sets them with the command line (`forgebase user permission add --user ann --doctype customer_group --value Retail`, see [the CLI guide](cli.md#user-permissions-m103)) or [the API](http-api.md#user-permissions-m103); no `[[permissions]]` file changes and no restart: they apply on the user's next request.

- **The rule.** For a DocType the user is restricted on, only the allowed documents exist for them. For **every other** DocType, a document is visible only if **each of its top-level Link fields to a restricted DocType is empty or points at an allowed document**: with the group `Retail` allowed, a customer whose `customer_group` is `Retail` or empty is visible, one in `Wholesale` is not, and a document with two Links to the group needs both to pass. A DocType with no such Link is not touched.
- **A hidden document is a missing one.** `get` is `404`, lists and exports leave it out, `update`, `delete`, `submit`, `cancel`, `amend` and workflow transitions say `not found` (before the version is compared, and the row is not even locked), and a Link to it, in a field or a child row, is refused like a Link to a name that does not exist. A Link already stored on the document is kept, so editing other fields still works.
- **Not considered:** the Links inside child-table rows (they do not hide the parent; a hidden name that is not already stored in that field of that table is refused when you write it, one already stored is kept), and chains: an order is not hidden because its customer is hidden by the customer's group. To restrict orders by group, give the order a Link to the group.
- **A restricted user cannot create** a document of the DocType they are restricted on (`403`): they could not see it. They create customers and orders as usual, as long as the Links point at what they can see.
- Combines with the rest: `if_owner` and user permissions both apply, a role without the read right is `403` first, and a Link field that a user cannot read (a `perm_level`) still restricts without being shown. The Administrator role and the system context are never restricted. **Not restricted:** query reports, webhook payloads and jobs.
- At most 1000 documents per user and DocType.

## Workflow

A DocType can have a **workflow** (M8.1, [ADR 0015](../adr/0015-workflow-states-and-transitions.md)): states, each with a docstatus, and transitions between them, written by the app in the DocType file. The first state is the initial one. A transition needs one of its `roles` **and** the right its move needs (`write` for draft to draft, `submit` for draft to submitted, `cancel` for submitted to cancelled), so a role in a workflow never grants more than `[[permissions]]` do. On a DocType with a workflow plain `submit` and `cancel` are refused (`workflow_required`): the state changes only through a transition.

```toml
[workflow]

[[workflow.states]]
name = "Draft"
docstatus = 0

[[workflow.states]]
name = "Pending Approval"
docstatus = 0
style = "warning"
edit_roles = ["Sales Manager"]   # only these roles may update a draft in this state

[[workflow.states]]
name = "Approved"
docstatus = 1
style = "success"

[[workflow.transitions]]
action = "Submit for Approval"
from = "Draft"
to = "Pending Approval"
roles = ["Sales User"]

[[workflow.transitions]]
action = "Approve"
from = "Pending Approval"
to = "Approved"
roles = ["Sales Manager"]
```

A transition is `POST /api/v1/resource/<doctype>/<name>/transition` with `{"action": "Approve", "version": N}`, or `forgebase doc transition <doctype> <name> "Approve" --version N` (the command runs as the system context, so the roles are not checked). `GET .../transitions` lists what the caller may take now, `GET .../history` lists the transitions taken (oldest first, the newest 500), and `GET /api/v1/meta/<doctype>` has the states and transitions without the roles. The document has `"workflow_state"`, which no client can set. A condition on a transition is written in Rust: implement `DocTypeHooks::before_transition` (it may veto, change values and enqueue a job; `ctx.transition` has the action and the two states). `migrate` gives the documents that exist when a workflow is added their state and warns about documents in a state the workflow no longer has. The desk shows the state as the badge and the actions you may take as buttons (M8.2, see [Desk](desk.md#desk)). The `sales` example has a full approval workflow on `sales_order` (Draft, Pending Approval, Approved, Cancelled; a Sales User approves up to 10 000, above that a Sales Manager uses "Approve as Manager": hooks see no roles, so the limit is a `before_transition` hook on the action name), a report "Sales by customer" and a shared saved view in the CI script; the guide explains them: [workflow, saved views and reports](workflow-and-reports.md).


## Apps

The stock `forgebase` binary has no apps: it loads DocTypes from `--doctypes DIR` (the quick start above uses the binary of an example app instead). Two example apps in this repository show what an app looks like, using the public `forgebase` API only (they change nothing under `crates/`): [`examples/sales`](../../examples/sales/README.md) (DocTypes, hooks that compute and refuse, a data migration, tests) and [`examples/sales_extension`](../../examples/sales_extension/README.md) (a custom app that depends on `sales`: a DocType that links to one of `sales`, and a hook on a DocType of `sales`; `cargo run -p sales_extension` is the composed binary). To build an application, make a crate that depends on `forgebase` (the only public API; `forgebase-core`, `forgebase-db` and `forgebase-http` are internal) and write apps and a binary:

```rust
// An app is a plain function that returns data: no registry, no macros that generate code.
pub fn app() -> forgebase::App {
    forgebase::App::new("sales")
        .version(env!("CARGO_PKG_VERSION"))
        .depends_on(&["crm"])
        .doctypes(forgebase::doctypes!["doctypes/sales_order.toml"])
        .hooks("sales_order", OrderHooks)
        .migrations(forgebase::migrations![(1, "backfill", "migrations/0001_backfill.sql")])
}

fn main() -> std::process::ExitCode {
    forgebase::Forgebase::new().app(crm::app()).app(sales::app()).run() // dependencies first
}
```

The binary has the same commands as the stock one (`migrate` also applies the app migrations). The stock binary has no apps, so it reports `"app": "local"` for every DocType in the metadata API (the `--doctypes` directory is the app `local`). Everything about how apps load, depend on each other, own DocTypes, order their hooks and migrate is in [ADR 0012](../adr/0012-app-model-and-public-api-boundary.md) and [the architecture](../blueprint/architecture.md#2-extension-model-accepted-adr-0006).

To start an app, generate one (a crate with a sample DocType, a hook, tests, a README and an `AGENTS.md` for its maintainers; it builds and passes its tests as created):

```sh
cargo build -p forgebase
target/debug/forgebase new-app crm --forgebase-path . --dir ~/apps    # creates ~/apps/crm
cd ~/apps/crm && FORGEBASE_TEST_DATABASE_URL=postgres://forgebase:forgebase@localhost:5432/postgres cargo test
```

The step-by-step guide (run it, extend another app, upgrade with migrations, common errors) is [docs/guide/first-app.md](first-app.md).
