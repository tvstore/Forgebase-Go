package forgebase_test

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tvstore/forgebase-go"
	"github.com/tvstore/forgebase-go/internal/webhooktest"
	fbtest "github.com/tvstore/forgebase-go/testing"
)

// Outgoing webhooks, end to end (M9.2, ADR 0020): a document operation enqueues a delivery, the worker posts it to
// an in-test receiver, and the receiver checks the headers, the body and the signature. Real PostgreSQL
// (forgebase/testing), real sockets on 127.0.0.1, real TLS with a throwaway CA made in the test. Ported from
// crates/forgebase/tests/webhooks.rs.
//
// The webhooks here are rows inserted with SQL, so a test knows the secret and sets the two development flags
// directly; the webhook commands have their own tests (internal/app/webhook_cmd_test.go and cmd/forgebase).

const whNoteDoc = `
name = "note"
label = "Note"
[[fields]]
name = "title"
type = "data"
required = true

[[permissions]]
role = "Agent"
read = true
write = true
create = true
delete = true
`

// whMakeNote inserts a note titled "from job", as the documents handle of a job.
func whMakeNote(ctx context.Context, job *forgebase.JobContext) error {
	_, err := job.Documents().Insert(ctx, "note", titled("from job"))
	return err
}

func whBinary() *forgebase.Binary {
	return forgebase.New().App(forgebase.NewApp("shop").DocTypes(docTypes("doctypes/note.toml", whNoteDoc)).Job("make_note", whMakeNote))
}

var (
	whPKIOnce sync.Once
	whPKI     *webhooktest.PKI
)

// whCA is the test CA. The first call also tells the webhook client to trust it and to give up after 3 seconds,
// for the whole test process; call it first in every test.
func whCA(t *testing.T) *webhooktest.PKI {
	t.Helper()
	whPKIOnce.Do(func() {
		whPKI = webhooktest.NewPKI(t, "main")
		fbtest.WebhookTestSettings(t, whPKI.CAPEM, 3*time.Second)
	})
	if whPKI == nil {
		t.Fatal("the test CA could not be made")
	}
	return whPKI
}

func whReady(t *testing.T) *fbtest.DB {
	t.Helper()
	whCA(t)
	return ready(t, whBinary())
}

const whSecret = "test-secret-do-not-print"

// whAddHook adds a webhook row with whSecret. flags are allow_private and insecure_http.
func whAddHook(db *fbtest.DB, name, events, url string, allowPrivate, insecureHTTP bool) {
	db.Exec(fmt.Sprintf("INSERT INTO fb_webhook (name, doctype, events, url, secret, allow_private, insecure_http) "+
		"VALUES ('%s', 'note', '{%s}', '%s', '%s', %t, %t)", name, events, url, whSecret, allowPrivate, insecureHTTP))
}

// whAddLocal is a local plain-HTTP webhook: both development flags on.
func whAddLocal(db *fbtest.DB, name, url string) { whAddHook(db, name, "insert", url, true, true) }

func whInsertNote(t *testing.T, db *fbtest.DB, binary *forgebase.Binary, title string) map[string]any {
	t.Helper()
	db.CreateUser("ann", "Agent")
	reply := db.Client(binary).Login("ann").Post("/api/v1/resource/note", map[string]any{"title": title})
	if reply.Status != 201 {
		t.Fatalf("%d %v", reply.Status, reply.Body)
	}
	return reply.Body.(map[string]any)
}

// whJob is status, attempts and last_error of the only delivery job.
func whJob(t *testing.T, db *fbtest.DB) [3]string {
	t.Helper()
	rows := db.Rows("SELECT status, attempts::text, coalesce(last_error, '-') FROM fb_job WHERE name = 'forgebase.deliver_webhook'")
	if len(rows) != 1 {
		t.Fatalf("%v", rows)
	}
	return [3]string{rows[0][0], rows[0][1], rows[0][2]}
}

func whMakeDue(db *fbtest.DB) { db.Exec("UPDATE fb_job SET run_at = now() WHERE status = 'queued'") }

// whAssertClean fails when a failed job shows the secret, the path or the query of the URL.
func whAssertClean(t *testing.T, text string) {
	t.Helper()
	for _, forbidden := range []string{whSecret, "TOPSECRET", "hook-path", "token=", "sha256="} {
		if strings.Contains(text, forbidden) {
			t.Errorf("`%s` in: %s", forbidden, text)
		}
	}
}

func whNote(t *testing.T, db *fbtest.DB, binary *forgebase.Binary) (*fbtest.Client, string) {
	t.Helper()
	db.CreateUser("ann", "Agent")
	ann := db.Client(binary).Login("ann")
	created := ann.Post("/api/v1/resource/note", map[string]any{"title": "one"})
	return ann, created.DataObject()["name"].(string)
}

// ---- the happy path -------------------------------------------------------------------------

// Rust: webhooks.rs::a_committed_document_is_delivered_signed
func TestACommittedDocumentIsDeliveredSigned(t *testing.T) {
	db := whReady(t)
	receiver := webhooktest.NewHTTP(t, webhooktest.Status(200))
	whAddLocal(db, "orders", receiver.URL("/hook-path?token=TOPSECRET"))
	binary := whBinary()

	created := whInsertNote(t, db, binary, "hello")
	if n := len(receiver.Requests()); n != 0 {
		t.Fatalf("nothing is sent by the request: %d", n)
	}
	if whJob(t, db)[0] != "queued" {
		t.Fatalf("%v", whJob(t, db))
	}

	run := fbtest.RunJobs(binary, db)
	expect(t, counts(run), [3]int{1, 0, 0})
	if whJob(t, db)[0] != "done" {
		t.Fatalf("%v", whJob(t, db))
	}

	requests := receiver.Requests()
	if len(requests) != 1 {
		t.Fatalf("%d requests", len(requests))
	}
	request := requests[0]
	if request.RequestLine != "POST /hook-path?token=TOPSECRET HTTP/1.1" {
		t.Errorf("%s", request.RequestLine)
	}
	if request.Header("content-type") != "application/json" {
		t.Errorf("%s", request.Header("content-type"))
	}
	if ua := request.Header("user-agent"); !strings.HasPrefix(ua, "Forgebase-Webhook/") {
		t.Errorf("%s", ua)
	}
	if request.Header("x-forgebase-event") != "insert" {
		t.Errorf("%s", request.Header("x-forgebase-event"))
	}
	if request.Header("accept-encoding") != "" {
		t.Errorf("the client asked for compression: %s", request.Header("accept-encoding"))
	}

	// The delivery id is the id of the job.
	jobID := db.Rows("SELECT id::text FROM fb_job")[0][0]
	if request.Header("x-forgebase-delivery") != jobID {
		t.Errorf("%s != %s", request.Header("x-forgebase-delivery"), jobID)
	}

	// The timestamp is the time of sending, in Unix seconds.
	timestamp := request.Header("x-forgebase-timestamp")
	sent, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil || time.Since(time.Unix(sent, 0)).Abs() > 30*time.Second {
		t.Errorf("%s %v", timestamp, err)
	}

	// The signature, computed independently of the code under test.
	if got, want := request.Header("x-forgebase-signature"), webhooktest.ExpectedSignature(whSecret, timestamp, request.Body); got != want {
		t.Errorf("%s != %s", got, want)
	}

	// The body: the event, the document as the API shows it, and the delivery id.
	body := request.JSON(t)
	if body["event"] != "insert" || body["doctype"] != "note" || body["name"] != created["data"].(map[string]any)["name"] || body["delivery"] != jobID {
		t.Errorf("%v", body)
	}
	if at, _ := body["occurred_at"].(string); !strings.HasSuffix(at, "Z") {
		t.Errorf("%v", body["occurred_at"])
	}
	if !reflect.DeepEqual(body["data"], created["data"]) {
		t.Errorf("%v != %v", body["data"], created["data"])
	}
	data := body["data"].(map[string]any)
	if data["title"] != "hello" || data["version"] != float64(1) {
		t.Errorf("%v", data)
	}
	// The keys are written in order, as serde_json writes a map, and compactly.
	text := string(request.Body)
	decoder := json.NewDecoder(strings.NewReader(text))
	var keys []string
	if _, err := decoder.Token(); err != nil { // the opening brace
		t.Fatal(err)
	}
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			t.Fatal(err)
		}
		keys = append(keys, key.(string))
		var skipped json.RawMessage
		if err := decoder.Decode(&skipped); err != nil {
			t.Fatal(err)
		}
	}
	expect(t, keys, []string{"data", "delivery", "doctype", "event", "name", "occurred_at"})
	if strings.Contains(text, ": ") || strings.Contains(text, ", ") {
		t.Errorf("the body is not compact: %s", text)
	}
}

// Rust: webhooks.rs::a_write_by_a_job_is_delivered_too
func TestAWriteByAJobIsDeliveredToo(t *testing.T) {
	db := whReady(t)
	receiver := webhooktest.NewHTTP(t, webhooktest.Status(204))
	whAddLocal(db, "orders", receiver.URL("/x"))
	queueJob(db, "shop.make_note", "{}")
	run := fbtest.RunJobs(whBinary(), db)
	// The job itself, and the delivery it caused (run in the same call).
	expect(t, counts(run), [3]int{2, 0, 0})
	requests := receiver.Requests()
	if len(requests) != 1 || requests[0].JSON(t)["data"].(map[string]any)["title"] != "from job" {
		t.Fatalf("%v", requests)
	}
	expect(t, db.Rows("SELECT enqueued_by FROM fb_job WHERE name = 'forgebase.deliver_webhook'"), [][]string{{"job:shop.make_note"}})
}

// Rust: webhooks.rs::the_update_and_delete_events_carry_their_documents
func TestTheUpdateAndDeleteEventsCarryTheirDocuments(t *testing.T) {
	db := whReady(t)
	receiver := webhooktest.NewHTTP(t, webhooktest.Status(200))
	whAddHook(db, "all", "insert,update,delete", receiver.URL("/x"), true, true)
	binary := whBinary()
	ann, name := whNote(t, db, binary)
	path := "/api/v1/resource/note/" + name
	patched := ann.Patch(path, map[string]any{"version": 1, "data": map[string]any{"title": "two"}})
	if patched.Status != 200 {
		t.Fatalf("%d %v", patched.Status, patched.Body)
	}
	if deleted := ann.Delete(path + "?version=2"); deleted.Status != 204 {
		t.Fatalf("%d %v", deleted.Status, deleted.Body)
	}

	run := fbtest.RunJobs(binary, db)
	if run.Done != 3 {
		t.Fatalf("%+v", run)
	}
	var summary [][2]string
	for _, r := range receiver.Requests() {
		body := r.JSON(t)
		summary = append(summary, [2]string{body["event"].(string), body["data"].(map[string]any)["title"].(string)})
	}
	// The worker of RunJobs runs one job at a time in creation order.
	expect(t, summary, [][2]string{{"insert", "one"}, {"update", "two"}, {"delete", "two"}})
}

// Rust: webhooks.rs::a_huge_response_body_is_discarded
func TestAHugeResponseBodyIsDiscarded(t *testing.T) {
	db := whReady(t)
	receiver := webhooktest.NewHTTP(t, webhooktest.Large(300_000))
	whAddLocal(db, "orders", receiver.URL("/x"))
	binary := whBinary()
	whInsertNote(t, db, binary, "x")
	expect(t, counts(fbtest.RunJobs(binary, db)), [3]int{1, 0, 0})
}

// ---- retries --------------------------------------------------------------------------------

// Rust: webhooks.rs::the_delivery_id_is_the_same_on_a_retry
func TestTheDeliveryIDIsTheSameOnARetry(t *testing.T) {
	db := whReady(t)
	receiver := webhooktest.NewHTTP(t, webhooktest.Sequence(webhooktest.Status(500), webhooktest.Status(200)))
	whAddLocal(db, "orders", receiver.URL("/hook-path?token=TOPSECRET"))
	binary := whBinary()
	whInsertNote(t, db, binary, "again")

	expect(t, counts(fbtest.RunJobs(binary, db)), [3]int{0, 1, 0})
	job := whJob(t, db)
	if job[0] != "queued" || job[1] != "1" || !strings.Contains(job[2], "HTTP 500") {
		t.Errorf("%v", job)
	}
	whAssertClean(t, job[2])

	whMakeDue(db)
	expect(t, counts(fbtest.RunJobs(binary, db)), [3]int{1, 0, 0})

	requests := receiver.Requests()
	if len(requests) != 2 {
		t.Fatalf("%d requests", len(requests))
	}
	if requests[0].Header("x-forgebase-delivery") == "" || requests[0].Header("x-forgebase-delivery") != requests[1].Header("x-forgebase-delivery") {
		t.Errorf("the same delivery id on a retry: %q %q", requests[0].Header("x-forgebase-delivery"), requests[1].Header("x-forgebase-delivery"))
	}
	if requests[0].JSON(t)["delivery"] != requests[1].JSON(t)["delivery"] {
		t.Error("the body carries the same delivery id")
	}
	// Each attempt is signed afresh, with its own timestamp.
	for _, r := range requests {
		timestamp := r.Header("x-forgebase-timestamp")
		if r.Header("x-forgebase-signature") != webhooktest.ExpectedSignature(whSecret, timestamp, r.Body) {
			t.Errorf("signature of %s", timestamp)
		}
	}
}

// Rust: webhooks.rs::a_receiver_that_keeps_failing_ends_dead_after_five_attempts
func TestAReceiverThatKeepsFailingEndsDeadAfterFiveAttempts(t *testing.T) {
	db := whReady(t)
	receiver := webhooktest.NewHTTP(t, webhooktest.Status(500))
	whAddLocal(db, "orders", receiver.URL("/hook-path?token=TOPSECRET"))
	binary := whBinary()
	whInsertNote(t, db, binary, "x")
	for attempt := 1; attempt <= 5; attempt++ {
		whMakeDue(db)
		run := fbtest.RunJobs(binary, db)
		want := [3]int{0, 1, 0}
		if attempt == 5 {
			want = [3]int{0, 0, 1}
		}
		if counts(run) != want {
			t.Fatalf("attempt %d: %v", attempt, counts(run))
		}
	}
	job := whJob(t, db)
	if job[0] != "dead" || job[1] != "5" || !strings.Contains(job[2], "HTTP 500") {
		t.Errorf("%v", job)
	}
	whAssertClean(t, job[2])
	if n := len(receiver.Requests()); n != 5 {
		t.Errorf("%d requests", n)
	}
}

// Rust: webhooks.rs::a_receiver_that_never_answers_is_a_retryable_timeout
func TestAReceiverThatNeverAnswersIsARetryableTimeout(t *testing.T) {
	db := whReady(t)
	receiver := webhooktest.NewHTTP(t, webhooktest.Hang())
	whAddLocal(db, "orders", receiver.URL("/hook-path"))
	binary := whBinary()
	whInsertNote(t, db, binary, "x")
	started := time.Now()
	expect(t, counts(fbtest.RunJobs(binary, db)), [3]int{0, 1, 0})
	if elapsed := time.Since(started); elapsed > 15*time.Second {
		t.Errorf("%v", elapsed)
	}
	job := whJob(t, db)
	if job[0] != "queued" || !strings.Contains(job[2], "did not answer within 3 s") {
		t.Errorf("%v", job)
	}
	whAssertClean(t, job[2])
	if n := len(receiver.Requests()); n != 1 {
		t.Errorf("the request arrived; no answer came: %d", n)
	}
}

// Rust: webhooks.rs::a_closed_port_is_a_retryable_connection_error
func TestAClosedPortIsARetryableConnectionError(t *testing.T) {
	db := whReady(t)
	// A port that was free a moment ago and has no listener.
	closed := webhooktest.NewHTTP(t, webhooktest.Status(200))
	port := closed.Port()
	closed.Close()
	whAddLocal(db, "orders", fmt.Sprintf("http://127.0.0.1:%d/hook-path", port))
	binary := whBinary()
	whInsertNote(t, db, binary, "x")
	expect(t, counts(fbtest.RunJobs(binary, db)), [3]int{0, 1, 0})
	job := whJob(t, db)
	if !strings.Contains(job[2], "cannot connect") {
		t.Errorf("%v", job)
	}
	whAssertClean(t, job[2])
}

// ---- permanent failures ---------------------------------------------------------------------

// Rust: webhooks.rs::a_redirect_is_permanent_and_is_not_followed
func TestARedirectIsPermanentAndIsNotFollowed(t *testing.T) {
	db := whReady(t)
	target := webhooktest.NewHTTP(t, webhooktest.Status(200))
	receiver := webhooktest.NewHTTP(t, webhooktest.Redirect(302, target.URL("/elsewhere")))
	whAddLocal(db, "orders", receiver.URL("/hook-path"))
	binary := whBinary()
	whInsertNote(t, db, binary, "x")
	expect(t, counts(fbtest.RunJobs(binary, db)), [3]int{0, 0, 1})
	job := whJob(t, db)
	if job[0] != "dead" || job[1] != "1" || !strings.Contains(job[2], "redirects are not followed") || !strings.Contains(job[2], "302") {
		t.Errorf("%v", job)
	}
	if n := len(receiver.Requests()); n != 1 {
		t.Errorf("%d requests", n)
	}
	if n := len(target.Requests()); n != 0 {
		t.Errorf("the redirect was followed: %d", n)
	}
}

// Rust: webhooks.rs::addresses_that_are_not_allowed_are_refused_for_good_and_nothing_is_sent
func TestAddressesThatAreNotAllowedAreRefusedForGoodAndNothingIsSent(t *testing.T) {
	db := whReady(t)
	receiver := webhooktest.NewHTTP(t, webhooktest.Status(200))
	port := receiver.Port()
	// insecure_http is on only to use http://, and allow_private is off, which is the point.
	for _, c := range []struct{ what, url string }{
		{"loopback", fmt.Sprintf("http://127.0.0.1:%d/hook-path?token=TOPSECRET", port)},
		{"a name that resolves to loopback", fmt.Sprintf("http://localhost:%d/hook-path", port)},
		{"private", "http://10.1.2.3/hook-path"},
		{"private 192.168", "http://192.168.0.10:8080/hook-path"},
		{"link-local", "http://169.254.7.7/hook-path"},
		{"metadata", "http://169.254.169.254/latest/meta-data/hook-path"},
		{"azure wireserver", "http://168.63.129.16/hook-path"},
		{"ipv6 loopback", fmt.Sprintf("http://[::1]:%d/hook-path", port)},
		{"ipv4-mapped ipv6", fmt.Sprintf("http://[::ffff:127.0.0.1]:%d/hook-path", port)},
	} {
		db.Exec("DELETE FROM fb_job; DELETE FROM fb_webhook")
		whAddHook(db, "w", "insert", c.url, false, true)
		queueJob(db, "forgebase.deliver_webhook",
			`{"webhook": "w", "event": "insert", "doctype": "note", "name": "n", "data": {}, "occurred_at": "2026-01-01T00:00:00Z"}`)
		expect(t, counts(fbtest.RunJobs(whBinary(), db)), [3]int{0, 0, 1})
		job := whJob(t, db)
		if job[0] != "dead" || job[1] != "1" || !strings.Contains(job[2], "--allow-private") {
			t.Errorf("%s: %v", c.what, job)
		}
		whAssertClean(t, job[2])
	}
	if n := len(receiver.Requests()); n != 0 {
		t.Errorf("nothing reached the receiver: %v", receiver.Requests())
	}
}

// Rust: webhooks.rs::http_without_the_insecure_flag_is_refused_at_send_time
func TestHTTPWithoutTheInsecureFlagIsRefusedAtSendTime(t *testing.T) {
	db := whReady(t)
	receiver := webhooktest.NewHTTP(t, webhooktest.Status(200))
	// A row written around the CLI: allow_private on, insecure_http off.
	whAddHook(db, "orders", "insert", receiver.URL("/hook-path"), true, false)
	binary := whBinary()
	whInsertNote(t, db, binary, "x")
	expect(t, counts(fbtest.RunJobs(binary, db)), [3]int{0, 0, 1})
	job := whJob(t, db)
	if !strings.Contains(job[2], "--insecure-http") {
		t.Errorf("%v", job)
	}
	whAssertClean(t, job[2])
	if n := len(receiver.Requests()); n != 0 {
		t.Errorf("%d requests", n)
	}
}

// Rust: webhooks.rs::a_url_with_user_info_is_refused_at_send_time
func TestAURLWithUserInfoIsRefusedAtSendTime(t *testing.T) {
	db := whReady(t)
	receiver := webhooktest.NewHTTP(t, webhooktest.Status(200))
	whAddHook(db, "orders", "insert", fmt.Sprintf("http://user:pw@127.0.0.1:%d/hook-path", receiver.Port()), true, true)
	binary := whBinary()
	whInsertNote(t, db, binary, "x")
	expect(t, counts(fbtest.RunJobs(binary, db)), [3]int{0, 0, 1})
	job := whJob(t, db)
	if !strings.Contains(job[2], "user info") || strings.Contains(job[2], "pw@") {
		t.Errorf("%v", job)
	}
	if n := len(receiver.Requests()); n != 0 {
		t.Errorf("%d requests", n)
	}
}

// A job payload that is not a delivery, and a webhook with a URL that cannot be parsed, are permanent failures
// (a retry cannot fix them).
func TestADeliveryThatCannotBeUnderstoodIsDeadAtOnce(t *testing.T) {
	db := whReady(t)
	whAddHook(db, "bad_url", "insert", "not a url", true, true)
	for _, payload := range []string{`{}`, `{"webhook": 5, "event": "insert"}`, `{"webhook": "w"}`} {
		queueJob(db, "forgebase.deliver_webhook", payload)
	}
	queueJob(db, "forgebase.deliver_webhook", `{"webhook": "bad_url", "event": "insert", "doctype": "note", "name": "n", "data": {}, "occurred_at": "x"}`)
	expect(t, counts(fbtest.RunJobs(whBinary(), db)), [3]int{0, 0, 4})
	errors := db.Rows("SELECT last_error FROM fb_job ORDER BY last_error COLLATE \"C\"")
	expect(t, errors, [][]string{
		{"the URL is not a valid absolute URL"},
		{"the job payload is not a webhook delivery"},
		{"the job payload is not a webhook delivery"},
		{"the job payload is not a webhook delivery"},
	})
}

// A snapshot too big for a job payload is delivered as "truncated": true with "data": null (ADR 0020).
func TestATruncatedSnapshotIsDeliveredWithoutItsDataAndMarked(t *testing.T) {
	db := whReady(t)
	receiver := webhooktest.NewHTTP(t, webhooktest.Status(200))
	whAddLocal(db, "orders", receiver.URL("/x"))
	queueJob(db, "forgebase.deliver_webhook",
		`{"webhook": "orders", "event": "insert", "doctype": "note", "name": "n", "data": null, "truncated": true, "occurred_at": "2026-01-01T00:00:00Z"}`)
	expect(t, counts(fbtest.RunJobs(whBinary(), db)), [3]int{1, 0, 0})
	body := receiver.Requests()[0].JSON(t)
	if body["truncated"] != true || body["data"] != nil || body["name"] != "n" {
		t.Errorf("%v", body)
	}
}

// ---- removed, disabled ----------------------------------------------------------------------

// Rust: webhooks.rs::a_webhook_removed_or_disabled_before_delivery_sends_nothing_and_completes
func TestAWebhookRemovedOrDisabledBeforeDeliverySendsNothingAndCompletes(t *testing.T) {
	for _, how := range []string{"DELETE FROM fb_webhook", "UPDATE fb_webhook SET enabled = false"} {
		db := whReady(t)
		receiver := webhooktest.NewHTTP(t, webhooktest.Status(200))
		whAddLocal(db, "orders", receiver.URL("/hook-path"))
		binary := whBinary()
		whInsertNote(t, db, binary, "x")
		if whJob(t, db)[0] != "queued" {
			t.Fatal("queued")
		}
		db.Exec(how)
		expect(t, counts(fbtest.RunJobs(binary, db)), [3]int{1, 0, 0})
		if whJob(t, db)[0] != "done" {
			t.Errorf("%s: %v", how, whJob(t, db))
		}
		if n := len(receiver.Requests()); n != 0 {
			t.Errorf("%s: %d requests", how, n)
		}
	}
}

// ---- TLS ------------------------------------------------------------------------------------

// Rust: webhooks.rs::a_delivery_over_https_verifies_the_certificate_of_the_test_ca
func TestADeliveryOverHTTPSVerifiesTheCertificateOfTheTestCA(t *testing.T) {
	pki := whCA(t)
	db := whReady(t)
	receiver := webhooktest.NewHTTPS(t, webhooktest.Status(200), pki)
	// By IP address and by name: the certificate has both, and the name is used for the SNI and the verification
	// while the connection goes to the vetted address.
	whAddHook(db, "by_ip", "insert", receiver.URL("/hook-path"), true, false)
	whAddHook(db, "by_name", "insert", receiver.LocalhostURL("/hook-path"), true, false)
	binary := whBinary()
	whInsertNote(t, db, binary, "secure")
	expect(t, counts(fbtest.RunJobs(binary, db)), [3]int{2, 0, 0})
	requests := receiver.Requests()
	if len(requests) != 2 {
		t.Fatalf("%d requests", len(requests))
	}
	for _, r := range requests {
		timestamp := r.Header("x-forgebase-timestamp")
		if r.Header("x-forgebase-signature") != webhooktest.ExpectedSignature(whSecret, timestamp, r.Body) {
			t.Errorf("signature")
		}
		if r.JSON(t)["data"].(map[string]any)["title"] != "secure" {
			t.Errorf("%v", r.JSON(t))
		}
	}
}

// Rust: webhooks.rs::a_certificate_of_another_authority_is_a_retryable_tls_error
func TestACertificateOfAnotherAuthorityIsARetryableTLSError(t *testing.T) {
	whCA(t)
	rogue := webhooktest.NewPKI(t, "rogue")
	db := whReady(t)
	receiver := webhooktest.NewHTTPS(t, webhooktest.Status(200), rogue)
	whAddHook(db, "orders", "insert", receiver.URL("/hook-path"), true, false)
	binary := whBinary()
	whInsertNote(t, db, binary, "x")
	expect(t, counts(fbtest.RunJobs(binary, db)), [3]int{0, 1, 0})
	job := whJob(t, db)
	if !strings.Contains(job[2], "TLS") {
		t.Errorf("%v", job)
	}
	whAssertClean(t, job[2])
	if n := len(receiver.Requests()); n != 0 {
		t.Errorf("no request was sent over the failed handshake: %d", n)
	}
}

// ---- sites ----------------------------------------------------------------------------------

// Rust: webhooks.rs::a_webhook_of_one_site_does_not_fire_for_another
func TestAWebhookOfOneSiteDoesNotFireForAnother(t *testing.T) {
	// A site is a database: the webhook table of site A is not in site B.
	siteA, siteB := whReady(t), whReady(t)
	receiver := webhooktest.NewHTTP(t, webhooktest.Status(200))
	whAddLocal(siteA, "orders", receiver.URL("/x"))
	binary := whBinary()

	// A write on site B enqueues nothing and sends nothing.
	whInsertNote(t, siteB, binary, "on b")
	expect(t, siteB.Rows("SELECT 1 FROM fb_job"), [][]string{})
	if run := fbtest.RunJobs(binary, siteB); run.Done != 0 {
		t.Errorf("%+v", run)
	}
	if n := len(receiver.Requests()); n != 0 {
		t.Errorf("%d requests", n)
	}

	// The same write on site A does.
	whInsertNote(t, siteA, binary, "on a")
	if run := fbtest.RunJobs(binary, siteA); run.Done != 1 {
		t.Errorf("%+v", run)
	}
	requests := receiver.Requests()
	if len(requests) != 1 || requests[0].JSON(t)["data"].(map[string]any)["title"] != "on a" {
		t.Errorf("%v", requests)
	}
}
