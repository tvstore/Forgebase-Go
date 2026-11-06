# 0019 App method endpoints

- Status: Accepted (owner decisions 1 and 2 of the M9 plan, 2026-10-30, *"setuju semua rencana mu"*; recorded in [owner decisions](../owner-decisions.md#m9-integration-2026-10-30-setuju-semua-rencana-mu-information-session)). Builds on [ADR 0010](0010-authentication-and-sessions.md) (sessions, CSRF, tokens), [ADR 0011](0011-permission-model-phase-1.md) (the one permission evaluator), [ADR 0012](0012-app-model-and-public-api-boundary.md) (the public API) and [ADR 0013](0013-jobs-scheduler-and-transactional-outbox.md) (the outbox). It was planned as 0018; M9.0 took that number ([ADR 0018](0018-postgres-tls.md)).
- Date: 2026-10-31 (decided 2026-10-30)

## Context

The document API (`/api/v1/resource`) is generic: it creates, reads, changes and deletes documents, and takes workflow transitions. A business application also needs **operations** that are not one of those: "approve these three orders", "close the month", "send this customer a statement", "give me the numbers for the dashboard". Frappe calls them whitelisted methods (`/api/method/app.module.function`). Hooks cannot do this (they have no database access and cannot be called), jobs run later and as the system, reports only read.

Two questions decide whether such an endpoint is safe: **who may call it**, and **with whose rights it touches the data**. Frappe's `allow_guest` and its permission flag per function are conventions; a function that forgets to check runs with the database role's full rights. Forgebase's rule is deny by default.

## Decision

**An app registers methods; a client calls them with `POST /api/v1/method/<app>.<method>` and a JSON body.**

```rust
App::new("sales")
    .method("order_summary", Method::new(OrderSummary).roles(&["Sales User", "Sales Manager"]))
    .method("ping", Method::new(Ping).any_user())
```

1. **Naming, contract.** A method is named `<app>.<method>` (the name rules of a DocType, unique within the app, like jobs and reports). The route is `POST /api/v1/method/{app}.{method}`; any other HTTP method is 405. The body is a JSON object: an empty body is `{}`, a body that is not a JSON object is 400 (and, when not empty, a content type other than JSON is 415, over the body limit 413). Query parameters are refused (`invalid_query`) like on every route. The answer is `200 {"data": <what the handler returned>}`. A method is a plain Rust handler (`MethodHandler`, a boxed future like `JobHandler`), compiled into the binary: no dotted-path import, no runtime code (the [architecture](../blueprint/architecture.md#what-we-deliberately-do-not-build) rule is unchanged).
2. **Deny by default; no guest methods.** A method needs **exactly one** of `.roles(&[..])` (a non-empty list) or `.any_user()`. Neither, an empty list, or both is a **startup error**, reported with the other app problems. `.any_user()` means any *authenticated* user; there is no way to declare an anonymous method. The Administrator role passes `.roles`, as it does for reports. The check is one function in `forgebase-core`, `permission::can_call_method(&MethodAccess, &Caller)`, next to `can_run_report`.
3. **404 for "not allowed".** A method that does not exist, whose app is **not installed on the site**, or that the caller **may not call** is the same `404 not_found` with the same body, so a caller cannot probe which methods exist (the rule of reports, [ADR 0016](0016-query-reports-read-only-execution.md)). The contents of the body are not interpreted before this check: only authentication, CSRF and the shape of the body (JSON, an object, within the limit) come first, and none of them reveals a method.
4. **Runs as the caller.** The handler gets a `MethodContext` whose `documents()` handle acts as the **authenticated user** (a `Caller::User` built from the session or token; the HTTP layer never builds the system context, and the context offers no way to become it). Every call goes through the same `forgebase-db` functions as the document API with that caller, so the evaluator of ADR 0011 is the **only** decision: `if_owner`, read rules on `get` and `list` (an unreadable document is "not found"), read-only fields, `edit_roles` and the roles of workflow transitions. A method cannot do more than its caller could do with the document API; a method that wants to do more is a job (the system context, enqueued with `ctx.enqueue`) and is written as one, visibly. `MethodContext::caller()` gives the username and roles for the app's own rules (for example "a manager sees all, a user sees theirs") and is read-only.
5. **One transaction.** Each call runs in one database transaction. It commits when the handler returns `Ok`, and **every error rolls it back**: a `MethodError`, a `DocError`, an enqueue that fails, and a **panic** (the handler runs on its own task, so the panic ends that task and not the server, the connection is closed instead of pooled with an open transaction, and the client gets a generic 500). A write that is refused leaves the transaction usable (each write is a savepoint, as for jobs), so a handler may handle the refusal and go on; whatever else it did stays and commits. A job enqueued with `ctx.enqueue` exists if and only if the method commits (the outbox of ADR 0013), with the user as `enqueued_by`. The task is not cancelled when the client goes away: the method finishes and commits or rolls back. There is **no time limit** in this version; a handler that can run long should enqueue a job.
6. **CSRF and tokens.** The route uses the same authentication as the resource routes: a cookie session needs the `X-Forgebase-CSRF` header on a `POST` (`403 csrf_failed`), an API token (`Authorization: token key:secret`) does not, and no credential is 401. Inbound webhooks and other integrations use a method plus an API token for a user with the narrow role the method needs; there is no separate webhook mechanism.
7. **Errors, and nothing internal in them.**

   | Source | Status and code |
   |---|---|
   | `MethodError::bad_request(msg)` | 400 `method_error`, the app's message |
   | `MethodError::unprocessable(msg)` | 422 `method_error`, the app's message |
   | a `DocError` (`?` on a `Documents` call) | what the document API gives the same error: `permission_denied` 403, `not_found` 404, `conflict` 409, `validation_failed` 422, `hook_rejected` 422, `invalid_query` 400, `unique_violation` 409, and the others of the [error table](../../crates/forgebase-http/src/error.rs) |
   | a database or connection error, an enqueue of an unknown job, a panic | 500 `internal`, the generic message; the cause goes to the server log |

   The app's message in a `method_error` is shown to the client as it is: apps write messages meant for users. Everything else is mapped by the one existing `From<DbError> for ApiError`, which already keeps SQL, constraint names and Rust error text out of responses; the method path adds no second mapping.
8. **The crate seam.** `forgebase-http` must not depend on `forgebase` (handlers live there). The registry of `forgebase-core` carries the **name and access** of each method (`MethodDef`, `Registry::add_method`, `method`, `methods`), like reports, so the per-site logic is reused: a site that does not install an app composes a registry without its methods. `forgebase-http` defines a trait `MethodDispatch` and a failure type `MethodFailure` (either the original `DbError`, or a status, code and message from the handler, or an internal failure), and takes an optional `Arc<dyn MethodDispatch>` in `AppState::with_methods`. `forgebase` implements it (`Methods`: transaction, the handle as the caller, handler, commit or rollback) and passes it in from `serve` and from `forgebase::testing`. Without a dispatcher every method call is 404. `forgebase-http` knows nothing about handlers.
9. **`DocError`.** `Documents` already took the caller (jobs pass the system, methods pass the user); what it could not do was report errors a method can map. Its methods now return a public `DocError`, a thin wrapper of the database error with `is_retryable()` and a `Display` for the server's log. `From<DocError> for JobError` keeps **exactly** the old retry-or-permanent mapping, so a job handler that uses `?` compiles and behaves as before; `From<DocError> for MethodError` carries the original database error to `forgebase-http`. `Documents::transition(doctype, name, action, version)` is added (workflow actions, which methods need and jobs gain). A bad `like` filter in a `ListQuery` is a `DocError` (`invalid_query` 400 in a method, permanent in a job), with the message it had. This is a **breaking change** of the 0.x API; the CHANGELOG has the migration note.

## Non-goals

- **GraphQL**, a schema language, or any query language over documents.
- **RPC by dotted path** (`/api/method/app.module.function`): methods are registered by name, in the app's code, and nothing is looked up by string at run time.
- **Guest endpoints** (`allow_guest`): every call is authenticated.
- **Inbound webhooks** as a feature: a method plus an API token does it, and the sender's secret is the token.
- Method-level rate limits, a handler time limit, streaming bodies, file upload, and per-method CORS (no CORS headers are sent anywhere).

## Alternatives considered

- **Methods as the system context with the app checking rights itself** (Frappe's default). Rejected: it makes every method a place where a forgotten check is a privilege escalation. As the caller, the framework's one evaluator decides and the handler cannot widen it; the escape (a job) is explicit and asynchronous.
- **A method per DocType action** (`POST /resource/{doctype}/{name}/method`). Rejected: not every operation belongs to one document, and the resource routes would grow a second meaning.
- **403 for a method the caller may not call.** Rejected: it tells a caller which method names exist. Reports already answer 404, and a role is not a secret worth defending by being different here.
- **Allowing `.roles` and `.any_user` together, or neither (default: any user).** Rejected: a default that opens a method is the mistake this design exists to prevent; an explicit `any_user` is greppable in review.
- **`catch_unwind` instead of a task.** Needs the handler future to be `UnwindSafe`, which an app cannot be asked to promise; a task also keeps the transaction's end out of the request's lifetime. The cost is a `tokio::spawn` per call.
- **Passing `Registry`, `Pool` and the name by reference to the dispatcher.** The dispatcher needs them on a task, so it takes the `Pool` and an `Arc<Registry>`; both are cheap to clone.
- **A mock-friendly `Documents` trait.** Not now: a trait with the same methods and one implementation would be an abstraction without a second user. Tests use a real database.

## Consequences

- **The public API grows** by `Method`, `MethodHandler`, `MethodFuture`, `MethodContext`, `MethodCaller`, `MethodError`, `DocError` and `App::method`, and `testing` gains `TestDb::create_token`, `HttpClient::with_token`, `HttpClient::without_csrf` and `HttpClient::post_raw`. See [ADR 0012](0012-app-model-and-public-api-boundary.md#public-surface-update-m91).
- **A job handler that matched on the `JobError` that `Documents` used to return** must convert (`?` or `.into()`); the repository's examples and tests were updated (listed in the CHANGELOG).
- **A method is as powerful as its caller, and no more.** A role that may call a method but may not read a DocType does not get its documents through the method, unless the method reads them for a job it enqueues.
- **A panic or an error loses the whole call's writes** (one transaction): good for correctness, and a reason to keep methods short.
- **A swallowed read error is a known gap.** Reads run directly on the call's transaction. If a handler ignores a database error of `get` or `list` and returns `Ok`, PostgreSQL has aborted the transaction and `COMMIT` becomes a rollback: the caller gets 200 while the call's writes are lost. Rust-side query errors (`InvalidQuery`) do not abort the transaction; only a server error during a read does. Jobs have the same gap. Handlers should propagate document errors with `?`; refusing to commit after a failed read is planned work. *Amendment (M9.2): closed by running `get` and `list` of `Documents` in a savepoint, as writes already ran; see below.*
- **No limit on duration:** a slow method holds a pool connection for as long as it runs. Until a limit exists, put long work in a job.
- Reports are unchanged and do not go through methods; a method may not run a report in this version.

## Amendment (2026-10-31, M9.2): reads run in a savepoint

The known gap above is closed. `Documents::get` and `Documents::list` (jobs and methods) now run in a savepoint on the call's transaction, exactly as every write does: a read that the server refuses (a `like` pattern that ends with a backslash, a statement timeout) rolls back to its savepoint, the transaction stays usable, and a handler that ignores the error neither loses its earlier writes nor fails its next call. The same holds for a job: it completes and commits. (A document with Table fields was already read in a savepoint by `forgebase_db::get`; the handle adds one only for the single-statement reads, so there is never a double savepoint.) The alternative that the ADR also considered, refusing to commit after a failed read, was not needed.

**Cost.** A savepoint is two more statements per read (`SAVEPOINT`, `RELEASE`). Measured against a PostgreSQL 16 on the loopback interface, a `get` of a document without tables takes about 300 µs plain and about 450 µs with the savepoint (a release build, 5000 reads in one transaction, three rounds: 293, 301, 302 µs against 435, 454, 451 µs): about 150 µs more per read, which is two round trips. Over a network with a 1 ms round trip it would be about 2 ms per read. A handler that reads many documents should list them (one read) instead of getting them one by one. Evidence: `a_read_the_server_refuses_and_the_handler_ignores_loses_nothing` in `crates/forgebase/tests/methods.rs` (a method and a job; it fails without the savepoint).
