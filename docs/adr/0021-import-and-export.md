# 0021 Import and export

- Status: Accepted (owner decisions 7 to 9 of the M9 plan, 2026-10-30, *"setuju semua rencana mu"*; the design below is the specification of the M9.3 working session and is reviewed with its pull request).
- Date: 2026-10-31

## Context

Business applications live on files: a list is sent to an accountant as a spreadsheet, a customer list arrives as a CSV. M9.3 adds both directions for the documents of one DocType, through the API, the command line and the desk. The two directions have opposite risks. **Export** moves a lot of data out in one request, so it must be a right of its own and must respect every rule that reading respects. **Import** writes many documents in one request, so it must go through the same pipeline as every other write (hooks, validation, naming, permissions, webhooks) and must not leave half a file behind.

## Decision

### Rights (owner decision 7; extends [ADR 0011](0011-permission-model-phase-1.md))

Two new rights, `export` and `import`, are keys of `[[permissions]]` like the others, `false` by default. **Deny by default:** an existing DocType has neither right, so only the Administrator and the system context can export or import until an app grants them. `forgebase_core::permission::evaluate` stays the only decision.

| Operation | Needs |
|---|---|
| Export | `read` **and** `export`. If either comes from an `if_owner` rule, only the caller's own documents are exported. |
| Import, insert mode | `import` **and** `create` |
| Import, upsert mode | `import`, `create` and `write` |

**Every row of an import still goes through the normal pipeline as the caller**, so `insert` and `update` check their own rights per row as they always do (an `if_owner` write, a workflow `edit_roles`, a locked submitted document). The import right is a gate to the file, not a bypass of the rows.

### Export: `GET /api/v1/export/{doctype}`

Parameters: `format` (`csv`, the default, or `json`), `filters`, `sort` (those of the list route, parsed by the same code and run by the same query builder) and `fields` (a comma separated list). There is no limit or offset.

- **Errors before the first byte** are the normal JSON envelope: 404 for an unknown or child DocType; 403 `permission_denied` for a missing right, naming it (`read` first, then `export`), as the list route answers 403 for a missing `read`; 400 `invalid_query` for a bad format, filter, sort or field list; 422 `export_too_large`.
- **One snapshot.** The export runs in one **read-only `REPEATABLE READ` transaction** (`forgebase_db::begin_export`): the count, every page and the child rows of every page see the same data.
- **Count first.** `SELECT count(*)` with the same `WHERE` as the rows (the one condition builder of the list query, `if_owner` included). More rows than `AppConfig::export_max_rows` (default **100 000**) is **422 `export_too_large`** with `details: {"count", "limit"}`, before anything is sent: the caller narrows the filters. The count is also sent as the header `X-Forgebase-Row-Count`.
- **Streamed.** The rows are read through a portal, 1000 at a time (`forgebase_db::Export::next_page`), formatted and written as body chunks; the memory in use does not depend on the size of the result. For JSON the child rows of a page are read with **one query per child table per page** (`parent_id = ANY(...)`), never once per document. The work runs in its own task that owns the pooled connection and the snapshot; the handler waits only for the count, so a refusal is an ordinary error response. A client that goes away ends the task and the snapshot, and so does one that stops reading: each chunk must be taken within `AppConfig::export_send_timeout` (default 30 s), else the body is aborted (an incomplete download, as for a failure) and the connection and snapshot are released.
- **A failure halfway** (a database error after the first page) **aborts the body**: the connection is closed without the end of the chunked body, so the client sees an incomplete download. A client must treat an incomplete body as a failure (for JSON that is also an unterminated array, for CSV compare the number of rows with `X-Forgebase-Row-Count`). This is the only honest signal once the status line has been sent.
- **Download headers:** `Content-Disposition: attachment; filename="<doctype>-<UTC timestamp>.csv|json"` and `Cache-Control: no-store`.
- **Fields.** CSV default: `name` and every field except Table fields, in the DocType's order. JSON default: the whole document as `GET /resource/{doctype}/{name}` returns it, child rows included. `fields=a,b` names the keys to write: fields of the DocType and the standard columns `name`, `owner`, `modified_by`, `created_at`, `modified_at`, `version` (and `docstatus`, `workflow_state` where the DocType has them); a Table field can be named for JSON only; a duplicate or unknown name is 400.

**CSV, written by hand (RFC 4180).** UTF-8 **without a BOM**, CRLF line ends, a header row of the keys. A cell is quoted when it contains `,`, `"`, CR or LF, or starts or ends with a space; a `"` inside is doubled. Dates and datetimes are ISO 8601 exactly as in the JSON API (datetimes in UTC), decimals are their exact strings, booleans `true` / `false`, links the target's name, null an empty cell. Never child tables.

**Formula injection.** A spreadsheet runs a cell that starts with `=`, `+`, `-`, `@`, TAB or CR as a formula. A value of a **text kind** (Data, Text, Select, Link, and the text standard columns) that starts with one of them gets a leading `'`. **Numbers, decimals, dates, datetimes and booleans are never prefixed**: `-5` is a number and must stay `-5`, and a prefix would turn every negative amount into text. The mitigation is always on for CSV and cannot be switched off. Its price: CSV import does not strip the `'` (a cell is exactly what it says), so a CSV export of text that starts with one of those characters does not round-trip exactly; **to keep exact text, round-trip through JSON**, which is never altered.

**JSON.** `Content-Type: application/json`, a top-level array of documents (not wrapped in `{"data": ...}`: it is a file), streamed as `[`, items separated by `,`, then `]`, so that a JSON export can be imported again.

### Report export: `POST /api/v1/report/{app}.{name}/export?format=csv|json`

The body is `{"params": ...}`, as for running the report; the access rules are the report's own (its roles, 404 otherwise, one answer for a report that does not exist and one the caller may not run). It uses `run_report`, so **the report's own row limit and timeout apply** ([ADR 0016](0016-query-reports-read-only-execution.md)), and it is **not streamed**: the result is already bounded. CSV has the column names as its header and the same formula rule (a Data or Link column is text; Int, Decimal, Date, Datetime and Check columns are never prefixed). JSON is `{"columns": [...], "rows": [[...]], "truncated": bool}`, the shape of the run result without the `data` envelope. A result cut at the report's limit has the header `X-Forgebase-Truncated: true`. The report's rights model is unchanged (**no row-level rules inside a report**): exporting a report gives a role exactly what running it gives.

### Import: `POST /api/v1/import/{doctype}?format=csv|json&mode=insert|upsert&dry_run=1`

The body is the raw file. `format` is required; `mode` defaults to `insert`; `dry_run` is `1` or absent (`0` is accepted). The `Content-Type` must be `text/csv` (CSV) or `application/json` (JSON), parameters and case ignored: another type is 415.

**Limits.** The import route has **its own body limit**, `AppConfig::import_body_limit_bytes` (default **16 MiB**), set as a layer of that route only; every other route keeps `body_limit_bytes`. Over it: 413 `payload_too_large`. More than `AppConfig::import_max_rows` rows (default **10 000**; for JSON the documents and their child rows are counted together) is **422 `import_too_large`** with `details: {"limit"}`, before anything runs. The rights are checked before the body is read, so a caller without them cannot make the server buffer 16 MiB.

**Reading the file.**

- **CSV** is read with the `csv` crate (owner decision 9). A UTF-8 BOM is stripped; anything else must be UTF-8. The first row is the header of field keys; a `name` column is allowed. A header with more columns than the DocType can have (its fields, `name` and the standard columns) is refused first, as one problem. The standard columns of an exported file (`id`, `owner`, `created_at`, `modified_at`, `modified_by`, `version`, `docstatus`, `amended_from`, `workflow_state`, the last only for a DocType with a workflow: elsewhere it may be a field of the DocType's own and is imported) are read and ignored, as in JSON, so a CSV export of a list imports again. An unknown column, a duplicate column and a Table column ("CSV import of child tables is not supported; use JSON") are refused for the whole file with **422 `invalid_import`** and the problems listed (at most 100, then "and N more"), before anything runs; so is a record with a different number of cells than the header, and, for an upsert, a file without a `name` column. A cell is parsed by the type of its field from the string: a date `YYYY-MM-DD`, a datetime RFC 3339, an integer, a decimal, a boolean `true`/`false`/`1`/`0` (any case), anything else text, a link its target's name. **The parsing is the one of the JSON API** (`forgebase_http::json`): a cell becomes the JSON value of its kind and goes through the same function, so a rule is written once. **A cell that cannot be parsed is an error of its row**, reported with the others.
- **An empty CSV cell** is "not given". In insert mode the field is left out, so its default or null applies. In an upsert of an existing document the field is **set to null**. Both are documented in the CLI and API guides.
- **JSON** is an array of objects, each as the body of `POST /resource` (child rows as arrays; an object may have `name`). The standard keys of an exported document (`id`, `owner`, `created_at`, `modified_at`, `modified_by`, `version`, `docstatus`, `amended_from`, `workflow_state`, the last only when the DocType has a workflow) are ignored, so an export can be imported again. Bad JSON, or JSON that is not an array, is 422 `invalid_import`.
- **The name of a row** is its `name` cell or key; for a DocType named by a field, the value of that field when no name is given (it is what the document's name will be), so an error can name the document and an upsert can find it. A name given for a DocType with random or series naming is refused for that row by the naming rules of `insert` (a JSON export of a randomly named DocType therefore re-imports as an **upsert** of the same documents, or without the `name` keys as new ones).

**Modes.**

- **Insert.** Every row is a new document through `forgebase_db::insert` with the caller.
- **Upsert** (explicit, needs `write`). A row names a document. If it exists (found with the caller's read rule), it is **updated** with the version read inside the same transaction, sending only the fields the row gives (for JSON, a given table replaces its rows, as in the API). A DocType named by a field cannot rename: a row whose naming field equals the name is fine (the field is left out of the update), a different value is that row's error. If it does not exist, the row is **inserted** with that name when the naming rule allows it, else it is a row error. Submitted documents follow the normal rules: an update fails per row unless the fields are `allow_on_submit`.

**All-or-nothing, with savepoints** (owner decision 8; `forgebase_db::import`). One transaction for the whole file; **each row runs in its own savepoint**, so a row's failure rolls back only that row and its error is collected. Hooks, validation, naming, permissions, workflow rules, the outbox jobs and the webhook deliveries of M9.2 are all written in that transaction. After the last row: if any row failed the transaction is **rolled back** and the answer is **422 `import_failed`**; with `dry_run=1` it is **always rolled back** and the answer is 200; otherwise it commits and the answer is 200. A dry run therefore changes nothing: no documents, no jobs, no webhook deliveries. It does run the hooks, so a hook must not have effects outside the transaction (the hook API has no way to reach the database, but an app could do I/O; do not).

- **The answer** is `{"data": {"dry_run", "mode", "rows", "inserted", "updated", "errors": [...], "error_count"}}`; the 422 has the same summary as `error.details`. Each error is `{"row": <1-based data row>, "line": <CSV only: the line of the file>, "name": <if known; shortened to 64 characters, like every other echo of the input>, "code", "message", "details"?}` with the code, message and per-field `details` of the error the document API would give for that row (`validation_failed`, `hook_rejected`, `unique_violation`, ...). The list holds the **first 100** errors; `error_count` is the total. `inserted` and `updated` of a dry run or a failed import are what the rows would have done.
- **Which errors are rows' errors.** A validation, naming, link, hook, unique, conflict, status or permission error belongs to the row. A failure of the database or the app that would fail every row (a lost connection, a missing job registration) ends the import and is answered as such (500, rolled back).

**CLI.** `forgebase data export DOCTYPE [--format csv|json] [--filter ...] [--sort ...] [--fields a,b] [--out FILE] [--limit-override N]` and `forgebase data import DOCTYPE FILE --format csv|json [--upsert] [--dry-run] [--actor NAME]`. They run as the system context like the `doc` commands (no permission rules; `--actor` is recorded on import), share the parsing, the formats, the pipeline and the transaction with the API (`forgebase_http::transfer`), print the summary as JSON, and exit 1 when any row failed (also for a dry run). `--out` creates a new file and never overwrites; a failed export removes its partial file.

### The transaction is long

The import is one transaction. **Measured:** 10 000 rows of a simple DocType (`customer` of the CLI tests: a unique naming field and three more fields, no hooks) take about **37 seconds** on a PostgreSQL 16 on the loopback interface with a debug build of the CLI (36.2 s imported, 38.3 s as a dry run; about 3.7 ms per row, most of it round trips: two savepoints, the link and series lookups, the insert, the outbox query). A release build is faster and was not measured. For that time the transaction holds its locks (unique values, series counters) and one pool connection, and a request that long may meet a proxy timeout. That is the price of "everything or nothing"; it is why the row limit is 10 000, and large loads belong in the command line, run when the site is quiet. The export of the same 10 000 rows takes about 0.5 s.

### Non-goals

Excel files; CSV import of child tables (use JSON); a background import of huge files (a job with its own state and progress); mapping of columns in the desk; update by a key other than `name`; resuming a failed import.

## Alternatives considered

- **Buffering the export** and answering with a `Content-Length`. Rejected: memory grows with the result, and the failure case (a database error halfway) would be a clean 500 at the price of holding up to 100 000 rows. The count-first check gives the clean refusal that matters; streaming gives bounded memory.
- **Paging the export with `LIMIT/OFFSET` or keyset queries** instead of a portal. Rejected: separate statements are not one snapshot (a concurrent write would duplicate or skip rows), and a portal is one statement.
- **A background import** (a job, a status route). Rejected for M9: it needs a place to keep the file, progress and the report, and a screen that polls. A synchronous request with a row limit and a dry run covers the stated need; the command line covers the large case.
- **No transaction per file, one per row** (partial imports). Rejected by the owner (decision 8): a file that half applies is the hardest state to repair.
- **Stopping at the first error.** Rejected: a user fixing a file wants every error at once, which is what the savepoints give.
- **PostgreSQL `COPY`.** Rejected: it bypasses the hooks, validation, naming, permissions and webhooks that "every row through the normal pipeline" requires.
- **Writing the CSV with the `csv` crate too.** Rejected: the writer is about thirty lines, and the formula mitigation and the exact quoting rule need control over each cell.
- **Prefixing numbers as well** (the OWASP advice for "every cell"). Rejected: negative numbers and signed decimals are data, and a prefix makes them text. The risk is a *text* the spreadsheet reads as a formula; numbers, dates and booleans cannot be one.
- **A BOM at the start of the CSV** (Excel opens a UTF-8 file without one as ANSI and garbles accented letters). Not written: it breaks tools that read the first header as `﻿name`, and RFC 4180 has none. The importer accepts one, so a file saved by Excel imports. Excel users can open the file through "From Text/CSV" with UTF-8, or use JSON.
- **403 or 404 for a missing right.** 403 `permission_denied`, as the list route: the existence of a DocType is not secret, and the answer does not depend on any document.
- **Using the import right alone** (no `create`). Rejected: import is a bulk create, and a role that may not create must not create by file.

## Consequences

- **Every error response is bounded at one place.** `ApiError::into_response` gives the message, the details and the object keys one total budget of 64 KiB of serialized bytes; each string is cut to 512 characters (and to what is left), a list takes entries while the budget and a count of 100 last and ends with "and N more", also inside an object `details` such as the summary of `import_failed`. The 200 summary of an import (a dry run lists the errors of its rows too) and the output of `forgebase data import` pass through the same bound. An error or summary body is therefore at most about 64 KiB plus the envelope, whatever the request echoed; the shortening at the call sites stays as a first line. A per-level cap was rejected: caps multiply through nested lists, a total budget does not.

- **A new dependency, `csv` 1.4.0** (Unlicense OR MIT), with `csv-core` 0.1.13 (Unlicense OR MIT); `itoa`, `ryu`, `memchr` and `serde_core` were already in the tree. Used for reading only. See the [dependency evaluation](../blueprint/dependency-evaluation.md#15-import-and-export-m93).
- **`http-body-util` gets its `channel` feature** (it needs `tokio`, already a dependency): the body of a streamed export. No new crate.
- **Breaking change of the defaults:** `Right::ALL` has two more entries and the metadata API's `rights` two more keys. Nothing is granted by it: **no role may export or import until an app grants it**. Migration note: grant `export = true` (with `read`) and `import = true` (with `create`) to the roles that should have them. `examples/sales` grants `export` on `customer` and `sales_order` to Sales User and Sales Manager and `import` on `customer` to Sales Manager.
- **`AppConfig` has four more fields:** `export_max_rows`, `import_max_rows`, `import_body_limit_bytes` and `export_send_timeout`.
- **A streamed export holds a pool connection** while the client reads it. A client that stops reading is cut off after `export_send_timeout` (30 s per chunk); one that reads slowly but steadily is not, so put a timeout in the reverse proxy.
- **The 16 MiB body of an import is buffered in memory** (and parsed into at most 10 000 rows) before the transaction starts, per request. With many concurrent imports that is real memory; the route needs an authenticated caller with the import right.
- **The security model** has a section for the export right and data leaving, formula injection, the import body limit and the dry run.
- **Known limits:** a CSV file is decoded as a whole (no streaming of the upload); one `name` per row; a date or datetime must be in the one documented form; the first 100 row errors are listed; the incomplete-body signal of a failed export is the connection, not a status.
