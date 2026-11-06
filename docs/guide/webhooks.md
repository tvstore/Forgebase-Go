# Webhooks

For the person who connects another system to Forgebase: a warehouse tool that wants every new sales order, an accounting system that wants every submitted invoice. A **webhook** makes a site `POST` a signed JSON snapshot of a document to a URL after the document operation has committed. The design and its reasons are in [ADR 0020](../adr/0020-outgoing-webhooks.md); the risks are in the [security model](../blueprint/security-model.md#outgoing-webhooks-m92-adr-0020). The commands are in the [CLI guide](cli.md#webhook-commands-m92).

Webhooks are configured by an operator, per site, with the command line. There is no desk screen and no HTTP route. Receiving a request from another system (the other direction) is an [app method](http-api.md#app-methods) plus an API token.

## How it works

1. An operator adds a webhook: `forgebase webhook add orders --doctype sales_order --events insert,submit --url https://hooks.example.com/orders`. The command prints a **secret**, once.
2. When a document of that DocType is inserted or submitted, the operation writes a delivery job to the job queue **in its own transaction**. If the operation is rolled back or refused, there is no job and nothing is sent. This holds for every writer: the HTTP API, the `doc` commands, jobs, methods and hooks that write.
3. `forgebase worker` picks the job up and posts it. A site that has webhooks needs a running worker.
4. A 2xx answer completes the job. Anything else is retried (5 attempts, 30 seconds doubling up to 1 hour) and then the job is `dead` until `forgebase job retry`. Delivery is **at least once**: a receiver can get the same delivery twice, with the same delivery id.

## The events

| Operation | Event |
|---|---|
| a document is inserted | `insert` |
| a document is updated | `update` |
| a document is deleted | `delete` (the snapshot is the document as it was) |
| a document is submitted | `submit` |
| a document is cancelled | `cancel` |
| a cancelled document is amended | `insert` (of the new draft) |
| a workflow transition is taken | `transition`, and also `submit` or `cancel` when the transition submits or cancels the document |

A webhook listens to the events you list. One that listens to both `transition` and `submit` gets two deliveries for a transition that submits, one per event. `event` is `test` for `forgebase webhook test`.

## What is sent

A `POST` with a JSON body and these headers:

| Header | Value |
|---|---|
| `Content-Type` | `application/json` |
| `User-Agent` | `Forgebase-Webhook/<version>` |
| `X-Forgebase-Event` | the event, for example `submit` |
| `X-Forgebase-Delivery` | the id of the delivery. **The same on every retry** of it: de-duplicate on it. |
| `X-Forgebase-Timestamp` | the time of this attempt, Unix seconds |
| `X-Forgebase-Signature` | `sha256=` and the lower-case hex of `HMAC-SHA256(secret, "<timestamp>.<body>")` |

The body is compact JSON:

```json
{
  "event": "submit",
  "doctype": "sales_order",
  "name": "SO-2026-0001",
  "data": { "id": "...", "name": "SO-2026-0001", "version": 3, "docstatus": 1, "customer": "ann@example.com", "items": [ { "item": "Desk", "qty": 2 } ] },
  "occurred_at": "2026-10-31T09:30:12.512345Z",
  "delivery": "0198a3c2-5b1e-7d2f-8a43-1c9e5f6a7b80"
}
```

- `data` is the document **as the document API shows it** (`GET /api/v1/resource/<doctype>/<name>`): the standard columns, `docstatus` and `workflow_state` where they apply, every field, and the rows of every table. It is the document **after** the operation, except for `delete`, where it is the document as it was. It is **not filtered by anybody's permissions**: a webhook is configured by an operator, and it carries every field.
- A snapshot over 64 KiB is sent as `"data": null` with `"truncated": true`; fetch the document by `doctype` and `name` if you need it. A snapshot that cannot be written as JSON at all (a stored value with no JSON form, for example a datetime before the year 0) is sent as `"data": null` with `"unavailable": true`: the write that caused it is never refused because of a webhook, and you can read the document through the API.
- `occurred_at` is the time of the transaction, in UTC.
- `data.version` increases with every change. Deliveries can arrive out of order (retries), so **ignore a delivery whose version is older** than what you have.
- The response body of your receiver is ignored (at most 64 KiB are read). Redirects are **not followed**: a 3xx answer is a permanent failure, so give the final URL.

## Verifying a delivery

Anyone who knows the URL can post to it, so a receiver must check the signature. The recipe, in Python (standard library only):

```python
import hashlib
import hmac
import json
import time

SECRET = b"the secret that `webhook add` printed"
TOLERANCE = 5 * 60          # seconds
seen = set()                # use a database or a cache with an expiry in real life


def verify(headers, body):
    """Return the parsed delivery, or raise ValueError. `body` is the raw bytes of the request."""
    timestamp = headers.get("X-Forgebase-Timestamp", "")
    signature = headers.get("X-Forgebase-Signature", "")
    if not timestamp.isdigit():
        raise ValueError("no timestamp")
    # 1. Reject old (or future) timestamps: a captured delivery cannot be replayed later.
    if abs(time.time() - int(timestamp)) > TOLERANCE:
        raise ValueError("timestamp out of tolerance")
    # 2. Sign "<timestamp>.<raw body>" and compare in constant time.
    expected = "sha256=" + hmac.new(
        SECRET, timestamp.encode() + b"." + body, hashlib.sha256
    ).hexdigest()
    if not hmac.compare_digest(expected, signature):
        raise ValueError("bad signature")
    # 3. Parse only after the signature is good.
    delivery = json.loads(body)
    # 4. De-duplicate on the delivery id: delivery is at least once.
    if delivery["delivery"] in seen:
        return None          # already handled: answer 200 and do nothing
    return delivery


def handled(delivery):
    """Call this only after the work for the delivery has succeeded (or has been safely queued):
    a delivery whose work failed must not be marked, so that its retry is processed."""
    seen.add(delivery["delivery"])
```

Rules that matter:

- Sign the **raw bytes** you received, before any JSON parsing or re-serialising.
- Compare with `hmac.compare_digest` (constant time), never `==`.
- Check the timestamp; the timestamp is part of the signed text, so it cannot be changed without breaking the signature. Each retry has a fresh timestamp and signature, with the same delivery id.
- Answer **2xx quickly** (the limit is 10 seconds for everything), and do the real work after answering. A slow or failing receiver is retried, and each retry is the same delivery.
- De-duplicate on `X-Forgebase-Delivery` (also `delivery` in the body), and make the work safe to repeat. Record a delivery id as handled only **after** its work has succeeded; if you answer an error, the retry carries the same id and must not be skipped.
- Migrations do not send webhooks: app SQL migrations and the workflow-state backfill of `migrate` write without the document operations.
- After `webhook rotate-secret` the receiver must use the new secret. Switch both at the same time, or accept both for a while.

A complete receiver is about twenty more lines with `http.server`; the recipe above is the part that matters. [`examples/sites/receiver.py`](../../examples/sites/receiver.py) is one, run by CI against a real worker, and [Integrating with other systems](integration.md#2-webhooks-forgebase-tells-another-system) walks through the whole flow on the `sales` example.

## Trying it locally

For development a receiver on your own machine needs both explicit flags: `--insecure-http` (plain `http://`) and `--allow-private` (a loopback address is blocked otherwise, which is the SSRF guard):

```sh
cargo run -p sales -- webhook add dev --doctype sales_order --events insert \
  --url http://127.0.0.1:9000/hook --insecure-http --allow-private
cargo run -p sales -- webhook test dev            # one signed test delivery, exit 0 on a 2xx answer
cargo run -p sales -- worker                      # sends the real ones
```

Do not use these flags in production: see below. A receiver with a self-made certificate needs that CA in the operating system's trust store of the host that runs the worker (or `SSL_CERT_FILE` pointing to a bundle that holds it); Forgebase does not skip certificate checks.

## What the server refuses

The worker is the thing that opens the connection, so it is strict (the SSRF guard):

- **`https://` only**, unless the webhook was added with `--insecure-http`. No `user:password@` in the URL.
- **The host is resolved by Forgebase and every address must be public.** The Azure WireServer `168.63.129.16`, reachable from Azure virtual machines, is refused as an explicit extra. Loopback (`127.0.0.0/8`, `::1`), private (`10/8`, `172.16/12`, `192.168/16`, `fc00::/7`), link-local (`169.254/16`, with the cloud metadata address `169.254.169.254`, and `fe80::/10`), carrier-grade NAT (`100.64/10`), multicast, reserved and the other special ranges are refused, and so are IPv4-mapped and NAT64 forms of them. One bad address in the answer refuses the delivery. The refusal is **permanent**: the job is `dead` after one attempt, and the message says to use `--allow-private` if you mean it.
- **The connection goes to the addresses that were checked**, and the name is not resolved a second time, so a DNS record that changes in between cannot send the request elsewhere.
- **Redirects are not followed** and a 3xx answer is a permanent failure.
- **10 seconds** for the lookup, the connection, TLS, the request and the answer. A timeout is retried.
- **Any port** is allowed.

Secrets and URLs: the secret is printed once by `add` and `rotate-secret` and never again. It is stored in plain text in the site's database (the signature needs the key), so the database and its backups are as sensitive as the secret. Messages, `job list`, the worker's log and `webhook list` never show the path or the query of a URL (it may hold a token), only the scheme, host and port.

## Troubleshooting

| What you see | What it means |
|---|---|
| Nothing arrives | Is `forgebase worker` running for the site? `forgebase job list --name forgebase.deliver_webhook` shows the deliveries, their status and the last error. Is the webhook enabled (`webhook list`) and does it list the event? |
| `resolves to 127.0.0.1, a loopback, private ... address` | The SSRF guard. A public receiver is fine; for one in your network add the webhook with `--allow-private`. |
| `the URL uses http://` | Use `https://`, or `--insecure-http` for development. |
| `answered HTTP 500` (retried) | Your receiver failed. Fix it and the retry (or `job retry` for a dead one) succeeds, with the same delivery id. |
| `answered HTTP 302 ... redirects are not followed` | Give the final URL. |
| `did not answer within 10 s` | Answer first and work later. |
| `TLS error (invalid peer certificate: UnknownIssuer)` | The receiver's certificate is not trusted by the host's CA store (or the chain is incomplete). |
| a delivery is `done` but nothing was sent | The webhook was removed or disabled before the worker ran the job; this is logged as `is removed or disabled`. |
