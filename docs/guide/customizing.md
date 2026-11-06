# Customizing and restricting data

A walkthrough of M10 on the [`sales`](../../examples/sales/README.md) example: add a field to a DocType you do not own, add a field to one site, keep a field away from most users, and show a user only some of the documents. It links the reference pages instead of repeating them; every command and request below was run against a scratch database with the composed binary of [`sales_extension`](../../examples/sales_extension/README.md) (single-site mode, so no `--site`; with a [config file](cli.md#sites) add `--site NAME`).

The pieces and where each one is decided:

| What | Who changes it | When it applies | Decision |
|---|---|---|---|
| A field an app adds to another app's DocType (an **overlay**) | the app's author, in Rust and TOML | with the next build and `migrate` | [ADR 0023](../adr/0023-metadata-overlays.md) |
| A **site custom field** | an Administrator of the server (command line or API) | within 10 seconds, no restart | [ADR 0023](../adr/0023-metadata-overlays.md) |
| A **field permission level** | the app's author, in the DocType file | with the next build | [ADR 0024](../adr/0024-field-permission-levels.md) |
| A **user permission** | an Administrator (command line or API) | on the user's next request | [ADR 0025](../adr/0025-user-permissions.md) |

The setup of the examples below: `sales` has the DocTypes `customer` and `customer_group` (the groups `Retail`, `Wholesale` and `Online` come from [fixtures](first-app.md#9-fixtures)), and the users `ann` (role `Sales User`) and `max` (`Sales Manager`).

```sh
export FORGEBASE_DATABASE_URL=postgres://user:password@localhost:5432/sales_ext   # an empty database
cargo run -p sales_extension -- migrate
echo 'a long password' | cargo run -p sales_extension -- user create ann --role "Sales User"
echo 'a long password' | cargo run -p sales_extension -- user create max --role "Sales Manager"
cargo run -p sales_extension -- doc insert customer \
  --data '{"email": "ada@example.com", "full_name": "Ada", "credit_limit": "1250.50", "customer_group": "Retail"}'
cargo run -p sales_extension -- doc insert customer \
  --data '{"email": "bob@example.com", "full_name": "Bob", "credit_limit": "99.00", "customer_group": "Wholesale"}'
cargo run -p sales_extension -- doc insert customer --data '{"email": "cy@example.com", "full_name": "Cy"}'
FORGEBASE_DEV_INSECURE_COOKIES=1 cargo run -p sales_extension -- serve
```

The `doc` commands run as the trusted system context, which sees and writes everything (see [what is not covered](#what-is-not-covered)). The HTTP examples log in with `POST /api/v1/login` first, as in the [HTTP API guide](http-api.md).

## Extending another app's DocType

`sales_extension` depends on `sales` and adds `loyalty_tier` to `customer`, a DocType it does not own, without changing `sales`. The overlay file is [`examples/sales_extension/overlays/customer.toml`](../../examples/sales_extension/overlays/customer.toml); it holds only `[[fields]]`, written exactly like those of a DocType file:

```toml
[[fields]]
name = "loyalty_tier"
label = "Loyalty Tier"
type = "select"
options = ["None", "Silver", "Gold"]
```

and the app registers it in [`src/lib.rs`](../../examples/sales_extension/src/lib.rs):

```rust
App::new("sales_extension")
    .depends_on(&["sales"])
    .extend_doctype("customer", forgebase::overlays!["overlays/customer.toml"])
```

What you get: `migrate` adds the column; `GET /api/v1/meta/customer` lists `loyalty_tier` after the fields of `sales` (and `"app": "sales"` still, the DocType is owned by `sales`); filters, sort, export, import, hooks and the desk see it; and the `[[permissions]]` of `customer` apply to it, so a Sales Manager writes it and a Sales User only reads it. It exists **only where the app is installed**: on a site that installs `sales` alone (the site `shop` of [examples/sites](../../examples/sites/README.md)) there is no column and the metadata does not list it. Tests: `the_overlay_field_is_a_field_of_customer` and `the_overlay_field_exists_only_where_the_extension_app_is_installed`.

Rules and limits (an overlay on an unknown DocType, on one of the app's own or of a non-dependency, on a child DocType, with a `table` field, a name that is taken, or one that starts with `custom_` stops the binary at startup, naming the app and file): [first-app guide](first-app.md#add-a-field-to-another-apps-doctype-overlays) and [ADR 0023](../adr/0023-metadata-overlays.md#app-overlays). Only fields can be added: no property setters, so a field's label, options or rights cannot be changed from outside.

## Site custom fields

An overlay is code. A **site custom field** is data: one site adds a field to a DocType in its own database, with no rebuild and no restart. The operator uses the command line (the field is one TOML table body, here on standard input):

```sh
printf 'name = "custom_region"\nlabel = "Region"\ntype = "data"\n' \
  | cargo run -p sales_extension -- customize field add --doctype customer --field -
# added custom field customer.custom_region (data); its column exists now, and a running server or worker uses it within 10 seconds
cargo run -p sales_extension -- customize field list --doctype customer
```

The running server picks it up within 10 seconds (it checks a per-site version; a request in progress finishes with the metadata it started with). Then `GET /api/v1/meta/customer` lists it, and it takes values like any field:

```sh
curl -s -b jar.txt -X PATCH -H "X-Forgebase-CSRF: $CSRF" -H 'Content-Type: application/json' \
  -d '{"version": 1, "data": {"custom_region": "North"}}' \
  http://localhost:8000/api/v1/resource/customer/ada@example.com
```

An Administrator can do the same over HTTP (`POST /api/v1/customize/customer/fields` with `{"name": "custom_vip", "label": "VIP", "type": "check"}`), see [the HTTP API](http-api.md#customize-fields-m101). Other users get `403` there.

- **The prefix.** The name must start with `custom_`; the prefix is reserved, so it can never collide with a field of an app. A name without it is refused (`422 validation_failed`, "the name of a site custom field must start with `custom_`").
- **Add only.** Any type except `table`; no `required` (the documents that exist have no value) and no `read_only` (nothing could write it). No rename, no delete, no change of type.
- **Disable keeps the data.** `customize field disable --doctype customer custom_region` hides the field everywhere (metadata, documents, input, filters, export, import, the desk); the **column and the data stay**, and `customize field enable` brings both back. Nothing drops a column.
- **Per site.** The field is stored in the site's database. With [several sites](cli.md#sites) on one server, adding it to `field` does not add it to `shop`; the `sites` job of CI proves that, with the live reload, in [`examples/sites/ci.sh`](../../examples/sites/ci.sh).
- **The desk** has no screen for any of this ([no admin UI](#what-is-not-covered)); it shows a custom field on the form like any other (Playwright spec in [`desk/e2e/restrict.spec.ts`](../../desk/e2e/restrict.spec.ts)).

All the commands, the rules and the lock behaviour: [CLI, customize field commands](cli.md#customize-field-commands-m101).

## Field permission levels

Rights are per DocType. For one field that not everybody may see, give the field a `perm_level` and grant that level to the roles that need it ([ADR 0024](../adr/0024-field-permission-levels.md)). In [`customer.toml`](../../examples/sales/doctypes/customer.toml) of `sales`:

```toml
[[fields]]
name = "credit_limit"
type = "decimal"
precision = 12
scale = 2
perm_level = 1

# ... the rules for the DocType (level 0): Sales User reads, Sales Manager reads and writes ...

[[permissions]]
role = "Sales Manager"
level = 1
read = true
write = true
```

A Sales User has no `level = 1` rule, so for them the field **does not exist**. Ann reads Ada; Max reads her too, with the limit:

```sh
curl -s -b ann.jar http://localhost:8000/api/v1/resource/customer/ada@example.com   # no "credit_limit" key at all
curl -s -b max.jar http://localhost:8000/api/v1/resource/customer/ada@example.com   # ... "credit_limit": "1250.50", ...
```

- **Absent everywhere, not just blank.** The field is left out of get, list, summary, export and `GET /api/v1/meta/customer` (Ann's metadata lists `email, full_name, notes, ...` and no `credit_limit`). A filter, sort or group-by on it is the same error as for a name that is no field: `{"error": {"code": "invalid_query", "message": "invalid filter: filter 1: unknown field \"credit_limit\" ..."}}`. So Ann cannot probe for it. A role that may write level 0 but not level 1 gets the same unknown-field error when it sends the field in a document; Ann, who has no write right on `customer` at all, gets `403 permission_denied` before any field is looked at.
- **Read-only.** A role with a `level = 1` rule that has `read = true` and no `write` reads the field but cannot change it: the desk shows plain text, the metadata marks the field `read_only`, and a value that would change it is `422 field_not_permitted` (sending the stored value back is fine). The Playwright spec `a role that reads level 1 but not writes it sees credit_limit as plain text` shows it with an e2e-only role.
- **An update by someone who cannot see the field keeps its value.** Hooks see the whole document. The Administrator role and the system context see and write every level.
- A field at a level that no rule names is visible to Administrators only.

The syntax, the definition errors (no level on the naming field, a `title_field`, a `list_fields` column, a `required` field or a child field) and the leak review are in [getting started](getting-started.md#field-permission-levels) and [ADR 0024](../adr/0024-field-permission-levels.md). Tests: `a_sales_user_does_not_see_the_credit_limit_anywhere` and the four tests after it in [`examples/sales/tests/sales.rs`](../../examples/sales/tests/sales.rs), the field-level suites of `forgebase-http` and `forgebase`, and the Playwright specs.

## User permissions

Roles say what a user may do; user permissions say **which documents**. Restrict Ann to the customer group `Retail`:

```sh
cargo run -p sales_extension -- user permission add --user ann --doctype customer_group --value Retail
# restricted user ann to customer_group Retail; it applies on the user's next request (at most 1000 per DocType)
cargo run -p sales_extension -- user permission list --user ann
# USER  DOCTYPE         VALUE   ADDED
# ann   customer_group  Retail  2026-11-01T09:31:13.533168Z
```

On Ann's next request (no restart, no new login):

```sh
curl -s -b ann.jar 'http://localhost:8000/api/v1/resource/customer?limit=50'          # Ada (Retail) and Cy (no group); not Bob (Wholesale)
curl -s -o /dev/null -w '%{http_code}\n' -b ann.jar http://localhost:8000/api/v1/resource/customer/bob@example.com   # 404
curl -s -b ann.jar http://localhost:8000/api/v1/resource/customer_group               # only Retail
curl -s -b ann.jar http://localhost:8000/api/v1/meta/customer_group                   # "rights": {"create": "none", ...}
```

- **The rule.** For the DocType Ann is restricted on (`customer_group`), only `Retail` exists. For every other DocType a document is visible only if **each non-empty Link to a restricted DocType is allowed**: a customer with `customer_group` `Retail` or **empty** is visible, one in `Wholesale` is not. A DocType with no such Link is not touched.
- **A hidden document is a missing one,** on every path: get is `404`, lists, summaries and exports leave it out, updates, deletes and workflow transitions say not found (before the version is compared), and a Link to it is refused like a name that does not exist. Max, a Sales Manager, once restricted to `Retail` in the same way, creating a customer in `Wholesale` gets `422 ... customer_group Wholesale does not exist`.
- **Empty links stay visible,** so a customer without a group is not lost to a restricted user. Chains do not propagate: an order is not hidden because its customer is hidden by the customer's group (give the order a Link to the group to restrict orders by group).
- **Create is refused** for the DocType the user is restricted on: a Sales Manager restricted to `Retail` gets `403` when creating a customer group, and the metadata says `"create": "none"`, so the desk does not offer **New**. They could not see the group they made. Everything else they do as before.
- **Removing** the row (`user permission remove --user ann --doctype customer_group --value Retail`) shows Bob again on the next request. Administrators and the system context are never restricted.
- Combines with the rest: `if_owner`, field levels and workflows all apply as well, and an unrestricted user is not affected at all.

The Administrator API is `POST /api/v1/user-permissions` and `.../remove` ([HTTP API](http-api.md#user-permissions-m103)); the commands are in [CLI](cli.md#user-permissions-m103); the rule with its edge cases is in [getting started](getting-started.md#user-permissions) and [ADR 0025](../adr/0025-user-permissions.md). The `sites` job of CI runs this flow with the Sales User bob, restricted to `Retail` on the site `shop` ([`examples/sites/ci.sh`](../../examples/sites/ci.sh)), and the Playwright spec `a Sales User restricted to Retail sees only the allowed customers and groups` shows the desk (a hidden customer opened by its address is the normal "Cannot open this page: customer ... not found").

## What is not covered

- **Query reports, webhook payloads, jobs, the scheduler, fixtures and the `doc` and `data` commands are exempt** from field levels and user permissions. A report is the app's SQL shown to the roles it names ("Sales by customer" is for Sales Managers); a webhook carries the whole document; the commands and jobs are the system context. Do not build a report for a role that must not see something; see [ADR 0024](../adr/0024-field-permission-levels.md#exemptions-documented-not-enforced) and [ADR 0025](../adr/0025-user-permissions.md#exempt-documented-not-enforced).
- **No admin UI.** There is no Customize Form and no screen for custom fields or user permissions: the command line and the Administrator API are the interface (owner decision 8 of the M10 plan).
- **No property setters.** An overlay or a custom field adds a field; it cannot change the label, options, required flag or rights of an existing one. No Table custom fields, no rename or delete of a custom field.
- **No sharing.** Giving one user access to one document is Later ([feature matrix](../blueprint/feature-matrix.md)); there are no deny rules and no role hierarchies.
- **Known limits** of the rules (a unique violation or a series gap can still tell a restricted user that something exists, a stored Link to a hidden target shows its name, child-table Links do not restrict the parent): [ADR 0025, known limits](../adr/0025-user-permissions.md#known-limits), [ADR 0024, the leak review](../adr/0024-field-permission-levels.md#the-leak-review) and the [security model](../blueprint/security-model.md).
