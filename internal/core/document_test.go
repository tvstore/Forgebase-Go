package core

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/tvstore/forgebase-go/internal/decimal"
)

func dec(t *testing.T, s string) Value {
	t.Helper()
	d, err := decimal.Parse(s)
	if err != nil {
		t.Fatalf("decimal %q: %v", s, err)
	}
	return Decimal(d)
}

func testDate() Value { return NewDate(2026, time.October, 28) }

func epoch() Value { return Datetime(time.Unix(0, 0).UTC()) }

// thingDocType is the DocType of the document tests (Rust: document.rs tests, doctype()).
func thingDocType() *DocType {
	title := dataField("title", 5)
	title.Required = true
	return &DocType{
		Name: "thing", Label: "Thing",
		Fields: []DocField{
			title,
			textField("notes"),
			intField("count"),
			decimalField("amount", 5, 2),
			mkField("active", FieldType{Kind: FieldCheck}),
			mkField("day", FieldType{Kind: FieldDate}),
			mkField("at", FieldType{Kind: FieldDatetime}),
			selectField("kind", "a", "b"),
			linkField("owner_ref", "person"),
		},
	}
}

type pair struct {
	k string
	v Value
}

// docOf is the document with title "ok" plus the given pairs.
func docOf(pairs ...pair) *Document {
	d := &Document{Values: map[string]Value{"title": Text("ok")}}
	for _, p := range pairs {
		d.Values[p.k] = p.v
	}
	return d
}

func errsOf(t *testing.T, d *Document) []ValidationError {
	t.Helper()
	errs := Validate(NewRegistry(), thingDocType(), d)
	if errs == nil {
		t.Fatal("expected errors")
	}
	return errs
}

func required(f string) ValidationError { return ValidationError{Kind: ErrRequired, Field: f} }

// Rust: document.rs::valid_document_passes
func TestValidDocumentPasses(t *testing.T) {
	d := docOf(
		pair{"notes", Text(strings.Repeat("x", 100_000))},
		pair{"count", Int(-9223372036854775808)},
		pair{"amount", dec(t, "123.45")},
		pair{"active", Bool(false)},
		pair{"day", testDate()},
		pair{"at", epoch()},
		pair{"kind", Text("b")},
	)
	if errs := Validate(NewRegistry(), thingDocType(), d); errs != nil {
		t.Errorf("errors: %v", errs)
	}
}

// Rust: document.rs::optional_fields_may_be_null_or_absent
func TestOptionalFieldsMayBeNullOrAbsent(t *testing.T) {
	d := docOf(pair{"count", Null()}, pair{"kind", Null()})
	if errs := Validate(NewRegistry(), thingDocType(), d); errs != nil {
		t.Errorf("errors: %v", errs)
	}
}

// Rust: document.rs::required_missing_or_null
func TestRequiredMissingOrNull(t *testing.T) {
	d := &Document{}
	sameValue(t, errsOf(t, d), []ValidationError{required("title")}, "missing")
	d.Values = map[string]Value{"title": Null()}
	sameValue(t, errsOf(t, d), []ValidationError{required("title")}, "null")
}

// Rust: document.rs::required_rejects_empty_and_whitespace_text
func TestRequiredRejectsEmptyAndWhitespaceText(t *testing.T) {
	dt := thingDocType()
	body := textField("body")
	body.Required = true
	stage := selectField("stage", "a")
	stage.Required = true
	dt.Fields = append(dt.Fields, body, stage)
	for _, blank := range []string{"", "   ", "\t\n "} {
		for _, field := range []string{"title", "body", "stage"} {
			d := &Document{Values: map[string]Value{"title": Text("ok"), "body": Text("x"), "stage": Text("a")}}
			d.Values[field] = Text(blank)
			sameValue(t, Validate(NewRegistry(), dt, d), []ValidationError{required(field)}, field+" = "+blank)
		}
	}
	// Optional fields may hold blank text.
	d := docOf(pair{"body", Text("x")}, pair{"stage", Text("a")}, pair{"notes", Text("  ")})
	if errs := Validate(NewRegistry(), dt, d); errs != nil {
		t.Errorf("optional blank: %v", errs)
	}
}

// Rust: document.rs::nul_is_rejected_in_text_data_and_select
func TestNulIsRejectedInTextDataAndSelect(t *testing.T) {
	for _, c := range [][2]string{{"title", "a\x00b"}, {"notes", "a\x00"}, {"kind", "\x00"}} {
		sameValue(t, errsOf(t, docOf(pair{c[0], Text(c[1])})), []ValidationError{{Kind: ErrContainsNul, Field: c[0]}}, c[0])
	}
}

// Rust: document.rs::invalid_decimal_definition_does_not_panic
func TestInvalidDecimalDefinitionDoesNotPanic(t *testing.T) {
	dt := thingDocType()
	dt.Fields = []DocField{decimalField("amount", 2, 3)}
	// The result is unspecified for an invalid DocType; it must only not panic.
	_ = Validate(NewRegistry(), dt, docOf(pair{"amount", dec(t, "0.001")}))
}

// Rust: document.rs::link_values_are_text_checked_like_data
func TestLinkValuesAreTextCheckedLikeData(t *testing.T) {
	// The target name is any non-blank text; existence is checked by internal/db.
	if errs := Validate(NewRegistry(), thingDocType(), docOf(pair{"owner_ref", Text("ann@example.com")})); errs != nil {
		t.Errorf("link: %v", errs)
	}
	sameValue(t, errsOf(t, docOf(pair{"owner_ref", Int(1)})),
		[]ValidationError{{Kind: ErrTypeMismatch, Field: "owner_ref", Expected: "text", Found: "int"}}, "int link")
	sameValue(t, errsOf(t, docOf(pair{"owner_ref", Text("a\x00b")})), []ValidationError{{Kind: ErrContainsNul, Field: "owner_ref"}}, "nul link")
	dt := thingDocType()
	mustLink := linkField("must_link", "person")
	mustLink.Required = true
	dt.Fields = append(dt.Fields, mustLink)
	for _, blank := range []string{"", "  \t"} {
		d := docOf(pair{"must_link", Text(blank)})
		sameValue(t, Validate(NewRegistry(), dt, d), []ValidationError{required("must_link")}, blank)
		d.Values["must_link"] = Null()
		if n := len(Validate(NewRegistry(), dt, d)); n != 1 {
			t.Errorf("null: %d errors", n)
		}
	}
	// Optional links may be null.
	if errs := Validate(NewRegistry(), thingDocType(), docOf(pair{"owner_ref", Null()})); errs != nil {
		t.Errorf("optional null link: %v", errs)
	}
	if got := (ValidationError{Kind: ErrLinkNotFound, Field: "owner_ref", Target: "person", Name: "x"}).FieldName(); got != "owner_ref" {
		t.Errorf("field = %q", got)
	}
}

// Rust: document.rs::unknown_field
func TestUnknownField(t *testing.T) {
	sameValue(t, errsOf(t, docOf(pair{"nope", Int(1)})), []ValidationError{{Kind: ErrUnknownField, Field: "nope"}}, "unknown")
}

// Rust: document.rs::type_mismatches
func TestTypeMismatches(t *testing.T) {
	cases := []struct {
		field    string
		value    Value
		expected string
		found    string
	}{
		{"notes", Int(1), "text", "int"},
		{"count", Text("1"), "int", "text"},
		{"count", dec(t, "1"), "int", "decimal"},
		{"amount", Int(1), "decimal", "int"},
		{"active", Int(1), "bool", "int"},
		{"day", Text("2026-10-28"), "date", "text"},
		{"day", epoch(), "date", "datetime"},
		{"at", testDate(), "datetime", "date"},
		{"kind", Int(1), "text", "int"},
		{"title", Bool(true), "text", "bool"},
	}
	for _, c := range cases {
		sameValue(t, errsOf(t, docOf(pair{c.field, c.value})),
			[]ValidationError{{Kind: ErrTypeMismatch, Field: c.field, Expected: c.expected, Found: c.found}}, "field "+c.field)
	}
}

// Rust: document.rs::data_max_length_counts_characters
func TestDataMaxLengthCountsCharacters(t *testing.T) {
	if errs := Validate(NewRegistry(), thingDocType(), docOf(pair{"title", Text("12345")})); errs != nil {
		t.Errorf("12345: %v", errs)
	}
	// Five multi-byte characters are 10 bytes but still within the limit.
	if errs := Validate(NewRegistry(), thingDocType(), docOf(pair{"title", Text("ééééé")})); errs != nil {
		t.Errorf("multibyte: %v", errs)
	}
	sameValue(t, errsOf(t, docOf(pair{"title", Text("123456")})), []ValidationError{{Kind: ErrTooLong, Field: "title", Len: 6, Max: 5}}, "long")
}

// Rust: document.rs::select_membership
func TestSelectMembership(t *testing.T) {
	if errs := Validate(NewRegistry(), thingDocType(), docOf(pair{"kind", Text("a")})); errs != nil {
		t.Errorf("a: %v", errs)
	}
	sameValue(t, errsOf(t, docOf(pair{"kind", Text("c")})), []ValidationError{{Kind: ErrInvalidOption, Field: "kind", Value: "c"}}, "c")
	// Case matters.
	if n := len(errsOf(t, docOf(pair{"kind", Text("A")}))); n != 1 {
		t.Errorf("A: %d errors", n)
	}
}

// Rust: document.rs::decimal_precision_and_scale
func TestDecimalPrecisionAndScale(t *testing.T) {
	// precision 5, scale 2: up to 3 integer digits and 2 decimal places.
	for _, ok := range []string{"0", "0.5", "-0.01", "999.99", "-999.99", "1.5", "1.500", "100", "0.00", "12.30000"} {
		if errs := Validate(NewRegistry(), thingDocType(), docOf(pair{"amount", dec(t, ok)})); errs != nil {
			t.Errorf("%s: %v", ok, errs)
		}
	}
	for _, c := range []struct {
		v     string
		scale uint32
	}{{"1.234", 3}, {"0.001", 3}} {
		sameValue(t, errsOf(t, docOf(pair{"amount", dec(t, c.v)})),
			[]ValidationError{{Kind: ErrDecimalScale, Field: "amount", Scale: c.scale, Max: 2}}, c.v)
	}
	for _, c := range []struct {
		v      string
		digits uint32
	}{{"1000", 4}, {"-1000.5", 4}, {"12345.67", 5}} {
		sameValue(t, errsOf(t, docOf(pair{"amount", dec(t, c.v)})),
			[]ValidationError{{Kind: ErrDecimalPrecision, Field: "amount", Digits: c.digits, Max: 3}}, c.v)
	}
}

// Rust: document.rs::decimal_with_scale_equal_to_precision
func TestDecimalWithScaleEqualToPrecision(t *testing.T) {
	d := thingDocType()
	d.Fields = append(d.Fields, decimalField("ratio", 3, 3))
	for _, ok := range []string{"0", "0.999", "-0.5"} {
		if errs := Validate(NewRegistry(), d, docOf(pair{"ratio", dec(t, ok)})); errs != nil {
			t.Errorf("%s: %v", ok, errs)
		}
	}
	sameValue(t, Validate(NewRegistry(), d, docOf(pair{"ratio", dec(t, "1")})),
		[]ValidationError{{Kind: ErrDecimalPrecision, Field: "ratio", Digits: 1, Max: 0}}, "1")
}

// Rust: document.rs::all_errors_are_collected
func TestAllErrorsAreCollected(t *testing.T) {
	d := &Document{Values: map[string]Value{"nope": Null(), "count": Text("x")}}
	if n := len(errsOf(t, d)); n != 3 {
		t.Errorf("%d errors", n)
	}
}

// ---- child tables ----

func orderRegistry(t *testing.T) (*Registry, *DocType) {
	t.Helper()
	registry := NewRegistry()
	order := &DocType{Name: "order", Label: "Order", Fields: []DocField{dataField("title", 5), tableField("items", "item")}}
	qty := intField("qty")
	qty.Required = true
	item := &DocType{Name: "item", Label: "Item", IsChild: true, Fields: []DocField{qty, dataField("note", 3), linkField("customer", "person")}}
	if errs := registry.Add(item); errs != nil {
		t.Fatal(errs)
	}
	if errs := registry.Add(order.Clone()); errs != nil {
		t.Fatal(errs)
	}
	return registry, order
}

func rowOf(pairs ...pair) Row {
	r := Row{}
	for _, p := range pairs {
		r[p.k] = p.v
	}
	return r
}

func withRows(rows ...Row) *Document {
	if rows == nil {
		rows = []Row{}
	}
	return &Document{Tables: map[string][]Row{"items": rows}}
}

func inRow(row int, e ValidationError) ValidationError {
	return ValidationError{Kind: ErrInRow, Field: "items", Row: row, Inner: &e}
}

// Rust: document.rs::valid_rows_pass_and_none_or_empty_is_fine
func TestValidRowsPassAndNoneOrEmptyIsFine(t *testing.T) {
	registry, order := orderRegistry(t)
	d := withRows(
		rowOf(pair{"qty", Int(1)}, pair{"customer", Text("ann")}),
		rowOf(pair{"qty", Int(2)}, pair{"note", Null()}),
	)
	if errs := Validate(registry, order, d); errs != nil {
		t.Errorf("valid rows: %v", errs)
	}
	if errs := Validate(registry, order, &Document{}); errs != nil {
		t.Errorf("no rows: %v", errs)
	}
	if errs := Validate(registry, order, withRows()); errs != nil {
		t.Errorf("empty rows: %v", errs)
	}
}

// Rust: document.rs::row_errors_carry_the_row_position_and_are_all_collected
func TestRowErrorsCarryTheRowPositionAndAreAllCollected(t *testing.T) {
	registry, order := orderRegistry(t)
	d := withRows(
		rowOf(pair{"qty", Int(1)}),
		rowOf(),
		rowOf(pair{"qty", Text("x")}, pair{"note", Text("toolong")}, pair{"nope", Int(1)}),
	)
	errs := Validate(registry, order, d)
	sameValue(t, errs, []ValidationError{
		inRow(2, required("qty")),
		inRow(3, ValidationError{Kind: ErrUnknownField, Field: "nope"}),
		inRow(3, ValidationError{Kind: ErrTooLong, Field: "note", Len: 7, Max: 3}),
		inRow(3, ValidationError{Kind: ErrTypeMismatch, Field: "qty", Expected: "int", Found: "text"}),
	}, "errors")
	if got := errs[0].Error(); got != "field `items` row 2: field `qty` is required" {
		t.Errorf("message %q", got)
	}
	if errs[0].FieldName() != "items" {
		t.Error("field")
	}
	// A row with a blank required value or NUL.
	d = withRows(rowOf(pair{"qty", Null()}))
	sameValue(t, Validate(registry, order, d), []ValidationError{inRow(1, required("qty"))}, "null qty")
}

// Rust: document.rs::a_required_table_needs_at_least_one_row
func TestARequiredTableNeedsAtLeastOneRow(t *testing.T) {
	registry, order := orderRegistry(t)
	order = order.Clone()
	order.Fields[1].Required = true
	want := []ValidationError{required("items")}
	sameValue(t, Validate(registry, order, &Document{}), want, "no table")
	sameValue(t, Validate(registry, order, withRows()), want, "empty table")
	if errs := Validate(registry, order, withRows(rowOf(pair{"qty", Int(1)}))); errs != nil {
		t.Errorf("one row: %v", errs)
	}
}

// Rust: document.rs::unknown_table_and_table_misuse
func TestUnknownTableAndTableMisuse(t *testing.T) {
	registry, order := orderRegistry(t)
	d := &Document{Tables: map[string][]Row{"ghosts": {}}}
	sameValue(t, Validate(registry, order, d), []ValidationError{{Kind: ErrUnknownField, Field: "ghosts"}}, "ghosts")
	// Rows given for a field that is not a Table field.
	d = &Document{Tables: map[string][]Row{"title": {Row{}}}}
	sameValue(t, Validate(registry, order, d), []ValidationError{{Kind: ErrTypeMismatch, Field: "title", Expected: "text", Found: "rows"}}, "rows for a text field")
	// A value given for a Table field (even null).
	for _, value := range []Value{Int(1), Null(), Text("x")} {
		d := &Document{Values: map[string]Value{"items": value}}
		sameValue(t, Validate(registry, order, d), []ValidationError{{Kind: ErrTypeMismatch, Field: "items", Expected: "rows", Found: value.KindName()}}, value.KindName())
	}
	// A Table field name inside a row is not a child field.
	d = withRows(rowOf(pair{"qty", Int(1)}, pair{"items", Int(1)}))
	sameValue(t, Validate(registry, order, d), []ValidationError{inRow(1, ValidationError{Kind: ErrUnknownField, Field: "items"})}, "table name in a row")
}

// Rust: document.rs::changes_know_when_they_are_empty
func TestChangesKnowWhenTheyAreEmpty(t *testing.T) {
	changes := Changes{}
	if !changes.IsEmpty() {
		t.Error("zero changes not empty")
	}
	changes.Tables = map[string][]Row{"items": {}}
	if changes.IsEmpty() {
		t.Error("changes with a table are empty")
	}
}

// Rust: document.rs::a_table_whose_child_is_missing_does_not_panic
func TestATableWhoseChildIsMissingDoesNotPanic(t *testing.T) {
	_, order := orderRegistry(t)
	d := withRows(rowOf())
	if errs := Validate(NewRegistry(), order, d); errs != nil {
		t.Errorf("errors: %v", errs)
	}
}

func TestValidationErrorMessages(t *testing.T) {
	cases := []struct {
		e    ValidationError
		want string
	}{
		{ValidationError{Kind: ErrUnknownField, Field: strings.Repeat("x", 70)}, "unknown field `" + strings.Repeat("x", 64) + "…`"},
		{ValidationError{Kind: ErrTypeMismatch, Field: "f", Expected: "int", Found: "text"}, "field `f`: expected int, found text"},
		{ValidationError{Kind: ErrTooLong, Field: "f", Len: 6, Max: 5}, "field `f`: 6 characters exceeds max_length 5"},
		{ValidationError{Kind: ErrInvalidOption, Field: "f", Value: "c"}, "field `f`: `c` is not one of the allowed options"},
		{ValidationError{Kind: ErrContainsNul, Field: "f"}, "field `f`: value must not contain a NUL character"},
		{ValidationError{Kind: ErrDecimalScale, Field: "f", Scale: 3, Max: 2}, "field `f`: 3 decimal places exceeds scale 2"},
		{ValidationError{Kind: ErrDecimalPrecision, Field: "f", Digits: 4, Max: 3}, "field `f`: 4 integer digits exceeds the maximum of 3"},
		{ValidationError{Kind: ErrReadOnly, Field: "f"}, "field `f` is read-only"},
		{ValidationError{Kind: ErrNotPermitted, Field: "f"}, "field `f` cannot be changed with your permissions"},
		{ValidationError{Kind: ErrHidden}, "a field that you cannot see has a value that is not valid"},
		{ValidationError{Kind: ErrLinkNotFound, Field: "f", Target: "person", Name: "x"}, "field `f`: person `x` does not exist"},
	}
	for _, c := range cases {
		if got := c.e.Error(); got != c.want {
			t.Errorf("got %q, want %q", got, c.want)
		}
	}
	if (ValidationError{Kind: ErrHidden}).FieldName() != "" {
		t.Error("hidden names a field")
	}
}

func TestValueKindsAndEquality(t *testing.T) {
	cases := map[string]Value{"text": Text("a"), "int": Int(1), "decimal": dec(t, "1"), "bool": Bool(true), "date": testDate(), "datetime": epoch(), "null": Null()}
	for name, v := range cases {
		if v.KindName() != name {
			t.Errorf("%s: %s", name, v.KindName())
		}
		if !v.Equal(v) {
			t.Errorf("%s not equal to itself", name)
		}
	}
	if !dec(t, "1.0").Equal(dec(t, "1.00")) || Text("a").Equal(Int(1)) || Null().Equal(Text("")) {
		t.Error("equality")
	}
	var zero Value
	if zero.Kind != KindNull || !reflect.DeepEqual(zero, Null()) {
		t.Error("the zero Value is Null")
	}
}
