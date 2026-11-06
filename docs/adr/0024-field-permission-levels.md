# 0024 Field permission levels

- Status: Accepted (owner decision 6 of the M10 plan, 2026-10-31, *"setuju semua rencana mu"*: levels 0 to 9, hidden fields omitted everywhere and not filterable, reports exempt; the design below is the specification of the M10.2 working session and is reviewed with its pull request). Extends [ADR 0011](0011-permission-model-phase-1.md) and builds on [ADR 0023](0023-metadata-overlays.md).
- Date: 2026-11-01

## Context

[ADR 0011](0011-permission-model-phase-1.md) decides rights per DocType: a caller who may read a document reads **every field** of it ("Rights are per DocType, not per field"). Real applications need a few fields that only some roles see or change: a credit limit, a cost price, an internal note. In Frappe this is the permission level (`permlevel`); the owner approved the same idea in the M10 plan (decision 6): a field has a level from 0 to 9, a rule grants read and write at a level, a field above the caller's levels is omitted everywhere and cannot be filtered, sorted or grouped by, and query reports are exempt.

[ADR 0023](0023-metadata-overlays.md) gave apps and sites two ways to add a field; both can now carry a level, so a site can protect a field it adds without a rebuild.

## Decision

### Metadata

- **`perm_level`** on a field: an integer 0 to 9, default 0, written in the DocType file, in an overlay file and in a site custom field (TOML or JSON, the same `RawField`).
- **`level`** on a `[[permissions]]` rule: an integer 0 to 9, default 0. **A rule at level 0 is the document rule of ADR 0011, unchanged.** A rule at level `n > 0` grants **`read` and `write` on the fields at level `n`, and nothing else**.
- `evaluate` (ADR 0011) looks at the rules of level 0 only, so a rule of a higher level can never grant a document right.
- A DocType without `perm_level` and without `level` rules behaves exactly as before; nothing in this ADR runs for it.

### Definition errors

Checked when a DocType loads (and again when an overlay or a site field is added, because the whole DocType is validated again):

| Error | Why |
|---|---|
| `perm_level` or `level` outside 0 to 9 (also a non-integer) | A typo must not silently mean "everybody" or "nobody". |
| `perm_level` above 0 on a field of a **child** DocType | A table replaces its rows on an update, so a hidden child column would be wiped. Protect the whole table with the **Table field's own `perm_level`** on the parent. (Orchestrator choice of the working session.) |
| A rule with `level` above 0 that grants anything but `read` and `write` | `create`, `delete`, `submit`, `cancel`, `amend`, `export` and `import` are document rights, decided at level 0. |
| A rule with `level` above 0 and `if_owner = true` | Kept out for simplicity (orchestrator choice): the level 0 rule already limits the documents; a level is then seen on the documents the caller may read. |
| A rule with `level` above 0 that grants `write` without `read` | A field that cannot be seen cannot be written. |
| Two rules with the same role and the same `level` above 0 | Merge them (the check of ADR 0011 for level 0 is unchanged: same role and same `if_owner`). |
| `perm_level` above 0 on the **naming field**, the **`title_field`** or a **`list_fields`** column | These settings show the field to everybody who can read a document (the name, the title of a Link, the desk list). The settings of a DocType that name a field are exactly these three (the other keys are `name`, `label`, `submittable`, `is_child`, `icon`, `[workflow]` and `[[permissions]]`). The **workflow** names no field: its state is the standard column `workflow_state`, and a field with that name is already refused. |
| `required = true` with `perm_level` above 0 | A caller who cannot write the field could never save a document. The framework has **no default value**, so this is always an error (orchestrator choice). |

**Not an error:** a field at level `n` when no rule of the DocType has `level = n`. The field is then visible only to Administrator and the system context, which is a legitimate use (a field for hooks and operators).

An app overlay field and a site custom field may carry `perm_level`; their level rules come from the **target DocType's** `[[permissions]]` (an overlay file holds only `[[fields]]`, so it cannot add rules). A site field is still refused when `required` (ADR 0023).

### The evaluator

One pure function in `forgebase_core::permission`, next to `evaluate`, unit-tested as a full matrix (rights at level 0, `if_owner`, level rule, role union, Administrator, System):

```rust
pub fn field_access(doctype: &DocType, caller: &Caller) -> FieldAccess
```

`FieldAccess` answers `can_read(level)` and `can_write(level)` (two bitsets) and `shows(level)`.

- **System and Administrator:** every level, read and write.
- **Level 0:** readable when `evaluate(Read)` is not `Denied`; writable when `evaluate(Write)` or `evaluate(Create)` is not `Denied`. One "writable" bit is enough: `forgebase-db` checks the right of the operation itself first (`create` for an insert, `write` for an update, with `PermissionDenied`), and `FieldAccess` decides about fields only.
- **Level `n > 0`:** readable when level 0 is readable **and** a role of the caller has a rule `level = n` with `read`; writable when level 0 is writable **and** a role of the caller has a rule `level = n` with `write`. So `if_owner` at level 0 alone limits the documents on which a level is seen. Rights are a union over roles: a level granted by one role applies to every document the caller can read through any of its roles, not only through the role that carries the level rule. A caller who can only create (no read at level 0) can never set a field above level 0: higher levels need level 0 to be readable, and an unreadable field is refused as unknown.
- **Level 0 fields are never hidden by levels** (`shows(0)` is always true): a DocType without `perm_level` is unchanged, also for a caller who may only create.

Helpers: `readable_fields(doctype, access)`, `readable_field(doctype, access, name)` (a hidden field gives `None`, **exactly like a name that is not a field**) and `readable_doctype(doctype, access)` / `readable_doctype_for(doctype, caller)`, the DocType as the caller sees it (borrowed, no copy, when nothing is hidden).

### Enforcement: one idea, applied in `forgebase-db`

Everything is in `forgebase-db` (module `field_levels` and the call sites), so HTTP, methods (`ctx.documents()`), the CLI and jobs get it. `Caller::System` and Administrator bypass.

**The idea.** A field the caller cannot read is *unknown* to the caller.

- **Reads work on the caller's DocType** (`readable_doctype`): `get`, `list`, `summary`, export, saved views, and the HTTP layer that parses input and filters. The hidden columns are never selected, the hidden Table fields are never loaded, and a filter, a sort, a group-by or an aggregate on a hidden field fails in the same lookup as a name that does not exist, with the **same message, byte for byte** (the tests compare it with the answer of a registry in which the field does not exist). The same holds for link-filter subqueries (they are built from the same lookup), saved views (checked when written with the same code, run as a list), import and export filters and `fields`, and methods (they call the same functions).
- **Writes work on the whole DocType** (hooks need every field, and `update` merges into the stored document), after a check of the caller's input:
  - **Insert.** Any key for a field the caller cannot read is `ValidationError::UnknownField`, **whatever the value** (null too: otherwise a null would tell a hidden field from a missing one). A non-null value for a field the caller can read but not write is the new `ValidationError::NotPermitted { field }` (HTTP `422 field_not_permitted`). Both are checked **before any hook runs**.
  - **Update.** The same rules on `changes`. Sending back the **stored value** of a readable but unwritable field is allowed, as for `read_only` (a missing stored value counts as null, so null over a stored value is a change); an **unreadable** field in `changes` is unknown even when it equals the stored one. Omitted fields keep their stored values: update is a merge, so **hidden data survives an update by a lower-level user**.
  - **For a DocType with levels, the unknown names of a caller's input are reported by the same early check**, alone and before the hooks, whether the name is hidden or really unknown, so the two cannot be told apart by the other problems or by the hooks that did or did not run.
  - **Table field at level `n`:** replacing its rows needs write at `n`; its rows are omitted on read without read at `n`.
  - **Import** goes through `insert` and `update` per row, so the rules apply per row; a hidden CSV column is an unknown column.
  - **Amend** copies the cancelled document, which is not caller input: the copy keeps the hidden values (the copy's Links are resolved again with the caller's rights, as ADR 0011 says).
  - **Transitions** take no field input (their hooks change the document, which is not caller input), so there is nothing to check; they return a redacted document.
- **What is returned is redacted.** `insert`, `update`, `submit`, `cancel`, `amend` and transitions return the document without the hidden fields, after the hooks ran and after the webhook deliveries were written (those carry the whole document, below).
- **Metadata API.** `GET /api/v1/meta/{doctype}` leaves hidden fields out and reports a field the caller can read but not write as `read_only: true`, so the desk (which never sends a `read_only` field and shows it read-only; checked in `desk/src/fields.ts` and `access.ts`) needs no change. The response depends on the caller already (the rights), and every API response is `no-store`, so no cache can serve one caller's metadata to another.
- **Link titles.** The `title_field` of a target is at level 0 (definition error otherwise), and a hidden Link value is not in the documents the titles are collected from, so no title can reveal a hidden field.

### Hooks

Hooks always see and may set **every field** (they run on the whole document). A value a hook sets is not caller input and is not checked against the caller's levels. A hook author must know that a hook that copies a hidden field into a visible one, or puts a hidden value in an error message, defeats the level.

### Exemptions (documented, not enforced)

- **Query reports** (owner decision 6): a report is app SQL shown to its roles whatever the DocType's rules say (ADR 0016); levels do not apply inside it, as `if_owner` does not.
- **Webhook payloads:** a delivery carries the whole document, as stored. It is configured by an Administrator for a system the Administrator chose (ADR 0020).
- **Jobs and the scheduler** run as the system context; the **`doc` and `data` commands** are the system context too.
- **Fixtures** are applied as the system context.

### The leak review

Every place that names a field in an error or a response was checked.

| Place | Result |
|---|---|
| Framework validation of the merged document (a stored hidden value that is no longer valid, a value a hook set) | Replaced by the new `ValidationError::Hidden`, which names nothing (`validation failed: a field that you cannot see has a value that is not valid`); one entry however many. The Administrator still gets the real error. |
| `LinkNotFound` for a hidden Link field | Only a hook can set one (the caller's own input is refused earlier); scrubbed to `Hidden` like the above. Stored Links are kept on update, so a hidden Link to an unreadable target does not block an update. |
| `FieldLocked` naming a hidden field (a hook changed it on a submitted document) | Scrubbed to `Hidden`. |
| Unique violation on a hidden field | Only a hook can set the value (the caller's input is refused): the message is generic, with no field and no value. |
| Unique violation on a **visible** unique field | Unchanged (ADR 0011, known limit). |
| `UnknownField` and `NotPermitted` | They name what the caller sent: a name the caller wrote, or a readable field. |
| Summary and list errors | `InvalidQuery` is built from the caller's DocType: the very message of an unknown field. |
| Saved views | Written with the caller's DocType (the unknown-field message). A shared view that an Administrator wrote and that names a hidden field is **not listed** to the caller (its definition would tell the field). |
| Export header `X-Forgebase-Row-Count`, `ExportTooLarge` | Count of documents the caller may read, not of fields. |
| The error body bound | Unchanged: the scrubbed messages are shorter than the bound. |
| Meta, `link_titles` | Above. |

**Known limits.**

- A hook's own error message (`DbError::Hook`) is the app's text: it must not contain hidden values.
- `DbError::Decode` (a stored value that cannot be read, which only manual SQL can produce) names the column, also a hidden one, on a write path.
- A stored hidden value that is invalid makes every update of the document by a caller who cannot read it fail with the generic message (the whole merged document is validated). The Administrator fixes the value.
- `ViewExists` can tell that a shared view with a label exists although it is not listed.
- A user's own private view that names a field they can no longer read is not listed to them any more; it can still be deleted by its id.
- `amend` copies hidden values of the cancelled document, and its Links are resolved again with the caller's rights. A hidden Link to a target the caller cannot read, or a hidden `unique` value, then makes the amend fail with the generic message, and the caller cannot fix it; a caller who can read the level (or the Administrator) amends instead.
- A visible unique field leaks existence as in ADR 0011.
- The row count of a list or export, and the existence of documents, are not about fields and are as before.

### Tests

The `field_access` matrix; every definition error; overlay and site-field validation; real PostgreSQL tests for get, list, summary, export (CSV and JSON over HTTP), meta, byte-identical refusals, insert and update returns, hidden data surviving an update, a reader of level 1 (`NotPermitted`, stored value back, `read_only` in the meta), a Table at level 1, saved views, hooks, `if_owner` plus a level, submit, cancel, amend and transitions, the leak review, a site custom field before and after a live reload and disabled and enabled, and a method. The authorization matrix of `permissions.rs` checks that a level rule grants no document right.

## Alternatives considered

- **Per-field rules** (a role list on each field): simpler to read for one field, but it scatters the rules over the field list, cannot be added to a field by an overlay or a site without a second rule store, and has no place for "read but not write". Levels keep the rules in `[[permissions]]` (one place, one evaluator), and an overlay or a site field only names a level.
- **Masking instead of omitting** (return `null` or `"***"`): the field would still be in the metadata, the filters and the sort (an oracle: sorting by it orders the hidden values), and a client could not tell "empty" from "hidden". Omitting means the field is simply not there.
- **`403` for a hidden field** in input or in a filter: it confirms that the field exists. The owner's decision is that a hidden field is unknown.
- **Filtering the result in Rust** after reading all columns: rejected for the reason of ADR 0011 decision 10; the columns are not selected.
- **A level on child fields:** a table is replaced as a whole on update, so a hidden column would be lost; protecting the table is the clear way.

## Consequences

- `DocField` has `perm_level` and `PermissionRule` has `level` (public structs of an internal crate, [ADR 0012](0012-app-model-and-public-api-boundary.md); code that builds them with a struct literal adds the field). `ValidationError` has `NotPermitted` and `Hidden`; `DbError` and the public API are otherwise unchanged. `forgebase_db::summary`, `list`, `get` and `Export` take the same arguments as before.
- The HTTP error code `field_not_permitted` (422) is new.
- **Migration note:** nothing changes for a DocType without `perm_level` and without `level` rules. A rule with `level` was a load error before (unknown key), so no existing file means something new.
- Rights are still per DocType for everything but fields; user permissions (M10.3) come on top, and sharing is later.
