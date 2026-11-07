package db

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/tvstore/forgebase-go/internal/core"
)

// The outbox (ADR 0013, ADR 0020): the jobs a document operation causes are inserted in the
// operation's transaction, so a job exists if and only if the operation committed. This file has
// the two ways in: the jobs that hooks enqueued (Rust jobs.rs enqueue and enqueue_collected) and
// the webhook deliveries (webhooks.rs enqueue). The rest of the job queue (claim, complete, ...) is in
// jobs.go and the rest of the webhooks (add, list, ...) is webhooks.go.

// Enqueue inserts a job and returns its id. name must be a job of registry, and the payload (any
// value encoding/json can marshal) must pass core.CheckJobPayload (64 KiB of JSON); otherwise
// ErrJob and nothing is written. enqueuedBy must be a valid actor (non-empty, no NUL), else
// ErrInvalidActor. It runs in the transaction of q.
func Enqueue(ctx context.Context, q Queryer, registry *core.Registry, name string, payload any, enqueuedBy string) (uuid.UUID, error) {
	if err := checkActor(enqueuedBy); err != nil {
		return uuid.UUID{}, err
	}
	if !registry.HasJob(name) {
		return uuid.UUID{}, &Error{Kind: ErrJob, Message: fmt.Sprintf(
			"unknown job `%s` (jobs are registered by the apps of the binary as `<app>.<job>`)", name)}
	}
	if reason := core.CheckJobPayload(payload); reason != "" {
		return uuid.UUID{}, &Error{Kind: ErrJob, Message: reason}
	}
	text, err := marshalJSON(payload)
	if err != nil {
		return uuid.UUID{}, &Error{Kind: ErrJob, Message: err.Error()}
	}
	id := newID()
	_, err = q.Exec(ctx,
		"INSERT INTO fb_job (id, name, payload, status, enqueued_by) VALUES ($1, $2, $3::text::jsonb, 'queued', $4)",
		[16]byte(id), name, text, enqueuedBy)
	if err != nil {
		return uuid.UUID{}, wrap(err)
	}
	return id, nil
}

// marshalJSON is compact JSON text without HTML escaping, like serde_json's to_string.
func marshalJSON(v any) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return "", err
	}
	return string(bytes.TrimSuffix(buf.Bytes(), []byte("\n"))), nil
}

// writeJobs writes the jobs the hooks of this operation enqueued, in the operation's transaction
// (the outbox, ADR 0013): after every hook has run and before the commit, so a refused or
// rolled-back operation leaves no job. An unknown job name or an oversize payload is ErrJob and
// fails the whole operation. The first problem stops the rest (Rust enqueue_collected).
func writeJobs(ctx context.Context, c Queryer, registry *core.Registry, jobs *core.EnqueuedJobs, caller core.Caller) error {
	for _, job := range jobs.Take() {
		if _, err := Enqueue(ctx, c, registry, job.Name(), job.Payload(), caller.Name()); err != nil {
			return err
		}
	}
	return nil
}

// DeliverWebhookJob is the full name of the job that delivers a webhook (M9.2).
const DeliverWebhookJob = "forgebase.deliver_webhook"

// writeWebhooks enqueues the webhook deliveries of the operation (M9.2, ADR 0020) in its
// transaction: the last step before the commit, so a failed or rolled-back operation enqueues none.
//
// events are the events of the operation in order and stored is the document after it (before it,
// for a delete). For each enabled webhook of the DocType that listens to an event, one job is
// enqueued, as the actor of the caller, with the payload
//
//	{"webhook", "event", "doctype", "name", "data", "occurred_at"}
//
// where data is the document in the JSON form of DocumentToJSON: all fields, child rows included,
// not filtered by the permissions of the caller (a webhook is configured by an operator, who
// decides where the data goes). A snapshot that does not fit a job payload (64 KiB) is replaced by
// "data": null and "truncated": true, and one that cannot be written as JSON (a stored value with
// no JSON form) by "data": null and "unavailable": true: a webhook never makes a business write
// fail because of its size or its content. The query is one indexed SELECT; with no match nothing
// else is done, not even the serialisation of the document.
func writeWebhooks(ctx context.Context, c Queryer, registry *core.Registry, doctype *core.DocType, events []string, stored *StoredDocument, caller core.Caller) error {
	type hook struct {
		name    string
		listens []string
		now     pgtype.Timestamptz
	}
	rows, err := c.Query(ctx,
		"SELECT name, events, now() FROM fb_webhook WHERE enabled AND doctype = $1 AND events && $2::text[] ORDER BY name COLLATE \"C\"",
		doctype.Name, events)
	if err != nil {
		return wrap(err)
	}
	var hooks []hook
	for rows.Next() {
		var h hook
		if err := rows.Scan(&h.name, &h.listens, &h.now); err != nil {
			rows.Close()
			return wrap(err)
		}
		hooks = append(hooks, h)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return wrap(err)
	}
	if len(hooks) == 0 {
		return nil
	}
	// A stored value with no JSON form (a datetime before the year 0, which PostgreSQL has and
	// RFC 3339 has not) makes the snapshot unavailable: the delivery is sent with "data": null and
	// "unavailable": true. The write never fails because of a webhook.
	snapshot, snapErr := DocumentToJSON(stored)
	for _, h := range hooks {
		occurredAt, err := UTCRFC3339(h.now.Time)
		if err != nil {
			return &Error{Kind: ErrJob, Message: "webhook timestamp: " + err.Error()}
		}
		for _, event := range events {
			listened := false
			for _, l := range h.listens {
				if l == event {
					listened = true
					break
				}
			}
			if !listened {
				continue
			}
			payload := map[string]any{
				"webhook":     h.name,
				"event":       event,
				"doctype":     doctype.Name,
				"name":        stored.Name,
				"occurred_at": occurredAt,
				"data":        nil,
			}
			if snapErr == nil {
				payload["data"] = snapshot
			} else {
				payload["unavailable"] = true
			}
			if core.CheckJobPayload(payload) != "" {
				payload["data"] = nil
				payload["truncated"] = true
			}
			if _, err := Enqueue(ctx, c, registry, DeliverWebhookJob, payload, caller.Name()); err != nil {
				return err
			}
		}
	}
	return nil
}
