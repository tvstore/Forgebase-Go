package core

import (
	"strings"
	"testing"
)

// Rust: ident.rs::doctype_names
func TestDoctypeNames(t *testing.T) {
	atLimit := "a" + strings.Repeat("b", 58)
	overLimit := "a" + strings.Repeat("b", 59)
	cases := []struct {
		name string
		want error
	}{
		{"customer", nil},
		{"sales_order_2", nil},
		{"a", nil},
		// "id" is only reserved as a field name.
		{"id", nil},
		{atLimit, nil},
		{overLimit, IdentError{Kind: IdentTooLong, Len: 60, Max: 59}},
		{"", IdentError{Kind: IdentEmpty}},
		{"Customer", IdentError{Kind: IdentInvalidFormat}},
		{"1customer", IdentError{Kind: IdentInvalidFormat}},
		{"_customer", IdentError{Kind: IdentInvalidFormat}},
		{"sales-order", IdentError{Kind: IdentInvalidFormat}},
		{"sales order", IdentError{Kind: IdentInvalidFormat}},
		{"café", IdentError{Kind: IdentInvalidFormat}},
		{"a\"; drop table x; --", IdentError{Kind: IdentInvalidFormat}},
	}
	for _, c := range cases {
		if got := ValidateDocTypeName(c.name); got != c.want {
			t.Errorf("name %q: got %v, want %v", c.name, got, c.want)
		}
	}
	if len(atLimit) != 59 {
		t.Errorf("len(atLimit) = %d", len(atLimit))
	}
	if len(TableName(atLimit)) != 63 {
		t.Errorf("table name length = %d", len(TableName(atLimit)))
	}
}

// Rust: ident.rs::field_names
func TestFieldNames(t *testing.T) {
	atLimit := "a" + strings.Repeat("b", 62)
	overLimit := "a" + strings.Repeat("b", 63)
	cases := []struct {
		name string
		want error
	}{
		{"email", nil},
		{"address_line_2", nil},
		{atLimit, nil},
		{overLimit, IdentError{Kind: IdentTooLong, Len: 64, Max: 63}},
		{"", IdentError{Kind: IdentEmpty}},
		{"Email", IdentError{Kind: IdentInvalidFormat}},
		{"9lives", IdentError{Kind: IdentInvalidFormat}},
	}
	for _, c := range cases {
		if got := ValidateFieldName(c.name); got != c.want {
			t.Errorf("name %q: got %v, want %v", c.name, got, c.want)
		}
	}
}

// Rust: ident.rs::every_reserved_name_is_rejected_as_field_name
func TestEveryReservedNameIsRejectedAsFieldName(t *testing.T) {
	for _, name := range ReservedFieldNames {
		if got := ValidateFieldName(name); got != (IdentError{Kind: IdentReserved}) {
			t.Errorf("name %q: got %v", name, got)
		}
	}
	if len(ReservedFieldNames) != 17 {
		t.Errorf("len = %d", len(ReservedFieldNames))
	}
}

// Rust: ident.rs::postgres_system_columns_are_reserved
func TestPostgresSystemColumnsAreReserved(t *testing.T) {
	for _, name := range []string{"tableoid", "xmin", "xmax", "cmin", "cmax", "ctid"} {
		if got := ValidateFieldName(name); got != (IdentError{Kind: IdentReserved}) {
			t.Errorf("name %q: got %v", name, got)
		}
	}
}

// Rust: ident.rs::table_name_has_prefix
func TestTableNameHasPrefix(t *testing.T) {
	if got := TableName("sales_order"); got != "doc_sales_order" {
		t.Errorf("got %q", got)
	}
}

func TestIdentErrorMessages(t *testing.T) {
	cases := map[IdentError]string{
		{Kind: IdentEmpty}:                     "must not be empty",
		{Kind: IdentInvalidFormat}:             "must match ^[a-z][a-z0-9_]*$ (lowercase ASCII letters, digits and underscores, starting with a letter)",
		{Kind: IdentTooLong, Len: 60, Max: 59}: "is 60 bytes long, the maximum is 59",
		{Kind: IdentReserved}:                  "is a reserved column name",
	}
	for e, want := range cases {
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
}

// Rust: lib.rs::shorten_tests::long_input_is_cut_on_a_character_boundary
func TestLongInputIsCutOnACharacterBoundary(t *testing.T) {
	if got := Shorten("short"); got != "short" {
		t.Errorf("got %q", got)
	}
	exact := strings.Repeat("x", EchoChars)
	if got := Shorten(exact); got != exact {
		t.Errorf("exact: %q", got)
	}
	if got := Shorten(strings.Repeat("x", EchoChars+1)); got != exact+"…" {
		t.Errorf("one over: %q", got)
	}
	wide := strings.Repeat("é", 1000)
	if got := len([]rune(Shorten(wide))); got != EchoChars+1 {
		t.Errorf("wide: %d runes", got)
	}
	if got := len(Shorten(strings.Repeat("\x01", 1_000_000))); got >= 100 {
		t.Errorf("long: %d bytes", got)
	}
}
