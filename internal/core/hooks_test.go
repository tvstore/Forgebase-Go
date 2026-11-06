package core

import (
	"encoding/json"
	"strings"
	"testing"
)

// Rust: hooks.rs::a_hook_enqueues_into_the_collector_in_order
func TestAHookEnqueuesIntoTheCollectorInOrder(t *testing.T) {
	jobs := NewEnqueuedJobs()
	ctx := NewHookContext("customer", "ann", OpInsert).WithJobs(jobs)
	if err := ctx.Enqueue("crm.welcome", map[string]any{"n": 1}); err != nil {
		t.Fatal(err)
	}
	// The context is a value: a copy shares the collector.
	copied := ctx
	if err := copied.Enqueue("crm.audit", nil); err != nil {
		t.Fatal(err)
	}
	taken := jobs.Take()
	if len(taken) != 2 || taken[0].Name() != "crm.welcome" || taken[1].Name() != "crm.audit" {
		t.Fatalf("taken = %v", taken)
	}
	sameValue(t, string(taken[0].Payload()), `{"n":1}`, "payload")
	// The payload is serialized when it is enqueued: a later change has no effect.
	m := map[string]any{"n": 1}
	if err := ctx.Enqueue("crm.x", m); err != nil {
		t.Fatal(err)
	}
	m["n"] = 2
	sameValue(t, string(jobs.Take()[0].Payload()), `{"n":1}`, "payload after mutation")
	if len(jobs.Take()) != 0 {
		t.Error("take leaves the collector empty")
	}
	if !ctx.Equal(copied) {
		t.Error("a copy is equal")
	}
}

// Rust: hooks.rs::a_context_without_a_collector_refuses_to_enqueue
func TestAContextWithoutACollectorRefusesToEnqueue(t *testing.T) {
	ctx := NewHookContext("customer", "ann", OpInsert)
	err := ctx.Enqueue("crm.welcome", map[string]any{})
	if err == nil || !strings.Contains(err.Error(), "no job collector") {
		t.Errorf("error: %v", err)
	}
	if ctx.Equal(ctx.WithJobs(NewEnqueuedJobs())) {
		t.Error("contexts with and without a collector are equal")
	}
}

// Rust: hooks.rs::the_transition_is_part_of_the_context
func TestTheTransitionIsPartOfTheContext(t *testing.T) {
	ctx := NewHookContext("order", "ann", OpTransition)
	if ctx.Transition != nil {
		t.Error("transition set")
	}
	info := NewTransitionInfo("Approve", "Pending", "Approved")
	with := ctx.WithTransition(info)
	if with.Transition == nil || with.Transition.Action != "Approve" || with.Transition.From != "Pending" || with.Transition.To != "Approved" {
		t.Errorf("transition = %v", with.Transition)
	}
	if ctx.Equal(with) {
		t.Error("with and without a transition are equal")
	}
	if !with.Equal(ctx.WithTransition(NewTransitionInfo("Approve", "Pending", "Approved"))) {
		t.Error("transitions compare by value")
	}
}

// Rust: hooks.rs::the_payload_limit_counts_serialized_json
func TestThePayloadLimitCountsSerializedJson(t *testing.T) {
	fits := map[string]any{"text": strings.Repeat("x", MaxJobPayloadBytes-20)}
	if reason := CheckJobPayload(fits); reason != "" {
		t.Errorf("fits: %s", reason)
	}
	tooBig := map[string]any{"text": strings.Repeat("x", MaxJobPayloadBytes)}
	if reason := CheckJobPayload(tooBig); !strings.Contains(reason, "larger than") {
		t.Errorf("too big: %q", reason)
	}
	if CheckJobPayload(map[string]any{"a": []any{"b\x00"}}) == "" {
		t.Error("NUL in an array")
	}
	if CheckJobPayload(map[string]any{"a\x00": 1}) == "" {
		t.Error("NUL in a key")
	}
	if reason := CheckJobPayload("a\\u0000"); reason != "" {
		t.Errorf("escaped text: %s", reason)
	}
	// Go-specific: a value that cannot be marshaled is refused like an oversized one; a struct
	// payload is checked through its JSON form.
	if CheckJobPayload(make(chan int)) == "" {
		t.Error("unmarshalable payload accepted")
	}
	type payload struct {
		Text string `json:"text"`
	}
	if CheckJobPayload(payload{Text: "a\x00"}) == "" {
		t.Error("NUL in a struct")
	}
	var raw json.RawMessage = []byte(`{"a":1}`)
	if CheckJobPayload(raw) != "" {
		t.Error("raw JSON refused")
	}
}

// loggingHooks embeds BaseHooks and overrides one point, as an app does.
type loggingHooks struct {
	BaseHooks
	calls []string
}

func (h *loggingHooks) Validate(ctx HookContext, doc *Document) error {
	h.calls = append(h.calls, "validate:"+ctx.DocType)
	if v, ok := doc.Values["email"]; ok && v.Kind == KindText {
		doc.Values["email"] = Text(strings.ToLower(strings.TrimSpace(v.Text)))
		if !strings.Contains(v.Text, "@") {
			return NewHookError("email must contain an @")
		}
	}
	return nil
}

// Rust: hooks.rs module documentation example (CustomerRules); the embedded base is the Go
// replacement of the trait's default methods (ADR 0026).
func TestHookSetEmbeddingBaseHooks(t *testing.T) {
	customer := mustDocType(t, "name = \"customer\"\nlabel = \"Customer\"\n[[fields]]\nname = \"email\"\ntype = \"data\"\n")
	registry := NewRegistry()
	if errs := registry.Add(customer); errs != nil {
		t.Fatal(errs)
	}
	hooks := &loggingHooks{}
	if err := registry.AddHooks("customer", hooks); err != nil {
		t.Fatal(err)
	}
	if len(registry.Hooks("customer")) != 1 {
		t.Fatal("hook set not registered")
	}
	ctx := NewHookContext("customer", "ann", OpInsert)
	doc := &Document{Values: map[string]Value{"email": Text("  Ann@Example.com ")}}
	if err := registry.Hooks("customer")[0].Validate(ctx, doc); err != nil {
		t.Fatal(err)
	}
	if !doc.Values["email"].Equal(Text("ann@example.com")) {
		t.Errorf("email = %v", doc.Values["email"])
	}
	doc.Values["email"] = Text("nobody")
	err := registry.Hooks("customer")[0].Validate(ctx, doc)
	if he, ok := err.(*HookError); !ok || he.Message() != "email must contain an @" {
		t.Errorf("error: %v", err)
	}
	// The points the set does not override do nothing.
	h := registry.Hooks("customer")[0]
	for name, err := range map[string]error{
		"before_insert":     h.BeforeInsert(ctx, doc),
		"before_delete":     h.BeforeDelete(ctx, doc),
		"before_submit":     h.BeforeSubmit(ctx, doc),
		"before_cancel":     h.BeforeCancel(ctx, doc),
		"before_transition": h.BeforeTransition(ctx, doc),
	} {
		if err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}
