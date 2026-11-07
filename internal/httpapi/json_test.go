package httpapi

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/tvstore/forgebase-go/internal/core"
	"github.com/tvstore/forgebase-go/internal/decimal"
)

// Ported from crates/forgebase-http/src/json.rs (the tests of the input mapping; the output mapping is
// tested in internal/db, where it lives, and the filters and sorts are tested in filters_test.go).

const mixed = `
name = "t"
label = "T"
[[fields]]
name = "d"
type = "data"
[[fields]]
name = "x"
type = "text"
[[fields]]
name = "i"
type = "int"
[[fields]]
name = "m"
type = "decimal"
precision = 12
scale = 2
[[fields]]
name = "c"
type = "check"
[[fields]]
name = "on"
type = "date"
[[fields]]
name = "at"
type = "datetime"
[[fields]]
name = "s"
type = "select"
options = ["a", "b"]
[[fields]]
name = "l"
type = "link"
target = "other"
`

func mixedType(t *testing.T) *core.DocType {
	t.Helper()
	d, err := core.ParseDocType(mixed)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func one(t *testing.T, text string) (map[string]core.Value, error) {
	t.Helper()
	input, err := ParseInput(core.NewRegistry(), mixedType(t), text)
	if err != nil {
		return nil, err
	}
	return input.Values, nil
}

func mustOne(t *testing.T, text string) map[string]core.Value {
	t.Helper()
	values, err := one(t, text)
	if err != nil {
		t.Fatalf("%s: %v", text, err)
	}
	return values
}

func message(t *testing.T, text string) string {
	t.Helper()
	_, err := one(t, text)
	if err == nil {
		t.Fatalf("%s must be refused", text)
	}
	return err.Error()
}

func kindOf(err error) JSONErrorKind {
	var je *JSONError
	if errors.As(err, &je) {
		return je.Kind
	}
	return 0
}

// Rust: json.rs::maps_every_type
func TestMapsEveryType(t *testing.T) {
	values := mustOne(t, `{"d":"x","x":"y","i":-5,"m":"12.50","c":true,"on":"2024-03-15",
                "at":"2024-03-15T10:20:30.5+07:00","s":"a"}`)
	dec, _ := decimal.Parse("12.50")
	want := map[string]core.Value{
		"d": core.Text("x"), "x": core.Text("y"), "i": core.Int(-5), "m": core.Decimal(dec), "c": core.Bool(true),
		"on": core.NewDate(2024, time.March, 15),
		"at": core.Datetime(time.Date(2024, time.March, 15, 3, 20, 30, 500_000_000, time.UTC)),
		"s":  core.Text("a"),
	}
	for name, w := range want {
		if !values[name].Equal(w) {
			t.Errorf("%s = %+v, want %+v", name, values[name], w)
		}
	}
	if len(values) != len(want) {
		t.Errorf("%d values", len(values))
	}
}

// Rust: json.rs::links_are_target_names
func TestLinksAreTargetNames(t *testing.T) {
	if v := mustOne(t, `{"l":"ann@example.com"}`)["l"]; !v.Equal(core.Text("ann@example.com")) {
		t.Fatalf("%+v", v)
	}
	if m := message(t, `{"l":5}`); !strings.Contains(m, "expected a string, found a number") {
		t.Fatal(m)
	}
	if v := mustOne(t, `{"l":null}`)["l"]; v.Kind != core.KindNull {
		t.Fatalf("%+v", v)
	}
}

// Rust: json.rs::null_maps_to_null_for_every_type
func TestNullMapsToNullForEveryType(t *testing.T) {
	values := mustOne(t, `{"d":null,"i":null,"m":null,"c":null,"on":null,"at":null}`)
	if len(values) != 6 {
		t.Fatalf("%d values", len(values))
	}
	for name, v := range values {
		if v.Kind != core.KindNull {
			t.Errorf("%s: %+v", name, v)
		}
	}
}

// Rust: json.rs::integers_are_exact
func TestIntegersAreExact(t *testing.T) {
	if v := mustOne(t, fmt.Sprintf(`{"i":%d}`, int64(math.MaxInt64)))["i"]; v.Int != math.MaxInt64 {
		t.Fatalf("%+v", v)
	}
	if v := mustOne(t, fmt.Sprintf(`{"i":%d}`, int64(math.MinInt64)))["i"]; v.Int != math.MinInt64 {
		t.Fatalf("%+v", v)
	}
	for _, bad := range []string{"1.5", "1.0", "1e3", "9223372036854775808", "-9223372036854775809", `"5"`, "true"} {
		if m := message(t, `{"i":`+bad+`}`); !strings.Contains(m, "field `i`") {
			t.Errorf("%s: %s", bad, m)
		}
	}
}

// Rust: json.rs::decimals_must_be_strings
func TestDecimalsMustBeStrings(t *testing.T) {
	if m := message(t, `{"m":12.5}`); !strings.Contains(m, "JSON string") {
		t.Error(m)
	}
	if m := message(t, `{"m":"abc"}`); !strings.Contains(m, "not a decimal") {
		t.Error(m)
	}
	if m := message(t, `{"m":true}`); !strings.Contains(m, "expected a decimal string") {
		t.Error(m)
	}
	// Precision is kept exactly; scale rules are the validator's job.
	want, _ := decimal.Parse("0.1234567890123456789")
	if v := mustOne(t, `{"m":"0.1234567890123456789"}`)["m"]; !v.Equal(core.Decimal(want)) {
		t.Fatalf("%+v", v)
	}
}

// Rust: json.rs::wrong_kinds_are_rejected
func TestWrongKindsAreRejected(t *testing.T) {
	for _, c := range [][2]string{
		{`{"d":5}`, "expected a string, found a number"},
		{`{"c":"yes"}`, "expected true or false"},
		{`{"s":["a"]}`, "found an array"},
		{`{"x":{"a":1}}`, "found an object"},
	} {
		if m := message(t, c[0]); !strings.Contains(m, c[1]) {
			t.Errorf("%s: %s", c[0], m)
		}
	}
}

// Rust: json.rs::dates_and_datetimes_are_checked
func TestDatesAndDatetimesAreChecked(t *testing.T) {
	for _, bad := range []string{"2024-3-5", "2024-02-30", "15/03/2024", "2024-03-15T00:00:00Z", "", "20240315"} {
		if m := message(t, `{"on":"`+bad+`"}`); !strings.Contains(m, "YYYY-MM-DD") {
			t.Errorf("%q: %s", bad, m)
		}
	}
	if m := message(t, `{"on":20240315}`); !strings.Contains(m, "expected a date string") {
		t.Error(m)
	}
	for _, bad := range []string{"2024-03-15", "2024-03-15 10:00:00", "2024-03-15T10:00:00", "yesterday"} {
		if m := message(t, `{"at":"`+bad+`"}`); !strings.Contains(m, "RFC 3339") {
			t.Errorf("%q: %s", bad, m)
		}
	}
}

// Rust: json.rs::input_must_be_an_object
func TestInputMustBeAnObject(t *testing.T) {
	for _, text := range []string{"[1]", `"x"`, "null"} {
		if _, err := one(t, text); kindOf(err) != JSONNotAnObject {
			t.Errorf("%s: %v", text, err)
		}
	}
	for _, text := range []string{"{", "", `{"a":1} x`, `{"a":1}{"b":2}`} {
		if _, err := one(t, text); kindOf(err) != JSONSyntax || !strings.HasPrefix(err.Error(), "invalid JSON: ") {
			t.Errorf("%q: %v", text, err)
		}
	}
	if err := CheckInputSyntax("[1]"); kindOf(err) != JSONNotAnObject {
		t.Error(err)
	}
	if err := CheckInputSyntax("{bad"); kindOf(err) != JSONSyntax {
		t.Error(err)
	}
	if err := CheckInputSyntax(` {"a": 1} `); err != nil {
		t.Error(err)
	}
}

// Rust: json.rs::unknown_keys_are_passed_through_for_validation
func TestUnknownKeysArePassedThroughForValidation(t *testing.T) {
	values := mustOne(t, `{"nope":[1,2],"d":"x"}`)
	if values["nope"].Kind != core.KindNull || len(values) != 2 {
		t.Fatalf("%+v", values)
	}
}

// A duplicate key is accepted and the last one wins (serde_json behaviour).
func TestADuplicateKeyTheLastWins(t *testing.T) {
	if v := mustOne(t, `{"d":"first","d":"last"}`)["d"]; v.Text != "last" {
		t.Fatalf("%+v", v)
	}
}

func orderRegistry(t *testing.T) (*core.Registry, *core.DocType) {
	t.Helper()
	registry := core.NewRegistry()
	for _, text := range []string{
		"name = \"line\"\nlabel = \"L\"\nis_child = true\n[[fields]]\nname = \"qty\"\ntype = \"int\"\n" +
			"[[fields]]\nname = \"price\"\ntype = \"decimal\"\nprecision = 10\nscale = 2\n",
		"name = \"ord\"\nlabel = \"O\"\n[[fields]]\nname = \"title\"\ntype = \"data\"\n" +
			"[[fields]]\nname = \"lines\"\ntype = \"table\"\ntarget = \"line\"\n",
	} {
		d, err := core.ParseDocType(text)
		if err != nil {
			t.Fatal(err)
		}
		if errs := registry.Add(d); errs != nil {
			t.Fatal(errs)
		}
	}
	return registry, registry.Get("ord")
}

// Rust: json.rs::table_fields_are_arrays_of_row_objects
func TestTableFieldsAreArraysOfRowObjects(t *testing.T) {
	registry, ord := orderRegistry(t)
	input, err := ParseInput(registry, ord, `{"title":"T","lines":[{"qty":1,"price":"2.50"},{"qty":2},{}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(input.Values) != 1 {
		t.Fatalf("%+v", input.Values)
	}
	rows := input.Tables["lines"]
	price, _ := decimal.Parse("2.50")
	if len(rows) != 3 || rows[0]["qty"].Int != 1 || !rows[0]["price"].Equal(core.Decimal(price)) || len(rows[2]) != 0 {
		t.Fatalf("%+v", rows)
	}
	// null and [] are both "no rows"; unknown row keys are passed on for validation.
	for _, text := range []string{`{"lines":null}`, `{"lines":[]}`} {
		input, err := ParseInput(registry, ord, text)
		if err != nil {
			t.Fatal(err)
		}
		if rows, ok := input.Tables["lines"]; !ok || len(rows) != 0 {
			t.Errorf("%s: %+v", text, input.Tables)
		}
	}
	input, err = ParseInput(registry, ord, `{"lines":[{"nope":1}]}`)
	if err != nil || input.Tables["lines"][0]["nope"].Kind != core.KindNull {
		t.Fatalf("%v %+v", err, input)
	}
}

// Rust: json.rs::table_input_errors_name_the_field_and_the_row
func TestTableInputErrorsNameTheFieldAndTheRow(t *testing.T) {
	registry, ord := orderRegistry(t)
	msg := func(text string) string {
		_, err := ParseInput(registry, ord, text)
		if err == nil {
			t.Fatalf("%s must be refused", text)
		}
		return err.Error()
	}
	for _, c := range [][2]string{
		{`{"lines":"x"}`, "field `lines`: expected an array of rows, found a string"},
		{`{"lines":{"qty":1}}`, "field `lines`: expected an array of rows, found an object"},
		{`{"lines":[{"qty":1},5]}`, "field `lines`: row 2: expected an object, found a number"},
		{`{"lines":[{"qty":1},{"qty":"two"}]}`, "field `lines`: row 2: field `qty`: expected an integer, found a string"},
	} {
		if got := msg(c[0]); got != c[1] {
			t.Errorf("%s: %q", c[0], got)
		}
	}
	if m := msg(`{"lines":[{"price":1.5}]}`); !strings.Contains(m, "row 1: field `price`") {
		t.Error(m)
	}
}

// Rust: json.rs::a_child_table_in_the_registry_is_needed_for_rows
func TestAChildTableInTheRegistryIsNeededForRows(t *testing.T) {
	_, ord := orderRegistry(t)
	_, err := ParseInput(core.NewRegistry(), ord, `{"lines":[{}]}`)
	if err == nil || !strings.Contains(err.Error(), "unknown child doctype `line`") {
		t.Fatalf("%v", err)
	}
	// But without rows nothing is looked up.
	if _, err := ParseInput(core.NewRegistry(), ord, `{"lines":null}`); err != nil {
		t.Fatal(err)
	}
}

// The first problem is always the same one: keys are read in sorted order.
func TestTheFirstProblemReportedDoesNotDependOnMapOrder(t *testing.T) {
	for i := 0; i < 20; i++ {
		if m := message(t, `{"on":"x","i":"y","d":5}`); !strings.Contains(m, "field `d`") {
			t.Fatalf("%s", m)
		}
	}
}

// The wording and position of the syntax errors are serde_json 1.0.154's (jsonparse.go mirrors its de.rs
// and read.rs). The expected texts were derived from that source, case by case.
func TestSyntaxErrorsReadLikeSerdeJSON(t *testing.T) {
	for _, c := range [][2]string{
		{``, "EOF while parsing a value at line 1 column 0"},
		{`   `, "EOF while parsing a value at line 1 column 3"},
		{`{`, "EOF while parsing an object at line 1 column 1"},
		{`{"a":1`, "EOF while parsing an object at line 1 column 6"},
		{`{bad`, "key must be a string at line 1 column 2"},
		{`{"a" 1}`, "expected `:` at line 1 column 6"},
		{`{"a":1 "b":2}`, "expected `,` or `}` at line 1 column 8"},
		{`{"a":1,}`, "trailing comma at line 1 column 8"},
		{`{"a":1,`, "EOF while parsing a value at line 1 column 7"},
		{`{"a":1,1:2}`, "key must be a string at line 1 column 8"},
		{`[`, "EOF while parsing a list at line 1 column 1"},
		{`[1 2]`, "expected `,` or `]` at line 1 column 4"},
		{`[1,]`, "trailing comma at line 1 column 4"},
		{`[,]`, "expected value at line 1 column 2"},
		{`{"a":1} x`, "trailing characters at line 1 column 9"},
		{`{"a":1}{`, "trailing characters at line 1 column 8"},
		{`x`, "expected value at line 1 column 1"},
		{"{\n\"a\": ?}", "expected value at line 2 column 6"},
		{`tru`, "EOF while parsing a value at line 1 column 3"},
		{`nul!`, "expected ident at line 1 column 4"},
		{`"abc`, "EOF while parsing a string at line 1 column 4"},
		{`"a\qb"`, "invalid escape at line 1 column 4"},
		{`"a\u12zz"`, "invalid escape at line 1 column 8"},
		{`"a\u12"`, "EOF while parsing a string at line 1 column 7"},
		{`"a\u12`, "EOF while parsing a string at line 1 column 6"},
		{`"\ud800x"`, "unexpected end of hex escape at line 1 column 8"},
		{`"\udc00"`, "lone leading surrogate in hex escape at line 1 column 7"},
		{"\"a\x01\"", "control character (\\u0000-\\u001F) found while parsing a string at line 1 column 3"},
		{"{\"a\":\"x\x01\"}", "control character (\\u0000-\\u001F) found while parsing a string at line 1 column 8"},
		{`01`, "invalid number at line 1 column 2"},
		{`-`, "EOF while parsing a value at line 1 column 1"},
		{`-x`, "invalid number at line 1 column 2"},
		{`1.`, "EOF while parsing a value at line 1 column 2"},
		{`1.x`, "invalid number at line 1 column 3"},
		{`1e`, "EOF while parsing a value at line 1 column 2"},
		{`1e+x`, "invalid number at line 1 column 4"},
		{`1e999`, "number out of range at line 1 column 5"},
		{`1e99999999999`, "number out of range at line 1 column 12"},
		{`1e+99999999999`, "number out of range at line 1 column 13"},
		{strings.Repeat("[", 128), "recursion limit exceeded at line 1 column 128"},
	} {
		_, err := parseJSON(c[0])
		if err == nil || err.Error() != c[1] {
			t.Errorf("%q: got %v, want %s", c[0], err, c[1])
		}
	}
	// The error of ParseInput is that text after "invalid JSON: ".
	_, err := one(t, `{bad`)
	if err == nil || err.Error() != "invalid JSON: key must be a string at line 1 column 2" {
		t.Errorf("%v", err)
	}
	// 127 levels are fine, and every valid kind of text parses.
	if _, err := parseJSON(strings.Repeat("[", 127) + strings.Repeat("]", 127)); err != nil {
		t.Errorf("%v", err)
	}
	// An exponent that overflows i32 is a zero when the number is zero or the exponent negative.
	for _, ok := range []string{`0e99999999999`, `1e-99999999999`, `-0e+99999999999`} {
		if _, err := parseJSON(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, ok := range []string{`null`, ` true `, `{"a":[1,2.5,-3e2,"xé😀\n"],"b":{}}`, `[]`, `"\/"`} {
		if _, err := parseJSON(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	v, _ := parseJSON(`"é😀"`)
	if v != "é😀" {
		t.Errorf("%q", v)
	}
}

// Number's Display, which the message about an integer out of range uses.
func TestNumbersDisplayLikeSerdeJSON(t *testing.T) {
	for _, c := range [][2]string{
		{`1.5`, "1.5"}, {`1.0`, "1.0"}, {`1e3`, "1000.0"}, {`-2.50`, "-2.5"}, {`1e21`, "1e+21"}, {`1e16`, "1e+16"},
		{`1e15`, "1000000000000000.0"}, {`1.5e-7`, "1.5e-7"}, {`0.00001`, "0.00001"}, {`0.000001`, "1e-6"}, {`6.62607015e-34`, "6.62607015e-34"}, {`3.439070283483335e+35`, "3.439070283483335e+35"},
		{`-0`, "-0.0"}, {`0.0`, "0.0"}, {`9223372036854775808`, "9223372036854775808"},
		{`-9223372036854775809`, "-9.223372036854776e+18"}, {`18446744073709551616`, "1.8446744073709552e+19"},
		{`123456.789`, "123456.789"}, {`5`, "5"}, {`-5`, "-5"},
	} {
		v, err := parseJSON(c[0])
		if err != nil {
			t.Errorf("%s: %v", c[0], err)
			continue
		}
		if got := v.(jsonNumber).String(); got != c[1] {
			t.Errorf("%s displays %s, want %s", c[0], got, c[1])
		}
	}
	if m := message(t, `{"i":1e3}`); !strings.Contains(m, "1000.0 is not an integer in the range") {
		t.Error(m)
	}
	if m := message(t, `{"i":9223372036854775808}`); !strings.Contains(m, "9223372036854775808 is not an integer") {
		t.Error(m)
	}
}
