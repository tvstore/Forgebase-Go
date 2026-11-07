package db_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/tvstore/forgebase-go/internal/core"
	"github.com/tvstore/forgebase-go/internal/db"
	"github.com/tvstore/forgebase-go/internal/db/dbtest"
)

// The outbox side of the outgoing webhooks (M9.2, ADR 0020) against a real PostgreSQL: the delivery jobs
// that every document operation enqueues in its own transaction. Ported from
// crates/forgebase-db/tests/webhooks.rs, the tests that are about the outbox. The management of the
// webhooks (add, list, enable, remove, rotate) is P7: a webhook is added here with a plain INSERT into
// fb_webhook (what webhooks::add does, without its checks).

const whNote = `
name = "note"
label = "Note"
[[fields]]
name = "title"
type = "data"
required = true
[[fields]]
name = "body"
type = "text"
`

const whLine = `
name = "invoice_line"
label = "Invoice Line"
is_child = true
[[fields]]
name = "item"
type = "data"
`

const whInvoice = `
name = "invoice"
label = "Invoice"
submittable = true
[[fields]]
name = "title"
type = "data"
required = true
[[fields]]
name = "lines"
type = "table"
target = "invoice_line"
`

const whOrder = `
name = "order"
label = "Order"
submittable = true
[[fields]]
name = "title"
type = "data"
required = true

[workflow]
[[workflow.states]]
name = "Draft"
docstatus = 0
[[workflow.states]]
name = "Review"
docstatus = 0
[[workflow.states]]
name = "Done"
docstatus = 1
[[workflow.states]]
name = "Void"
docstatus = 2
[[workflow.transitions]]
action = "Send"
from = "Draft"
to = "Review"
roles = ["Manager"]
[[workflow.transitions]]
action = "Finish"
from = "Review"
to = "Done"
roles = ["Manager"]
[[workflow.transitions]]
action = "Revoke"
from = "Done"
to = "Void"
roles = ["Manager"]
`

func whWorld(t *testing.T, texts ...string) *core.Registry {
	t.Helper()
	reg := registry(t, texts...)
	if !reg.AddJob(db.DeliverWebhookJob) {
		t.Fatal("job")
	}
	return reg
}

func whSetup(t *testing.T) (*dbtest.DB, *core.Registry) {
	t.Helper()
	d := dbtest.Migrated(t)
	reg := whWorld(t, whNote, whLine, whInvoice, whOrder)
	mustSync(t, d, reg)
	return d, reg
}

// hook adds a webhook the way webhooks::add stores it.
func hook(t *testing.T, d *dbtest.DB, name, doctype string, events ...string) {
	t.Helper()
	list := make([]string, len(events))
	for i, e := range events {
		list[i] = "'" + e + "'"
	}
	d.Exec(fmt.Sprintf("INSERT INTO fb_webhook (name, doctype, events, url, secret) VALUES ('%s', '%s', ARRAY[%s]::text[], 'https://example.com/hook', 'secret')",
		name, doctype, strings.Join(list, ", ")))
}

// deliveries are webhook/event/doctype/name/enqueued_by/status of every delivery job, oldest first.
func deliveries(d *dbtest.DB) []string {
	out := []string{}
	for _, r := range d.Rows("SELECT payload->>'webhook' || '/' || (payload->>'event') || '/' || (payload->>'doctype') " +
		"|| '/' || (payload->>'name') || '/' || enqueued_by || '/' || status FROM fb_job " +
		"WHERE name = 'forgebase.deliver_webhook' ORDER BY id") {
		out = append(out, r[0])
	}
	return out
}

// Rust: webhooks.rs::a_committed_insert_enqueues_one_job_per_matching_webhook
func TestACommittedInsertEnqueuesOneJobPerMatchingWebhook(t *testing.T) {
	d, reg := whSetup(t)
	hook(t, d, "a_both", "note", "insert", "update")
	hook(t, d, "b_insert", "note", "insert")
	hook(t, d, "c_update_only", "note", "update")
	hook(t, d, "d_other_doctype", "invoice", "insert")
	hook(t, d, "e_disabled", "note", "insert")
	d.Exec("UPDATE fb_webhook SET enabled = false WHERE name = 'e_disabled'")
	stored := mustInsert(t, d, reg, "note", doc("title", text("hello")), sys("ann"))
	want(t, deliveries(d), []string{
		fmt.Sprintf("a_both/insert/note/%s/ann/queued", stored.Name),
		fmt.Sprintf("b_insert/insert/note/%s/ann/queued", stored.Name),
	}, "deliveries")
	// The payload: the document as the document API writes it, and a time.
	line := d.Rows("SELECT payload->'data'->>'title' || '|' || (payload->'data'->>'version') || '|' " +
		"|| (payload->'data'->>'owner') || '|' || (payload->>'occurred_at') FROM fb_job ORDER BY id LIMIT 1")[0][0]
	if !strings.HasPrefix(line, "hello|1|ann|20") {
		t.Error(line)
	}
	data := d.Rows("SELECT (payload->'data')::text FROM fb_job ORDER BY id LIMIT 1")[0][0]
	var got any
	if err := json.Unmarshal([]byte(data), &got); err != nil {
		t.Fatal(err)
	}
	expected, err := db.DocumentToJSON(stored)
	if err != nil {
		t.Fatal(err)
	}
	// Both sides through JSON, so that numbers are numbers on both.
	text, _ := json.Marshal(expected)
	var wantJSON any
	if err := json.Unmarshal(text, &wantJSON); err != nil {
		t.Fatal(err)
	}
	deepWant(t, got, wantJSON, "the data is the document API's JSON")
}

// Rust: webhooks.rs::a_rolled_back_or_failed_operation_enqueues_nothing
func TestARolledBackOrFailedOperationEnqueuesNothing(t *testing.T) {
	d, reg := whSetup(t)
	hook(t, d, "w", "note", "insert", "update", "delete")
	ann := sys("ann")
	// The caller's own transaction is rolled back after the insert: no job, no document.
	tx, err := d.Pool.Begin(bg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Insert(bg, tx, reg, "note", doc("title", text("gone")), ann); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(bg); err != nil {
		t.Fatal(err)
	}
	if len(deliveries(d)) != 0 || count(t, d, "doc_note") != 0 {
		t.Error("something survived the rollback")
	}
	// A refused operation (a required field is missing) enqueues nothing.
	if _, err := db.Insert(bg, d.Pool, reg, "note", core.Document{}, ann); err == nil {
		t.Fatal("the insert worked")
	}
	if len(deliveries(d)) != 0 {
		t.Error("a delivery was enqueued")
	}
	// A failed update (stale version) enqueues nothing either; the insert before it did.
	stored := mustInsert(t, d, reg, "note", doc("title", text("one")), ann)
	if _, err := db.Update(bg, d.Pool, reg, "note", stored.Name, 99, titleChange("x"), ann); err == nil {
		t.Fatal("the update worked")
	}
	if len(deliveries(d)) != 1 {
		t.Error(deliveries(d))
	}
	// A committed caller transaction keeps its jobs: they are the operations' own.
	tx, err = d.Pool.Begin(bg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Update(bg, tx, reg, "note", stored.Name, 1, titleChange("two"), ann); err != nil {
		t.Fatal(err)
	}
	if err := db.Delete(bg, tx, reg, "note", stored.Name, 2, ann); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(bg); err != nil {
		t.Fatal(err)
	}
	want(t, deliveries(d), []string{
		fmt.Sprintf("w/insert/note/%s/ann/queued", stored.Name),
		fmt.Sprintf("w/update/note/%s/ann/queued", stored.Name),
		fmt.Sprintf("w/delete/note/%s/ann/queued", stored.Name),
	}, "deliveries")
}

// Rust: webhooks.rs::every_operation_fires_its_events
func TestEveryOperationFiresItsEvents(t *testing.T) {
	d, reg := whSetup(t)
	hook(t, d, "w", "invoice", "insert", "update", "delete", "submit", "cancel", "transition")
	ann := sys("ann")
	one := mustInsert(t, d, reg, "invoice", doc("title", text("inv")), ann)
	two := mustUpdate(t, d, reg, "invoice", one.Name, 1, titleChange("inv 2"), ann)
	mustSubmit(t, d, reg, "invoice", one.Name, two.Version, ann)
	mustCancel(t, d, reg, "invoice", one.Name, 3, ann)
	// An amendment is an insert of the new draft.
	amended := mustAmend(t, d, reg, "invoice", one.Name, 4, ann)
	if err := db.Delete(bg, d.Pool, reg, "invoice", amended.Name, amended.Version, ann); err != nil {
		t.Fatal(err)
	}
	n := one.Name
	want(t, deliveries(d), []string{
		fmt.Sprintf("w/insert/invoice/%s/ann/queued", n),
		fmt.Sprintf("w/update/invoice/%s/ann/queued", n),
		fmt.Sprintf("w/submit/invoice/%s/ann/queued", n),
		fmt.Sprintf("w/cancel/invoice/%s/ann/queued", n),
		fmt.Sprintf("w/insert/invoice/%s/ann/queued", amended.Name),
		fmt.Sprintf("w/delete/invoice/%s/ann/queued", amended.Name),
	}, "deliveries")
	// The statuses in the snapshots: draft, draft, submitted, cancelled, draft (the amendment).
	var statuses []string
	for _, r := range d.Rows("SELECT payload->'data'->>'docstatus' FROM fb_job ORDER BY id") {
		statuses = append(statuses, r[0])
	}
	want(t, statuses, []string{"0", "0", "1", "2", "0", "0"}, "statuses")
}

// Rust: webhooks.rs::a_transition_fires_transition_and_the_docstatus_event
func TestATransitionFiresTransitionAndTheDocstatusEvent(t *testing.T) {
	d, reg := whSetup(t)
	hook(t, d, "t", "order", "transition")
	hook(t, d, "s", "order", "submit", "cancel")
	ann := sys("ann")
	order := mustInsert(t, d, reg, "order", doc("title", text("o")), ann)
	step := func(stored *db.StoredDocument, action string) *db.StoredDocument {
		got, err := db.Transition(bg, d.Pool, reg, "order", stored.Name, action, stored.Version, ann)
		if err != nil {
			t.Fatalf("%s: %v", action, err)
		}
		return got
	}
	// Draft -> Review: a transition only. Review -> Done: moves docstatus to 1. Done -> Void: moves it to 2.
	got := step(step(step(order, "Send"), "Finish"), "Revoke")
	_ = got
	n := order.Name
	// One op after the other; within one op by webhook name (s before t), and for a webhook the events in the
	// order of the operation.
	want(t, deliveries(d), []string{
		fmt.Sprintf("t/transition/order/%s/ann/queued", n),
		fmt.Sprintf("s/submit/order/%s/ann/queued", n),
		fmt.Sprintf("t/transition/order/%s/ann/queued", n),
		fmt.Sprintf("s/cancel/order/%s/ann/queued", n),
		fmt.Sprintf("t/transition/order/%s/ann/queued", n),
	}, "deliveries")
	// The snapshot has the workflow state after the transition.
	var states []string
	for _, r := range d.Rows("SELECT payload->>'event' || ':' || (payload->'data'->>'workflow_state') FROM fb_job ORDER BY id") {
		states = append(states, r[0])
	}
	want(t, states, []string{"transition:Review", "submit:Done", "transition:Done", "cancel:Void", "transition:Void"}, "states")
}

// Rust: webhooks.rs::a_delete_sends_the_document_as_it_was_with_its_rows
func TestADeleteSendsTheDocumentAsItWasWithItsRows(t *testing.T) {
	d, reg := whSetup(t)
	hook(t, d, "w", "invoice", "delete")
	withRows := doc("title", text("inv"))
	withRows.Tables["lines"] = []core.Row{row("item", text("a")), row("item", text("b"))}
	stored := mustInsert(t, d, reg, "invoice", withRows, sys("ann"))
	if err := db.Delete(bg, d.Pool, reg, "invoice", stored.Name, 1, sys("ann")); err != nil {
		t.Fatal(err)
	}
	want(t, d.Rows("SELECT payload->'data'->>'title', payload->'data'->'lines'->1->>'item', payload->>'event' FROM fb_job"),
		[][]string{{"inv", "b", "delete"}}, "payload")
}

// Rust: webhooks.rs::a_snapshot_over_the_job_payload_limit_is_sent_without_its_data
func TestASnapshotOverTheJobPayloadLimitIsSentWithoutItsData(t *testing.T) {
	d, reg := whSetup(t)
	hook(t, d, "w", "note", "insert")
	big := doc("title", text("big"), "body", text(strings.Repeat("x", 70_000)))
	stored, err := db.Insert(bg, d.Pool, reg, "note", big, sys("ann"))
	if err != nil {
		t.Fatalf("the write is not refused because of the webhook: %v", err)
	}
	want(t, d.Rows("SELECT payload->>'truncated', coalesce(payload->>'data', 'null'), payload->>'name' FROM fb_job"),
		[][]string{{"true", "null", stored.Name}}, "payload")
	if count(t, d, "doc_note") != 1 {
		t.Error("count")
	}
}

const whStamp = `
name = "stamp"
label = "Stamp"
[[fields]]
name = "title"
type = "data"
required = true
[[fields]]
name = "at"
type = "datetime"
`

// Rust: webhooks.rs::a_snapshot_that_cannot_be_serialised_is_sent_unavailable_and_the_write_commits
//
// M9.2 review, carry-over: a snapshot that cannot be written as JSON must not fail the write.
// PostgreSQL has datetimes before the year 0 (0002-01-01 BC is the year -1), which can be read and
// which RFC 3339 cannot write.
func TestASnapshotThatCannotBeSerialisedIsSentUnavailableAndTheWriteCommits(t *testing.T) {
	d := dbtest.Migrated(t)
	reg := whWorld(t, whStamp)
	mustSync(t, d, reg)
	hook(t, d, "w", "stamp", "insert", "update")
	ann := sys("ann")
	stored := mustInsert(t, d, reg, "stamp", doc("title", text("one")), ann)
	// A normal snapshot first: data present, not unavailable.
	want(t, d.Rows("SELECT coalesce(payload->>'unavailable', '-') || '/' || (payload->'data'->>'title') FROM fb_job"),
		[][]string{{"-/one"}}, "normal snapshot")
	d.Exec("UPDATE doc_stamp SET \"at\" = '0002-01-01 BC 00:00:00+00'")
	updated, err := db.Update(bg, d.Pool, reg, "stamp", stored.Name, 1, titleChange("two"), ann)
	if err != nil {
		t.Fatalf("the write is not refused because of the webhook: %v", err)
	}
	if updated.Version != 2 {
		t.Error(updated.Version)
	}
	rows := d.Rows("SELECT payload->>'event' || '/' || coalesce(payload->>'unavailable', '-') || '/' " +
		"|| coalesce(payload->>'data', 'null') || '/' || coalesce(payload->>'truncated', '-') FROM fb_job ORDER BY id")
	want(t, rows[1], []string{"update/true/null/-"}, "unavailable")
	want(t, d.Rows("SELECT title FROM doc_stamp"), [][]string{{"two"}}, "the update was committed")
}

// Rust: webhooks.rs::a_binary_without_the_delivery_job_refuses_the_write_clearly
func TestABinaryWithoutTheDeliveryJobRefusesTheWriteClearly(t *testing.T) {
	d := dbtest.Migrated(t)
	// No AddJob(DeliverWebhookJob): a registry that the framework did not build.
	reg := registry(t, whNote)
	mustSync(t, d, reg)
	hook(t, d, "w", "note", "insert")
	_, err := db.Insert(bg, d.Pool, reg, "note", doc("title", text("x")), sys("ann"))
	if e := wantKind(t, err, db.ErrJob); !strings.Contains(e.Message, "forgebase.deliver_webhook") {
		t.Error(e.Message)
	}
	if count(t, d, "doc_note") != 0 {
		t.Error("the document was written")
	}
}

// Rust: webhooks.rs::the_lookup_uses_the_partial_index
func TestTheLookupUsesThePartialIndex(t *testing.T) {
	d, _ := whSetup(t)
	hook(t, d, "w", "note", "insert")
	conn, err := d.Pool.Acquire(bg)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	// The planner prefers a sequential scan on a table of one row; force the index to show that the
	// query can use it.
	if _, err := conn.Exec(bg, "SET enable_seqscan = off"); err != nil {
		t.Fatal(err)
	}
	rows, err := conn.Query(bg, "EXPLAIN SELECT name, events, now() FROM fb_webhook "+
		"WHERE enabled AND doctype = $1 AND events && $2::text[]", "note", []string{"insert"})
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	found := false
	var plan []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, line)
		found = found || strings.Contains(line, "fb_webhook_doctype")
	}
	if !found {
		t.Errorf("%v", plan)
	}
}
