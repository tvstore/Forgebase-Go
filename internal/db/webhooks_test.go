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
// crates/forgebase-db/tests/webhooks.rs: the tests of the outbox and, since P7.1, the management of the
// webhooks (add, list, enable, remove, rotate), which is in webhooks.go.

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

// hook adds a webhook of reg with db.AddWebhook (a random secret, https://example.com/hook).
func hook(t *testing.T, d *dbtest.DB, reg *core.Registry, name, doctype string, events ...string) {
	t.Helper()
	if _, err := db.AddWebhook(bg, d.Pool, reg, db.NewWebhook{Name: name, DocType: doctype, Events: events, URL: "https://example.com/hook"}, nil); err != nil {
		t.Fatalf("add: %v", err)
	}
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
	hook(t, d, reg, "a_both", "note", "insert", "update")
	hook(t, d, reg, "b_insert", "note", "insert")
	hook(t, d, reg, "c_update_only", "note", "update")
	hook(t, d, reg, "d_other_doctype", "invoice", "insert")
	hook(t, d, reg, "e_disabled", "note", "insert")
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
	hook(t, d, reg, "w", "note", "insert", "update", "delete")
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
	hook(t, d, reg, "w", "invoice", "insert", "update", "delete", "submit", "cancel", "transition")
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
	hook(t, d, reg, "t", "order", "transition")
	hook(t, d, reg, "s", "order", "submit", "cancel")
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
	hook(t, d, reg, "w", "invoice", "delete")
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
	hook(t, d, reg, "w", "note", "insert")
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
	hook(t, d, reg, "w", "stamp", "insert", "update")
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
	hook(t, d, reg, "w", "note", "insert")
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
	d, reg := whSetup(t)
	hook(t, d, reg, "w", "note", "insert")
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

// ---- the management (webhooks.go) -------------------------------------------------------------

func isHexText(s string) bool {
	for _, c := range s {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return false
		}
	}
	return true
}

func newHook(name, doctype string, events ...string) db.NewWebhook {
	return db.NewWebhook{Name: name, DocType: doctype, Events: events, URL: "https://example.com/x"}
}

func strp(s string) *string { return &s }

func webhookMessage(t *testing.T, err error) string {
	t.Helper()
	return wantKind(t, err, db.ErrWebhook).Message
}

// Rust: webhooks.rs::adding_checks_the_name_the_events_and_the_doctype
func TestAddingChecksTheNameTheEventsAndTheDocType(t *testing.T) {
	d, reg := whSetup(t)

	// A good one: the secret is 32 random bytes as 64 hex characters.
	secret, err := db.AddWebhook(bg, d.Pool, reg, newHook("good", "note", "insert"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(secret) != 64 || !isHexText(secret) {
		t.Errorf("%q", secret)
	}
	// A given secret is used as it is.
	given, err := db.AddWebhook(bg, d.Pool, reg, newHook("given", "note", "insert"), strp("my-own-secret-of-24-bytes"))
	if err != nil || given != "my-own-secret-of-24-bytes" {
		t.Fatalf("%q %v", given, err)
	}

	// A given secret must have at least 16 bytes: 15 are refused, 16 pass; an empty one is its own error.
	for _, short := range []string{"", "a", "fifteen-bytes!!"} {
		_, err := db.AddWebhook(bg, d.Pool, reg, newHook("short", "note", "insert"), &short)
		message := webhookMessage(t, err)
		expected := "at least 16 bytes"
		if short == "" {
			expected = "empty"
		}
		if !strings.Contains(message, expected) {
			t.Errorf("%q: %s", short, message)
		}
	}
	if _, err := db.AddWebhook(bg, d.Pool, reg, newHook("short", "note", "insert"), strp("sixteen-bytes!!!")); err != nil {
		t.Fatalf("16 bytes are enough: %v", err)
	}
	if err := db.RemoveWebhook(bg, d.Pool, "short"); err != nil {
		t.Fatal(err)
	}
	// The name is taken, or not a valid name.
	_, err = db.AddWebhook(bg, d.Pool, reg, newHook("good", "note", "insert"), nil)
	if message := webhookMessage(t, err); !strings.Contains(message, "exists already") {
		t.Error(message)
	}
	for _, bad := range []string{"", "Bad", "has space", "1abc", "a-b"} {
		_, err := db.AddWebhook(bg, d.Pool, reg, newHook(bad, "note", "insert"), nil)
		message := webhookMessage(t, err)
		if bad != "" && !strings.Contains(message, "the name "+core.DebugQuote(bad)+" must match") {
			t.Errorf("%q: %s", bad, message)
		}
	}
	// The DocType must exist and must not be a child DocType.
	_, err = db.AddWebhook(bg, d.Pool, reg, newHook("a", "nobody", "insert"), nil)
	wantKind(t, err, db.ErrUnknownDocType)
	_, err = db.AddWebhook(bg, d.Pool, reg, newHook("b", "invoice_line", "insert"), nil)
	wantKind(t, err, db.ErrChildDocType)
	// The events: at least one, all known.
	for _, events := range [][]string{nil, {"insert", "save"}, {"Insert"}} {
		_, err := db.AddWebhook(bg, d.Pool, reg, newHook("c", "note", events...), nil)
		webhookMessage(t, err)
	}
	// An empty URL is refused.
	empty := newHook("c", "note", "insert")
	empty.URL = ""
	_, err = db.AddWebhook(bg, d.Pool, reg, empty, nil)
	if message := webhookMessage(t, err); message != "the URL is empty" {
		t.Error(message)
	}
	// The database refuses an empty or unknown events list as well, whoever writes the row.
	if err := d.TryExec("INSERT INTO fb_webhook (name, doctype, events, url, secret) VALUES ('raw', 'note', '{}', 'https://x', 's')"); err == nil {
		t.Error("the CHECK constraint holds for an empty list")
	}
	if err := d.TryExec("INSERT INTO fb_webhook (name, doctype, events, url, secret) VALUES ('raw', 'note', '{save}', 'https://x', 's')"); err == nil {
		t.Error("the CHECK constraint holds for an unknown event")
	}

	// Nothing but the two good ones was stored.
	all, err := db.ListWebhooks(bg, d.Pool)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, w := range all {
		names = append(names, w.Name)
		if !w.Enabled || w.AllowPrivate || w.InsecureHTTP || w.CreatedAt.IsZero() {
			t.Errorf("%+v", w)
		}
	}
	want(t, names, []string{"given", "good"}, "stored")
	loaded, err := db.GetWebhook(bg, d.Pool, "given")
	if err != nil || loaded == nil || loaded.Secret != "my-own-secret-of-24-bytes" {
		t.Fatalf("%v %v", loaded, err)
	}
	if missing, err := db.GetWebhook(bg, d.Pool, "nobody"); err != nil || missing != nil {
		t.Errorf("%v %v", missing, err)
	}
	// The events are kept in the order given, without duplicates.
	_, err = db.AddWebhook(bg, d.Pool, reg, newHook("ordered", "note", "submit", "insert", "submit"), nil)
	if err != nil {
		t.Fatal(err)
	}
	ordered, _ := db.GetWebhook(bg, d.Pool, "ordered")
	want(t, ordered.Events, []string{"submit", "insert"}, "events")
}

// Rust: webhooks.rs::enable_disable_remove_and_rotate_report_a_missing_webhook
func TestEnableDisableRemoveAndRotateReportAMissingWebhook(t *testing.T) {
	d, reg := whSetup(t)
	hook(t, d, reg, "one", "note", "insert")
	enabled := func() bool {
		w, err := db.GetWebhook(bg, d.Pool, "one")
		if err != nil || w == nil {
			t.Fatalf("%v %v", w, err)
		}
		return w.Enabled
	}
	secret := func() string {
		w, _ := db.GetWebhook(bg, d.Pool, "one")
		return w.Secret
	}
	if err := db.SetWebhookEnabled(bg, d.Pool, "one", false); err != nil || enabled() {
		t.Fatal("disable")
	}
	if err := db.SetWebhookEnabled(bg, d.Pool, "one", true); err != nil || !enabled() {
		t.Fatal("enable")
	}
	old := secret()
	fresh, err := db.RotateWebhookSecret(bg, d.Pool, "one", nil)
	if err != nil || fresh == old || secret() != fresh {
		t.Fatalf("%q %q %v", old, fresh, err)
	}
	// A secret that is given must have at least 16 bytes; a refused one changes nothing.
	_, err = db.RotateWebhookSecret(bg, d.Pool, "one", strp("too-short"))
	if message := webhookMessage(t, err); !strings.Contains(message, "at least 16 bytes") {
		t.Error(message)
	}
	if secret() != fresh {
		t.Error("a refused secret changed the secret")
	}
	sixteen, err := db.RotateWebhookSecret(bg, d.Pool, "one", strp("exactly-16-bytes"))
	if err != nil || sixteen != "exactly-16-bytes" || secret() != "exactly-16-bytes" {
		t.Fatalf("%q %v", sixteen, err)
	}
	if err := db.RemoveWebhook(bg, d.Pool, "one"); err != nil {
		t.Fatal(err)
	}
	_, rotateErr := db.RotateWebhookSecret(bg, d.Pool, "one", nil)
	for _, err := range []error{
		db.SetWebhookEnabled(bg, d.Pool, "one", true),
		db.RemoveWebhook(bg, d.Pool, "one"),
		rotateErr,
	} {
		if message := webhookMessage(t, err); message != "no webhook named `one`" {
			t.Error(message)
		}
	}
	// The error text of the kind.
	if got := (&db.Error{Kind: db.ErrWebhook, Message: "x"}).Error(); got != "webhook: x" {
		t.Error(got)
	}
}

// Rust: webhooks.rs::events_are_checked_and_deduplicated (unit test)
func TestWebhookEventsAreCheckedAndDeduplicated(t *testing.T) {
	got, err := db.CheckWebhookEvents([]string{"submit", "insert", "submit"})
	if err != nil {
		t.Fatal(err)
	}
	want(t, got, []string{"submit", "insert"}, "events")
	for _, bad := range [][]string{nil, {}, {"insert", "save"}, {"Insert"}} {
		if _, err := db.CheckWebhookEvents(bad); err == nil {
			t.Errorf("%v", bad)
		}
	}
}

// Rust: webhooks.rs::the_secret_is_not_in_debug_output (unit test)
func TestTheWebhookSecretIsNotInItsPrintedForm(t *testing.T) {
	hook := db.Webhook{Name: "n", DocType: "d", Events: []string{"insert"}, URL: "https://example.com/TOKEN", Secret: "very-secret", Enabled: true}
	for _, text := range []string{fmt.Sprint(hook), fmt.Sprintf("%v", hook), fmt.Sprintf("%+v", hook), fmt.Sprintf("%#v", hook), fmt.Sprint(&hook)} {
		if strings.Contains(text, "very-secret") || strings.Contains(text, "TOKEN") {
			t.Errorf("%s", text)
		}
	}
}
