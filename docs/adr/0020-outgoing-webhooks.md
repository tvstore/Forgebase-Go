# 0020 Outgoing webhooks

- Status: Accepted (owner decisions 3 to 6 of the M9 plan, 2026-10-30, *"setuju semua rencana mu"*; recorded in [owner decisions](../owner-decisions.md#m92-outgoing-webhooks-2026-10-31-working-session)). Builds on [ADR 0013](0013-jobs-scheduler-and-transactional-outbox.md) (the outbox and the queue), [ADR 0014](0014-multi-site-database-per-site.md) (a site is a database), [ADR 0011](0011-permission-model-phase-1.md) (permissions) and [ADR 0018](0018-postgres-tls.md) (the TLS stack). It was planned as 0019; M9.0 took 0018 and M9.1 took 0019.
- Date: 2026-10-31 (decided 2026-10-30)

## Context

Other systems want to know when a document changes: a new sales order should reach the warehouse system, a submitted invoice the accounting tool. Polling the API is wasteful and misses nothing only if the client is careful. Frappe offers webhooks with a template language and conditions; Forgebase wants the small, explicit version: **a site pushes a signed JSON snapshot to a URL after a document operation commits**.

Three things make this risky, and the decision is mostly about them:

- **Lost or phantom events.** An event sent before the commit describes a document that may never exist. An event sent after the commit by the request itself can be lost with the process. The outbox ([ADR 0013](0013-jobs-scheduler-and-transactional-outbox.md)) already solves both: the job is written in the transaction of the change.
- **SSRF.** A URL set by an operator is still a way to make the server open connections from inside the network: to a database, to an admin interface on localhost, to the cloud metadata service (`169.254.169.254`), or, through a DNS name that changes its answer, to anything. A guard that checks the name and then lets the HTTP library resolve it again is defeated by DNS rebinding.
- **Secrets.** The signing secret must be known to the sender in clear (HMAC needs the key), and URLs often carry a token in the path or the query. Neither may reach a log, an error message or a list.

## Decision

### The table

Webhooks are configured per site at runtime (owner decision 3), in the table `fb_webhook` of the site's database (migration 0010), so a webhook belongs to one site and a backup of the site includes it.

| Column | Meaning |
|---|---|
| `name` | Primary key; the rules of a DocType name (`^[a-z][a-z0-9_]*$`). |
| `doctype` | The DocType it listens to: a DocType of the site's registry that is not a child DocType (checked by `webhook add`). |
| `events` | `text[]`, not empty, only `insert`, `update`, `delete`, `submit`, `cancel`, `transition` (also a `CHECK` constraint). |
| `url` | `https://` only, a host, no user info (checked by `webhook add` and again at every delivery). |
| `secret` | The signing secret, **stored in plain text** because the signature needs it. Anyone who can read the database can read it. |
| `enabled` | Default true. |
| `allow_private` | Default false. See the SSRF guard. |
| `insecure_http` | Default false. The explicit development flag that allows `http://`. Stored per webhook and shown by `list`. |
| `created_at`, `modified_at` | Timestamps. |

A partial index on `(doctype) WHERE enabled` serves the lookup below. The commands are `forgebase webhook add|list|disable|enable|remove|rotate-secret|test` (the [CLI guide](../guide/cli.md#webhook-commands-m92)). There is no desk screen and no HTTP route for webhooks: an operator configures them.

### The outbox, in `forgebase-db`

Every document operation of `forgebase-db` (`insert`, `update`, `delete`, `submit`, `cancel`, `amend`, `transition`) calls `webhooks::enqueue` **inside its own transaction, after the hooks and before the commit**: one indexed `SELECT` for the enabled webhooks of the DocType that listen to the event, and one `forgebase.deliver_webhook` job per match, inserted by the same function as every other job ([ADR 0013](0013-jobs-scheduler-and-transactional-outbox.md)), recorded as enqueued by the caller of the operation. The call is in the one place that every writer passes through: the HTTP API, the `doc` commands, the `Documents` handle of jobs and methods, and hooks that write. A rolled-back or failed operation enqueues nothing, by construction (a caller-owned transaction that is rolled back takes the jobs with it). Writes outside the document operations send no webhook: app SQL migrations and the workflow-state backfill of `migrate` are bookkeeping (the backfill does not change `version`), and a mass migration must not flood receivers.

| Operation | Events |
|---|---|
| `insert` | `insert` |
| `update` | `update` |
| `delete` | `delete` (the snapshot is the document as it was, with its rows) |
| `submit` | `submit` |
| `cancel` | `cancel` |
| `amend` | `insert` (of the new draft) |
| workflow transition | `transition`, and also `submit` or `cancel` when the transition moves the docstatus to 1 or 2 |

A webhook that listens to two events of one operation gets two deliveries, one per event, in the order above. The webhooks of one operation are enqueued in name order.

**Cost.** One indexed `SELECT` per write, on every site, with or without webhooks; with no match nothing else is done (the document is not even serialised). There is no cached "this site has no webhooks" flag: it would be state to keep consistent across processes (a `webhook add` in the CLI must reach a running server), for a query that costs a fraction of the write it follows.

**A binary without the job.** The job `forgebase.deliver_webhook` is registered by every binary (it is a built-in job, next to the clean-up jobs). A registry that lacks it, which only a hand-built one in a test can, fails the write with a clear `job:` error.

### The snapshot

The payload is **the document as stored after the operation** (before it, for a delete), with its child rows, `version`, `docstatus` and `workflow_state`, in the JSON of the document API: the code is `forgebase_db::json::document_to_json`, which `forgebase-http` uses for `GET /api/v1/resource/...`, so a receiver sees a familiar shape and there is one serialisation to maintain.

**The snapshot is not filtered by anybody's permissions.** A webhook is configured by an operator, who decides where the data goes; there is no "caller" to filter for, and a field that the user who made the change cannot read is still in the snapshot. This is the point of the feature and also its risk: see the [security model](../blueprint/security-model.md#outgoing-webhooks-m92-adr-0020).

A job payload is limited to 64 KiB. A snapshot that does not fit is replaced by `"data": null` and `"truncated": true` in the payload and the body: the write is never refused because of a webhook, and the receiver can fetch the document by its `name`.

### Delivery: the built-in job `forgebase.deliver_webhook`

It runs in the worker like every job (5 attempts, a delay of 30 s doubling up to 1 h, then `dead`; a 30 s job timeout around the client's own 10 s limit). The steps:

1. **Load the webhook by name.** Removed or disabled since the operation: the job **completes without sending** and the worker logs it (no secret).
2. **Body**, compact JSON: `{"event", "doctype", "name", "data", "occurred_at", "delivery"}` (and `"truncated": true` when it applies). `occurred_at` is the database time of the operation's transaction, RFC 3339 UTC. `delivery` is the id of the job.
3. **Headers.** `Content-Type: application/json`, `User-Agent: Forgebase-Webhook/<version>`, `X-Forgebase-Event`, `X-Forgebase-Delivery` (the job id: **the same on every retry**, so a receiver de-duplicates on it), `X-Forgebase-Timestamp` (Unix seconds at the time of this attempt) and `X-Forgebase-Signature`.
4. **Signature.** `sha256=` and the lower-case hex of `HMAC-SHA256(secret, "<timestamp>.<body>")` over the exact bytes of the body. Each attempt is signed afresh, with its own timestamp. The timestamp is in the signed text so that a captured delivery cannot be replayed later: a receiver that checks the timestamp against its clock (5 minutes is a good tolerance) and the signature rejects an old copy.
5. **Outcomes.**

   | Result | Job |
   |---|---|
   | 2xx | done |
   | 3xx | **permanent** failure: "redirects are not followed" |
   | other status, connection error, TLS error, timeout | retryable |
   | an address that is not allowed, an invalid URL | **permanent** |

6. **The response body is ignored:** at most 64 KiB are read and discarded, then the connection is dropped.
7. **Timeout:** 10 s for everything the delivery does (DNS, connection, TLS, the request, the response head and the discarded body).
8. **Logging and messages.** The messages (which become the job's `last_error` and the worker's log line) name only the scheme, the host and the port and the status: `http://host:port answered HTTP 500`. Never the URL's path or query, the secret or the signature.

At-least-once: a worker that dies after the request and before the job completes sends again, with the same delivery id.

**Verification recipe** (the receiver; the [webhooks guide](../guide/webhooks.md#verifying-a-delivery) has it in Python): take the raw body bytes (before parsing), the timestamp header and the signature header; reject if the timestamp is more than 5 minutes from now; compute `HMAC-SHA256(secret, timestamp + "." + body)`; compare with `hmac.compare_digest` (constant time); then de-duplicate on `X-Forgebase-Delivery`.

### The SSRF guard (owner decision 5): resolve, vet, pin

1. **URL.** `https`, or `http` only for a webhook with `insecure_http`; a host; no user info. Checked when the webhook is added, and again at every delivery (a row written around the CLI is checked too).
2. **Resolve ourselves.** `tokio::net::lookup_host`; an IP literal is used as it is.
3. **Check every address.** If **any** resolved address is blocked, the delivery is refused (permanent), unless the webhook has `allow_private`. The blocklist is one function with a unit test per range and its edges:

   | Family | Blocked |
   |---|---|
   | IPv4 | `0.0.0.0/8`, `10.0.0.0/8`, `100.64.0.0/10`, `127.0.0.0/8`, `169.254.0.0/16` (link-local, with the cloud metadata address `169.254.169.254`), `172.16.0.0/12`, `192.0.0.0/24`, `192.168.0.0/16`, `198.18.0.0/15`, `224.0.0.0/4` (multicast), `240.0.0.0/4` (reserved, with `255.255.255.255`) |
   | IPv6 | `::/96` (this includes `::` and `::1` and the old IPv4-compatible form), `fc00::/7`, `fe80::/10`, `ff00::/8`; `::ffff:0:0/96` (IPv4-mapped) and `64:ff9b::/96` (NAT64) are checked as the IPv4 address they embed |

   Not listed, so allowed: documentation ranges (`192.0.2.0/24` and the like), 6to4 (`2002::/16`) and the other IPv6 transition prefixes. A public address that embeds a private one in a prefix that is not routed to the host cannot reach it from here.
4. **Pin.** The connector is given **only the vetted addresses**, through a resolver that returns them and never looks anything up; the name of the URL is used for the `Host` header and, with `https`, for the SNI and the certificate check. A DNS answer that changes between the check and the connection (DNS rebinding) therefore cannot redirect the request. The unit test connects to a host name that no resolver can answer, through the address it is given.
5. **No redirects.** The client does not follow them, and a 3xx is a permanent failure. A redirect is how an allowed host would send us to a blocked one.
6. **Any port is allowed.** A webhook to a port is the operator's decision; the address rules are what protects the network.
7. **`allow_private`** turns off step 3 only (an in-network receiver, a test). **`insecure_http`** turns off the `https` requirement only. Both are per webhook, off by default, set with an explicit flag at `add`, and shown by `list`. The security model names them as risks.

### TLS and the client (owner decision 6)

`hyper-util`'s legacy client (already in the tree through axum) with `hyper-rustls` 0.27 (`ring`, `http1`, `tls12`; **no** `aws-lc-rs`, `native-tokio` or `webpki-tokio`) and the operating system's trust store through `rustls-native-certs` (it honours `SSL_CERT_FILE`). The configuration is built by `forgebase_db::tls::native_client_config`, the code of ADR 0018 with the same roots, `ring` and TLS 1.2 and 1.3, so the connections to PostgreSQL and to webhook receivers share **one** TLS stack and one `rustls` version. HTTP/1.1 only; one request on a fresh connection (no pooling between deliveries). The new crates are three (`hyper-rustls`, `try-lock`, `want`); the [dependency evaluation](../blueprint/dependency-evaluation.md#14-outgoing-webhooks-m92) has the licences and the checks. The Dockerfile already provides the root store (ADR 0018).

Tests need to trust a test CA: `forgebase::testing::webhook_test_settings(ca_pem, timeout)` sets, once for the whole process, the roots the client trusts and its time limit (environment variables are not used: changing them in a running process is racy). It is part of the `testing` feature and changes nothing else.

### Secrets handling

- The secret is 32 random bytes from the operating system as 64 hex characters (the source of API token secrets), or the first line of standard input with `--secret-stdin`.
- `add` and `rotate-secret` print a generated secret **once**. A secret given on standard input is not printed back. `list` and every error never show it; `Webhook`'s `Debug` omits it and the URL.
- It is stored in clear in `fb_webhook`: HMAC needs the key. A backup of the site contains it (like the password hashes it already contains, see the [CLI guide](../guide/cli.md)), so a backup is as sensitive as the database.
- **`rotate-secret`** replaces it; the receiver must switch at the same time, or accept both for a while.

### Non-goals

A webhook screen in the desk; payload templates or conditions in the configuration; inbound webhook receivers (an app method and an API token do that, [ADR 0019](0019-app-method-endpoints.md)); sync between sites; a delivery log table (the job queue is the log: `forgebase job list`, and a dead delivery stays until the purge job removes it after 7 days); per-webhook retry settings (the job's defaults apply).

## Alternatives considered

- **`reqwest`.** Rejected: version 0.13 defaults to rustls with `aws-lc-rs`, a C library that needs CMake; the other rustls feature set still pulls `webpki-roots` (CDLA-Permissive-2.0, avoided in ADR 0018). More dependencies for a client that needs one POST.
- **`ureq`.** Rejected: version 3 adds about 46 crates for a blocking client, which would also have to run on a blocking thread.
- **Sending inline, in the request.** Rejected: the request would wait for a third party (and fail or time out because of it), an event could be sent for a transaction that then rolls back (the very bug the outbox exists to prevent), and a crash after the commit would lose it.
- **A hook that enqueues.** Rejected: hooks are app code, per app; the framework's guarantee must not depend on every app remembering. The enqueue is in `forgebase-db`, below the hooks.
- **Checking the name, then letting the connector resolve it.** Rejected: DNS rebinding. Resolving and pinning is a few dozen lines.
- **Filtering the snapshot by the caller's permissions.** Rejected: there is no caller (the receiver is an operator's system), and a filtered snapshot would differ by who happened to make the change.
- **A flag "this site has webhooks" to skip the query.** Rejected, see Cost.
- **Encrypting the secret in the database.** Not done: the key would have to live somewhere the process can read, which is the same place an attacker with the database already is in most deployments; the honest statement is "plain text, protect the database".
- **Delivery in order.** Not promised: deliveries are independent jobs, retries reorder them. The snapshot carries `version`, so a receiver can ignore an older one.

## Consequences

- **A site with webhooks needs a running worker** (`forgebase worker`); without one the deliveries wait in the queue, which is the same as any job.
- **The public API grows** by `forgebase::testing::webhook_test_settings` only ([ADR 0012](0012-app-model-and-public-api-boundary.md#public-surface-update-m92)). `forgebase-db` gains the public modules `webhooks`, `json` and `tls` (an internal crate, no promise).
- **A new `DbError::Webhook`** for the webhook commands, answered as an internal error by the HTTP API (it never comes from a document operation).
- **The `forgebase` crate depends on `axum` and `tower` unconditionally** now (they were optional, for the `testing` feature): the webhook client builds its requests with `axum::http` and uses `tower::Service` for the pinned resolver, both of which `forgebase-http` already compiles in. No new crate.
- **The worker count of built-in jobs is 4** (the three clean-ups and the delivery); `forgebase.deliver_webhook` has no schedule.
- **Limits:** one request per delivery on a new connection; no keep-alive; the response is never inspected beyond its status; a receiver that needs ordering must use `version`.
- **Not covered:** client certificates (mTLS) to receivers, a proxy for outgoing requests (`HTTPS_PROXY` is not read), a per-webhook timeout, IPv6 transition prefixes beyond the two listed.

## Amendment (2026-10-31, M9.3): three carry-overs of the M9.2 review

- **A given secret has at least 16 bytes.** A secret that `add` or `rotate-secret` reads with `--secret-stdin` must be at least 16 bytes long (the empty secret keeps its own message). The signature is only as strong as the secret, and the generated one is 32 random bytes; a refused secret changes nothing. The check is in `forgebase_db::webhooks` (`MIN_SECRET_BYTES`), so every caller has it.
- **A snapshot that cannot be written as JSON no longer fails the write.** Before, `webhooks::enqueue` mapped a serialisation error to `DbError::Job` and the business write failed. A stored value can have no JSON form (a `timestamptz` before the year 0, which PostgreSQL has and RFC 3339 has not). The delivery is now enqueued with `"data": null` and `"unavailable": true`, like `"truncated": true` for a snapshot over the payload limit: a webhook never makes a write fail because of its content. The receiver can read the document through the API. Tested with a datetime of year -1.
- **The Azure WireServer is on the blocklist.** `168.63.129.16/32` is reachable from Azure virtual machines (host and platform services) and is a public address otherwise, so the private ranges do not cover it. It is blocked as an explicit extra, also as an IPv4-mapped or NAT64 address, and `allow_private` allows it like the other blocked addresses. See the [security model](../blueprint/security-model.md#outgoing-webhooks-m92-adr-0020).
