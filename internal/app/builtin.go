package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/tvstore/forgebase-go/internal/db"
)

// The clean-up jobs the framework owns (ADR 0013, owner decision 8; the Rust module is builtin.rs). They are
// registered in every binary under the reserved app name `forgebase`, with the default job options, and
// scheduled like an app's jobs. The SQL is in internal/db; this file only runs it, in batches, in the
// transaction of the job.
//
//	| Job                              | Deletes                                               | Schedule         |
//	| forgebase.purge_jobs             | done and dead jobs finished more than 7 days ago      | daily 03:00 UTC  |
//	| forgebase.purge_login_failures   | login failure rows that no longer count or lock       | every 60 minutes |
//	| forgebase.purge_sessions         | sessions that are past their expiry or idle limit     | every 60 minutes |
//
// forgebase.deliver_webhook (M9.2, ADR 0020) is the framework's one job that is not a clean-up and has no
// schedule: a document operation enqueues it, in its own transaction, for every webhook that listens to the
// event, and the worker posts the signed payload.

const (
	// purgeBatch is the rows deleted per statement.
	purgeBatch = 1000
	// purgeMaxBatches is the statements per run. A run that hits the limit leaves the rest for the next
	// scheduled run, so one run stays short whatever the backlog.
	purgeMaxBatches = 100
)

// builtinJob is a built-in job with its schedule (nil: not scheduled).
type builtinJob struct {
	def  jobDef
	spec *db.Spec
}

// builtinJobs are the built-in jobs with their schedules, in a fixed order.
func builtinJobs() []builtinJob {
	purges := []struct {
		name  string
		purge func(context.Context, db.Queryer, int64) (uint64, error)
		spec  db.Spec
	}{
		{"purge_jobs", db.PurgeFinishedJobs, db.DailyAt(3, 0)},
		{"purge_login_failures", db.PurgeLoginFailures, db.EveryMinutes(60)},
		{"purge_sessions", db.PurgeSessions, db.EveryMinutes(60)},
	}
	out := make([]builtinJob, len(purges))
	for i, p := range purges {
		spec := p.spec
		out[i] = builtinJob{
			def:  jobDef{name: frameworkApp + "." + p.name, handler: purgeHandler(p.purge), options: NewJobOptions()},
			spec: &spec,
		}
	}
	// The delivery of a webhook has no schedule. The client's own limit is 10 seconds, the job's timeout is the
	// margin.
	out = append(out, builtinJob{
		def: jobDef{name: db.DeliverWebhookJob, handler: deliverWebhook, options: NewJobOptions().Timeout(deliverTimeout)},
	})
	return out
}

// purgeHandler deletes in batches in the job's transaction until a batch is not full.
func purgeHandler(purge func(context.Context, db.Queryer, int64) (uint64, error)) JobHandler {
	return func(ctx context.Context, job *JobContext) error {
		var total uint64
		for range purgeMaxBatches {
			deleted, err := purge(ctx, job.tx, purgeBatch)
			if err != nil {
				return fromDB(asDBError(err))
			}
			total += deleted
			if deleted < purgeBatch {
				break
			}
		}
		if total > 0 {
			job.log(fmt.Sprintf("%s: deleted %d row(s)", job.name, total))
		}
		return nil
	}
}

// deliverTimeout is the job timeout of a delivery: the client's own limit is 10 seconds, this is the margin.
const deliverTimeout = 30 * time.Second

// deliverWebhook is the webhook delivery (ADR 0020). One attempt is one request; the retries are the job's
// (ADR 0013). The steps:
//
//  1. Load the webhook by name. Removed or disabled since the operation: the job completes without sending and
//     the worker logs it (no secret).
//  2. Body, compact JSON: {"event", "doctype", "name", "data", "occurred_at", "delivery"} (and "truncated":
//     true when it applies). delivery is the id of the job, the same on every attempt.
//  3. Send it (sendWebhook: the URL rules, the SSRF guard, the signature) and map the answer: 2xx is done, 3xx
//     is a permanent failure (redirects are not followed), any other status, a connection error, a TLS error or a
//     timeout is retryable, an address that is not allowed or an invalid URL is permanent.
//
// The messages, which become the job's last_error and the worker's log line, name only the scheme, the host and
// the port and the status: never the path or query of the URL, the secret or the signature.
func deliverWebhook(ctx context.Context, job *JobContext) error {
	var payload map[string]any
	decoder := json.NewDecoder(bytes.NewReader(job.payload))
	decoder.UseNumber() // a number in the snapshot is sent as it was stored
	if err := decoder.Decode(&payload); err != nil {
		return NewPermanentJobError("the job payload is not a webhook delivery")
	}
	name, nameOK := payload["webhook"].(string)
	event, eventOK := payload["event"].(string)
	if !nameOK || !eventOK {
		return NewPermanentJobError("the job payload is not a webhook delivery")
	}
	webhook, err := db.GetWebhook(ctx, job.tx, name)
	if err != nil {
		return fromDB(asDBError(err))
	}
	if webhook == nil || !webhook.Enabled {
		// Removed or disabled since the document operation: nothing to send, and retrying would not change it.
		job.log(fmt.Sprintf("webhook `%s` is removed or disabled; the delivery of `%s` is dropped", name, event))
		return nil
	}
	body := map[string]any{
		"event":       event,
		"doctype":     payload["doctype"],
		"name":        payload["name"],
		"data":        payload["data"],
		"occurred_at": payload["occurred_at"],
		"delivery":    job.id,
	}
	if payload["truncated"] == true {
		body["truncated"] = true
	}
	text, err := compactJSON(body)
	if err != nil {
		return NewPermanentJobError("the delivery body cannot be serialized")
	}
	target, err := parseWebhookURL(webhook.URL, webhook.InsecureHTTP)
	if err != nil {
		return NewPermanentJobError(err.Error())
	}
	status, failure := sendWebhook(ctx, outgoingWebhook{
		url:          webhook.URL,
		secret:       webhook.Secret,
		allowPrivate: webhook.AllowPrivate,
		insecureHTTP: webhook.InsecureHTTP,
		event:        event,
		// The job id is the same on every attempt: receivers de-duplicate on it.
		delivery: job.id,
		body:     text,
	})
	if failure != nil {
		if failure.refused {
			return NewPermanentJobError(failure.message)
		}
		return NewJobError(failure.message)
	}
	return webhookOutcome(status, target.label())
}

// webhookOutcome is what a delivery found out, as the outcome of the job.
func webhookOutcome(status int, label string) error {
	switch {
	case status >= 200 && status <= 299:
		return nil
	case status >= 300 && status <= 399:
		return NewPermanentJobError(fmt.Sprintf("%s answered HTTP %d: redirects are not followed", label, status))
	}
	return NewJobError(fmt.Sprintf("%s answered HTTP %d", label, status))
}

// compactJSON is v as compact JSON as serde_json writes it: keys of a map in order, no HTML escaping, no
// final newline, and U+2028 / U+2029 written as themselves.
func compactJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return []byte(unescapeLineSeparators(string(bytes.TrimSuffix(buf.Bytes(), []byte("\n"))))), nil
}
