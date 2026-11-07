package db

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/tvstore/forgebase-go/internal/core"
)

// Outgoing webhooks, the storage side (M9.2, ADR 0020; the Rust module webhooks.rs of forgebase-db).
//
// A webhook is a row of fb_webhook (migration 0010): the DocType and the events it listens to, the URL, the
// secret that signs the deliveries and the two development flags. This file stores them; the outbox side
// (what a document operation enqueues, writeWebhooks) is in outbox.go. Nothing here sends anything: the
// delivery is the built-in job forgebase.deliver_webhook of internal/app.

// WebhookEvents are the events a webhook can listen to.
var WebhookEvents = []string{"insert", "update", "delete", "submit", "cancel", "transition"}

// webhookSecretBytes is the length of a generated secret in bytes (it is written as 64 hex characters), the
// same as an API token secret.
const webhookSecretBytes = 32

// MinWebhookSecretBytes is the shortest secret a caller may give (--secret-stdin), in bytes: the signature is
// only as strong as the secret.
const MinWebhookSecretBytes = 16

// Webhook is a webhook as the delivery needs it, with its secret. Never log it whole.
type Webhook struct {
	// Name is the primary key.
	Name string
	// DocType is the DocType it listens to.
	DocType string
	// Events are the events, in the order they were given.
	Events []string
	// URL is where deliveries are posted.
	URL string
	// Secret signs the deliveries (plain text: the signature needs it).
	Secret string
	// Enabled says whether it fires. A disabled webhook enqueues nothing and a queued delivery of it is
	// dropped.
	Enabled bool
	// AllowPrivate allows the URL to resolve to a loopback, private or link-local address.
	AllowPrivate bool
	// InsecureHTTP allows http:// (development only).
	InsecureHTTP bool
}

// String is the webhook without its secret and its URL (they may hold a token).
func (w Webhook) String() string {
	return fmt.Sprintf("Webhook{Name: %s, DocType: %s, Events: %v, Enabled: %t, AllowPrivate: %t, InsecureHTTP: %t}",
		w.Name, w.DocType, w.Events, w.Enabled, w.AllowPrivate, w.InsecureHTTP)
}

// GoString is String too, so that %#v prints no secret either.
func (w Webhook) GoString() string { return w.String() }

// WebhookInfo is a webhook as `list` shows it: everything but the secret.
type WebhookInfo struct {
	Name         string
	DocType      string
	Events       []string
	URL          string
	Enabled      bool
	AllowPrivate bool
	InsecureHTTP bool
	CreatedAt    time.Time
}

// NewWebhook is what AddWebhook needs to know about a new webhook. The URL is checked by the caller (which
// parses URLs); this package stores it.
type NewWebhook struct {
	// Name follows the rules of a DocType name.
	Name string
	// DocType is a DocType of the registry that is not a child DocType.
	DocType string
	// Events are at least one of WebhookEvents.
	Events       []string
	URL          string
	AllowPrivate bool
	InsecureHTTP bool
}

func webhookError(format string, args ...any) error {
	return &Error{Kind: ErrWebhook, Message: fmt.Sprintf(format, args...)}
}

// CheckWebhookEvents checks and normalises a list of events: known names only, at least one, no duplicate (the
// first occurrence wins).
func CheckWebhookEvents(events []string) ([]string, error) {
	checked := []string{}
	for _, event := range events {
		if !slices.Contains(WebhookEvents, event) {
			return nil, webhookError("unknown event `%s` (use %s)", event, strings.Join(WebhookEvents, ", "))
		}
		if !slices.Contains(checked, event) {
			checked = append(checked, event)
		}
	}
	if len(checked) == 0 {
		return nil, webhookError("at least one event is needed (use %s)", strings.Join(WebhookEvents, ", "))
	}
	return checked, nil
}

// AddWebhook adds a webhook and returns its secret: secret if not nil, else 32 random bytes as hex. The DocType
// must exist in registry and must not be a child DocType (ErrUnknownDocType, ErrChildDocType). The name must
// pass the rules of a DocType name and be unused, the events and a given secret must be valid (ErrWebhook).
// The secret is returned here and can be read from the database only (the commands never print it again).
func AddWebhook(ctx context.Context, q Queryer, registry *core.Registry, webhook NewWebhook, secret *string) (string, error) {
	if err := core.ValidateDocTypeName(webhook.Name); err != nil {
		return "", webhookError("the name %s %s", core.DebugQuote(webhook.Name), err)
	}
	if _, err := documentDocType(registry, webhook.DocType); err != nil {
		return "", err
	}
	events, err := CheckWebhookEvents(webhook.Events)
	if err != nil {
		return "", err
	}
	if webhook.URL == "" {
		return "", webhookError("the URL is empty")
	}
	text, err := secretOrRandom(secret)
	if err != nil {
		return "", err
	}
	tag, err := q.Exec(ctx,
		"INSERT INTO fb_webhook (name, doctype, events, url, secret, allow_private, insecure_http) "+
			"VALUES ($1, $2, $3, $4, $5, $6, $7) ON CONFLICT (name) DO NOTHING",
		webhook.Name, webhook.DocType, events, webhook.URL, text, webhook.AllowPrivate, webhook.InsecureHTTP)
	if err != nil {
		return "", wrap(err)
	}
	if tag.RowsAffected() == 0 {
		return "", webhookError("a webhook named `%s` exists already", webhook.Name)
	}
	return text, nil
}

// ListWebhooks is every webhook, in name order (byte order), without secrets.
func ListWebhooks(ctx context.Context, q Queryer) ([]WebhookInfo, error) {
	rows, err := q.Query(ctx,
		"SELECT name, doctype, events, url, enabled, allow_private, insecure_http, created_at "+
			"FROM fb_webhook ORDER BY name COLLATE \"C\"")
	if err != nil {
		return nil, wrap(err)
	}
	defer rows.Close()
	out := []WebhookInfo{}
	for rows.Next() {
		var w WebhookInfo
		if err := rows.Scan(&w.Name, &w.DocType, &w.Events, &w.URL, &w.Enabled, &w.AllowPrivate, &w.InsecureHTTP, &w.CreatedAt); err != nil {
			return nil, wrap(err)
		}
		out = append(out, w)
	}
	return out, wrap(rows.Err())
}

// GetWebhook is the webhook with this name, secret included, or nil.
func GetWebhook(ctx context.Context, q Queryer, name string) (*Webhook, error) {
	rows, err := q.Query(ctx,
		"SELECT name, doctype, events, url, secret, enabled, allow_private, insecure_http FROM fb_webhook WHERE name = $1", name)
	if err != nil {
		return nil, wrap(err)
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, wrap(rows.Err())
	}
	var w Webhook
	if err := rows.Scan(&w.Name, &w.DocType, &w.Events, &w.URL, &w.Secret, &w.Enabled, &w.AllowPrivate, &w.InsecureHTTP); err != nil {
		return nil, wrap(err)
	}
	return &w, nil
}

// SetWebhookEnabled enables or disables a webhook. ErrWebhook when there is none with this name.
func SetWebhookEnabled(ctx context.Context, q Queryer, name string, enabled bool) error {
	tag, err := q.Exec(ctx, "UPDATE fb_webhook SET enabled = $2, modified_at = now() WHERE name = $1", name, enabled)
	if err != nil {
		return wrap(err)
	}
	return webhookFound(tag.RowsAffected(), name)
}

// RemoveWebhook removes a webhook. Deliveries already queued for it are dropped when they run. ErrWebhook when
// there is none with this name.
func RemoveWebhook(ctx context.Context, q Queryer, name string) error {
	tag, err := q.Exec(ctx, "DELETE FROM fb_webhook WHERE name = $1", name)
	if err != nil {
		return wrap(err)
	}
	return webhookFound(tag.RowsAffected(), name)
}

// RotateWebhookSecret gives the webhook a new secret (secret if not nil, else random) and returns it. A given
// secret that is refused changes nothing.
func RotateWebhookSecret(ctx context.Context, q Queryer, name string, secret *string) (string, error) {
	text, err := secretOrRandom(secret)
	if err != nil {
		return "", err
	}
	tag, err := q.Exec(ctx, "UPDATE fb_webhook SET secret = $2, modified_at = now() WHERE name = $1", name, text)
	if err != nil {
		return "", wrap(err)
	}
	if err := webhookFound(tag.RowsAffected(), name); err != nil {
		return "", err
	}
	return text, nil
}

// secretOrRandom is the secret a caller gave, checked (not empty, at least MinWebhookSecretBytes bytes), or a
// new random one.
func secretOrRandom(secret *string) (string, error) {
	switch {
	case secret == nil:
		text, err := randomHex(webhookSecretBytes)
		if err != nil {
			return "", webhookError("cannot get random bytes from the operating system")
		}
		return text, nil
	case *secret == "":
		return "", webhookError("the secret is empty")
	case len(*secret) < MinWebhookSecretBytes:
		return "", webhookError("the secret must be at least %d bytes long, this one has %d (a short secret is easy to guess; leave it out to get 32 random bytes)",
			MinWebhookSecretBytes, len(*secret))
	}
	return *secret, nil
}

func webhookFound(changed int64, name string) error {
	if changed == 0 {
		return webhookError("no webhook named `%s`", name)
	}
	return nil
}
