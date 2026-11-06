# Workflow, saved views and reports

For the person who builds an app on Forgebase and has done the [first-app guide](first-app.md). This page covers the three parts of M8 that an app uses: an **approval workflow** on a DocType, **saved views and the summary** that the desk gives every list, and **query reports** (SQL that the app ships). Everything here is in the `sales` example, [`examples/sales`](../../examples/sales/README.md), and uses only the public `forgebase` crate. The rules and the reasons are in [ADR 0015](../adr/0015-workflow-states-and-transitions.md) (workflow), [ADR 0016](../adr/0016-query-reports-read-only-execution.md) (reports) and [architecture, section 11](../blueprint/architecture.md#11-desk-reports-workflow) (desk, views, summary).

## 1. An approval workflow

A submittable document normally goes draft, submitted, cancelled, and anyone with the `submit` right submits it. A workflow puts **states and actions** around that: an order is sent for approval, a manager approves or rejects it, and an approved order is the submitted one. The states and transitions are written in the DocType file, next to the fields and the `[[permissions]]`.

### The file

`examples/sales/doctypes/sales_order.toml`, after the fields and the permissions:

```toml
[workflow]

[[workflow.states]]
name = "Draft"                    # the first state is the initial one
docstatus = 0
style = "neutral"                 # the badge tone: neutral | success | warning | danger | info

[[workflow.states]]
name = "Pending Approval"
docstatus = 0
style = "warning"
edit_roles = ["Sales Manager"]    # only these roles may update a draft in this state

[[workflow.states]]
name = "Approved"
docstatus = 1                     # approving submits the order
style = "success"

[[workflow.states]]
name = "Cancelled"
docstatus = 2
style = "danger"

[[workflow.transitions]]
action = "Submit for Approval"    # the label of the button; unique per `from` state
from = "Draft"
to = "Pending Approval"
roles = ["Sales User", "Sales Manager"]

[[workflow.transitions]]
action = "Approve"
from = "Pending Approval"
to = "Approved"
roles = ["Sales User", "Sales Manager"]

[[workflow.transitions]]
action = "Approve as Manager"
from = "Pending Approval"
to = "Approved"
roles = ["Sales Manager"]

[[workflow.transitions]]
action = "Reject"
from = "Pending Approval"
to = "Draft"
roles = ["Sales Manager"]

[[workflow.transitions]]
action = "Cancel"
from = "Approved"
to = "Cancelled"
roles = ["Sales Manager"]
```

The allowed docstatus moves are 0 to 0, 0 to 1, 1 to 1 and 1 to 2: nothing leaves a cancelled state and a submitted document never goes back to draft. Every state must be reachable from the first one. A mistake in the file (an unknown state, a duplicate action, a docstatus 1 on a DocType that is not submittable) is reported when the app starts, with the other DocType errors.

### Role and right

A transition needs **a role of the transition and the right its move needs**: `write` for 0 to 0 and 1 to 1, `submit` for 0 to 1, `cancel` for 1 to 2. Neither is enough alone. A role in a workflow never grants a right, so every role of a transition must also be in `[[permissions]]` with that right; the example's Sales User has `submit` and no `cancel`, so even if "Cancel" listed it, it could not cancel. The Administrator role and the system context (the CLI) pass both checks.

On a DocType with a workflow **plain submit and cancel are refused** (409 `workflow_required`, and `doc submit` and `doc cancel` fail with exit code 1). The state changes only through a transition, so every change of state has an action, a role and a row in the log. The `before_submit` and `before_cancel` hooks still run, inside the transition that submits or cancels.

`edit_roles` (docstatus 0 states only) limits who may **update** a draft while it is in that state, in addition to the `write` right. In the example nobody but a Sales Manager can change an order that waits for approval, so a Sales User cannot lower the total after sending the order. A state without `edit_roles` changes nothing.

### A condition on a transition: `before_transition`

Conditions are Rust, not expressions in the TOML. `DocTypeHooks::before_transition` runs after the permission checks and before the `validate`, `before_submit` and `before_cancel` hooks of the move. It gets the **whole stored document** and `ctx.transition` (the `action`, `from` and `to`), may veto with `Err`, change values (the changes are validated and written with the move) and enqueue jobs (the outbox: a job exists only if the transition committed).

The example wants "a Sales User may approve orders up to 10 000; above that a Sales Manager is needed". **Hooks do not see roles**, only `ctx.actor` ([ADR 0009](../adr/0009-lifecycle-pipeline-and-hooks.md): rules that depend on roles belong in the permission rules, which the framework checks before any hook). So the role goes into the *action*:

- `"Approve"` is open to Sales User and Sales Manager and is limited by the hook;
- `"Approve as Manager"` is open to the Sales Manager only (the framework checks the role) and has no limit and no hook rule.
- There is no four-eyes rule in the example: a Sales User may approve an order they sent, up to the limit. An app that needs one can veto in `before_transition` when `ctx.actor` equals the document's `owner`.

```rust
/// The largest total a Sales User may approve.
pub const APPROVAL_LIMIT: Decimal = Decimal::from_parts(1_000_000, 0, 0, false, 2); // 10 000.00

fn before_transition(&self, ctx: &HookContext<'_>, doc: &mut Document) -> Result<(), HookError> {
    let is_approve = ctx.transition.is_some_and(|t| t.action == "Approve");
    if let (true, Some(Value::Decimal(total))) = (is_approve, doc.values.get("total"))
        && *total > APPROVAL_LIMIT
    {
        return Err(HookError::new(format!(
            "orders above {APPROVAL_LIMIT} need a Sales Manager: use 'Approve as Manager'"
        )));
    }
    Ok(())
}
```

`total` is the value `validate` computed when the order entered Pending Approval (that draft-to-draft transition runs `validate`), and `edit_roles` on Pending Approval keeps a Sales User from changing it before approving. A veto answers 422 `hook_rejected` with the message, and **rolls everything back**: the state, the version, the log row and any job are unchanged. A hook is tested without a database: build the context with `HookContext::new(..).with_transition(&TransitionInfo::new("Approve", "Pending Approval", "Approved"))`, as `tests/sales.rs` does.

### Using it

Over HTTP (a transition takes the version you last read; each transition adds 1). The examples use an API token, so there is no cookie or CSRF header; `Host` must be allowed:

```sh
# What can I do with this order now? (role and right, `if_owner` applied; a hook may still veto)
curl -s -H "Authorization: token $TOKEN" http://localhost:8000/api/v1/resource/sales_order/SO-2026-00001/transitions
# Take one
curl -s -X POST -H "Authorization: token $TOKEN" -H 'Content-Type: application/json' \
  -d '{"action": "Submit for Approval", "version": 1}' \
  http://localhost:8000/api/v1/resource/sales_order/SO-2026-00001/transition
# Who moved it where, oldest first (the newest 500 rows)
curl -s -H "Authorization: token $TOKEN" http://localhost:8000/api/v1/resource/sales_order/SO-2026-00001/history
```

Errors, all 409 unless noted: `workflow_required` (plain submit or cancel), `unknown_transition` (a wrong state, an unknown action, or a document without a state), `no_workflow` (a transition on a DocType without one), `conflict` (a stale version; of two concurrent transitions one wins); 403 `permission_denied` (a missing role or right, or an update refused by `edit_roles`); 422 `hook_rejected` (a veto). `GET /api/v1/meta/sales_order` has the states and transitions, **without the roles**.

From the command line: `cargo run -p sales -- doc transition sales_order SO-2026-00001 "Approve" --version 2 [--actor NAME]`. Like every `doc` command it runs as the system context, so **the roles of the transition are not checked** (the actor is recorded in the log).

**In the desk** the form shows the state as the badge (in the tone of its `style`, also in the list's Status column); Submit and Cancel are replaced by the actions the user may take (the first is the primary button, the second a secondary one, the rest are in the More menu). Every action asks for a confirmation, an action is disabled while the form has unsaved changes ("Save first"), and the history is the Activity timeline in the side panel. A veto or a conflict is shown in the form.

### Adding a workflow to a DocType that has data

`migrate` adds the column `workflow_state` and then gives every document that has no state **the first state with the document's docstatus** (a document whose docstatus has no state stays without one and has no transitions). The backfill is idempotent and does not change `version` or `modified_at`. If you later rename or remove a state, documents in it have no transitions until an operator fixes their state; `migrate` prints one warning per DocType for them and changes nothing.

### What a job cannot do

The jobs `Documents` API (`ctx.documents()`) has **no `transition`**, and its `submit` and `cancel` are refused on a workflow DocType ([ADR 0015](../adr/0015-workflow-states-and-transitions.md)). A background job cannot approve or cancel an order; the `sales` jobs only read orders and write logs.

## 2. Saved views and the summary

Every list in the desk has three buttons, with no work for the app author:

- **Views.** Save the current filters, sort and columns under a name and open them again later. A **private** view is the user's own: nobody else sees it and nobody else, an Administrator included, can change or delete it (it answers 404 for them). A **shared** view is created, changed and deleted by an Administrator only and is seen by everyone who may read the DocType. A view holds filters, a sort and columns, never a right: opening it runs the ordinary list under the caller's own read rules.
- **Columns.** Choose the columns of the list (at most 8).
- **Summary.** Group the list by a field and show the count and, for number fields, a sum, an average, a minimum or a maximum.

A shared view is **runtime data**, not part of an app (a fixture that ships one comes in a later milestone), so you create it through the API. `examples/sites/ci.sh` does exactly this on the `sales` app:

```sh
# As an Administrator: a view for everyone, "Pending approval" on the sales orders
curl -s -X POST -H "Authorization: token $ADMIN_TOKEN" -H 'Content-Type: application/json' \
  -d '{"label": "Pending approval", "shared": true, "definition": {"filters": [{"field": "workflow_state", "op": "=", "value": "Pending Approval"}]}}' \
  http://localhost:8000/api/v1/views/sales_order
# As a Sales User: the view is listed ("shared": true) ...
curl -s -H "Authorization: token $USER_TOKEN" http://localhost:8000/api/v1/views/sales_order
# ... but a Sales User cannot delete it (403), and cannot create a shared one (403)
curl -s -X DELETE -H "Authorization: token $USER_TOKEN" "http://localhost:8000/api/v1/views/sales_order/$VIEW_ID?version=1"
```

`definition` is `{"filters", "sort", "columns"}`, each optional; `filters` and `sort` have the forms of the list API (`workflow_state` and `docstatus` can be used where the DocType has them). A label is 1 to 64 characters and is unique per user (and among the shared views) for a DocType. In the desk an Administrator ticks "Shared with everyone" in the Save view dialog. Views are changed with `PATCH /api/v1/views/{doctype}/{id}` (`{"version", "label"?, "definition"?}`) and deleted with `DELETE .../{id}?version=N`.

The **summary** has an API as well, for scripts and dashboards:

```sh
curl -s -H "Authorization: token $TOKEN" -G http://localhost:8000/api/v1/summary/sales_order \
  --data-urlencode 'group_by=workflow_state' --data-urlencode 'aggregates=sum:total' \
  --data-urlencode 'filters=[{"field":"order_date","op":">=","value":"2026-10-01"}]'
```

It answers `{"data": {"groups": [{"key", "count", "values"}], "truncated"}}`: the groups are the documents the caller may read (an `if_owner` read counts only the caller's own documents), biggest first, at most 500. `group_by` is a Data, Select, Link, Check, Date or Int field, or `owner`, `modified_by`, `docstatus` or `workflow_state`.

## 3. Query reports

A list shows one DocType. A **report** is a SQL statement the app ships, for numbers that cross DocTypes: sales by customer, receivables by age. It appears in the desk under **Reports** and in the command bar.

### Declare it

`examples/sales/src/lib.rs`:

```rust
.report(
    "sales_by_customer",
    Report::sql(include_str!("../reports/sales_by_customer.sql"))
        .label("Sales by customer")
        .roles(&["Sales Manager"])
        .param(Param::date("from").required())
        .param(Param::date("to").required())
        .param(Param::link("customer", "customer"))
        .column(Column::link("customer", "customer"))
        .column(Column::int("orders"))
        .column(Column::decimal("total"))
        .column(Column::date("last_order")),
)
```

`examples/sales/reports/sales_by_customer.sql`:

```sql
SELECT c.name AS customer,
       count(*) AS orders,
       sum(o.total) AS total,
       max(o.order_date) AS last_order
FROM doc_sales_order o
JOIN doc_customer c ON c.id = o.customer
WHERE o.docstatus = 1
  AND o.order_date BETWEEN $1::date AND $2::date
  AND ($3::text IS NULL OR c.name = $3::text)
GROUP BY c.name
ORDER BY sum(o.total) DESC, c.name COLLATE "C"
```

- **The SQL is compiled into the binary** (`include_str!`), so it is trusted code like an app migration: written by a developer and reviewed in a pull request. Nothing is stored in the database or editable at run time.
- **Positional parameters.** `$1..$n` are the parameters in the order you declare them (types: `data`, `int`, `decimal`, `date`, `check`, `link`, `select`). They are checked like fields and **always bound**, never put into the text, so a value cannot inject SQL. An optional parameter that is absent is bound as `NULL`: write `($3::text IS NULL OR c.name = $3::text)`. Cast a placeholder (`$1::date`) when PostgreSQL cannot infer its type.
- **Positional columns.** The statement returns the declared columns in order, with SQL types that fit (`int2`/`int4`/`int8` for int, `numeric` for decimal, `text` or `varchar` for data and link, `date`, `timestamptz`, `bool`). A Link field is stored as the **id** of the target row, so join the target table and return its `name` (the document name is what a Link column carries). Table names are `doc_<doctype>`.
- Use `COLLATE "C"` in an `ORDER BY` of names so the order does not depend on the database's locale.

### Who can run it, and what it can do

- **Roles are required** (a report without a role is a startup error: there is no "any user" report). The Administrator and the system context always pass. Anyone else gets **404**, as for a report that does not exist, and does not see it in the list. In the example a Sales User cannot run it.
- **Row-level rules do not apply inside a report.** The SQL shows what it selects to every role that may run it, whatever the `[[permissions]]` or `if_owner` of the DocTypes say, and the caller is not available to the SQL. **Filter in the SQL yourself, and gate the report with roles**: give it only to roles that may see all the rows it selects.
- **Read-only, with limits.** It runs in a `READ ONLY` transaction: a statement that writes fails and changes nothing. It has a statement timeout (30 seconds by default, at most 300; `.timeout(..)`) and returns at most 10 000 rows (at most 100 000; `.max_rows(..)`); more rows only set `truncated`. A timeout is 504 `report_timeout`. The SQL text is never in a response.
- **The static check.** After the migrations, `forgebase migrate` **prepares** every report of the site's apps (nothing runs) and exits 1 when the parameter or column counts or types do not fit what the report declares. The migrations stay applied; fix the report and run `migrate` again, and treat exit 1 as a failed release. In your tests call it directly:

```rust
db.migrate(&binary, false).await.expect("migrate"); // runs the check too
assert_eq!(forgebase::testing::check_reports(&binary, &db).await, Vec::<String>::new());
```

### Run it

In the desk, open **Reports**, choose "Sales by customer", fill the parameters and press **Run report**; click a column header to sort. The parameters are kept in the address, so a reload or a shared link runs it again. Over HTTP:

```sh
curl -s -H "Authorization: token $MANAGER_TOKEN" http://localhost:8000/api/v1/report                     # the reports I may run
curl -s -X POST -H "Authorization: token $MANAGER_TOKEN" -H 'Content-Type: application/json' \
  -d '{"params": {"from": "2026-10-01", "to": "2026-10-31"}}' \
  http://localhost:8000/api/v1/report/sales.sales_by_customer
# {"data": {"columns": [...], "rows": [["bob@example.com", 1, "40.00", "2026-10-03"], ...], "truncated": false}}
```

**Export.** In the desk the result has an **Export** menu (CSV or JSON): it runs the report again with the same parameters and saves the file. Over HTTP, `POST /api/v1/report/<app>.<name>/export?format=csv|json` takes the same body as a run and answers the file (CSV has the column names as its header; JSON is `{"columns", "rows", "truncated"}`); it is for the same roles, with the report's own row limit and time limit, and `X-Forgebase-Truncated: true` marks a result that was cut at the limit ([ADR 0021](../adr/0021-import-and-export.md)). Text that a spreadsheet could take for a formula gets a leading `'` in CSV; numbers and dates never change. There are no charts yet.

## 4. Test it

`tests/sales.rs` in the example shows each of the following against a real PostgreSQL, with the public API only (`forgebase::testing`): the whole workflow (`an_order_goes_from_draft_to_cancelled_through_the_workflow`), the veto and the manager's action (`a_sales_user_approves_up_to_the_limit_and_a_manager_above_it`), reject and edit again, refused plain submit and cancel, the report (`sales_by_customer_counts_the_approved_orders_of_the_period`, `a_sales_user_cannot_run_or_see_the_report`, `the_report_sql_fits_its_declaration`) and the hook alone, without a database (`approve_is_limited_to_the_approval_limit_and_the_limit_itself_passes`). `examples/sales_extension/tests/extension.rs` shows that an app that extends `sales` submits an order through the same transitions. The framework's own tests cover the rules for every DocType: the workflow matrix, saved views, the summary and the report limits.
