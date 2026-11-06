# 0025 User permissions

- Status: Accepted (owner decisions 7 and 8 of the M10 plan, 2026-10-31, *"setuju semua rencana mu"*: every non-empty Link must match, an empty Link is visible, the restriction is applied in SQL on every path, query reports are exempt; command line and Administrator API, no admin UI; the design below is the specification of the M10.3 working session and is reviewed with its pull request). Extends [ADR 0011](0011-permission-model-phase-1.md) and builds on [ADR 0024](0024-field-permission-levels.md).
- Date: 2026-11-01

## Context

[ADR 0011](0011-permission-model-phase-1.md) decides rights per DocType and, with `if_owner`, per owner. Real applications also need to limit a user to **some documents**: a salesperson who sees only the customer group `Retail` and the customers and orders of that group, a clerk of one branch. In Frappe this is a *User Permission* (a record filter by a linked value). The owner approved the idea in the M10 plan (decisions 7 and 8) with a small rule and one hard requirement taken from the security model: what a caller may see is decided in one place and applied **in SQL on every path**, so that no path (a locking read, a link lookup, a title, an export) can be forgotten.

## Decision

### Storage

`fb_user_permission` (framework migration 0013): `user_id` (references `fb_user`, `ON DELETE CASCADE`), `doctype` (the restricted DocType), `value` (the name of the allowed document), `created_at`; primary key `(user_id, doctype, value)`. The data is per site, as every table (each site is its own database, [ADR 0014](0014-multi-site-database-per-site.md)). `doctype` and `value` are not foreign keys: a DocType is metadata, and the check is made when a row is added. A row for a DocType that is not in the site's registry (or for a document that was deleted) is **ignored**, never an error, and can be listed and removed. A database that an older binary migrated has no such table: its users have no restriction until `migrate` has run.

### The rule

A caller carries its restrictions: `Caller::User` has `restrictions: UserRestrictions`, a map from a DocType to the sorted, deduplicated names the user may see (cheap to clone, an `Arc`). **A DocType with no names, or not listed, is not restricted** (the empty value means unrestricted). `Caller::user` gives none; `Caller::with_restrictions` attaches them.

One pure function in `forgebase_core::permission`, next to `evaluate` and `field_access`, decides what a caller may see of a DocType (`row_scope(&DocType, &Caller) -> RowScope`):

1. **Own name.** If the user is restricted on the DocType itself, a document is visible only if its name is one of the allowed names.
2. **Links.** For **every top-level Link field** of the DocType whose target the user is restricted on, the field must be **empty or point at an allowed name** of the target. All such fields must hold (AND), whatever their `perm_level`: a Link field the caller cannot read (ADR 0024) still restricts, and its value is never shown. A document with no Link to a restricted target is not touched by that restriction. An empty Link is visible.
3. **Child rows are not considered.** A Link inside a row of a Table field does not decide whether the parent is visible. This is a choice for simple, predictable SQL (one condition per field of the parent, no joins); the Link value of a row is still resolved when it is written (below).
4. **The restriction does not propagate through a chain.** An order that links to a customer (not restricted by name) whose group is hidden is visible if the order's own Links to the group are empty or allowed; only **direct** Links to a restricted DocType restrict. To restrict orders by group, give the order a Link to the group (a fetched field), or restrict `customer` too.
5. **Bypass.** `Caller::System` and any caller with the `Administrator` role get an empty scope, whatever rows exist. A child DocType has an empty scope (it is never read alone).
6. **A disabled site custom field** (ADR 0023) is not part of the effective DocType, so a disabled Link field restricts nothing; enabling it brings the restriction back.

`row_scope` returns **empty** for a caller or a DocType that nothing restricts, and every statement is then exactly what it is without user permissions. The role rights come first: a caller without the read right is `PermissionDenied` before any scope is looked at.

### The SQL

`forgebase-db` only turns the scope into conditions. One value, `ReadScope` (`crate::scope`), holds the `if_owner` owner and the `RowScope`, and builds the `WHERE` conditions for every statement that finds a document on behalf of a caller; the owner and the scope are ANDed:

```sql
"owner" = $1                                           -- if_owner, as in ADR 0011
AND "name" = ANY($2::text::text[])                     -- own name of a restricted DocType
AND ("customer_group" IS NULL OR "customer_group" IN   -- one per Link field to a restricted target
       (SELECT t."id" FROM "doc_customer_group" t WHERE t."name" = ANY($3::text::text[])))
```

A Link column stores the target's `id`, so the subquery goes through the primary key and the unique `name` index of the target. Every value is a bound parameter: the allowed names are **one array literal in a text parameter** (every element quoted, `\` and `"` escaped, tested with commas, braces, quotes, `NULL` and Unicode), one parameter per target even when several fields point at it. A text parameter lets the list query, the summary, the export and the single-document statements bind alike (the list builder binds typed values, not arrays); nothing from outside reaches the SQL text.

### Every path

| Path | What happens to a document outside the scope |
|---|---|
| `get` | `None`, as a missing name |
| `list` (filters, sort, paging), saved views | absent; a filter or sort cannot reach it, a page counts only visible rows |
| `summary` | not counted |
| export (CSV, JSON) and its count and `ExportTooLarge` | not exported, not counted |
| locking reads of `update`, `delete`, `submit`, `cancel`, `amend` (the cancelled original) and workflow transitions, `available_transitions` | `NotFound` **before** the version is compared; the row is in the `WHERE` of the `FOR NO KEY UPDATE` / `FOR UPDATE` statement, so it is **neither returned nor locked** (a test holds a lock on the row in another connection: the restricted caller gets `NotFound` at once, an unrestricted one waits) |
| the safeguard statement after a write that matched no row | `NotFound` |
| link resolution (below) | `LinkNotFound` |
| link titles | no title |
| import | every row runs through `insert` or `update`, so the rules apply per row |
| methods (`ctx.documents()`) | they run as the caller with its restrictions |

**Link resolution.** A Link value in a document **and in its child rows**, on `insert`, `update`, `submit` and `amend`, is looked up with the caller's read access **and the scope of the target DocType** (the same `row_scope`, applied to the target, so a customer hidden by its group is a hidden target too). A target outside it is `LinkNotFound`, **exactly like a missing name** (one code path builds the error; a test compares the messages). With no read right on the target nothing is looked up, as in ADR 0011. **The kept rule of ADR 0011 still applies:** a stored Link value equal to the current one (a row Link whose name is among the stored values of the same field of the same table) is resolved with full access, with no scope. A stored Link can be outside the scope in two cases: (1) a **child-row** Link (rows do not decide visibility), and (2) a **top-level Link to a DocType the user is not restricted on** whose own target is hidden by its scope (the no-chain rule: a visible order may link to a customer that its group hides; the name is shown and kept on update and submit). In both cases the kept rule applies exactly as in ADR 0011: a hidden name that is **already stored in that field of that table** is accepted, including in a **new** child row (the rule looks at the values stored, not at the rows); only a hidden name that is not already stored there is `LinkNotFound`, and so is changing the Link to another hidden target. `amend` never uses the kept rule, as before.

**Creating in a restricted DocType.** A user restricted on a DocType **cannot create or amend a document of it**: `PermissionDenied` for `create` (or `amend`), before anything is looked up, also for an import row. One function, `forgebase_core::permission::may_make_documents`, decides it for `forgebase-db` and for the `create` and `amend` rights of the metadata API (`GET /api/v1/meta`, which says `none` for them). The name of the new document could not be checked against the allowed names before naming, and the user could not see the result. (Orchestrator choice, documented.) A user restricted to some `customer_group` can still create customers and orders, whose own name is free: no write produces a document the caller then cannot see, apart from the two stored-Link cases above, because a new top-level Link to a hidden target is `LinkNotFound`.

**Link filters** (ADR 0011): the subquery of a Link filter, sort or group-by on the target stays **unrestricted**. It can only select among rows that are already in the caller's scope, and what it tells (that a visible row links to a name) is what the caller sees in that row. Confirmed.

### Who can change it, and when it applies

- **Command line:** `forgebase user permission add --user U --doctype D --value NAME [--doctypes DIR]`, `remove` (same options) and `list [--user U] [--doctype D]` (a table sorted by user, DocType and value), run as the system context like the other `user` commands. `add` needs the user, the DocType (in the site's effective registry, not a child DocType) and the document (looked up as the system context) to exist; adding a row that exists succeeds and says so; removing a missing row succeeds and says so.
- **API, Administrator only** (403 `permission_denied` for anyone else, as the customize routes; the cookie needs the CSRF header like every write): `GET /api/v1/user-permissions?user=U&doctype=D`, `POST /api/v1/user-permissions` (`{"user", "doctype", "value"}`; 201, or 200 when the row exists) and `POST /api/v1/user-permissions/remove` (this API has no `DELETE` with a body; the other actions are `POST`s too). The checks are those of the command line (one function in `forgebase_db::user_permissions`).
- **No admin UI** (decision 8): the desk is unchanged.
- **When:** the permissions are read with the roles of every authenticated request (`validate_session` and `validate_api_token`: **two more round trips**, a `to_regclass('fb_user_permission')` check so that a database not migrated yet serves its users without restrictions, then the rows of the user), and `AuthUser::caller` builds the caller from them, so a change applies on the user's **next request**, like a role change; no registry reload, no cache, no restart. A job or a method started earlier keeps the caller it had.
- **The cap:** at most **1000 documents per user and DocType**, refused on `add` with a message that says so, so the bound array stays small. The user row is locked during an add, so two adds cannot both pass the limit. A row added by hand in SQL is not checked.

### Exempt (documented, not enforced)

- **Query reports** (owner decision 7), as for `if_owner` and levels: a report names the roles that may run it and its SQL is the app's. The Link titles of a report's columns follow the scope of the target (a title may be missing), which is harmless.
- **Webhook payloads** (the whole document, sent by the system), **jobs, the scheduler and fixtures** (`Caller::System`), and the **`doc` and `data` command line** (system context).

### Known limits

- A **unique violation** (a `unique` field, a field-naming name, an amended name) and `AlreadyAmended` can tell a caller that a document exists that they cannot see (ADR 0011). An import row that upserts a hidden document is not an update; it is an insert that collides.
- **`LinkedDocumentExists`** still tells that some document links to the one being deleted (ADR 0011, unchanged).
- **Counts and numbers.** A naming **series** counter is shared, so gaps in the numbers a restricted user sees show that other documents exist. A stored Link in a visible document still shows the **name** of a hidden target (not its title); those are the two stored-Link cases above (a child-row Link, and an order's Link to a customer that is hidden only by its group, rule 4). A visible document also tells the caller that its Link to a restricted target, even one the caller cannot read (a `perm_level`), is empty or an allowed name.
- Child rows do not restrict their parent, and the restriction does not propagate through a chain of Links (rules 3 and 4).
- A user restricted on a DocType cannot create or amend one of it.
- Rows are checked when added; a document that is deleted later leaves a row that is ignored.

## Alternatives considered

- **PostgreSQL row-level security.** Rejected as in ADR 0011 and the security model: it ties permissions to database roles per user, complicates pooling, and moves the rule out of the one evaluator where it is tested as a matrix.
- **Filtering in Rust after the query.** Rejected by ADR 0011: it breaks paging, leaks through counts and timing, and is the easiest way to forget a path.
- **A per-request cache of the allowed names** (or of which documents are visible). Rejected for now: the names cost two round trips per authenticated request (a table check, then one indexed query), a cache would add invalidation (a change must apply on the next request on every server), and nothing is measured to need it. Revisit with a measurement.
- **Restricting a parent by the Links in its child rows.** Rejected: it needs `EXISTS` over each child table (or a join that multiplies rows), makes the SQL and the cost unpredictable, and a row-level Link is rarely the key of a document. The Link of a row is still resolved on write.
- **Propagating a restriction through chains of Links** (a customer hidden by its group hides its orders). Rejected: unbounded joins and a rule that is hard to state; the order can carry its own Link to the group.
- **Native `text[]` parameters.** The text array literal was chosen only because the list builder binds a closed set of typed values; it is as safe (bound, parsed by the server) and covered by tests with hostile names.
- **An admin UI, deny rules, sharing.** Out of scope (decision 8; sharing is Later).

## Consequences

- **Breaking change inside the internal crates (0.x).** `Caller::User` and `AuthUser` have a `restrictions` field. `forgebase-core`, `forgebase-db` and `forgebase-http` are internal ([ADR 0012](0012-app-model-and-public-api-boundary.md)); the public `forgebase` crate is **unchanged** (a method reads through `ctx.documents()`, which inherits the restriction), so ADR 0012 needs no amendment. Behaviour for a user without user permissions is unchanged; the existing tests pass unchanged apart from the framework migration count (13) and mechanical edits: the call sites of `build` and `get_sql` in the unit tests of `list.rs` and `document.rs` (their owner argument is a `ReadScope`), the `restrictions` field in the `AuthUser` literals of the `auth.rs` unit tests, and `..` in a `Caller::User` pattern in the `forgebase` tests; `setup()` in `forgebase-db/tests/permissions.rs` also registers the new `agent_case` DocType, which the existing tests of that file share without using it.
- Every read statement of a restricted user has the extra conditions; with the unique `name` index and the primary key on the target the cost is a hash or index lookup per Link field, and nothing for a user without restrictions. Two more round trips per authenticated request (the table check and the rows of the user); they could be one only with dynamic SQL or by catching an undefined-table error inside a transaction, which is not worth it here.
- `docs/blueprint/security-model.md` phase 2 is complete (field levels and user permissions); sharing and deny rules stay Later.
- Tests: `forgebase-core` `row_scope` unit tests (own name, one Link, two Links to one target, two restricted targets, an unrestricted target, empty set, Administrator, System, child DocTypes), `forgebase-db/tests/user_permissions.rs` (every path above against PostgreSQL, the lock proof, the kept rule, create and amend refusal, titles, import, two sites, loading, the management functions and the cap), the matrix in `forgebase-db/tests/permissions.rs`, and over HTTP and the command line `crates/forgebase/tests/user_permissions.rs` and `cli.rs` (the Administrator-only API with CSRF, freshness on the next request, methods).
