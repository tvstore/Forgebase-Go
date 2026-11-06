# 0011 Permission model, phase 1

- Status: Accepted (owner decisions 2, 9, 10 and 12 of the M3 plan, 2026-10-29; implementation details of M3.2 separated below). Extended by [ADR 0015](0015-workflow-states-and-transitions.md) (2026-10-30): the transition-role rule, a transition needs a role of the transition and the underlying right.
- Date: 2026-10-29

## Context

M3.1 ([ADR 0010](0010-authentication-and-sessions.md)) stores users, roles, sessions and API tokens, but nothing yet decides what a user may do. Every operation of `forgebase-db` trusts its caller. M3.2 adds the role permission model, enforces it in every document operation, and adds the list query that the HTTP API (M3.3) needs. The [security model](../blueprint/security-model.md) already requires one evaluator in `forgebase-core`, deny by default, and read filtering in SQL instead of "fetch then filter".

## Decision

The decisions below are the owner's (M3 plan, 2026-10-29).

- **Rules in the DocType file** (decision 2). A DocType lists `[[permissions]]` entries: `role`, the rights `read`, `write`, `create`, `delete`, `submit`, `cancel`, `amend` (all default `false`) and `if_owner` (default `false`: the rights then apply to documents the caller owns only). The built-in role `Administrator` bypasses every rule. **Deny by default:** a DocType without rules is inaccessible to everyone except `Administrator` (and the trusted system context below). **Child rows follow the parent's permissions;** a child DocType has no rules of its own.
- **Information leakage** (decision 9). A Link to a document the caller cannot read is reported exactly like a link to a missing document (`LinkNotFound`). A "still linked" delete error names the referencing DocType only if the caller can read that DocType.
- **List query** (decision 10). Filters `=`, `!=`, `<`, `<=`, `>`, `>=`, `in`, `is null`, `like` (text only); multi-field sort; limit and offset paging, default 20, maximum 500; field names are validated against the metadata and values are always bound; `if_owner` is applied as `WHERE owner = $user` in the same statement, never by fetching and filtering.
- **This ADR** (decision 12). Planner decisions of the M3 plan that it records: every `forgebase-db` operation takes a caller context (username and roles) and enforces permissions; a trusted system context exists for the command-line tool, and is documented; the filter AST is typed; `HookContext.actor` is the caller's username.

## Implementation details (M3.2)

These were chosen while implementing M3.2; they are to be reviewed with that change, not owner decisions.

**Evaluator and caller (`forgebase_core::permission`).**

- `Caller` is `User { username, roles }` or `System { actor }`. `System` is the trusted context for code that is not acting for a user (the CLI, migrations, tests); it bypasses every rule and **must never be built from request data**. A user is an administrator if `roles` contains `Administrator` (constant `ADMINISTRATOR`, compared exactly, case-sensitive). `AuthUser::caller()` converts an authenticated user.
- `evaluate(&DocType, &Caller, Right) -> Access` is the **only** permission decision. It is pure. The result is `All`, `OwnerOnly` or `Denied`: the union over the rules whose role the caller has and that grant the right; a rule without `if_owner` gives `All`, else one with `if_owner` gives `OwnerOnly`, else `Denied`. There are no deny rules and no inheritance between roles. `forgebase-db` turns the result into SQL or a refusal and never compares roles itself. It is unit-tested as a full matrix (right x `if_owner` x role).
- **Definition errors** (checked when a DocType is loaded): an invalid role name (the same rule as the roles of users, one function `check_role_name` shared by both, so a rule can always name a role a user can have); a rule that grants nothing (almost certainly a mistake); `submit`, `cancel` or `amend` on a DocType that is not submittable; two rules with the same role and the same `if_owner` (merge them; the same role with and without `if_owner` is allowed); any `[[permissions]]` on a child DocType. A rule for `Administrator` is allowed (pointless, harmless).
- **`Caller` name rules.** The name recorded as `owner` and `modified_by` and given to hooks is the username, or the system actor. It must be non-empty without NUL (`DbError::InvalidActor`), for users too. A system actor that equals a username (the CLI default is `cli`) writes the same `owner` value: do not create a user named like a system actor, or documents of the system would count as that user's own under `if_owner`.

**Enforcement (`forgebase-db`).** The `actor: &str` argument of `insert`, `update`, `delete`, `submit`, `cancel` and `amend` became `caller: &Caller`; `get` gained a `caller` argument. The rules, in the order they apply:

1. **Doctype-level check first.** A right the caller has on no document of the DocType at all (`Denied`) is `DbError::PermissionDenied { doctype, right }` before anything is looked up. The answer does not depend on any document, so it tells nothing about the data (that a DocType exists is not secret). `update`, `delete`, `submit`, `cancel` and `amend` also need read access (a caller who can write but not read cannot see the document); without it the error is `PermissionDenied` for the attempted right.
2. **Read access is applied in SQL.** For an `if_owner` read every statement that finds a document has `AND "owner" = $n` (bound), including the locking read (`FOR NO KEY UPDATE` / `FOR UPDATE`), so a hidden row is neither returned nor locked. `get` returns `None`; the other operations return `NotFound`, exactly as for a missing name.
3. **Then the right.** A readable document of somebody else under an `if_owner` write, delete, submit, cancel or amend right is `PermissionDenied` (the caller can see the document, so this leaks nothing).
4. **Then the version.** `Conflict` is reported only for a document the caller may use the right on, so a stale `expected_version` on a hidden document is `NotFound`, never `Conflict`, and the current version of a hidden document is never revealed. The safeguard queries after a write that matched no row apply the read scope too.
5. **Create** needs the create right. `if_owner` does not restrict it: the creator is the owner. **Amend** needs the amend right and a readable cancelled original; the new draft is owned by the caller; the create right is not needed.
6. **Links** (decision 9). A Link name in a document or a row is resolved with the caller's **read** access on the **target** DocType: no read access means no lookup at all, an `if_owner` read adds `AND "owner" = $n`, and the result is `LinkNotFound` exactly as for a missing target (one code path builds the error). `System` and `Administrator` resolve everything. The same applies to `amend` (the copy's Links are resolved again) and to child rows. **On `update` and `submit` the check applies to new or changed Link values only; stored ones are kept.** A document Link equal to the stored one, and a row Link whose name is among the stored values of the same field in the same table, is resolved with full access (the caller already sees that name through `get`; it can only reuse a name already stored on this document), so a document that links to a target the caller cannot read can still be updated or submitted. A changed or new value gets the check above. The kept rule is scoped per field and per child table: a name stored in one Link field (or in one table's rows) is not kept in another field of another target DocType or in the other table, and a value that a lifecycle hook sets is a new value too (tests: `the_kept_rule_is_scoped_per_field_and_per_table`, `a_before_submit_hook_cannot_link_to_an_unreadable_document`). **`amend` never uses the kept rule:** it creates a new document, so amending a cancelled document whose stored Link the caller cannot read fails with `LinkNotFound`.
7. **Still-linked errors** (decision 9). `LinkedDocumentExists::referenced_by` is `Some(doctype)` only if the caller has read access (`All` or `OwnerOnly`) to the referencing DocType (for a link in a child row: its parent); otherwise it is `None` and the message says only "linked from another document". This still tells the caller that something links to the document: the delete would fail whatever the DocType.
8. **Hooks** run after the checks, so a refused operation runs none. `HookContext.actor` is the caller's name; the hooks do not see the roles.
9. **Child rows** follow the parent: no separate check, and a child DocType is still never addressable alone (`ChildDocType`).

**List query (`forgebase_db::list`).**

- `ListQuery { filters, sort, limit, offset }`, `Filter { field, op }` with `FilterOp::{Eq, Ne, Lt, Le, Gt, Ge, In, Like, IsNull}` and `Sort { field, descending }`. The operand of each operator is part of the variant, so a filter cannot have the wrong shape; `IsNull(true)` is `IS NULL`, `IsNull(false)` is `IS NOT NULL`.
- Fields: every column field and the standard columns `name`, `owner`, `modified_by`, `created_at`, `modified_at`, `version` and (submittable DocTypes) `docstatus`. A name is looked up in the metadata; an unknown one (or a Table field) is `DbError::InvalidQuery` and nothing reaches SQL. Identifiers are quoted. Every value is a bound parameter with the cast of its field type. A value of the wrong kind, a NUL character, a null operand (use `IsNull`), an empty or oversized `In` list (1 to 500 values), or an operator that does not fit the field (`Like` on a non-text field, ordering comparisons on Check and Link) is `InvalidQuery`.
- **Link filters** compare through a bound subquery on the target table by the target's `name` (`=`, `!=`, `in`, `is null`); `!=` leaves out unlinked rows like every other `!=`. The subquery is not restricted by the caller's rights on the target: it can only select among the rows the caller can already read, and what it tells (that a readable row links to a name) is what the caller sees in that row. Sorting by a Link sorts by the target's name.
- `Like` is a SQL `LIKE` pattern (`%`, `_`, `\` escapes), case-sensitive, on Data, Text, Select, `name`, `owner` and `modified_by`. `!=` and the comparisons leave out nulls (SQL).
- Sorting always ends with `name` ascending (unique), so the order is total and paging is stable. Nulls sort as PostgreSQL does.
- **Paging.** `limit` defaults to 20; a limit above 500 or equal to 0 is an **error**, not silently changed (a client that asks for more must know it did not get it). `offset` defaults to 0.
- **Read access** is part of the same statement. No read access at all is `PermissionDenied` (listing a DocType you cannot read; the DocType's existence is not secret).
- The rows are as `get` returns them, except that `tables` is empty (child rows are not listed) and Link values are names. One statement, no transaction.

**CLI.** `forgebase doc ...` keeps running as the trusted system context: `--actor` names the `System` actor (default `cli`), no permission rules apply, output and behaviour are unchanged. New: `doc list DOCTYPE [--filter JSON] [--sort FIELD[:desc]]... [--limit N] [--offset N]`, printing a JSON array. `--as-user` was left out: real users arrive with the HTTP API (M3.3).

**M3.1 follow-ups included.** The login failure window is now `>=` (a failure exactly 15 minutes after the previous one still counts), so "at most 15 minutes" in the documentation is exact; a test proves that a failure 14 minutes 59 seconds after the previous one continues the count.

## Alternatives considered

- **Permission checks in the HTTP layer only.** Rejected: every future caller (CLI-as-user, jobs, hooks that read) would have to repeat them; one enforcement point in the library is the stated principle.
- **Fetch, then filter in Rust.** Rejected by decision 10: it breaks paging, leaks through counts and timing, and invites mistakes.
- **PostgreSQL row-level security.** Rejected for phase 1 (idea in the security model's deferred items): it ties permissions to database roles per user and complicates pooling; the evaluator plus SQL predicates is simpler to read and test.
- **Deny rules, role hierarchies, permission levels, user permissions, sharing.** Not in phase 1 (decision 2 asks for a union of allow rules); each would need its own decision.
- **`PermissionDenied` for hidden documents.** Rejected (decision 9): it would confirm that the document exists.
- **Clamping a too large `limit`.** Rejected: silent clamping hides data from a client that does not know.

## Consequences

- **Breaking change (0.x).** The `actor: &str` argument is `caller: &Caller` everywhere, `get` has a `caller`, `DocType` has a `permissions` field, and `DbError` has `PermissionDenied` and `InvalidQuery`. All code that called the document functions now says whether it acts for a user or as the system.
- **Known limits.**
  - A **unique violation** (a `unique` field, a field-naming name, an amended name) and `AlreadyAmended` can tell a caller that a document exists that they cannot read: the database enforces uniqueness over all rows. Closing this would need either dropping the constraint or checking uniqueness per caller, which phase 1 does not do. The name of an amendment (`<name>-<n>`) is derived from a document the caller can read.
  - `LinkedDocumentExists` still reveals that some document links to the one being deleted.
  - Timing differs between a hidden and a missing document only by database effects (indexes, locks); no attempt is made to hide it.
  - `FOR KEY SHARE` (Link lookups) and `FOR UPDATE` need the `UPDATE` privilege on the table, so a **read-only database role** cannot run these operations. A read-only role is a **non-goal** of M3.2; reports (later) must be designed around it.
  - `owner` has no index: an `if_owner` read on a very large table scans by owner. Add an index when a measurement shows the need (schema sync would add it as an additive step).
  - Rules are read from the DocType files at startup: changing a rule needs a restart (or a new registry); roles of a user are read when the caller is built (M3.3: per request), so a role change applies to the next request.
  - Rights are per DocType, not per field; list results include every column field.
- The system context is a bypass by design. Misuse (building it from request data) would defeat the model, so the HTTP layer must only ever build `Caller::User` from a validated session or token, and a review of M3.3 must check it.
- Decisions deferred: field-level permissions, user permissions, sharing, a read-only role, `/api/v1/method`, per-row child permissions, list results with child tables, aggregation and grouping.

## Amendment (2026-10-31, M9.3): the `export` and `import` rights

Owner decision 7 of the M9 plan adds two rights to the list above, `export` and `import`, as keys of `[[permissions]]` (`false` by default) and as `Right::Export` and `Right::Import` of the one evaluator. The rest of this ADR is unchanged: no inheritance, no deny rules, `if_owner` as before, deny by default, so **an existing DocType gives nobody the new rights** (migration note: grant `export = true` to roles that should export). They are not lifecycle rights: they are valid on every DocType, submittable or not, and the metadata API reports them for all.

- **Export** needs `read` and `export`; if either comes from an `if_owner` rule the export is limited to the caller's own documents, by the same `WHERE "owner" = $n` of the list query.
- **Import** needs `import` and `create`, and `write` for an upsert; every row then runs through `insert` or `update`, which check their own rights per row.

The details are in [ADR 0021](0021-import-and-export.md).

## Amendment (2026-11-01, M10.2): field permission levels

Owner decision 6 of the M10 plan adds **field levels**, recorded in [ADR 0024](0024-field-permission-levels.md). The rest of this ADR is unchanged except where the sentences "Rights are per DocType, not per field; list results include every column field" and the deferral of "field-level permissions" above are concerned:

- A field has a `perm_level` (0 to 9, 0 by default) and a `[[permissions]]` rule has a `level` (0 to 9, 0 by default). **A rule at level 0 is the rule of this ADR, unchanged**, and `evaluate` decides about documents with the rules of level 0 only: a rule of a higher level grants `read` and `write` on the fields of that level and never a document right.
- A caller reads or writes a field at level `n > 0` only with a rule `level = n` (the new pure function `field_access`, next to `evaluate`). A field the caller cannot read is **unknown** to them: omitted from get, list, summary, export and the metadata API, refused in filters, sort and group-by with the error of a missing field, refused in input as an unknown field; a field they can read but not write is refused in input with the new `ValidationError::NotPermitted`.
- A DocType without `perm_level` and without `level` rules is not changed in any way. Query reports stay exempt, as they are from `if_owner`.

## Amendment (2026-11-01, M10.3): user permissions

Owner decisions 7 and 8 of the M10 plan add **user permissions**, recorded in [ADR 0025](0025-user-permissions.md). The rest of this ADR is unchanged except where the deferral of "user permissions" above and the sentences about the read scope and Links are concerned:

- A user can be restricted to some documents of a DocType (`fb_user_permission`, set with `forgebase user permission` or the Administrator-only API). The pure function `row_scope`, next to `evaluate`, says which documents of a DocType a caller may see: a restricted DocType by its own name, and every document whose **top-level Links to a restricted DocType are empty or allowed** (child rows do not count). `forgebase-db` applies it **in SQL, in the same statement and by the same code as `if_owner`**, and the two are ANDed: `get`, `list`, `summary`, export, the locking reads of `update`, `delete`, `submit`, `cancel`, `amend` and transitions, link resolution and link titles. A hidden document is `None` or `NotFound`, exactly like a missing one, **before the version is compared**.
- A Link is resolved with the read access **and the scope of the target**: a target outside it is `LinkNotFound`. **The kept rule of item 6 is unchanged** (a stored value is kept with full access); ADR 0025 explains why that is safe.
- A user restricted on a DocType cannot create or amend a document of it (`PermissionDenied`).
- A user without user permissions, `Administrator` and the system context are not changed in any way; query reports stay exempt, as they are from `if_owner` and levels.
