# HTTP API

`serve` (here `cargo run -p sales -- serve`) runs the API on `FORGEBASE_LISTEN` (default `127.0.0.1:8000`). It serves the same DocTypes as `migrate` (the app's, or the `--doctypes` directory in the stock binary) but **does not run migrations**: run `migrate` first. Requests need a `Host` header from `FORGEBASE_ALLOWED_HOSTS` (comma separated host names without ports, default `localhost,127.0.0.1`; an entry with a port is refused at startup). Ctrl-C or SIGTERM stops accepting connections and lets running requests finish for up to 10 seconds; a second signal, or the end of that period, exits at once (exit code 0; open database transactions are rolled back by PostgreSQL when the connection closes). The session cookie is `Secure`, so browsers send it over HTTPS only: put a TLS-terminating reverse proxy in front (the application has no TLS). For local development over plain HTTP, `FORGEBASE_DEV_INSECURE_COOKIES=1` omits `Secure` (and prints a warning); never use it in production. There is no CORS: serve the front-end from the same origin.

```sh
# Prepare: tables, a user with the Administrator role (the example DocTypes name other roles; Administrator may do everything)
cargo run -p sales -- migrate
printf '%s\n' 'a long password' | cargo run -p sales -- user create root --role Administrator

# Terminal 1: serve (development mode)
FORGEBASE_DEV_INSECURE_COOKIES=1 cargo run -p sales -- serve

# Terminal 2: log in. The response has the one-time CSRF token; the session cookie is saved in jar.txt.
CSRF=$(curl -s -c jar.txt -H 'Content-Type: application/json' \
  -d '{"username": "root", "password": "a long password"}' \
  http://localhost:8000/api/v1/login | sed -n 's/.*"csrf_token":"\([0-9a-f]*\)".*/\1/p')

# Create (201). Changing requests with the cookie need the CSRF header; reads do not.
curl -s -b jar.txt -H "X-Forgebase-CSRF: $CSRF" -H 'Content-Type: application/json' \
  -d '{"email": "ann@example.com", "full_name": "Ann", "tier": "gold", "loyalty_points": 12}' \
  http://localhost:8000/api/v1/resource/customer

# List with filters (a JSON array like `doc list --filter`), sort, limit and offset
curl -s -b jar.txt -G http://localhost:8000/api/v1/resource/customer \
  --data-urlencode 'filters=[{"field":"tier","op":"=","value":"gold"}]' \
  --data-urlencode 'sort=loyalty_points:desc' --data-urlencode 'limit=10'

# Read, then change with the version you read (PATCH needs {"version", "data"}; a stale version is 409 `conflict`)
curl -s -b jar.txt http://localhost:8000/api/v1/resource/customer/ann@example.com
curl -s -b jar.txt -X PATCH -H "X-Forgebase-CSRF: $CSRF" -H 'Content-Type: application/json' \
  -d '{"version": 1, "data": {"loyalty_points": 20}}' \
  http://localhost:8000/api/v1/resource/customer/ann@example.com

# Create a sales order (its name comes from the series; use the `name` in the response below), then send it for approval and approve it
# (a transition takes {"action", "version"}; the order has a workflow, so there is no plain submit. On a DocType without a workflow, submit and cancel take {"version": N}; amend takes {"version": N} and answers 201 with the new draft.
# The user needs a role of the transition and the right: `root` is an Administrator, who passes every role check)
curl -s -b jar.txt -H "X-Forgebase-CSRF: $CSRF" -H 'Content-Type: application/json' \
  -d '{"customer": "ann@example.com", "items": [{"description": "Widget", "qty": 2, "rate": "9.50"}]}' \
  http://localhost:8000/api/v1/resource/sales_order
curl -s -b jar.txt -X POST -H "X-Forgebase-CSRF: $CSRF" -H 'Content-Type: application/json' \
  -d '{"action": "Submit for Approval", "version": 1}' http://localhost:8000/api/v1/resource/sales_order/SO-2026-00001/transition
curl -s -b jar.txt -X POST -H "X-Forgebase-CSRF: $CSRF" -H 'Content-Type: application/json' \
  -d '{"action": "Approve", "version": 2}' http://localhost:8000/api/v1/resource/sales_order/SO-2026-00001/transition

# Delete (204; the version goes in the query string). Here it is refused with 409 `linked_document_exists`, because the sales order links to the customer
curl -s -b jar.txt -X DELETE -H "X-Forgebase-CSRF: $CSRF" \
  'http://localhost:8000/api/v1/resource/customer/ann@example.com?version=2'

# Log out (204; clears the cookie and ends the session)
curl -s -b jar.txt -X POST -H "X-Forgebase-CSRF: $CSRF" http://localhost:8000/api/v1/logout

# A machine client uses an API token instead: no cookie, no CSRF header. The token header wins over any cookie.
TOKEN=$(cargo run -q -p sales -- token create root)    # prints key:secret once
curl -s -H "Authorization: token $TOKEN" http://localhost:8000/api/v1/resource/customer
```

Responses are JSON: `{"data": ...}` on success and `{"error": {"code", "message", "details"?}}` on failure, with stable codes (for example `unauthenticated` 401, `permission_denied` 403, `csrf_failed` 403, `not_found` 404, `conflict` 409, `validation_failed` 422, `invalid_query` 400). A document you cannot read is a 404, like a missing one, and a **field** you cannot read (a `perm_level` you have no rule for, [ADR 0024](../adr/0024-field-permission-levels.md)) is left out everywhere and is an unknown field in filters, sort, `group_by`, `fields` and input; a field you can read but not write answers `422 field_not_permitted` when a value would change it, and `GET /api/v1/meta/{doctype}` lists it with `read_only: true`. The route table, the request pipeline, the codes and the limits are in the [architecture](../blueprint/architecture.md#7-api-rest-resource-api-implemented-in-m33-metadata-and-session-routes-in-m41-the-rest-proposed) and the [security model](../blueprint/security-model.md#the-http-layer-implemented-in-m33).

## App methods

An app can register its own endpoints ([first app, section 8](first-app.md#8-app-methods), [ADR 0019](../adr/0019-app-method-endpoints.md)): `POST /api/v1/method/<app>.<method>` with a JSON object body (an empty body is `{}`) answers `200 {"data": ..}`. The caller must be logged in (a cookie with the CSRF header, or an API token) and have a role of the method, or the method must be declared `any_user`; **there are no guest methods**. The call runs as that user in one transaction. The `sales` example has the method `sales.order_summary` (a Sales User or Sales Manager may call it; the walkthrough is [Integrating with other systems](integration.md#1-app-methods-another-system-asks-a-question)). These calls need a user with one of those roles and, for the cookie call, a login as in the first example above (it ends with a logout):

```sh
curl -s -b jar.txt -H "X-Forgebase-CSRF: $CSRF" -H 'Content-Type: application/json' \
  -d '{"from": "2026-10-01", "to": "2026-10-31"}' http://localhost:8000/api/v1/method/sales.order_summary
# a token call: no cookie and no CSRF header
curl -s -H "Authorization: token $TOKEN" -H 'Content-Type: application/json' \
  -d '{"customer": "ann@example.com"}' http://localhost:8000/api/v1/method/sales.order_summary
```

| Status | When |
|---|---|
| 200 | `{"data": <what the method returned>}` |
| 400 | the body is not a JSON object (`bad_request`), a query parameter was sent (`invalid_query`), or the method refused (`method_error`, with the app's message) |
| 401, 403 `csrf_failed` | no valid credential, or a cookie call without the CSRF header |
| 404 | the method does not exist, its app is not installed on the site, **or you may not call it**: one answer, so names are not revealed |
| 405 | any HTTP method but `POST` |
| 409, 403 `permission_denied`, 422 | an error of a document the method touched, with the status and code of the document API; 422 `method_error` is the method's own refusal |
| 413, 415 | the body is over the limit, or not `application/json` when it is not empty |
| 500 | a failure inside the method or the server: a generic message, the cause is in the server log |

Whatever the method did in the transaction is rolled back on every error.

## Customize fields (M10.1)

Walkthrough: [Customizing and restricting data](customizing.md#site-custom-fields).

Site custom fields over HTTP ([ADR 0023](../adr/0023-metadata-overlays.md); the command line does the same, see [Customize field commands](cli.md#customize-field-commands-m101)). **Only the Administrator role** may use these routes: any other authenticated user gets `403 permission_denied`, a cookie call needs the CSRF header like any write, and no credentials is `401`.

| Route | Effect |
|---|---|
| `GET /api/v1/customize/{doctype}/fields` | The custom fields of the DocType, enabled and disabled, in the order they were added: `{"data": [{"doctype", "name", "label", "type", "definition", "enabled", "created_at", "modified_at", "modified_by"}]}`. |
| `POST /api/v1/customize/{doctype}/fields` | Adds a field. The body is the field as JSON with the keys of a TOML `[[fields]]` entry (`name`, `type`, `label`, `unique`, `allow_on_submit`, `perm_level`, `max_length`, `precision`, `scale`, `options`, `target`). `201` with the stored field. |
| `POST /api/v1/customize/{doctype}/fields/{name}/disable` and `/enable` | Hides or shows the field; the column and data stay. `200` with the field. The body is ignored (send `{}`). |

```sh
# Add a checkbox to customer (a POST with a session cookie needs the CSRF header)
curl -s -b jar.txt -X POST -H "X-Forgebase-CSRF: $CSRF" -H 'Content-Type: application/json' \
  -d '{"name": "custom_vip", "label": "VIP", "type": "check"}' \
  http://localhost:8000/api/v1/customize/customer/fields
# The server that took the change uses it at once; other servers within 10 seconds.
curl -s -b jar.txt -X PATCH -H "X-Forgebase-CSRF: $CSRF" -H 'Content-Type: application/json' \
  -d '{"version": 1, "data": {"custom_vip": true}}' http://localhost:8000/api/v1/resource/customer/ada@example.com
```

| Status | When |
|---|---|
| `404` | The DocType, or the field for `disable` and `enable`, does not exist. |
| `422 validation_failed` | A rule is broken: the name must start with `custom_`; no `required`, `read_only` or `table`; not a child DocType; a Link target that does not exist; a `perm_level` outside 0 to 9; an unknown key or type. `details` says which. |
| `409 conflict` | The DocType has a field of that name already (a disabled custom field included). |
| `503 lock_timeout` | A `migrate` or another schema change holds the lock longer than 5 s (`Retry-After: 5`); nothing was written, retry. |

A custom field may have a `perm_level` (0 to 9, see [field permission levels](getting-started.md#field-permission-levels)): the rules for it are the `level` rules of the DocType, and a caller without the level does not see the field at all. A custom field is a field of its DocType in every other respect: `GET /api/v1/meta/{doctype}` lists it (after the fields of the app), the `[[permissions]]` of the DocType apply to it, and it can be filtered, sorted, summarized, exported and imported. A disabled field is none of that: input with it is `422` (unknown field) and a filter or sort on it is `400 invalid_query`.

## User permissions (M10.3)

Walkthrough: [Customizing and restricting data](customizing.md#user-permissions).

Which documents a user is restricted to, over HTTP ([ADR 0025](../adr/0025-user-permissions.md); the command line does the same, see [User permissions](cli.md#user-permissions-m103), and the rule is in [getting started](getting-started.md#user-permissions)). **Only the Administrator role** may use these routes: any other authenticated user gets `403 permission_denied`, a cookie call needs the CSRF header like any write, and no credentials is `401`. A change applies on the affected user's **next request**.

| Route | Effect |
|---|---|
| `GET /api/v1/user-permissions?user=U&doctype=D` | The rows, sorted by user, DocType and value; both parameters are optional filters: `{"data": [{"user", "doctype", "value", "created_at"}]}`. An unknown `user` is `404`. |
| `POST /api/v1/user-permissions` | Body `{"user", "doctype", "value"}` (all strings, no other key). `201` with `{"data": {"user", "doctype", "value", "added": true}}`, or `200` with `"added": false` when the row exists. |
| `POST /api/v1/user-permissions/remove` | The same body. `200` with `{"data": {"user", "doctype", "value", "removed": true}}`, or `"removed": false` when the user did not have the row. This API has no `DELETE` with a body, so a removal is a `POST` to `.../remove`, like `.../disable`. |

```sh
# Restrict ann to the customer group Retail (a POST with a session cookie needs the CSRF header)
curl -s -b jar.txt -X POST -H "X-Forgebase-CSRF: $CSRF" -H 'Content-Type: application/json' \
  -d '{"user": "ann", "doctype": "customer_group", "value": "Retail"}' \
  http://localhost:8000/api/v1/user-permissions
curl -s -b jar.txt 'http://localhost:8000/api/v1/user-permissions?user=ann'
```

| Status | When |
|---|---|
| `404` | The user does not exist. |
| `422 validation_failed` | The DocType is unknown or a child DocType, the document does not exist, the value is empty, the username is invalid, or the user has 1000 documents for the DocType already. `details` says which. |
| `400` | The body is not an object of three strings, or a query parameter is unknown or repeated. |

For the restricted user nothing changes in the API except what they see: a document outside their permissions is `404` for get, patch and delete (like a missing one), absent from lists, summaries and exports, and a Link to a target they cannot see is `422` with the message of a name that does not exist. Creating a document of the DocType they are restricted on is `403 permission_denied`.

## Import and export (M9.3)

Files in and out ([ADR 0021](../adr/0021-import-and-export.md)). Both are **rights of their own**, off for every role until a DocType's `[[permissions]]` grant them: **export** needs `read` and `export`, **import** needs `import` and `create` (and `write` for an upsert). An `if_owner` right limits an export to the caller's own documents.

```sh
# Export the gold customers as CSV: the filters and sort of the list, and a fields list. A GET: no CSRF header.
curl -s -b jar.txt -G http://localhost:8000/api/v1/export/customer \
  --data-urlencode 'format=csv' --data-urlencode 'fields=name,full_name,tier' \
  --data-urlencode 'filters=[{"field":"tier","op":"=","value":"gold"}]' --data-urlencode 'sort=name' -o gold.csv

# Everything as JSON, child rows included (a top-level array, the shape of GET /resource/{doctype}/{name})
curl -s -b jar.txt 'http://localhost:8000/api/v1/export/sales_order?format=json' -o orders.json

# Check a CSV file without keeping anything (dry_run=1), then import it. The body is the raw file.
curl -s -b jar.txt -X POST -H "X-Forgebase-CSRF: $CSRF" -H 'Content-Type: text/csv' \
  --data-binary @new-customers.csv 'http://localhost:8000/api/v1/import/customer?format=csv&dry_run=1'
curl -s -b jar.txt -X POST -H "X-Forgebase-CSRF: $CSRF" -H 'Content-Type: text/csv' \
  --data-binary @new-customers.csv 'http://localhost:8000/api/v1/import/customer?format=csv'
# {"data": {"dry_run": false, "mode": "insert", "rows": 250, "inserted": 250, "updated": 0, "errors": [], "error_count": 0}}

# A report as a file (a POST with the parameters, like a run)
curl -s -b jar.txt -X POST -H "X-Forgebase-CSRF: $CSRF" -H 'Content-Type: application/json' \
  -d '{"params": {"from": "2026-10-01", "to": "2026-10-31"}}' \
  'http://localhost:8000/api/v1/report/sales.sales_by_customer/export?format=csv' -o report.csv
```

**Export** `GET /api/v1/export/{doctype}`: `format` (`csv`, the default, or `json`), `filters` and `sort` (as for the list), `fields` (a comma separated list of fields and of the standard columns `name`, `owner`, `modified_by`, `created_at`, `modified_at`, `version`; a table field only for JSON); no `limit` or `offset`.

| Status | When |
|---|---|
| 200 | a streamed file; `Content-Disposition: attachment; filename="<doctype>-<UTC time>.csv"`, `Cache-Control: no-store` and `X-Forgebase-Row-Count` |
| 400 `invalid_query` | a bad format, filter, sort or field list, or a parameter that is not allowed (`limit`, a repeated one) |
| 403 `permission_denied` | no `read` or no `export` right (the message names it) |
| 404 | an unknown DocType (a child DocType too) |
| 422 `export_too_large` | more rows than the limit (100 000): `details` has the `count` and the `limit`; narrow the filters. Nothing was sent. |

- **One snapshot.** The export reads from one consistent snapshot of the database and writes page by page (1000 rows), so it needs little memory whatever its size. A client that does not take a chunk within 30 seconds (`export_send_timeout`) is cut off: the body is aborted and the snapshot released.
- **A failure halfway cannot change the status** (it is sent with the first bytes): the connection is closed and the body is incomplete. **Treat an incomplete body as a failure**: a JSON file whose array is not closed, or a CSV with fewer rows than `X-Forgebase-Row-Count`.
- **CSV rules.** UTF-8 without a byte order mark, CRLF, a header of the keys; a cell is quoted when it has a comma, a quote, a line break or a space at either end, and a quote is doubled. Dates and datetimes are ISO 8601, decimals exact strings, booleans `true`/`false`, links the target's name, null empty, no child tables. **Text that starts with `=`, `+`, `-`, `@`, a tab or a carriage return gets a leading `'`** so a spreadsheet does not run it as a formula; numbers, dates and booleans are never changed. CSV import does not strip the `'`: to keep the exact text, export and import JSON.
- **Report export** `POST /api/v1/report/{app}.{name}/export?format=csv|json` takes the body of a run and answers the same file formats (JSON is `{"columns", "rows", "truncated"}`); the report's own roles (404 otherwise), row limit and timeout; `X-Forgebase-Truncated: true` when the result was cut at the limit. It is not streamed: the result is already bounded.

**Import** `POST /api/v1/import/{doctype}?format=csv|json&mode=insert|upsert&dry_run=1`. The body is the file, with `Content-Type: text/csv` or `application/json` (else 415). `format` is required, `mode` defaults to `insert`.

| Status | When |
|---|---|
| 200 | `{"data": {"dry_run", "mode", "rows", "inserted", "updated", "errors": [...], "error_count"}}`. With `dry_run=1` this is also the answer when rows have errors: it lists them and nothing was kept. |
| 400 `invalid_query` | a bad or missing `format`, `mode` or `dry_run`, or another parameter |
| 403 `permission_denied` | no `import` right, no `create`, or (upsert) no `write` (checked before the body is read) |
| 413 `payload_too_large` | the body is over 16 MiB (`import_body_limit_bytes`; other routes keep their 1 MiB) |
| 415 | the `Content-Type` is not that of the format |
| 422 `invalid_import` | the file cannot be used at all: not UTF-8, bad JSON, an unknown, duplicate or table column in a CSV (`details` lists them), a ragged CSV row, a header with more columns than the DocType can have; at most 100 problems are listed |
| 422 `import_too_large` | more than 10 000 rows, a JSON file's child rows counted with its documents (`details.limit`); nothing ran |
| 422 `import_failed` | at least one row has an error: **nothing was imported**. `details` is the summary above, with `errors` (the first 100) and `error_count` (all). |

- **All or nothing.** The file is one transaction and every row goes through the normal pipeline as the caller (hooks, validation, naming, permissions, webhooks), each in a savepoint, so one run finds every error. A row error is `{"row": 2, "line": 3, "name": "ann@example.com", "code": "validation_failed", "message": "validation failed", "details": ["field `full_name` is required"]}`: `name` is the document's name, shortened to 64 characters if it is longer, `row` counts the data rows from 1, `line` is the line of a CSV file, `code`, `message` and `details` are what the document API would answer for that row. Webhook deliveries and jobs of the rows exist only if the import commits.
- **CSV**: the first row names the fields (a `name` column is allowed). A cell is read by the type of its field (a date `YYYY-MM-DD`, a datetime RFC 3339, an integer, a decimal, a boolean `true`/`false`/`1`/`0`, a link by name); a cell that cannot be read is an error of its row. **An empty cell is not given:** on insert the default (or null) applies, on an upsert of an existing document the field is **set to null**. A byte order mark is accepted. The standard columns of an export (`owner`, `modified_at`, `version`, `docstatus`, `workflow_state`, ...; `workflow_state` only when the DocType has a workflow) may be in the file and are ignored, so an exported list imports again. Table fields are not CSV columns: use JSON.
- **JSON**: an array of objects as for `POST /resource/{doctype}` (child rows as arrays, an optional `name`). The standard keys an export has (`id`, `owner`, `created_at`, `modified_at`, `modified_by`, `version`, `docstatus`, `amended_from`, `workflow_state`, the last only when the DocType has a workflow) are ignored.
- **`mode=upsert`** (needs `write`) updates the document a row's `name` names, sending only the fields the row gives (a given table replaces its rows), and inserts the others when the naming allows a given name. A DocType with random or series naming gives its own names, so a row for a name that does not exist is an error there; for such a DocType a JSON export imports again as an upsert of the same documents. A DocType named by a field takes the name that equals the field's value and cannot be renamed.
- **It is a long transaction**: about 37 seconds for 10 000 rows (measured). Keep imports to the few roles that load data, and put a timeout and a request limit in the reverse proxy.

