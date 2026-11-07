# sales_extension

An example [Forgebase](../../README.md) app in the role of a customer's own **custom app**: it depends on [`sales`](../sales/README.md) and extends it without changing it. It is a Go module of its own (`github.com/tvstore/forgebase-go/examples/sales_extension`, which requires `examples/sales` and `forgebase`) and uses the public `forgebase` package only.

It shows:

- **`DependsOn("sales")`**: `sales` must be added before it in `cmd/sales_extension/main.go`; apps are never sorted.
- **A DocType of its own** (`doctypes/customer_visit.toml`) with a Link to `customer`, a DocType of `sales`.
- **A hook on another app's DocType** (`MinimumOrder` on `sales_order`): `Validate` refuses an order whose `total` is below `MinOrderTotal` (10.00). Hooks of one DocType run in app order, so it runs after the `sales` hook that computes `total`. The minimum is a variable of the package because hooks have no database access. In the composed binary, every save of a sales order (draft included) needs a total of at least 10.00, so a draft without items (total 0) can no longer be saved, unlike with `sales` alone.
- **A second hook set for the same DocType** (`FollowUp`): when an order is submitted (in `sales` that is an approval transition, because `sales_order` has an approval workflow: plain submit is refused), a `BeforeSubmit` hook, registered after `MinimumOrder` and so after the hooks of `sales` too, enqueues `sales_extension.plan_follow_up_visit` with `ctx.Enqueue` (the transactional outbox: no job if the submit is refused).
- **The composed binary** (`cmd/sales_extension/main.go`): `sales`, then this app.
- **Tests** (`sales_extension_test.go`): the hooks without a database, and both apps together against a real PostgreSQL: hook order, the cross-app Link, the `"app"` of each DocType in `GET /api/v1/meta`, permissions.

**Not in the Go port yet** (each arrives with the phase that ports the feature): the job `plan_follow_up_visit`, which plans a `customer_visit` for the customer of a submitted order (P5); the workflow transitions that submit an order (P6); the field `loyalty_tier` that the Rust example adds to `customer` with an overlay file (`overlays/customer.toml` and `ExtendDocType`, P7). The tests of the Rust example that need them are listed at the top of `sales_extension_test.go`.

## Run it

Needs PostgreSQL 16 and an empty database.

```sh
export FORGEBASE_DATABASE_URL=postgres://user:password@localhost:5432/sales_ext
go run ./cmd/sales_extension migrate          # sales' DocTypes and migration, then customer_visit
echo 'a long password' | go run ./cmd/sales_extension user create ann --role "Sales User"
FORGEBASE_DEV_INSECURE_COOKIES=1 go run ./cmd/sales_extension serve
```

Log in with `POST /api/v1/login`; `GET /api/v1/meta` then lists `customer_visit` with `"app": "sales_extension"` and `sales_order` with `"app": "sales"`.

## Test it

```sh
export FORGEBASE_TEST_DATABASE_URL=postgres://user:password@localhost:5432/postgres   # a superuser or CREATEDB URL
cd examples/sales_extension && go test ./...
```

The tests build the same binary as `main.go` (`sales.App()` then `sales_extension.App()`).
