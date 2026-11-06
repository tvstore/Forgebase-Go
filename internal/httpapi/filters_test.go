package httpapi

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/tvstore/forgebase-go/internal/core"
	"github.com/tvstore/forgebase-go/internal/db"
)

// Go addition: the Rust parse_filters, parse_sort and check_filters_syntax have no unit tests of their
// own (cli.rs and the HTTP tests of crates/forgebase and forgebase-http run them end to end, ported in
// internal/app and, from P3.2, here). The messages are the Rust ones.

const filterType = `
name = "t"
label = "T"
[[fields]]
name = "d"
type = "data"
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
name = "when"
type = "datetime"
[[fields]]
name = "link"
type = "link"
target = "other"
[[fields]]
name = "rows"
type = "table"
target = "row"
`

func filterDoctype(t *testing.T) *core.DocType {
	t.Helper()
	d, err := core.ParseDocType(filterType)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func filterError(t *testing.T, text string) string {
	t.Helper()
	_, err := ParseFilters(filterDoctype(t), text)
	if err == nil {
		t.Fatalf("%s: expected an error", text)
	}
	return err.Error()
}

func TestEveryOperatorBecomesItsFilter(t *testing.T) {
	filters, err := ParseFilters(filterDoctype(t), `[
		{"field":"d","op":"=","value":"x"},
		{"field":"i","op":"!=","value":1},
		{"field":"i","op":"<","value":2},
		{"field":"i","op":"<=","value":3},
		{"field":"i","op":">","value":4},
		{"field":"i","op":">=","value":5},
		{"field":"i","op":"in","value":[6,7]},
		{"field":"d","op":"like","value":"a%"},
		{"field":"d","op":"is null"},
		{"field":"d","op":"is not null"},
		{"field":"m","op":"=","value":"1.50"},
		{"field":"c","op":"=","value":true},
		{"field":"when","op":">","value":"2024-03-15T10:20:30Z"},
		{"field":"link","op":"=","value":"acme"},
		{"field":"version","op":"=","value":2},
		{"field":"created_at","op":">","value":"2024-03-15T10:20:30Z"}
	]`)
	if err != nil {
		t.Fatal(err)
	}
	kinds := []db.FilterOpKind{db.OpEq, db.OpNe, db.OpLt, db.OpLe, db.OpGt, db.OpGe, db.OpIn, db.OpLike, db.OpIsNull, db.OpIsNull,
		db.OpEq, db.OpEq, db.OpGt, db.OpEq, db.OpEq, db.OpGt}
	if len(filters) != len(kinds) {
		t.Fatalf("%d filters", len(filters))
	}
	for i, f := range filters {
		if f.Op.Kind != kinds[i] {
			t.Errorf("filter %d: %+v", i+1, f)
		}
	}
	if len(filters[6].Op.Values) != 2 || filters[6].Op.Values[1].Int != 7 || filters[7].Op.Pattern != "a%" ||
		!filters[8].Op.Null || filters[9].Op.Null {
		t.Errorf("%+v", filters)
	}
	if filters[10].Op.Value.Kind != core.KindDecimal || filters[11].Op.Value.Kind != core.KindBool ||
		filters[12].Op.Value.Kind != core.KindDatetime || filters[13].Op.Value.Text != "acme" {
		t.Errorf("%+v", filters)
	}
	// No filters, no problem.
	if got, err := ParseFilters(filterDoctype(t), "[]"); err != nil || len(got) != 0 {
		t.Errorf("%v %v", got, err)
	}
}

func TestFilterProblemsNameTheFilterAndTheProblem(t *testing.T) {
	for _, c := range []struct{ text, message string }{
		{`{}`, "invalid filter: expected a JSON array of filters"},
		{`"x"`, "invalid filter: expected a JSON array of filters"},
		{`[1]`, "invalid filter: filter 1: expected an object, found a number"},
		{`[{"field":"d","op":"=","value":"a"},[]]`, "invalid filter: filter 2: expected an object, found an array"},
		{`[{"field":"d","op":"=","value":"a","x":1}]`, "invalid filter: filter 1: unknown key `x` (use field, op, value)"},
		{`[{"op":"="}]`, "invalid filter: filter 1: `field` must be a string"},
		{`[{"field":"d"}]`, "invalid filter: filter 1: `op` must be a string"},
		{`[{"field":1,"op":"="}]`, "invalid filter: filter 1: `field` must be a string"},
		{`[{"field":"nope","op":"=","value":1}]`, "invalid filter: filter 1: unknown field \"nope\" (table fields cannot be filtered)"},
		{`[{"field":"rows","op":"is null"}]`, "invalid filter: filter 1: unknown field \"rows\" (table fields cannot be filtered)"},
		{`[{"field":"d","op":"="}]`, "invalid filter: filter 1: operator `=` needs a `value`"},
		{`[{"field":"d","op":"~","value":1}]`, "invalid filter: filter 1: unknown operator `~` (use =, !=, <, <=, >, >=, in, like, is null, is not null)"},
		{`[{"field":"i","op":"in","value":1}]`, "invalid filter: filter 1: operator `in` needs a `value` array"},
		{`[{"field":"i","op":"in"}]`, "invalid filter: filter 1: operator `in` needs a `value` array"},
		{`[{"field":"d","op":"like","value":1}]`, "invalid filter: filter 1: operator `like` needs a string `value`"},
		{`[{"field":"d","op":"is null","value":null}]`, "invalid filter: filter 1: operator `is null` takes no `value`"},
		{`[{"field":"i","op":"=","value":"1"}]`, "field `i`: expected an integer, found a string"},
		{`[{"field":"m","op":"=","value":1}]`, "field `m`: a decimal must be a JSON string such as \"12.50\", not a number"},
		{`[{"field":"i","op":"in","value":[1,"x"]}]`, "field `i`: expected an integer, found a string"},
	} {
		if got := filterError(t, c.text); got != c.message {
			t.Errorf("%s\n got %q\nwant %q", c.text, got, c.message)
		}
	}
	if got := filterError(t, "{bad"); !strings.HasPrefix(got, "invalid JSON") {
		t.Error(got)
	}
	// A hostile key or field is shortened in the message.
	long := strings.Repeat("k", 200)
	if got := filterError(t, fmt.Sprintf(`[{"field":"d","op":"=","value":"a","%s":1}]`, long)); len(got) > 200 || !strings.Contains(got, "…") {
		t.Error(got)
	}
	// The first problem is the same whatever the order of the keys.
	if got := filterError(t, `[{"zz":1,"yy":2}]`); !strings.Contains(got, "unknown key `yy`") {
		t.Error(got)
	}
}

func TestTheSyntaxCheckNeedsNoDocType(t *testing.T) {
	for _, ok := range []string{`[]`, `[{"field":"anything","op":"whatever","value":[1,{"a":2}]}]`, `[{"field":"a","op":"is null"}]`} {
		if err := CheckFiltersSyntax(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, c := range []struct{ text, message string }{
		{`{bad`, "invalid JSON"},
		{`{"field":"a"}`, "invalid filter: expected a JSON array of filters"},
		{`[{"field":"a"}]`, "invalid filter: filter 1: `op` must be a string"},
		{`[{"op":"="}]`, "invalid filter: filter 1: `field` must be a string"},
		{`[true]`, "invalid filter: filter 1: expected an object, found a boolean"},
		{`[{"field":"a","op":"=","extra":1}]`, "invalid filter: filter 1: unknown key `extra` (use field, op, value)"},
	} {
		err := CheckFiltersSyntax(c.text)
		if err == nil || !strings.HasPrefix(err.Error(), c.message) {
			t.Errorf("%s: %v", c.text, err)
		}
	}
}

func TestSortSpecs(t *testing.T) {
	got, err := ParseSort([]string{"a", "b:asc", "c:desc"})
	if err != nil || !reflect.DeepEqual(got, []db.Sort{db.Asc("a"), db.Asc("b"), db.Desc("c")}) {
		t.Errorf("%v %v", got, err)
	}
	for _, bad := range []string{"a:sideways", "a:", "a:ASC", "a:desc:x"} {
		_, err := ParseSort([]string{bad})
		if err == nil || err.Error() != fmt.Sprintf("invalid sort %q: use FIELD, FIELD:asc or FIELD:desc", bad) {
			t.Errorf("%q: %v", bad, err)
		}
	}
	// An empty list is no sort; an empty field is the database's problem (an unknown field).
	if got, err := ParseSort(nil); err != nil || len(got) != 0 {
		t.Errorf("%v %v", got, err)
	}
	if got, _ := ParseSort([]string{""}); len(got) != 1 || got[0].Field != "" {
		t.Errorf("%v", got)
	}
}
