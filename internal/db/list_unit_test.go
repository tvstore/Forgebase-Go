package db

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tvstore/forgebase-go/internal/core"
)

// Unit tests of the list statement (no database). Ported from the #[cfg(test)] module of
// crates/forgebase-db/src/list.rs.

func listOrderType(t *testing.T) *core.DocType {
	return parse(t, "name = \"order\"\nlabel = \"O\"\nsubmittable = true\n"+
		"[[fields]]\nname = \"title\"\ntype = \"data\"\n"+
		"[[fields]]\nname = \"qty\"\ntype = \"int\"\n"+
		"[[fields]]\nname = \"done\"\ntype = \"check\"\n"+
		"[[fields]]\nname = \"customer\"\ntype = \"link\"\ntarget = \"customer\"\n"+
		"[[fields]]\nname = \"lines\"\ntype = \"table\"\ntarget = \"line\"\n")
}

func ltext(s string) core.Value { return core.Text(s) }

func lquery(filters ...Filter) ListQuery { return ListQuery{Filters: filters} }

// build is buildList with the SQL and the bound values as the Rust tests see them.
func build(t *testing.T, doctype *core.DocType, scope readScope, query ListQuery) (string, []core.Value, error) {
	t.Helper()
	sql, binds, err := buildList(doctype, scope, query)
	if err != nil {
		return "", nil, err
	}
	return sql, binds.values, nil
}

func buildOK(t *testing.T, doctype *core.DocType, scope readScope, query ListQuery) (string, []core.Value) {
	t.Helper()
	sql, binds, err := build(t, doctype, scope, query)
	if err != nil {
		t.Fatal(err)
	}
	return sql, binds
}

// queryError is the message of the InvalidQuery of a query on the order DocType.
func queryError(t *testing.T, q ListQuery) string {
	t.Helper()
	_, _, err := build(t, listOrderType(t), readScope{}, q)
	var e *Error
	if !errors.As(err, &e) || e.Kind != ErrInvalidQuery {
		t.Fatalf("expected InvalidQuery, got %v", err)
	}
	return e.Message
}

// Rust: list.rs::the_default_statement_orders_by_name_and_pages
func TestTheDefaultStatementOrdersByNameAndPages(t *testing.T) {
	sql, binds := buildOK(t, listOrderType(t), readScope{}, ListQuery{})
	if !strings.HasPrefix(sql, `SELECT "id", "name", "owner"`) {
		t.Error(sql)
	}
	if !strings.HasSuffix(sql, `FROM "doc_order" ORDER BY "doc_order"."name" ASC LIMIT $1::bigint OFFSET $2::bigint`) {
		t.Error(sql)
	}
	if !slices.EqualFunc(binds, []core.Value{core.Int(20), core.Int(0)}, core.Value.Equal) {
		t.Error(binds)
	}
}

// Rust: list.rs::an_owner_scope_comes_first_and_is_bound
func TestAnOwnerScopeComesFirstAndIsBound(t *testing.T) {
	q := lquery(Filter{"qty", Gt(core.Int(5))})
	sql, binds := buildOK(t, listOrderType(t), readScope{ownerOnly: true, owner: "ann'; DROP TABLE x; --"}, q)
	if !strings.Contains(sql, `WHERE "doc_order"."owner" = $1::text AND "doc_order"."qty" > $2::bigint ORDER BY`) {
		t.Error(sql)
	}
	if strings.Contains(sql, "DROP") {
		t.Error(sql)
	}
	if binds[0].Text != "ann'; DROP TABLE x; --" || binds[1].Int != 5 {
		t.Error(binds)
	}
}

// Rust: list.rs::user_permissions_are_bound_conditions_before_the_filters
func TestUserPermissionsAreBoundConditionsBeforeTheFilters(t *testing.T) {
	rows := core.RowScope{Links: []core.LinkScope{{Field: "customer", Target: "customer", Allowed: []string{"Retail", `a"b`}}}}
	q := lquery(Filter{"qty", Gt(core.Int(5))})
	sql, binds := buildOK(t, listOrderType(t), readScope{ownerOnly: true, owner: "ann", rows: rows}, q)
	if !strings.Contains(sql, `WHERE "doc_order"."owner" = $1::text `+
		`AND ("doc_order"."customer" IS NULL OR "doc_order"."customer" IN `+
		`(SELECT t."id" FROM "doc_customer" t WHERE t."name" = ANY($2::text::text[]))) `+
		`AND "doc_order"."qty" > $3::bigint ORDER BY`) {
		t.Error(sql)
	}
	if binds[0].Text != "ann" || binds[1].Text != `{"Retail","a\"b"}` || binds[2].Int != 5 {
		t.Error(binds)
	}
	// Without a scope the statement is the plain one.
	plain, _ := buildOK(t, listOrderType(t), readScope{}, q)
	if strings.Contains(plain, "ANY") || strings.Contains(plain, `"owner" =`) {
		t.Error(plain)
	}
}

// Rust: list.rs::every_operator_binds_its_values_with_a_cast
func TestEveryOperatorBindsItsValuesWithACast(t *testing.T) {
	q := lquery(
		Filter{"title", Eq(ltext("a"))},
		Filter{"title", Ne(ltext("b"))},
		Filter{"qty", Lt(core.Int(1))},
		Filter{"qty", Le(core.Int(2))},
		Filter{"qty", Ge(core.Int(3))},
		Filter{"qty", In(core.Int(4), core.Int(5))},
		Filter{"title", Like("a%")},
		Filter{"title", IsNull(true)},
		Filter{"version", IsNull(false)},
		Filter{"docstatus", Eq(core.Int(1))},
	)
	sql, binds := buildOK(t, listOrderType(t), readScope{}, q)
	tbl := `"doc_order"`
	expected := fmt.Sprintf(`WHERE %[1]s."title" = $1::varchar AND %[1]s."title" <> $2::varchar `+
		`AND %[1]s."qty" < $3::bigint AND %[1]s."qty" <= $4::bigint AND %[1]s."qty" >= $5::bigint `+
		`AND %[1]s."qty" IN ($6::bigint, $7::bigint) AND %[1]s."title" LIKE $8::text `+
		`AND %[1]s."title" IS NULL AND %[1]s."version" IS NOT NULL AND %[1]s."docstatus" = $9::bigint ORDER BY`, tbl)
	if !strings.Contains(sql, expected) {
		t.Error(sql)
	}
	if len(binds) != 9+2 {
		t.Error(len(binds))
	}
}

// Rust: list.rs::a_link_filter_compares_through_a_bound_subquery_on_the_target
func TestALinkFilterComparesThroughABoundSubqueryOnTheTarget(t *testing.T) {
	q := lquery(
		Filter{"customer", Eq(ltext("acme"))},
		Filter{"customer", Ne(ltext("zed"))},
		Filter{"customer", In(ltext("a"), ltext("b"))},
		Filter{"customer", IsNull(true)},
	)
	sql, _ := buildOK(t, listOrderType(t), readScope{}, q)
	c := `"doc_order"."customer"`
	sub := `(SELECT t."id" FROM "doc_customer" t WHERE t."name"`
	for _, want := range []string{
		fmt.Sprintf("%s IN %s = $1::text)", c, sub),
		fmt.Sprintf("%s IS NOT NULL AND %s NOT IN %s = $2::text))", c, c, sub),
		fmt.Sprintf("%s IN %s IN ($3::text, $4::text))", c, sub),
		fmt.Sprintf("%s IS NULL", c),
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("missing %s in %s", want, sql)
		}
	}
}

// Rust: list.rs::sorting_is_multi_field_with_name_last_and_links_sort_by_name
func TestSortingIsMultiFieldWithNameLastAndLinksSortByName(t *testing.T) {
	q := ListQuery{Sort: []Sort{Desc("qty"), Asc("customer"), Desc("owner")}}
	sql, _ := buildOK(t, listOrderType(t), readScope{}, q)
	if !strings.Contains(sql, `ORDER BY "doc_order"."qty" DESC, `+
		`(SELECT t."name" FROM "doc_customer" t WHERE t."id" = "doc_order"."customer") ASC, `+
		`"doc_order"."owner" DESC, "doc_order"."name" ASC LIMIT`) {
		t.Error(sql)
	}
}

// Rust: list.rs::identifiers_come_from_metadata_only
func TestIdentifiersComeFromMetadataOnly(t *testing.T) {
	for _, bad := range []string{"name; drop table doc_order", `title"`, "--", "qty or 1=1", "", "id", "amended_from", "NAME", "doc_order.name"} {
		if message := queryError(t, lquery(Filter{bad, IsNull(true)})); !strings.Contains(message, "unknown field") {
			t.Errorf("%q: %s", bad, message)
		}
		if message := queryError(t, ListQuery{Sort: []Sort{Asc(bad)}}); !strings.Contains(message, "unknown field") {
			t.Errorf("%q: %s", bad, message)
		}
	}
	// The text of a hostile name appears escaped in the message, never in SQL.
	if message := queryError(t, lquery(Filter{"a\nb", IsNull(true)})); !strings.Contains(message, `\n`) {
		t.Error(message)
	}
}

// Rust: list.rs::docstatus_exists_only_on_a_submittable_doctype
func TestDocstatusExistsOnlyOnASubmittableDoctype(t *testing.T) {
	plain := parse(t, "name = \"p\"\nlabel = \"P\"\n")
	q := lquery(Filter{"docstatus", IsNull(true)})
	if _, _, err := build(t, plain, readScope{}, q); err == nil {
		t.Error("a plain DocType has no docstatus")
	}
	buildOK(t, listOrderType(t), readScope{}, q)
}

// Rust: list.rs::table_fields_cannot_be_filtered_or_sorted
func TestTableFieldsCannotBeFilteredOrSorted(t *testing.T) {
	if message := queryError(t, lquery(Filter{"lines", IsNull(true)})); !strings.Contains(message, "table field") {
		t.Error(message)
	}
	if message := queryError(t, ListQuery{Sort: []Sort{Asc("lines")}}); !strings.Contains(message, "table field") {
		t.Error(message)
	}
}

// Rust: list.rs::operators_must_fit_the_field
func TestOperatorsMustFitTheField(t *testing.T) {
	for _, c := range []struct {
		filter Filter
		needle string
	}{
		{Filter{"qty", Like("1%")}, "text fields only"},
		{Filter{"done", Like("t%")}, "text fields only"},
		{Filter{"customer", Like("a%")}, "text fields only"},
		{Filter{"version", Like("1%")}, "text fields only"},
		{Filter{"done", Lt(core.Bool(true))}, "does not apply"},
		{Filter{"customer", Gt(ltext("a"))}, "does not apply"},
		{Filter{"qty", Eq(ltext("1"))}, "expected a int value"},
		{Filter{"qty", Eq(core.Null())}, "cannot be null"},
		{Filter{"title", Eq(core.Int(1))}, "expected a data value"},
		{Filter{"title", Eq(ltext("a\x00b"))}, "NUL"},
		{Filter{"title", Like("a\x00")}, "NUL"},
		{Filter{"qty", In()}, "needs 1 to"},
		{Filter{"qty", In(core.Int(1), ltext("x"))}, "expected"},
		{Filter{"created_at", Gt(core.Int(1))}, "expected a datetime"},
	} {
		if message := queryError(t, lquery(c.filter)); !strings.Contains(message, c.needle) {
			t.Errorf("%+v: %s", c.filter, message)
		}
	}
	// The exact messages, as Rust writes them.
	for _, c := range []struct {
		filter  Filter
		message string
	}{
		{Filter{"qty", Eq(ltext("1"))}, `field "qty" is int: expected a int value, found text`},
		{Filter{"qty", Eq(core.Null())}, "field \"qty\": a filter value cannot be null (use `is null`)"},
		{Filter{"done", Lt(core.Bool(true))}, `operator < does not apply to check field "done"`},
		{Filter{"qty", Like("1%")}, "`like` applies to text fields only, but field \"qty\" is int"},
		{Filter{"nope", IsNull(true)}, "unknown field \"nope\" for DocType `order`"},
		{Filter{"lines", IsNull(true)}, `field "lines" is a table field: its rows cannot be filtered or sorted`},
	} {
		if got := queryError(t, lquery(c.filter)); got != c.message {
			t.Errorf("%q, want %q", got, c.message)
		}
	}
	many := make([]core.Value, MaxInValues+1)
	for i := range many {
		many[i] = core.Int(1)
	}
	if message := queryError(t, lquery(Filter{"qty", In(many...)})); !strings.Contains(message, "needs 1 to") {
		t.Error(message)
	}
	buildOK(t, listOrderType(t), readScope{}, lquery(Filter{"qty", In(many[:MaxInValues]...)}))
	// A datetime out of range once converted to UTC is refused before it is bound.
	far := core.Datetime(time.Date(10001, 1, 1, 0, 0, 0, 0, time.UTC))
	if message := queryError(t, lquery(Filter{"created_at", Gt(far)})); !strings.Contains(message, "out of range") {
		t.Error(message)
	}
}

// Rust: list.rs::limit_and_offset_are_checked_not_clamped
func TestLimitAndOffsetAreCheckedNotClamped(t *testing.T) {
	with := func(limit *uint32, offset *uint64) ListQuery { return ListQuery{Limit: limit, Offset: offset} }
	u32 := func(n uint32) *uint32 { return &n }
	u64 := func(n uint64) *uint64 { return &n }
	for _, c := range []struct {
		q      ListQuery
		needle string
	}{
		{with(u32(0), nil), "at least 1"},
		{with(u32(501), nil), "maximum of 500"},
		{with(u32(math.MaxUint32), nil), "maximum"},
		{with(nil, u64(math.MaxUint64)), "offset"},
	} {
		if message := queryError(t, c.q); !strings.Contains(message, c.needle) {
			t.Errorf("%+v: %s", c.q, message)
		}
	}
	_, binds := buildOK(t, listOrderType(t), readScope{}, with(u32(500), u64(7)))
	if binds[0].Int != 500 || binds[1].Int != 7 {
		t.Error(binds)
	}
	_, binds = buildOK(t, listOrderType(t), readScope{}, with(u32(1), u64(math.MaxInt64)))
	if binds[1].Int != math.MaxInt64 {
		t.Error(binds)
	}
}

// Rust: list.rs::column_types
func TestColumnTypes(t *testing.T) {
	d := listOrderType(t)
	for _, c := range []struct {
		field string
		kind  core.FieldKind
		ok    bool
	}{
		{"qty", core.FieldInt, true},
		{"owner", core.FieldText, true},
		{"created_at", core.FieldDatetime, true},
		{"docstatus", core.FieldInt, true},
		{"lines", 0, false},
		{"nope", 0, false},
	} {
		ft, ok := ColumnType(d, c.field)
		if ok != c.ok || ft.Kind != c.kind {
			t.Errorf("%s: %v %v", c.field, ft, ok)
		}
	}
}

// Rust: list.rs::the_evaluator_is_the_only_source_of_the_owner_scope
func TestTheEvaluatorIsTheOnlySourceOfTheOwnerScope(t *testing.T) {
	// List derives the scope from Evaluate; check the mapping it relies on.
	d := listOrderType(t)
	if core.Evaluate(d, core.SystemCaller("cli"), core.RightRead) != core.AccessAll {
		t.Error("the system reads everything")
	}
	if core.Evaluate(d, core.UserCaller("u", nil), core.RightRead) != core.AccessDenied {
		t.Error("a user without a rule reads nothing")
	}
}
