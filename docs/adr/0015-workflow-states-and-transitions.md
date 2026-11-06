# 0015 Workflow: states and transitions in the DocType file

- Status: Accepted (owner decisions 1 to 5 and 10 of the M8 plan, 2026-10-30); implementation details of M8.1 and M8.2 are separated below and are not owner decisions. Extends [ADR 0009](0009-lifecycle-pipeline-and-hooks.md) (a new hook point) and [ADR 0011](0011-permission-model-phase-1.md) (the transition-role rule), and updates the public surface of [ADR 0012](0012-app-model-and-public-api-boundary.md#public-surface-update-m81).
- Date: 2026-10-30

## Context

The [architecture](../blueprint/architecture.md#11-desk-reports-workflow) listed a workflow (states, transitions and roles mapped onto docstatus) as Proposed for M8. Business applications need approvals: a draft is sent for approval, a manager approves or rejects it, and an approved document is submitted. Today a user with the submit right just calls submit, and nothing records who moved a document or why it is where it is.

The owner approved the M8 plan on 2026-10-30. M8 is split into M8.1 (the workflow engine: this ADR), M8.2 (the desk: state badge, action buttons, history), M8.3 to M8.5 (other parts of the plan). The design must follow the guiding principle: build for longevity, a small state machine over what already exists (docstatus, the permission evaluator, hooks, the outbox), no new subsystem.

## Decision

The decisions below are the owner's (M8 plan, 2026-10-30).

1. **The workflow is defined by the app in the DocType file** (`[workflow]`) and is owned by the app. Another app cannot change it. A workflow defined at runtime, or changed by another app, is a non-goal (the customization overlay is M10).
2. **A transition needs the transition role and the underlying right.** The caller must have one of the roles the transition names **and** the right that its docstatus move needs: write for draft to draft and submitted to submitted, submit for draft to submitted, cancel for submitted to cancelled. A role never grants a right, and a right without the role does not allow the transition. `evaluate` stays the only decision for rights ([ADR 0011](0011-permission-model-phase-1.md)); the role rule is one pure function next to it, `transition_access`. `Administrator` and the trusted system context pass both, as everywhere.
3. **On a DocType with a workflow, plain submit and cancel are refused.** The state changes only through transitions, so every change of state has an action, a role and a log row.
4. **A new hook point, `before_transition`.** Conditions on a transition are written in **Rust**, not as expressions in TOML. The hook runs after the permission checks and before the `validate`, `before_submit` and `before_cancel` hooks of the move; it may veto, change values (as allowed below) and enqueue jobs ([ADR 0013](0013-jobs-scheduler-and-transactional-outbox.md)).
5. **A transition log, `fb_workflow_log`.** One row for every committed transition. The history view in the desk is M8.2.
10. **M8 is split into M8.1 to M8.5.**

Non-goals (from the plan): workflows defined at runtime or changed by another app; notifications on transitions (an app enqueues a job from the hook); parallel or branching approvals beyond a state machine; conditions as TOML expressions.

### The format

```toml
[workflow]

[[workflow.states]]
name = "Draft"              # unique in the workflow, 1 to 64 characters, no leading or trailing whitespace; the first state is the initial state
docstatus = 0               # 0, 1 or 2; 1 and 2 only on a submittable DocType
style = "neutral"           # badge tone: neutral (default) | success | warning | danger | info

[[workflow.states]]
name = "Pending Approval"
docstatus = 0
style = "warning"
edit_roles = ["Sales Manager"]   # only on a docstatus 0 state: who may update a draft in this state

[[workflow.states]]
name = "Approved"
docstatus = 1
style = "success"

[[workflow.transitions]]
action = "Approve"          # the label of the button; unique per `from` state
from = "Pending Approval"
to = "Approved"
roles = ["Sales Manager"]   # at least one
```

Unknown keys are refused, like everywhere in the DocType file. Loading checks, and reports together with the other DocType errors: not on a child DocType; at least one state; the first state has docstatus 0; unique state names; docstatus 1 or 2 only on a submittable DocType; `edit_roles` only on a docstatus 0 state, valid role names; each transition names existing states, has a valid and unique-per-`from` action, at least one valid role without duplicates, `from != to`, and a docstatus move among **0 to 0, 0 to 1, 1 to 1 and 1 to 2** (so nothing leaves a cancelled state, and a submitted document never goes back to draft); every state is reachable from the initial state. A field of the DocType may not be named `workflow_state` (the column of the state).

### The docstatus moves

Each move runs the pipeline it stands for, after the common start (lock, find the transition, check the role and the right, `before_transition`), and writes the new state with the document in one statement:

| Move | Right | Pipeline after `before_transition` |
|---|---|---|
| 0 to 0 | write | like an update of the whole document: `validate` hooks (`Operation::Transition`), framework validation, Links, write |
| 0 to 1 | submit | the submit pipeline: `validate` and `before_submit` (`Operation::Submit`), framework validation, Links, write with docstatus 1 |
| 1 to 1 | write | like an update of a submitted document: only `allow_on_submit` fields may differ |
| 1 to 2 | cancel | the cancel pipeline: `before_cancel` (`Operation::Cancel`); only `allow_on_submit` fields may be changed by `before_transition` |

### Other rules

- **`edit_roles`.** A plain update of a draft in a state that has `edit_roles` needs one of them in addition to the write right (`Administrator` and the system context pass). A state without `edit_roles` changes nothing.
- **Initial state.** Insert and amend set it; a caller can never set `workflow_state` (it is not a field, so the input is an unknown field).
- **Existing documents.** `migrate` gives a document that has no state the first state with its docstatus, and warns about documents in a state the workflow no longer defines (see below).
- **Without a workflow** a DocType behaves exactly as before.

## Implementation details (M8.1)

These choices were made while implementing M8.1 and are reviewed with that change; they are not owner decisions.

**Where things live.** `forgebase-core::workflow` has the types (`Workflow`, `WorkflowState`, `StateStyle`, `Transition`, `Move`), `forgebase-core::permission::transition_access` is the role-and-right function (it calls `evaluate` for the right), `forgebase-core::hooks` has `Operation::Transition`, `HookContext::transition` (a `TransitionInfo` with `action`, `from` and `to`; `None` for every other operation) and `DocTypeHooks::before_transition`. `forgebase-db::workflow` has `transition`, `available_transitions`, `history` and the backfill. The update, submit and cancel pipelines of `forgebase-db::document` were split so that a transition calls their tails (`update_tail`, `submit_tail`, `cancel_tail`) with the target state instead of copying them.

**The transition order.** One transaction: the DocType must have a workflow (`DbError::NoWorkflow`); the document is locked with the **read** grant, so an unreadable document is not found and a stale version is a conflict before anything else; the transition is found from the stored state and the action (`DbError::UnknownTransition`: a wrong state, an unknown action, or a document without a state); then the role and the right (`DbError::TransitionDenied`; `if_owner` is applied to the document like for update); `before_transition`; the pipeline of the move; the log row and the jobs the hooks enqueued; commit. A veto, a failed validation or any other error leaves the document, its state, the log and the jobs unchanged.

**Storage.** The column `workflow_state` (`text`, nullable) is added by schema sync to the table of a DocType with a workflow, like `docstatus`. Removing the workflow keeps the column and its data (sync warns, like for `submittable`); drift in the column is a conflict. The framework migration 0008 creates `fb_workflow_log (id, doctype, name, action, from_state, to_state, actor, at)` with an index on `(doctype, name, id)`. **Deleting a document deletes its log rows in the same transaction** (there is no audit trail yet, and a name can be reused after a delete); the delete removes them whether or not the DocType still has a workflow, so a removed workflow leaves no orphan rows behind. Amend does not copy the history.

**Backfill.** After the schema sync, `migrate` sets `workflow_state` of every document of a workflow DocType where it is NULL to the first state with the document's docstatus (a document whose docstatus has no state stays NULL), then counts the documents whose state is not one the workflow defines and prints one warning line per DocType on standard error (`warning: site `x`: DocType `y`: 3 documents are in a workflow state that no longer exists; no transition applies to them`); exit code 0. It does not touch `version` or `modified_at`, it is idempotent, and a dry run does not run it.

**Effective state.** The stored `workflow_state`; a NULL or unknown state has no transitions. The JSON of a document has `"workflow_state"` only when the document has one, so the output of a DocType without a workflow is unchanged. `workflow_state` can be filtered and sorted in list queries.

**HTTP.** `POST /api/v1/resource/{doctype}/{name}/transition` with `{"action", "version"}` returns the document (200). `GET .../transitions` returns `{"state", "transitions": [{"action", "to"}]}`: the transitions from the current state that this caller may take now (role and right, `if_owner` applied, and only those whose from-state has the document's docstatus; the hooks are not run, so one may still veto); it needs read access (404 otherwise). `GET .../history` returns `{"history": [{"action", "from", "to", "actor", "at"}]}`, oldest first, capped at the newest 500 rows. `GET /api/v1/meta/{doctype}` has `"workflow": null` or `{"states": [{"name", "docstatus", "style"}], "transitions": [{"action", "from", "to"}]}`: **roles and `edit_roles` are not sent**, like the `[[permissions]]` rules; the desk uses `/transitions` for what the caller may do. New error codes, all 409: `workflow_required` (plain submit or cancel), `no_workflow` (a transition, its list or its history on a DocType without a workflow), `unknown_transition`. A missing role or right, and an update refused by `edit_roles`, are 403 `permission_denied`.

**CLI.** `forgebase doc transition DOCTYPE NAME ACTION --version N [--doctypes DIR] [--actor NAME]` prints the document as stored. Like every `doc` command it runs as the trusted system context: **the roles of the transition are not checked**, and `--actor` is recorded in the log. `doc submit` and `doc cancel` on a workflow DocType fail with exit code 1.

**Public surface.** Only `TransitionInfo` is new at the crate root of `forgebase` (plus the new `Operation::Transition`, `HookContext::transition` and `DocTypeHooks::before_transition`). The workflow types are internal; apps write the workflow in the DocType file. The job API (`Documents`) has no `transition` yet: a job cannot take a transition, and its `submit` and `cancel` are refused on a workflow DocType.

## Implementation details (M8.2, desk)

These choices were made while implementing M8.2 and are reviewed with that change; they are not owner decisions, and the Decision section above is unchanged. The desk needs no backend change: it reads `workflow` from the metadata, `workflow_state` from the document, `GET .../transitions` and `GET .../history`. The badge (form header and list Status column) is the workflow state in the tone of its `style`, falling back to the docstatus badge for a document without a state and to the raw state name, neutral, for a state the workflow does not define. The plain Submit and Cancel buttons are hidden on a workflow DocType; the actions from `/transitions` replace them (first: primary, second: secondary, the rest in the More menu), are fetched again whenever the document's version changes or an action is refused, and are **disabled while the form has unsaved changes** (Save first), so a transition can never discard edits. Every action asks for a confirmation; a conflict offers Reload, other errors appear in the form's alert. The history is an "Activity" timeline in the info panel (newest first, 20 entries and "Show all"). The list filters and sorts by `workflow_state`. Details: [architecture, section 11](../blueprint/architecture.md#11-desk-reports-workflow).

## Alternatives considered

- **Conditions as expressions in the TOML** (Frappe's workflow conditions are Python expressions in a DocType record): rejected by the owner (decision 4). They need an expression language, its parser, its security review and its error messages, and they would not be testable like code. A hook is Rust, typed and unit-testable.
- **A separate Workflow DocType, editable at runtime, as in Frappe:** rejected for now by the owner (decision 1). It would make the metadata of two apps interact and needs the customization overlay; it comes with M10 if at all.
- **A workflow without docstatus** (states with no link to draft, submitted and cancelled): rejected. The docstatus is what locks a document and what the rest of the framework (permissions, amend, the desk badge) already understands; the workflow is a refinement of the draft state and a guard around submit and cancel, not a second status system.
- **Letting plain submit and cancel work next to transitions:** rejected by the owner (decision 3). A document could change state without an action, a role check against the workflow, or a log row.
- **A role alone as the permission to take a transition:** rejected (decision 2). The right stays the single source of truth for what a role may do with a DocType; the transition role only narrows it.

## Consequences

- A DocType with a workflow has a stricter lifecycle that is still the same lifecycle: the existing pipelines, locks, hooks and tests apply, and a workflow adds a column, a table and a few functions.
- The transition role cannot widen access, so a mistake in a workflow cannot give a user a right the DocType's rules deny. The cost is that every role of a transition must also be in `[[permissions]]` with the right; a transition that nobody can take is not detected at load (the rules are checked at run time).
- Conditions need a Rust hook, so a change of a condition is a code change (compile and deploy), which matches the compile-time app model.
- The log is deleted with its document: it is a history for users, not an audit trail. An audit log is a separate decision.
- Changing a workflow that has documents in it (renaming or removing a state) leaves those documents without transitions until an operator updates their state; `migrate` warns, and nothing is changed automatically.
- The desk (badge, action buttons from `/transitions`, history from `/history`) is delivered by M8.2, and the guide section is M8.5.
