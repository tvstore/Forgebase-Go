package core

import (
	"os"
	"testing"
	"time"

	"github.com/tvstore/forgebase-go/internal/decimal"
)

// Loads the example DocType file and exercises the public API end to end.
// Rust: tests/customer_fixture.rs (the fixture is testdata/customer.toml, a copy of
// tests/fixtures/customer.toml).

func customerFixture(t *testing.T) *DocType {
	t.Helper()
	text, err := os.ReadFile("testdata/customer.toml")
	if err != nil {
		t.Fatal(err)
	}
	return mustDocType(t, string(text))
}

// Rust: customer_fixture.rs::fixture_loads
func TestFixtureLoads(t *testing.T) {
	d := customerFixture(t)
	if d.Name != "customer" || TableName(d.Name) != "doc_customer" {
		t.Errorf("name %q table %q", d.Name, TableName(d.Name))
	}
	sameValue(t, d.Naming, Naming{Kind: NamingField, Field: "email"}, "naming")
	if len(d.Fields) != 9 {
		t.Errorf("fields = %d", len(d.Fields))
	}
	sameValue(t, d.Field("full_name").Type, FieldType{Kind: FieldData, MaxLength: 140}, "full_name")
	if errs := d.Validate(); errs != nil {
		t.Errorf("validate: %v", errs)
	}
	registry := NewRegistry()
	if errs := registry.Add(d.Clone()); errs != nil {
		t.Fatal(errs)
	}
	if errs := registry.Add(d); errs == nil {
		t.Error("a second add succeeded")
	}
}

// Rust: customer_fixture.rs::fixture_validates_documents
func TestFixtureValidatesDocuments(t *testing.T) {
	d := customerFixture(t)
	registry := NewRegistry()
	credit, err := decimal.Parse("1500.50")
	if err != nil {
		t.Fatal(err)
	}
	doc := &Document{Values: map[string]Value{
		"email":        Text("a@example.com"),
		"full_name":    Text("Ada"),
		"credit_limit": Decimal(credit),
		"joined_on":    NewDate(2026, time.October, 28),
		"last_seen_at": Datetime(time.Unix(0, 0).UTC()),
		"tier":         Text("gold"),
	}}
	if errs := Validate(registry, d, doc); errs != nil {
		t.Errorf("valid document: %v", errs)
	}
	doc.Values["tier"] = Text("platinum")
	delete(doc.Values, "email")
	sameValue(t, Validate(registry, d, doc), []ValidationError{
		{Kind: ErrRequired, Field: "email"},
		{Kind: ErrInvalidOption, Field: "tier", Value: "platinum"},
	}, "invalid document")
}
