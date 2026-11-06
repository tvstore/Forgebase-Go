# 0009 Lifecycle pipeline and hooks

- Status: Accepted. Extended by [ADR 0013](0013-jobs-scheduler-and-transactional-outbox.md) (2026-10-30): hooks may enqueue jobs into the transactional outbox; they still have no database access. Extended by [ADR 0015](0015-workflow-states-and-transitions.md) (2026-10-30): a new hook point, `before_transition`, for DocTypes with a workflow.
- Date: 2026-10-28 (owner decisions and implementation details separated on 2026-10-29, per owner instruction)

## Context

[ADR 0006](0006-extension-model.md) decided that apps are Rust crates and that hooks are a trait with default no-op methods, registered explicitly and run in registration order. The owner decisions 4 and 7 of the M2 plan (2026-10-28, see [architecture, section 3](../blueprint/architecture.md#m2-owner-decisions-2026-10-28-accepted)) narrowed the first version: hooks are synchronous and pure, may modify the document or reject it, and have no database access in M2; the insert order is fixed and any hook failure rolls the operation back; ADR 0009 records it.

Frappe offers many overlapping hook surfaces (controller methods such as `before_insert`, `validate`, `before_save`, `on_update`, `on_submit`, `on_trash`, plus `doc_events` in `hooks.py`), and its documentation does not state the relative order of some of them clearly (Source-indexed, order Unverified; see the [feature matrix](../blueprint/feature-matrix.md)). Forgebase wants one small set of hook points with one tested order.

Before this ADR, `forgebase-db` validated and wrote a document in a fixed order but had no hook points, and `update` validated only the fields named in the changes.

## Decision

The owner decided (M2 plan decisions 4 and 7): synchronous, pure hooks without database access; the insert pipeline order; rollback of the whole operation on any hook failure; and this ADR. Registration applies [ADR 0006](0006-extension-model.md); refusing hooks and CRUD for child DocTypes follows from M2 decision 3 (child tables); that hooks must be defensive follows from the insert order. The update and delete pipelines and the further choices in [Implementation details (M2.3)](#implementation-details-m23) were made during implementation and reviewed; they are not owner decisions.

- **Hooks are synchronous and pure.** `forgebase_core::hooks::DocTypeHooks` has default no-op methods `before_insert(ctx, &mut doc)`, `validate(ctx, &mut doc)` and `before_delete(ctx, &doc)`, each returning `Result<(), HookError>`. A hook gets the document and a `HookContext { doctype, actor, operation }` and nothing else: **no database access in M2**, no async. `forgebase-core` stays free of I/O.
- **Registration.** `Registry::add_hooks(doctype, Arc<dyn DocTypeHooks>)` registers a hook set for a DocType that is already in the registry; an unknown DocType and a child DocType are refused (a child table has no lifecycle of its own; the hooks of its parent see the rows). Several sets per DocType run in registration order, at every hook point.
- **Fixed order, one transaction.** Each operation of `forgebase-db` runs in one transaction (a savepoint inside a caller's), and any hook error rolls all of it back (`DbError::Hook { doctype, hook, message }`). **Insert order:** reject a child DocType; `before_insert`; `validate`; framework validation; Link resolution; naming; write the document and its rows.
- **Hooks run before framework validation**, so they may see invalid input and must be defensive; the framework validates again after them, so a hook cannot leave an invalid document behind. `validate` hooks run on insert and update (and on submit, with M2.4); `before_submit` and `before_cancel` are added in M2.4 as new default methods, which does not break existing implementations. `Operation` is `#[non_exhaustive]` for the same reason.

## Implementation details (M2.3)

These choices were made while implementing M2.3 and passed the independent review of that change; they are recorded here as reviewed implementation details of M2.3, not as owner decisions.

- **Panics are not caught.** A hook is app code; a panic aborts the operation like any other panic in the caller (the transaction is rolled back when it unwinds).
- **Update pipeline:** load the stored document and rows with a row lock (`NotFound` or `Conflict` are reported here, before any hook); merge the changes into it; reject a change to the naming field; `validate` on the merged document; framework validation of the whole merged document; Link resolution; write every column field, replace the rows of each table that differs, bump `version`.
- **Delete pipeline:** load with a row lock (`NotFound` or `Conflict`); `before_delete` with the stored document and its rows; delete.
- **Update sees the whole document.** Hooks and validation work on the stored values with the changes applied, not on the changes alone. A hook cannot change the naming field.
- **Delete takes an actor.** `delete` has an `actor` argument like `insert` and `update` (non-empty, no NUL; given to the hooks, not stored since there is no audit yet, and not authenticated), so `HookContext::actor` is a plain `&str` for every operation, `Operation::Delete` included. Adding it now, in 0.x, avoids a second breaking change when permissions (M3) need the actor.
- **Behaviour change of `update`** (0.x): it reads the document first (a `FOR NO KEY UPDATE` lock), validates the whole merged document, writes every column field, and replaces only tables whose rows differ. A stale update with invalid changes now reports `Conflict`, not the validation errors. An update always bumps `version`, even when nothing differs (an explicit save). `delete` also reads the document first, for `before_delete`.
- **Lock choice.** `update` locks with `FOR NO KEY UPDATE`: it is the lock the UPDATE itself takes, it serializes with other updates and deletes, and it leaves Link lookups (`FOR KEY SHARE`) and foreign key checks unblocked (PostgreSQL upgrades it to `FOR UPDATE` when a unique (key) column changes). `delete` locks with `FOR UPDATE`, so concurrent linkers wait and `before_delete` hooks do not run for a delete that would then be refused.

## Implementation details (M2.4)

These choices were made while implementing M2.4 (docstatus). They concern the hooks and the pipeline only; they are implementer decisions for review with that change, not owner decisions. The docstatus rules themselves are in [architecture, section 6](../blueprint/architecture.md).

- **Submit pipeline:** load with `FOR NO KEY UPDATE` (`NotFound`, `Conflict`); the document must be a draft; `validate` hooks, then `before_submit` hooks, both on the whole stored document with `Operation::Submit`; framework validation of the whole document; Link resolution; write like an update (so changes made by hooks are persisted) with `docstatus = 1` and the version bump in one statement.
- **Cancel pipeline:** load with `FOR NO KEY UPDATE`; the document must be submitted; `before_cancel` with the stored document and rows; write `docstatus = 2` and bump the version. No validation: nothing else changes.
- **Amend pipeline:** load the cancelled document with `FOR NO KEY UPDATE`; it must be cancelled and not amended yet; then the insert pipeline on a copy, with `Operation::Amend`: `before_insert`, `validate`, framework validation, Link resolution, naming (the amend name rule; a series takes no number), write as a draft. Hooks that must see every new document keep working for amendments and tell them apart with `ctx.operation`.
- **Hook signatures.** `before_submit` takes `&mut Document` (like `validate`: a submit hook typically finalizes values, and the framework validates the result afterwards); `before_cancel` takes `&Document` (like `before_delete`: a veto).
- **Docstatus checks come after the locking read and before any hook:** a cancelled document cannot be updated, a submitted one cannot be deleted. On a submitted document an update may only change `allow_on_submit` fields; the check runs on the merged document before the `validate` hooks and again after them, so a hook cannot change a locked field (`DbError::FieldLocked`).
- **Series and the pipeline.** A naming series takes its number at the naming step, in the insert transaction, so a document refused by a hook or by validation takes no number, and a rollback gives the number back.

## Alternatives considered

- **Async hooks with a restricted database handle.** Would let hooks read other documents, but needs a handle design (what it may do, user or system permissions, savepoints, a way to forbid `COMMIT`) and makes `forgebase-core` async or `forgebase-db`-aware. Not needed by any approved requirement; deferred.
- **Class override (Frappe's `override_doctype_class`).** Rejected in ADR 0006.
- **Many Frappe-style hook points** (`before_save`, `on_update`, `after_insert`, ...). More surface and ambiguity about order; start with the three that M2 needs and add points when a requirement needs them.
- **After-commit hooks.** Effects that must happen only after commit belong to the transactional outbox (M5), not to a callback that can fail after the data is committed.
- **Validating only the changes on update, as before.** Hooks would see a partial document and could not enforce rules across fields; a stored value that is invalid would never be noticed. Rejected in favour of merging (an implementation choice, see Implementation details).

## Consequences

- Hooks cannot read other documents yet. Hooks that read the database, and effects after commit, need a new ADR that supersedes or extends this one; the outbox arrives in M5.
- A hook runs while the document row is locked, so it must be quick; a hook error or panic undoes the whole operation.
- One registration point (`Registry`) and one trait keep the extension surface small; adding a hook point later is an additive change.
