package app

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/tvstore/forgebase-go/internal/db"
	"github.com/tvstore/forgebase-go/internal/webhooktest"
)

// The webhook commands (M9.2, ADR 0020), in process against a real PostgreSQL. Ported from the webhooks section
// of crates/forgebase/tests/cli.rs and the #[cfg(test)] module of crates/forgebase/src/webhook_cmd.rs.

// printedSecret is the secret an add or rotate-secret printed (the 64 hex characters after "secret: " or ": ").
func printedSecret(t *testing.T, text string) string {
	t.Helper()
	secret := ""
	for _, line := range strings.Split(text, "\n") {
		if rest, ok := strings.CutPrefix(line, "secret: "); ok {
			secret = rest
			break
		}
		if _, after, ok := strings.Cut(line, "webhook "); ok {
			if _, s, ok := strings.Cut(after, ": "); ok {
				secret = s
				break
			}
		}
	}
	if len(secret) != 64 || strings.Trim(secret, "0123456789abcdef") != "" {
		t.Fatalf("no secret printed in: %s", text)
	}
	return secret
}

func webhookEnv(t *testing.T) *env {
	t.Helper()
	e := newEnv(t)
	e.migrate()
	return e
}

func (e *env) addWebhook(name, url string, flags ...string) result {
	args := []string{"webhook", "add", name, "--doctype", "customer", "--events", "insert,update", "--url", url, "--doctypes", doctypes}
	return e.run(append(args, flags...)...)
}

func fields(line string) []string { return strings.Fields(line) }

func equalStrings(t *testing.T, got, want []string, label string) {
	t.Helper()
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("%s: %q, want %q", label, got, want)
	}
}

// Rust: cli.rs::webhooks_are_added_listed_disabled_enabled_and_removed
func TestWebhooksAreAddedListedDisabledEnabledAndRemoved(t *testing.T) {
	e := webhookEnv(t)
	added := e.addWebhook("orders", "https://hooks.example.com:8443/services/T0/B0/TOPSECRET?token=TOPSECRET")
	expectCode(t, added, 0, "add")
	if !strings.HasPrefix(added.stdout, "added webhook orders\n") || added.stderr != "" {
		t.Errorf("%+v", added)
	}
	secret := printedSecret(t, added.stdout)
	// Printed once, and it is what the database holds.
	if n := strings.Count(added.stdout, secret); n != 1 {
		t.Errorf("printed %d times: %s", n, added.stdout)
	}
	if got := e.scalar("SELECT secret FROM fb_webhook WHERE name = 'orders'"); got != secret {
		t.Errorf("%s", got)
	}
	if !strings.HasSuffix(added.stdout, "store the secret now: it cannot be shown again\n") {
		t.Errorf("%s", added.stdout)
	}

	// list: the host only, the flags, never the secret or the path.
	listed := e.run("webhook", "list")
	expectCode(t, listed, 0, "list")
	lines := strings.Split(strings.TrimSuffix(listed.stdout, "\n"), "\n")
	equalStrings(t, fields(lines[0]), []string{"NAME", "DOCTYPE", "EVENTS", "URL", "ENABLED", "ALLOW_PRIVATE", "INSECURE_HTTP"}, "header")
	equalStrings(t, fields(lines[1]), []string{"orders", "customer", "insert,update", "https://hooks.example.com:8443", "yes", "no", "no"}, "row")
	for _, forbidden := range []string{secret, "TOPSECRET", "services", "token"} {
		if strings.Contains(listed.stdout, forbidden) || strings.Contains(listed.stderr, forbidden) {
			t.Errorf("%s in %s", forbidden, listed.stdout)
		}
	}
	// The columns are aligned and the lines have no trailing spaces.
	for _, line := range lines {
		if line != strings.TrimRight(line, " ") {
			t.Errorf("trailing space: %q", line)
		}
	}

	// Disable, enable, remove.
	expectCode(t, e.run("webhook", "disable", "orders"), 0, "disable")
	row := fields(strings.Split(e.run("webhook", "list").stdout, "\n")[1])
	if row[4] != "no" {
		t.Errorf("disabled: %v", row)
	}
	if got := e.scalar("SELECT enabled::text FROM fb_webhook"); got != "false" {
		t.Error(got)
	}
	if r := e.run("webhook", "disable", "orders"); r.stdout != "disabled webhook orders\n" {
		t.Errorf("%+v", r)
	}
	if r := e.run("webhook", "enable", "orders"); r.code != 0 || r.stdout != "enabled webhook orders\n" {
		t.Errorf("%+v", r)
	}
	if got := e.scalar("SELECT enabled::text FROM fb_webhook"); got != "true" {
		t.Error(got)
	}
	if r := e.run("webhook", "remove", "orders"); r.code != 0 || r.stdout != "removed webhook orders\n" {
		t.Errorf("%+v", r)
	}
	if n := len(strings.Split(strings.TrimSuffix(e.run("webhook", "list").stdout, "\n"), "\n")); n != 1 {
		t.Errorf("only the header: %d lines", n)
	}

	// A webhook that is not there.
	for _, command := range []string{"disable", "enable", "remove", "rotate-secret", "test"} {
		missing := e.run("webhook", command, "orders")
		expectCode(t, missing, 1, command)
		if missing.stderr != "error: no webhook named `orders`\n" {
			t.Errorf("%s: %q", command, missing.stderr)
		}
	}

	// The same name twice is refused.
	expectCode(t, e.addWebhook("dup", "https://example.com/x"), 0, "first")
	again := e.addWebhook("dup", "https://example.com/y")
	expectCode(t, again, 1, "again")
	expectContains(t, again.stderr, "exists already")
}

// Rust: cli.rs::a_webhook_that_is_not_valid_is_refused_and_nothing_is_stored
func TestAWebhookThatIsNotValidIsRefusedAndNothingIsStored(t *testing.T) {
	e := webhookEnv(t)
	const url = "https://example.com/hook-path"
	with := func(name, doctype, events, url string, flags ...string) result {
		args := []string{"webhook", "add", name, "--doctype", doctype, "--events", events, "--url", url, "--doctypes", doctypes}
		return e.run(append(args, flags...)...)
	}
	for _, c := range []struct {
		out      result
		expected string
	}{
		{with("w", "nobody", "insert", url), "unknown DocType `nobody`"},
		// A child DocType has no documents of its own.
		{with("w", "sales_order_item", "insert", url), "child DocType"},
		{with("w", "customer", "insert", "http://example.com/hook-path"), "--insecure-http"},
		{with("w", "customer", "insert", "https://user:pw@example.com/hook-path"), "user info"},
		{with("w", "customer", "insert", "https://example.com@evil.test/hook-path"), "user info"},
		{with("w", "customer", "insert", "ftp://example.com/hook-path"), "https"},
		{with("w", "customer", "insert", "example.com/hook-path"), "absolute URL"},
		{with("w", "customer", "insert", "not a url"), "valid absolute URL"},
		{with("w", "customer", "", url), "event"},
		{with("w", "customer", "insert,save", url), "unknown event `save`"},
		{with("Bad Name", "customer", "insert", url), "must match"},
	} {
		if c.out.code != 1 || c.out.stdout != "" {
			t.Errorf("%s: %+v", c.expected, c.out)
			continue
		}
		if !strings.Contains(c.out.stderr, c.expected) {
			t.Errorf("%s: %s", c.expected, c.out.stderr)
		}
		// The URL is never repeated: it may hold credentials or a token.
		for _, forbidden := range []string{"hook-path", "pw@", "evil.test"} {
			if strings.Contains(c.out.stderr, forbidden) {
				t.Errorf("%s in %s", forbidden, c.out.stderr)
			}
		}
	}
	if n := e.scalar("SELECT count(*)::text FROM fb_webhook"); n != "0" {
		t.Errorf("%s webhooks stored", n)
	}

	// http:// is allowed with the explicit development flag, and the list shows it.
	dev := with("dev", "customer", "insert", "http://localhost:8080/hook", "--insecure-http")
	expectCode(t, dev, 0, "dev")
	expectContains(t, dev.stdout, "warning: --insecure-http")
	table := strings.Split(e.run("webhook", "list").stdout, "\n")
	equalStrings(t, fields(table[1]), []string{"dev", "customer", "insert", "http://localhost:8080", "yes", "no", "yes"}, "row")
	// --allow-private warns too.
	private := with("lan", "customer", "insert", "https://10.0.0.5/hook", "--allow-private")
	expectCode(t, private, 0, "lan")
	expectContains(t, private.stdout, "warning: --allow-private")
}

// Rust: cli.rs::a_secret_from_standard_input_is_used_and_not_printed_and_rotation_prints_once
func TestASecretFromStandardInputIsUsedAndNotPrintedAndRotationPrintsOnce(t *testing.T) {
	e := webhookEnv(t)
	add := func(name, stdin string) result {
		return e.runStdin(stdin, "webhook", "add", name, "--doctype", "customer", "--events", "insert",
			"--url", "https://example.com/x", "--doctypes", doctypes, "--secret-stdin")
	}
	added := add("w", "my own secret value\n")
	expectCode(t, added, 0, "add")
	if strings.Contains(added.stdout, "my own secret value") || !strings.Contains(added.stdout, "the secret is the one read from standard input") {
		t.Errorf("%s", added.stdout)
	}
	if got := e.scalar("SELECT secret FROM fb_webhook"); got != "my own secret value" {
		t.Error(got)
	}
	// An empty secret is refused, and so is no standard input at all.
	for _, stdin := range []string{"\n", ""} {
		empty := add("w2", stdin)
		expectCode(t, empty, 1, "empty")
		expectContains(t, empty.stderr, "the secret is empty")
	}

	// A secret of less than 16 bytes is refused, by add and by rotate-secret, and changes nothing.
	short := add("w3", "fifteen-bytes!!\n")
	expectCode(t, short, 1, "short")
	expectContains(t, short.stderr, "at least 16 bytes")
	if n := e.scalar("SELECT count(*)::text FROM fb_webhook"); n != "1" {
		t.Error(n)
	}
	short = e.runStdin("too short\n", "webhook", "rotate-secret", "w", "--secret-stdin")
	expectCode(t, short, 1, "rotate short")
	expectContains(t, short.stderr, "at least 16 bytes")
	if got := e.scalar("SELECT secret FROM fb_webhook"); got != "my own secret value" {
		t.Error(got)
	}
	sixteen := e.runStdin("exactly-16-bytes\n", "webhook", "rotate-secret", "w", "--secret-stdin")
	expectCode(t, sixteen, 0, "rotate 16")
	if sixteen.stdout != "changed the secret of webhook w to the one read from standard input\n" {
		t.Errorf("%s", sixteen.stdout)
	}
	if got := e.scalar("SELECT secret FROM fb_webhook"); got != "exactly-16-bytes" {
		t.Error(got)
	}

	rotated := e.run("webhook", "rotate-secret", "w")
	expectCode(t, rotated, 0, "rotate")
	fresh := printedSecret(t, rotated.stdout)
	if strings.Count(rotated.stdout, fresh) != 1 {
		t.Errorf("%s", rotated.stdout)
	}
	if got := e.scalar("SELECT secret FROM fb_webhook"); got != fresh {
		t.Error(got)
	}
	if strings.Contains(rotated.stdout, "exactly-16-bytes") {
		t.Errorf("%s", rotated.stdout)
	}
	if !strings.HasPrefix(rotated.stdout, "new secret of webhook w: ") || !strings.HasSuffix(rotated.stdout, "\nstore the secret now: it cannot be shown again\n") {
		t.Errorf("%s", rotated.stdout)
	}
}

// Rust: cli.rs::webhook_test_sends_one_signed_delivery_and_exits_by_the_answer
func TestWebhookTestSendsOneSignedDeliveryAndExitsByTheAnswer(t *testing.T) {
	e := webhookEnv(t)
	receiver := webhooktest.NewHTTP(t, webhooktest.Status(200))
	url := receiver.URL("/hook-path?token=TOPSECRET")
	added := e.addWebhook("w", url, "--allow-private", "--insecure-http")
	expectCode(t, added, 0, "add")
	secret := printedSecret(t, added.stdout)

	ok := e.run("webhook", "test", "w")
	expectCode(t, ok, 0, "test")
	expectContains(t, ok.stdout, "HTTP 200", receiver.URL(""))
	if !strings.HasPrefix(ok.stdout, "webhook w: http://127.0.0.1:") || !strings.Contains(ok.stdout, " (delivery test-") {
		t.Errorf("%s", ok.stdout)
	}
	for _, forbidden := range []string{secret, "TOPSECRET", "hook-path"} {
		if strings.Contains(ok.stdout, forbidden) || strings.Contains(ok.stderr, forbidden) {
			t.Errorf("%s", forbidden)
		}
	}
	requests := receiver.Requests()
	if len(requests) != 1 {
		t.Fatalf("%d requests", len(requests))
	}
	request := requests[0]
	if request.Header("x-forgebase-event") != "test" {
		t.Errorf("%s", request.Header("x-forgebase-event"))
	}
	delivery := request.Header("x-forgebase-delivery")
	if !strings.HasPrefix(delivery, "test-") {
		t.Errorf("%s", delivery)
	}
	timestamp := request.Header("x-forgebase-timestamp")
	if request.Header("x-forgebase-signature") != webhooktest.ExpectedSignature(secret, timestamp, request.Body) {
		t.Error("the signature")
	}
	body := request.JSON(t)
	if body["event"] != "test" || body["doctype"] != "customer" || body["delivery"] != delivery || body["data"] != nil || body["name"] != nil {
		t.Errorf("%v", body)
	}
	if _, err := time.Parse(time.RFC3339Nano, body["occurred_at"].(string)); err != nil {
		t.Error(err)
	}

	// A disabled webhook can be tested.
	expectCode(t, e.run("webhook", "disable", "w"), 0, "disable")
	expectCode(t, e.run("webhook", "test", "w"), 0, "disabled test")

	// Not a 2xx answer: exit 1, the status on standard output.
	for _, c := range []struct {
		reply  webhooktest.Reply
		status string
	}{
		{webhooktest.Status(500), "500"},
		{webhooktest.Status(404), "404"},
		{webhooktest.Redirect(302, receiver.URL("/other")), "302"},
	} {
		receiver.SetReply(c.reply)
		failed := e.run("webhook", "test", "w")
		expectCode(t, failed, 1, c.status)
		expectContains(t, failed.stdout, "HTTP "+c.status)
		expectContains(t, failed.stderr, "not accepted")
		for _, forbidden := range []string{secret, "TOPSECRET", "hook-path"} {
			if strings.Contains(failed.stdout, forbidden) || strings.Contains(failed.stderr, forbidden) {
				t.Errorf("%s", forbidden)
			}
		}
		if c.status == "302" {
			expectContains(t, failed.stderr, "redirects are not followed")
		}
	}
}

// Rust: cli.rs::webhook_test_refuses_a_private_address_unless_the_webhook_allows_it
func TestWebhookTestRefusesAPrivateAddressUnlessTheWebhookAllowsIt(t *testing.T) {
	e := webhookEnv(t)
	receiver := webhooktest.NewHTTP(t, webhooktest.Status(200))
	url := receiver.URL("/hook-path?token=TOPSECRET")
	// Plain http is allowed by the flag; loopback is not, since --allow-private is missing.
	added := e.addWebhook("w", url, "--insecure-http")
	expectCode(t, added, 0, "add")
	secret := printedSecret(t, added.stdout)
	refused := e.run("webhook", "test", "w")
	expectCode(t, refused, 1, "test")
	expectContains(t, refused.stderr, "--allow-private", "the test delivery failed")
	for _, forbidden := range []string{secret, "TOPSECRET", "hook-path"} {
		if strings.Contains(refused.stderr, forbidden) || strings.Contains(refused.stdout, forbidden) {
			t.Errorf("%s", forbidden)
		}
	}
	if n := len(receiver.Requests()); n != 0 {
		t.Errorf("nothing was sent: %d", n)
	}
}

// Rust: cli.rs::a_document_written_by_the_cli_enqueues_the_delivery
func TestADocumentWrittenByTheCLIEnqueuesTheDelivery(t *testing.T) {
	e := webhookEnv(t)
	expectCode(t, e.addWebhook("w", "https://example.com/x"), 0, "add")
	inserted := e.doc("insert", "customer", "--data", `{"email":"a@example.com","full_name":"A"}`)
	expectCode(t, inserted, 0, "insert")
	got := e.scalar("SELECT name || '/' || (payload->>'event') || '/' || (payload->'data'->>'email') || '/' || enqueued_by FROM fb_job")
	if got != "forgebase.deliver_webhook/insert/a@example.com/cli" {
		t.Errorf("%s", got)
	}
	// The job is visible to `job list`, and its payload (the document, not the secret) to `job show`.
	listed := e.run("job", "list")
	expectContains(t, listed.stdout, "forgebase.deliver_webhook")
	id := e.scalar("SELECT id::text FROM fb_job")
	shown := e.run("job", "show", id)
	expectCode(t, shown, 0, "show")
	if strings.Contains(shown.stdout, e.scalar("SELECT secret FROM fb_webhook")) {
		t.Errorf("the secret is in the job: %s", shown.stdout)
	}
	var job struct {
		Payload struct{ Webhook, Event string }
	}
	if err := json.Unmarshal([]byte(shown.stdout), &job); err != nil || job.Payload.Webhook != "w" || job.Payload.Event != "insert" {
		t.Errorf("%v: %s", err, shown.stdout)
	}
}

// Rust: webhook_cmd.rs::the_list_shows_the_host_only_and_no_secret (unit test)
func TestTheWebhookListShowsTheHostOnlyAndNoSecret(t *testing.T) {
	text := webhookTable([]db.WebhookInfo{{
		Name: "orders", DocType: "sales_order", Events: []string{"insert", "submit"},
		URL: "https://hooks.example.com:8443/services/T0/B0/TOKEN?k=v", Enabled: true,
	}})
	expectContains(t, text, "https://hooks.example.com:8443", "insert,submit")
	for _, forbidden := range []string{"TOKEN", "services"} {
		if strings.Contains(text, forbidden) {
			t.Errorf("%s in %s", forbidden, text)
		}
	}
	// A URL that cannot be read is shown as (invalid), never as it is.
	text = webhookTable([]db.WebhookInfo{{Name: "w", DocType: "d", Events: []string{"insert"}, URL: "not a url TOKEN"}})
	if !strings.Contains(text, "(invalid)") || strings.Contains(text, "TOKEN") {
		t.Errorf("%s", text)
	}
}

// Rust: webhook_cmd.rs::a_bad_url_error_does_not_repeat_the_url (unit test)
func TestABadURLErrorDoesNotRepeatTheURL(t *testing.T) {
	_, err := parseWebhookURL("https://user:pw@example.com/TOKEN", false)
	if err == nil || strings.Contains(err.Error(), "TOKEN") || strings.Contains(err.Error(), "pw@") {
		t.Errorf("%v", err)
	}
}

// Rust: sites.rs::a_webhook_of_one_site_enqueues_nothing_on_the_other
func TestAWebhookOfOneSiteEnqueuesNothingOnTheOther(t *testing.T) {
	// M9.2: a webhook is a row of the site's own database, so it belongs to that site alone.
	p := newPair(t)
	for _, site := range []string{"a", "b"} {
		expectCode(t, p.run("migrate", "--site", site, "--doctypes", doctypes), 0, "migrate "+site)
	}
	added := p.run("webhook", "add", "orders", "--doctype", "customer", "--events", "insert", "--url", "https://example.com/x",
		"--site", "a", "--doctypes", doctypes)
	expectCode(t, added, 0, "add")
	// The other site has no such webhook.
	listedB := p.run("webhook", "list", "--site", "b")
	expectCode(t, listedB, 0, "list b")
	if n := len(strings.Split(strings.TrimSuffix(listedB.stdout, "\n"), "\n")); n != 1 {
		t.Errorf("only the header: %s", listedB.stdout)
	}
	if !strings.Contains(p.run("webhook", "list", "--site", "a").stdout, "orders") {
		t.Error("site a lacks the webhook")
	}
	// Without --site the command is ambiguous with two sites.
	expectCode(t, p.run("webhook", "list"), 2, "no site")

	for _, site := range []string{"a", "b"} {
		inserted := p.run("doc", "insert", "customer", "--site", site, "--doctypes", doctypes, "--data", `{"email":"x@example.com","full_name":"X"}`)
		expectCode(t, inserted, 0, "insert on "+site)
	}
	const jobs = "SELECT count(*)::text FROM fb_job WHERE name = 'forgebase.deliver_webhook'"
	if got := scalar(t, p.a, jobs); got != "1" {
		t.Errorf("site a: %s", got)
	}
	if got := scalar(t, p.b, jobs); got != "0" {
		t.Errorf("site b: %s", got)
	}
}
