package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/tvstore/forgebase-go/internal/db"
)

// The webhook commands (M9.2, ADR 0020; the Rust module webhook_cmd.rs): add, list, disable, enable,
// remove, rotate-secret and test. Operators use the command line only; there is no desk screen for webhooks.
//
// Secrets: add and rotate-secret print the secret, once. Nothing else prints it: not list, not an error, not
// the log of the worker.

// webhook runs a webhook command on site. add checks the DocType against the --doctypes directory too.
func (r *runner) webhook(ctx context.Context, site *site) (err error) {
	verb := r.m.path[1]
	local := ""
	if verb == "add" {
		local = r.m.str("doctypes")
	}
	composed, err := r.siteComposed(site, local)
	if err != nil {
		return err
	}
	// The secret of add or rotate-secret --secret-stdin is read from standard input before the database is
	// opened, so a bad input never touches it. nil: generate one (and for the other commands).
	var secret *string
	if (verb == "add" || verb == "rotate-secret") && r.m.flag("secret_stdin") {
		text, err := r.readPassword()
		if err != nil {
			return err
		}
		secret = &text
	}
	insecure := r.m.flag("insecure_http")
	// The site is opened (the pool, with the registry loaded) before anything of add that needs no database is
	// checked, as the Rust command does: an unreachable database is reported before a bad URL.
	sc, err := site.open(ctx, r.o.Getenv, composed.registry, 0)
	if err != nil {
		return err
	}
	defer sc.close()
	if verb == "add" {
		// Everything of add that needs no database: the URL. The error never contains the URL.
		if _, err := parseWebhookURL(r.m.str("url"), insecure); err != nil {
			return errf("invalid URL: %s", err)
		}
	}
	name := r.m.str("name")
	// A refusal of the webhook functions prints its message as it is (the Rust CliError::Webhook).
	defer func() { err = webhookMessage(err) }()
	switch verb {
	case "add":
		new := db.NewWebhook{
			Name:         name,
			DocType:      r.m.str("doctype"),
			Events:       r.m.list("events"),
			URL:          r.m.str("url"),
			AllowPrivate: r.m.flag("allow_private"),
			InsecureHTTP: insecure,
		}
		given := secret != nil
		text, err := db.AddWebhook(ctx, sc.pool, composed.registry, new, secret)
		if err != nil {
			return err
		}
		var out strings.Builder
		fmt.Fprintf(&out, "added webhook %s\n", name)
		if insecure {
			out.WriteString("warning: --insecure-http: the payload and its signature travel unencrypted; development only\n")
		}
		if new.AllowPrivate {
			out.WriteString("warning: --allow-private: the URL may reach loopback, private and link-local addresses\n")
		}
		if given {
			// The operator has it already: it is not printed back.
			out.WriteString("the secret is the one read from standard input\n")
		} else {
			// Printed once; it is not shown by any other command.
			fmt.Fprintf(&out, "secret: %s\n", text)
			out.WriteString("store the secret now: it cannot be shown again\n")
		}
		return r.write(out.String())
	case "list":
		hooks, err := db.ListWebhooks(ctx, sc.pool)
		if err != nil {
			return err
		}
		return r.write(webhookTable(hooks))
	case "disable", "enable":
		enable := verb == "enable"
		if err := db.SetWebhookEnabled(ctx, sc.pool, name, enable); err != nil {
			return err
		}
		return r.write(fmt.Sprintf("%sd webhook %s\n", verb, name))
	case "remove":
		if err := db.RemoveWebhook(ctx, sc.pool, name); err != nil {
			return err
		}
		return r.write(fmt.Sprintf("removed webhook %s\n", name))
	case "rotate-secret":
		given := secret != nil
		text, err := db.RotateWebhookSecret(ctx, sc.pool, name, secret)
		if err != nil {
			return err
		}
		if given {
			return r.write(fmt.Sprintf("changed the secret of webhook %s to the one read from standard input\n", name))
		}
		return r.write(fmt.Sprintf("new secret of webhook %s: %s\nstore the secret now: it cannot be shown again\n", name, text))
	case "test":
		return r.webhookTest(ctx, sc, name)
	}
	return fmt.Errorf("unknown webhook command %q", verb)
}

// webhookMessage is err with the prefix of a webhook error taken off: the command prints the reason alone.
func webhookMessage(err error) error {
	var dbe *db.Error
	if errors.As(err, &dbe) && dbe.Kind == db.ErrWebhook {
		return errf("%s", dbe.Message)
	}
	return err
}

// webhookTest is `webhook test`: one synchronous delivery with the event `test`, without the queue. It prints
// the status of the answer; the exit code is 0 for a 2xx answer, 1 for anything else.
func (r *runner) webhookTest(ctx context.Context, sc *siteContext, name string) error {
	hook, err := db.GetWebhook(ctx, sc.pool, name)
	if err != nil {
		return err
	}
	if hook == nil {
		return errf("no webhook named `%s`", name)
	}
	target, err := parseWebhookURL(hook.URL, hook.InsecureHTTP)
	if err != nil {
		return errf("invalid URL: %s", err)
	}
	label := target.label()
	delivery := "test-" + uuid.Must(uuid.NewV7()).String()
	body, err := compactJSON(map[string]any{
		"event":       "test",
		"doctype":     hook.DocType,
		"name":        nil,
		"data":        nil,
		"occurred_at": time.Now().UTC().Format(time.RFC3339Nano),
		"delivery":    delivery,
	})
	if err != nil {
		return errf("%s", err)
	}
	status, failure := sendWebhook(ctx, outgoingWebhook{
		url:          hook.URL,
		secret:       hook.Secret,
		allowPrivate: hook.AllowPrivate,
		insecureHTTP: hook.InsecureHTTP,
		event:        "test",
		delivery:     delivery,
		body:         body,
	})
	switch {
	case failure != nil:
		return errf("the test delivery failed: %s", failure.message)
	case status >= 200 && status <= 299:
		return r.write(fmt.Sprintf("webhook %s: %s answered HTTP %d (delivery %s)\n", name, label, status, delivery))
	}
	if err := r.write(fmt.Sprintf("webhook %s: %s answered HTTP %d\n", name, label, status)); err != nil {
		return err
	}
	if status >= 300 && status <= 399 {
		return errf("the test delivery was not accepted: HTTP %d (redirects are not followed)", status)
	}
	return errf("the test delivery was not accepted: HTTP %d", status)
}

// webhookTable is the table of `list`: the URL column shows only scheme, host and port.
func webhookTable(hooks []db.WebhookInfo) string {
	header := []string{"NAME", "DOCTYPE", "EVENTS", "URL", "ENABLED", "ALLOW_PRIVATE", "INSECURE_HTTP"}
	flag := func(v bool) string {
		if v {
			return "yes"
		}
		return "no"
	}
	rows := make([][]string, len(hooks))
	for i, hook := range hooks {
		label := "(invalid)"
		if target, err := parseWebhookURL(hook.URL, true); err == nil {
			label = target.label()
		}
		rows[i] = []string{hook.Name, hook.DocType, strings.Join(hook.Events, ","), label,
			flag(hook.Enabled), flag(hook.AllowPrivate), flag(hook.InsecureHTTP)}
	}
	widths := make([]int, len(header))
	for i, h := range header {
		widths[i] = len(h)
		for _, row := range rows {
			widths[i] = max(widths[i], utf8.RuneCountInString(row[i]))
		}
	}
	var text strings.Builder
	line := func(cells []string) {
		var out strings.Builder
		for i, cell := range cells {
			fmt.Fprintf(&out, "%-*s  ", widths[i], cell)
		}
		text.WriteString(strings.TrimRight(out.String(), " "))
		text.WriteByte('\n')
	}
	line(header)
	for _, row := range rows {
		line(row)
	}
	return text.String()
}
