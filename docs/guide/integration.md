# Integrating with other systems

For the person who connects Forgebase to other systems: a warehouse tool that reads order totals, an accounting system that wants every approved order, a spreadsheet full of customers. This page is a walkthrough on the [`sales` example app](../../examples/sales/README.md) with four tools: an **app method** (another system asks a question), a **webhook** (Forgebase tells another system something happened), **import and export** (files in and out) and **fixtures** (records the app ships). It links the reference pages instead of repeating them.

Every command and snippet below was run against the `sales` binary (`cargo run -p sales -- ...`) on a scratch PostgreSQL database at the time of writing (each section on its own; the sample outputs are illustrative, as the setup creates no orders). Set it up like this; the examples assume a Sales Manager `max` and a Sales User `ann`:

```sh
export FORGEBASE_DATABASE_URL=postgres://user:password@localhost:5432/sales
cargo run -p sales -- migrate
printf '%s\n' 'a long password' | cargo run -p sales -- user create max --role "Sales Manager"
printf '%s\n' 'a long password' | cargo run -p sales -- user create ann --role "Sales User"
FORGEBASE_DEV_INSECURE_COOKIES=1 cargo run -p sales -- serve      # http://127.0.0.1:8000
```

and log in as in the [HTTP API guide](http-api.md), as `max`: `CSRF` holds the token of the login and `jar.txt` the session cookie (the import in section 3 needs the Sales Manager).

## 1. App methods: another system asks a question

A method is your own endpoint, `POST /api/v1/method/<app>.<method>`, for an operation that is not the create, read or change of one document ([first app, section 8](first-app.md#8-app-methods); [ADR 0019](../adr/0019-app-method-endpoints.md)). The `sales` app has one, `sales.order_summary`: how many sales orders there are, their total and how many are in each workflow state, optionally for one customer and a period.

Registered in [`examples/sales/src/lib.rs`](../../examples/sales/src/lib.rs):

```rust
App::new("sales")
    .method(
        "order_summary",
        Method::new(OrderSummary).roles(&["Sales User", "Sales Manager"]),
    )
```

and the handler, shortened (the real one validates its input and pages through the list):

```rust
impl MethodHandler for OrderSummary {
    fn call<'a>(&'a self, ctx: &'a mut MethodContext<'_>) -> MethodFuture<'a> {
        Box::pin(async move {
            let filters = summary_filters(ctx.body())?;   // a bad input is MethodError::bad_request(..)
            let mut documents = ctx.documents();           // acts as the caller
            // ... for every workflow state: list the orders the caller can read, count them, add up `total`
            Ok(json!({"orders": orders, "total": total.to_string(), "by_state": by_state}))
        })
    }
}
```

Call it with the session cookie and the CSRF header, or with an API token:

```sh
curl -s -b jar.txt -H "X-Forgebase-CSRF: $CSRF" -H 'Content-Type: application/json' \
  -d '{"from": "2026-10-01", "to": "2026-10-31"}' http://localhost:8000/api/v1/method/sales.order_summary
# {"data":{"by_state":{"Approved":1,"Cancelled":0,"Draft":1,"Pending Approval":0},"orders":2,"total":"139.00"}}

# system to system: a token, no cookie and no CSRF header
TOKEN=$(cargo run -p sales -- token create ann)        # printed once; store it in the other system
curl -s -H "Authorization: token $TOKEN" -H 'Content-Type: application/json' \
  -d '{"customer": "ann@example.com"}' http://localhost:8000/api/v1/method/sales.order_summary
```

The input is `{"customer": <customer name>, "from": "YYYY-MM-DD", "to": "YYYY-MM-DD"}`, every key optional (`from` and `to` bound `order_date`, both included). The output has `orders`, `by_state` and `total`, which is a **string**, so no system rounds money through a float.

The access rules, all enforced by the framework and the same for every method:

- **No session** is `401`. A **cookie call without the CSRF header** is `403 csrf_failed`; a token call needs none.
- **A user without a role of the method** (here, neither Sales User nor Sales Manager; the Administrator always may) gets `404`, the same answer as a method that does not exist, so names are not revealed. There are no guest methods.
- **It runs as the caller.** `ctx.documents()` applies the permission rules of `sales_order` to every read. `sales_order` has **no `if_owner` rule** (both roles read every order), so the summary of a Sales User and of a Sales Manager are the same; if you add an `if_owner` read rule, the same code counts only the caller's own orders.
- **A bad input** (`{"from": "yesterday"}`, an unknown key, `from` after `to`) is `400 method_error` with a message written for the person who sent it. Whatever the method did is rolled back on every error.
- **`total` counts every order the caller can read, whatever its state, Cancelled included**; `by_state` gives the split. The counts come from one query per state and are not one snapshot: an order that changes state while the method runs may be counted twice or not at all. For an exact report, use a [query report](workflow-and-reports.md) instead.
- **No limit of 500.** `Documents::list` returns one page of at most 500 and has no offset, so the handler pages by `name` (the sort ends with it, and it is unique) and counts every page.

The route, the statuses and the limits are in the [HTTP API guide](http-api.md#app-methods).

## 2. Webhooks: Forgebase tells another system

A webhook makes the site `POST` a signed JSON snapshot of a document to a URL **after** the operation committed ([ADR 0020](../adr/0020-outgoing-webhooks.md); the full reference, with the headers, the retries and the troubleshooting table, is the [webhooks guide](webhooks.md)). An operator adds it with the command line, per site. Say the accounting system wants every approved order:

```sh
cargo run -p sales -- webhook add sales_order_submit \
  --doctype sales_order --events submit \
  --url https://hooks.example.com/forgebase/orders
# added webhook sales_order_submit
# secret: 3f9c...   (64 hex characters; printed once)
# store the secret now: it cannot be shown again

cargo run -p sales -- webhook list
# NAME                DOCTYPE      EVENTS  URL                            ENABLED  ALLOW_PRIVATE  INSECURE_HTTP
# sales_order_submit  sales_order  submit  https://hooks.example.com:443  yes      no             no
```

Give the secret to the receiver. An order of the `sales` app is submitted by the transition `Approve` (or `Approve as Manager`): that fires the event `transition`, and `submit` as well, because the transition submits the document. This webhook listens to `submit` only, so the receiver gets one delivery per approved order. A site that has webhooks needs a running `cargo run -p sales -- worker`, which sends them.

**What the receiver gets** is a `POST` with `X-Forgebase-Event: submit`, a `X-Forgebase-Delivery` id (the same on every retry), a `X-Forgebase-Timestamp` and a `X-Forgebase-Signature: sha256=<hex>`, and a JSON body: `event`, `doctype`, `name`, `data` (the document as `GET /api/v1/resource/sales_order/<name>` shows it, items included), `occurred_at` and `delivery`. The full table is in [What is sent](webhooks.md#what-is-sent).

**Verifying it** is the receiver's job, because anyone who knows the URL can post to it. Compute `HMAC-SHA256(secret, "<timestamp>.<raw body>")`, compare it with the signature in constant time, and refuse an old timestamp. The recipe, in Python, is in [Verifying a delivery](webhooks.md#verifying-a-delivery), and [`examples/sites/receiver.py`](../../examples/sites/receiver.py) is a complete receiver that uses it (about 80 lines, standard library only).

**It is tested in CI.** The `sites` job runs [`examples/sites/ci.sh`](../../examples/sites/ci.sh), which starts that receiver, adds a webhook on `sales_order` for `submit`, approves an order, runs the worker and asserts that the receiver recorded **exactly one verified `submit` delivery** of that order (and that a forged signature is refused). To try it on your machine the receiver is on `127.0.0.1` and speaks plain HTTP, which the SSRF guard refuses by default, so the webhook needs two explicit **development-only** flags:

```sh
secret="$(head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n')"   # never printed
WEBHOOK_SECRET="$secret" python3 -I examples/sites/receiver.py 9000 deliveries.jsonl &
printf '%s\n' "$secret" | cargo run -p sales -- webhook add local_submit \
  --doctype sales_order --events submit --url http://127.0.0.1:9000/hook \
  --allow-private --insecure-http --secret-stdin
cargo run -p sales -- webhook test local_submit            # HTTP 200, exit 0
cargo run -p sales -- worker                                # sends the real deliveries
```

Never use `--allow-private` or `--insecure-http` in production: the first lets the worker reach your internal network, the second sends the payload unencrypted.

## 3. Import and export: files in and out

Export and import are **rights of their own**, off until a DocType grants them ([ADR 0021](../adr/0021-import-and-export.md)). The `sales` app grants them as an example in [`customer.toml`](../../examples/sales/doctypes/customer.toml): the Sales User may **export** customers, the Sales Manager may export and **import** them. A Sales User who tries to import gets `403 permission_denied` ("no `import` right").

Export the customers as CSV (a `GET`, so no CSRF header; `filters`, `sort` and `fields` work as in the list):

```sh
curl -s -b jar.txt -G http://localhost:8000/api/v1/export/customer \
  --data-urlencode 'format=csv' --data-urlencode 'fields=name,full_name,tier,customer_group' -o customers.csv
cat customers.csv
# name,full_name,tier,customer_group
# ann@example.com,Ann,gold,
```

Import customers from a small CSV. The first row names the fields, the body is the raw file. Run it as a **dry run** first: it reads every row through the normal pipeline (validation, hooks, naming, your permissions) and keeps nothing:

```sh
cat > new-customers.csv <<'EOF'
email,full_name,tier,customer_group
bob@example.com,Bob Builder,silver,Retail
cy@example.com,Cy Young,gold,Online
EOF

curl -s -b jar.txt -X POST -H "X-Forgebase-CSRF: $CSRF" -H 'Content-Type: text/csv' \
  --data-binary @new-customers.csv 'http://localhost:8000/api/v1/import/customer?format=csv&dry_run=1'
# {"data":{"dry_run":true,"error_count":0,"errors":[],"inserted":2,"mode":"insert","rows":2,"updated":0}}

curl -s -b jar.txt -X POST -H "X-Forgebase-CSRF: $CSRF" -H 'Content-Type: text/csv' \
  --data-binary @new-customers.csv 'http://localhost:8000/api/v1/import/customer?format=csv'
# {"data":{"dry_run":false,"error_count":0,"errors":[],"inserted":2,"mode":"insert","rows":2,"updated":0}}
```

The import is **all or nothing**: one transaction, and if any row has an error nothing is kept. A dry run finds every error in one go, each with its row number, its CSV line and the reasons; for a file with a customer group that does not exist and no name:

```
{"data":{"dry_run":true,"error_count":1,"errors":[{"code":"validation_failed","details":["field `full_name` is required","field `customer_group`: customer_group `Nowhere` does not exist"],"line":2,"message":"validation failed","name":"z@example.com","row":1}],...}}
```

The same from the command line (an alternative to the curl calls: on a site where the curl import already ran, use a new file or `--upsert`), as the trusted system context (no permission rules; `--actor` names who is recorded as owner), and the same in the desk, where the list's **Export** menu and the **Import** page (Validate, then Import) do it without curl:

```sh
cargo run -p sales -- data export customer --format csv --fields name,full_name,tier --out customers-cli.csv
cargo run -p sales -- data import customer new-customers.csv --format csv --dry-run
cargo run -p sales -- data import customer new-customers.csv --format csv --actor max
```

The details (the JSON format, `mode=upsert`, the CSV cell rules, the protection of exported text against spreadsheet formulas, the 100 000 and 10 000 row limits) are in the [HTTP API guide](http-api.md#import-and-export-m93), the [CLI guide](cli.md#data-commands-export-and-import-m93) and the [desk guide](desk.md).

## 4. Fixtures: records the app ships

Some records should exist on every site that installs the app. `sales` ships its customer groups as a fixture, [`examples/sales/fixtures/customer_group.json`](../../examples/sales/fixtures/customer_group.json), listed in `src/lib.rs` with `.fixtures(forgebase::fixtures!["fixtures/customer_group.json"])`:

```json
{
  "doctype": "customer_group",
  "records": [
    {"title": "Retail"},
    {"title": "Wholesale"},
    {"title": "Online"}
  ]
}
```

`Retail` and `Wholesale` are **also** created by the SQL migration `0001_default_customer_groups`, which predates the fixture (a migration is append-only, so it stays). That is on purpose: it shows two outcomes in one file. The first `migrate` of a new database:

```
app migrations: 1 applied
fixtures: sales: 1 inserted, 0 updated, 2 kept, 0 unchanged
```

(abridged: the lines of the framework migrations, the schema sync, the schedules and the reports check are left out)

`Online` was missing, so it was **inserted**. `Retail` and `Wholesale` existed already, so they were **kept**: left exactly as they are and tracked from now on (the site's data wins). The next `migrate` finds all three **unchanged** and writes nothing:

```
fixtures: sales: 0 inserted, 0 updated, 0 kept, 3 unchanged
```

From then on a site can edit or delete a group and the next `migrate` leaves that alone; only when the app **changes** a record in the file does `migrate` update it (the fields the record has), and it never deletes a record. The rules are the table in [first app, section 9](first-app.md#9-fixtures) and in [ADR 0022](../adr/0022-fixtures.md); the output is explained in the [CLI guide](cli.md#migrate-and-fixtures-m94).

## 5. TLS to PostgreSQL

A managed database such as Neon requires TLS, and since M9.0 Forgebase connects with it: put `?sslmode=require` at the end of the database URL and the certificate and the host name are verified against the operating system's CA store. Nothing else changes in an app. See [TLS to PostgreSQL](operations.md#tls-to-postgresql), the [deploy guide](deploy.md#3-render--neon-free-path) and [ADR 0018](../adr/0018-postgres-tls.md).
