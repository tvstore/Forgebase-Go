# sales

An example [Forgebase](../../README.md) app in the role of the ERPNext sales module: customers, customer groups and submittable sales orders. It is documentation that runs: it is a Go module of its own (`github.com/tvstore/forgebase-go/examples/sales`) that imports the public `forgebase` package and `forgebase/testing` only, which the Go toolchain enforces, because everything else of the framework is under `internal/`. It changes nothing outside `examples/`.

It shows:

- **DocTypes** (`doctypes/*.toml`, embedded with `//go:embed doctypes/*.toml` and added with `DocTypes`): a Link (`customer` to `customer_group`), a child table (`sales_order.items`), a naming series, a submittable DocType and role permissions (`Sales User`, `Sales Manager`).
- **Hooks** (`sales.go`, `OrderRules` on `sales_order`): `Validate` computes each item's `amount` and the order's `total`; `BeforeSubmit` refuses an order without items or with a total that is not positive; `BeforeTransition` limits the `"Approve"` action (below); `BeforeCancel` and `BeforeSubmit` also ask for the job that logs the event (`ctx.Enqueue`, the transactional outbox).
- **A field at permission level 1** (`credit_limit` of `customer`, `perm_level = 1`; [ADR 0024](../../docs/adr/0024-field-permission-levels.md), [guide](../../docs/guide/customizing.md#field-permission-levels)): only a Sales Manager reads or writes it (a `[[permissions]]` rule with `level = 1`). To a Sales User the field does not exist: it is absent from get, list and the metadata, and a filter on it is an unknown field. The `forgebase doc` commands run as the system context and see every level. Tests: `TestASalesUserDoesNotSeeTheCreditLimitAnywhere` and the four tests after it in `sales_test.go`.
- **An approval workflow** (`[workflow]` in `doctypes/sales_order.toml`; [ADR 0015](../../docs/adr/0015-workflow-states-and-transitions.md), [guide](../../docs/guide/workflow-and-reports.md)): Draft, Pending Approval (docstatus 0; only a Sales Manager may edit an order that waits for approval), Approved (docstatus 1, submitted) and Cancelled (docstatus 2). The transitions are "Submit for Approval" (Sales User, Sales Manager), "Approve" (Sales User, Sales Manager), "Approve as Manager" (Sales Manager), "Reject" back to Draft (Sales Manager) and "Cancel" (Sales Manager). **Plain submit and cancel are refused** on `sales_order` (409 `workflow_required`): an order is submitted by approving it and cancelled by the "Cancel" transition, and the `BeforeSubmit` and `BeforeCancel` hooks (and so the log jobs) run inside those transitions.
  - **The approval limit.** Hooks see the actor's name and the transition, never roles (ADR 0009), so a rule "a Sales Manager is needed above 10 000" is written with two actions: `BeforeTransition` refuses `"Approve"` when the stored `total` is above `ApprovalLimit` (10 000.00, a variable of `sales.go` that must not change) and the message says to use `"Approve as Manager"`, which only a Sales Manager may take and which has no limit. `edit_roles` on Pending Approval stops a Sales User from lowering the total after sending the order.
- **A data migration** (`migrations/0001_default_customer_groups.sql`, embedded with `//go:embed migrations/*.sql` and added with `Migrations`; the file name is the version and the name): creates the customer groups `Retail` and `Wholesale`, once per database.
- **Background jobs and a schedule** (`sales.go`; [guide](../../docs/guide/first-app.md#7-background-jobs-and-schedules)): `sales.log_order_event` (registered with `Job`, enqueued by the two hooks above) writes one `sales_order_log` row per submit or cancel and is safe to run twice; `sales.daily_summary` (`Schedule("daily_summary", forgebase.DailyAt(2, 0))`) writes the `sales_daily_summary` of the day before the tick. `forgebase worker` runs them; `migrate` stores the schedule.
- **Tests** (`sales_test.go`): the hooks without a database (and the jobs they enqueue, with `forgebase.EnqueuedJobs`), and the HTTP API and the jobs (`fbtest.RunJobs`, `fbtest.RunSchedulesAt`) against a real PostgreSQL: the data migration, the computed amounts and total, permissions, the level 1 field and the two jobs.

**Not in the Go port yet** (the Rust example has them; each arrives with the phase that ports the feature, and then the tests of the Rust example listed at the top of `sales_test.go` come with it): the workflow transitions over HTTP and the command line, and the query report "Sales by customer" (P6); the app method `order_summary` and the fixture of customer groups (P7). The two hooks that enqueue `sales.log_order_event` run inside the workflow transitions, which are P6, so until then the tests queue that job with SQL and make an order submitted with SQL.

## Run it

Needs PostgreSQL 16 and an empty database.

```sh
export FORGEBASE_DATABASE_URL=postgres://user:password@localhost:5432/sales
go run ./cmd/sales migrate
go run ./cmd/sales worker                                      # in another terminal: runs the jobs and the schedule
echo 'a long password' | go run ./cmd/sales user create ann --role "Sales User" --role "Sales Manager"
FORGEBASE_DEV_INSECURE_COOKIES=1 go run ./cmd/sales serve     # http://127.0.0.1:8000/api/v1
```

The commands (`doc`, `user`, `token`, ...) are those of the stock binary; see [the quick start](../../docs/guide/getting-started.md#quick-start) for what to do with them. `FORGEBASE_DEV_INSECURE_COOKIES=1` is for plain-HTTP development only.

## Test it

```sh
export FORGEBASE_TEST_DATABASE_URL=postgres://user:password@localhost:5432/postgres   # a superuser or CREATEDB URL
cd examples/sales && go test ./...
```

Every database test creates and drops its own database. They fail, they do not skip, when the variable is not set. The module is separate from the framework's: `go test ./...` at the root of the repository does not run it (CI runs it in the job `check`).

## Notes

- The computed `amount` and `total` overwrite what a client sends. Amounts are rounded to 2 places, half away from zero.
- `sales_order.items` is not required, so a draft can be saved without items; approving (the submit) is what needs them.
- A `sales_order_log` row keeps its order: PostgreSQL refuses to delete an order that a log row links to (a cancelled order is still amended, not deleted).
- [`sales_extension`](../sales_extension/README.md) builds on this app.
