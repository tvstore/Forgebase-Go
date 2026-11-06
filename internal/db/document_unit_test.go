package db

import (
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/tvstore/forgebase-go/internal/core"
)

// Unit tests of the statements and helpers of the document operations (no database). Ported from
// the #[cfg(test)] module of crates/forgebase-db/src/document.rs, and value.rs, json.rs and scope.rs.

func parse(t *testing.T, text string) *core.DocType {
	t.Helper()
	d, err := core.ParseDocType(text)
	if err != nil {
		t.Fatalf("valid DocType: %v", err)
	}
	return d
}

func unitCustomer(t *testing.T) *core.DocType {
	return parse(t, "name = \"customer\"\nlabel = \"C\"\nnaming = { field = \"email\" }\n"+
		"[[fields]]\nname = \"email\"\ntype = \"data\"\nrequired = true\nunique = true\n"+
		"[[fields]]\nname = \"points\"\ntype = \"int\"\n")
}

func unitOrder(t *testing.T) *core.DocType {
	return parse(t, "name = \"order\"\nlabel = \"O\"\n"+
		"[[fields]]\nname = \"customer\"\ntype = \"link\"\ntarget = \"customer\"\n"+
		"[[fields]]\nname = \"parent\"\ntype = \"link\"\ntarget = \"order\"\n")
}

func unitLine(t *testing.T) *core.DocType {
	return parse(t, "name = \"line\"\nlabel = \"L\"\nis_child = true\n"+
		"[[fields]]\nname = \"qty\"\ntype = \"int\"\n"+
		"[[fields]]\nname = \"customer\"\ntype = \"link\"\ntarget = \"customer\"\n")
}

func unitWithTable(t *testing.T) *core.DocType {
	return parse(t, "name = \"ord\"\nlabel = \"O\"\n"+
		"[[fields]]\nname = \"title\"\ntype = \"data\"\n"+
		"[[fields]]\nname = \"lines\"\ntype = \"table\"\ntarget = \"line\"\n"+
		"[[fields]]\nname = \"note\"\ntype = \"text\"\n")
}

func unitQuote(t *testing.T) *core.DocType {
	return parse(t, "name = \"quote\"\nlabel = \"Q\"\nsubmittable = true\n"+
		"[[fields]]\nname = \"title\"\ntype = \"data\"\n")
}

func unitFlow(t *testing.T) *core.DocType {
	return parse(t, "name = \"flow\"\nlabel = \"F\"\nsubmittable = true\n"+
		"[[fields]]\nname = \"title\"\ntype = \"data\"\n"+
		"[workflow]\n[[workflow.states]]\nname = \"Open\"\ndocstatus = 0\n")
}

func eq(t *testing.T, got, want, label string) {
	t.Helper()
	if got != want {
		t.Errorf("%s:\n got %s\nwant %s", label, got, want)
	}
}

func has(t *testing.T, s, part, label string) {
	t.Helper()
	if !strings.Contains(s, part) {
		t.Errorf("%s: %s does not contain %s", label, s, part)
	}
}

// Rust: document.rs::insert_sql_binds_every_value_with_a_cast
func TestInsertSQLBindsEveryValueWithACast(t *testing.T) {
	eq(t, insertSQL(unitCustomer(t), false),
		"INSERT INTO \"doc_customer\" (\"id\", \"name\", \"owner\", \"created_at\", \"modified_at\", "+
			"\"modified_by\", \"version\", \"email\", \"points\") VALUES ($1::uuid, $2::text, $3::text, now(), "+
			"now(), $3::text, 1, $4::varchar, $5::bigint) RETURNING \"id\", \"name\", \"owner\", \"created_at\", "+
			"\"modified_at\", \"modified_by\", \"version\", \"email\", \"points\"", "insert")
}

// Rust: document.rs::update_sql_numbers_placeholders_in_key_order
func TestUpdateSQLNumbersPlaceholdersInKeyOrder(t *testing.T) {
	sql := updateSQL(unitCustomer(t), map[string]core.Value{"points": core.Int(1), "email": core.Null()}, nil)
	prefix := "UPDATE \"doc_customer\" SET \"email\" = $1::varchar, \"points\" = $2::bigint, " +
		"\"modified_at\" = now(), \"modified_by\" = $3::text, \"version\" = \"version\" + 1 " +
		"WHERE \"name\" = $4::text AND \"version\" = $5::bigint RETURNING "
	if !strings.HasPrefix(sql, prefix) {
		t.Error(sql)
	}
}

// Rust: document.rs::links_are_written_as_uuid_and_read_back_as_names
func TestLinksAreWrittenAsUUIDAndReadBackAsNames(t *testing.T) {
	sql := insertSQL(unitOrder(t), false)
	has(t, sql, "$4::uuid, $5::uuid) RETURNING", "bound as ids")
	// The INSERT column list is plain; only RETURNING resolves names.
	has(t, sql, "\"version\", \"customer\", \"parent\") VALUES", "plain columns")
	if !strings.HasSuffix(sql, "\"version\", (SELECT t.\"name\" FROM \"doc_customer\" t WHERE t.\"id\" = \"doc_order\".\"customer\") AS \"customer\", "+
		"(SELECT t.\"name\" FROM \"doc_order\" t WHERE t.\"id\" = \"doc_order\".\"parent\") AS \"parent\"") {
		t.Error(sql)
	}
	has(t, getSQL(unitOrder(t), ""), "AS \"customer\"", "get")
}

// Rust: document.rs::a_table_field_is_not_a_column_in_any_statement
func TestATableFieldIsNotAColumnInAnyStatement(t *testing.T) {
	doctype := unitWithTable(t)
	insert := insertSQL(doctype, false)
	has(t, insert, "(\"id\", \"name\", \"owner\", \"created_at\", \"modified_at\", \"modified_by\", \"version\", \"title\", \"note\") VALUES", "columns")
	has(t, insert, "$4::varchar, $5::text) RETURNING", "placeholders")
	for label, s := range map[string]string{
		"insert": insert, "get": getSQL(doctype, ""), "columns": columnList(doctype),
		"select": selectList(doctype), "items": strings.Join(fieldItems(doctype), ""),
	} {
		if strings.Contains(s, "lines") {
			t.Errorf("%s mentions the table field: %s", label, s)
		}
	}
	if len(fieldPlaceholders(doctype, 1)) != 2 {
		t.Error("placeholders")
	}
	// No fields at all: only the meta columns change.
	sql := updateSQL(doctype, map[string]core.Value{}, nil)
	if !strings.HasPrefix(sql, "UPDATE \"doc_ord\" SET \"modified_at\" = now(), \"modified_by\" = $1::text, "+
		"\"version\" = \"version\" + 1 WHERE \"name\" = $2::text AND \"version\" = $3::bigint RETURNING ") {
		t.Error(sql)
	}
}

// Rust: document.rs::child_statements_bind_values_and_resolve_links_on_read
func TestChildStatementsBindValuesAndResolveLinksOnRead(t *testing.T) {
	eq(t, childInsertSQL(unitLine(t)),
		"INSERT INTO \"doc_line\" (\"id\", \"parent_id\", \"idx\", \"qty\", \"customer\") "+
			"VALUES ($1::uuid, $2::uuid, $3::integer, $4::bigint, $5::uuid)", "insert")
	eq(t, childSelectSQL(unitLine(t)),
		"SELECT \"qty\", (SELECT t.\"name\" FROM \"doc_customer\" t WHERE t.\"id\" = \"doc_line\".\"customer\") AS \"customer\" "+
			"FROM \"doc_line\" WHERE \"parent_id\" = $1::uuid ORDER BY \"idx\"", "select")
}

// Rust: document.rs::an_owner_scope_is_a_bound_condition_in_the_statement
func TestAnOwnerScopeIsABoundConditionInTheStatement(t *testing.T) {
	doctype := unitCustomer(t)
	plain := getSQL(doctype, "")
	if !strings.HasSuffix(plain, "FROM \"doc_customer\" WHERE \"name\" = $1::text") {
		t.Error(plain)
	}
	scoped := getSQL(doctype, readScope{ownerOnly: true, owner: "ann"}.andSQL(2).sql)
	if !strings.HasSuffix(scoped, "WHERE \"name\" = $1::text AND \"owner\" = $2::text") {
		t.Error(scoped)
	}
}

// Rust: document.rs::actor_rules
func TestActorRules(t *testing.T) {
	if checkActor("cli") != nil {
		t.Error("cli")
	}
	for _, bad := range []string{"", "a\x00b"} {
		if e, ok := checkActor(bad).(*Error); !ok || e.Kind != ErrInvalidActor {
			t.Errorf("%q", bad)
		}
	}
}

// Rust: document.rs::submittable_statements_select_the_status_columns_and_bind_amended_from
func TestSubmittableStatementsSelectTheStatusColumnsAndBindAmendedFrom(t *testing.T) {
	doctype := unitQuote(t)
	if !strings.HasPrefix(selectList(doctype),
		"\"id\", \"name\", \"owner\", \"created_at\", \"modified_at\", \"modified_by\", \"version\", \"docstatus\", \"amended_from\", \"title\"") {
		t.Error(selectList(doctype))
	}
	// A draft insert does not mention docstatus (the column default is 0) ...
	plain := insertSQL(doctype, false)
	has(t, plain, "(\"id\", \"name\", \"owner\", \"created_at\", \"modified_at\", \"modified_by\", \"version\", \"title\") VALUES (", "draft")
	// ... an amendment binds amended_from after the fields.
	amended := insertSQL(doctype, true)
	has(t, amended, "\"version\", \"title\", \"amended_from\") VALUES (", "amended columns")
	has(t, amended, "1, $4::varchar, $5::text) RETURNING", "amended placeholders")
	// The new docstatus is a constant of the enum.
	cancelled := core.Cancelled
	sql := updateSQL(doctype, map[string]core.Value{}, &cancelled)
	if !strings.HasPrefix(sql, "UPDATE \"doc_quote\" SET \"modified_at\" = now(), \"modified_by\" = $1::text, "+
		"\"version\" = \"version\" + 1, \"docstatus\" = 2 WHERE \"name\" = $2::text AND \"version\" = $3::bigint RETURNING ") {
		t.Error(sql)
	}
	// A plain DocType has no such columns anywhere.
	if strings.Contains(selectList(unitCustomer(t)), "docstatus") {
		t.Error("docstatus")
	}
}

// Rust: document.rs::the_series_statement_binds_the_prefix
func TestTheSeriesStatementBindsThePrefix(t *testing.T) {
	eq(t, seriesSQL, "INSERT INTO fb_series (prefix, current) VALUES ($1::text, 1) "+
		"ON CONFLICT (prefix) DO UPDATE SET current = fb_series.current + 1 RETURNING current", "series")
}

// Rust: document.rs::amended_names
func TestAmendedNames(t *testing.T) {
	for _, c := range []struct {
		name      string
		amendment bool
		want      string
	}{
		// Not an amendment itself: the name gets -1, also when it ends in a number.
		{"X", false, "X-1"},
		{"INV-2026-00007", false, "INV-2026-00007-1"},
		// An amendment counts up from its own number.
		{"X-1", true, "X-2"},
		{"INV-2026-00007-9", true, "INV-2026-00007-10"},
		// A name that is not <root>-<digits> starts over with -1.
		{"X-a", true, "X-a-1"},
		{"X-", true, "X--1"},
		{"X-+3", true, "X-+3-1"},
		{"X-18446744073709551615", true, "X-18446744073709551615-1"},
	} {
		eq(t, amendedName(c.name, c.amendment), c.want, c.name)
	}
}

// Rust: document.rs::a_workflow_doctype_selects_and_binds_the_state_after_the_status_columns
func TestAWorkflowDoctypeSelectsAndBindsTheStateAfterTheStatusColumns(t *testing.T) {
	doctype := unitFlow(t)
	if !strings.HasPrefix(selectList(doctype),
		"\"id\", \"name\", \"owner\", \"created_at\", \"modified_at\", \"modified_by\", \"version\", \"docstatus\", \"amended_from\", \"workflow_state\", \"title\"") {
		t.Error(selectList(doctype))
	}
	// The initial state is the last bound value, after the fields and amended_from.
	plain := insertSQL(doctype, false)
	has(t, plain, "\"version\", \"title\", \"workflow_state\") VALUES (", "plain columns")
	has(t, plain, "1, $4::varchar, $5::text) RETURNING", "plain placeholders")
	amended := insertSQL(doctype, true)
	has(t, amended, "\"title\", \"amended_from\", \"workflow_state\") VALUES (", "amended columns")
	has(t, amended, "1, $4::varchar, $5::text, $6::text) RETURNING", "amended placeholders")
	// A DocType without a workflow has no such column in any statement.
	for _, s := range []string{selectList(unitQuote(t)), insertSQL(unitQuote(t), true), selectList(unitCustomer(t))} {
		if strings.Contains(s, "workflow_state") {
			t.Error(s)
		}
	}
}

// Rust: scope.rs::an_array_literal_quotes_every_element
func TestAnArrayLiteralQuotesEveryElement(t *testing.T) {
	eq(t, arrayLiteral(nil), "{}", "empty")
	eq(t, arrayLiteral([]string{"a"}), `{"a"}`, "one")
	eq(t, arrayLiteral([]string{"a", "b c"}), `{"a","b c"}`, "two")
	eq(t, arrayLiteral([]string{`x,"y"\z`, "{b}", "NULL", ""}), `{"x,\"y\"\\z","{b}","NULL",""}`, "hostile")
}

// Rust: scope.rs::no_restriction_adds_nothing
func TestNoRestrictionAddsNothing(t *testing.T) {
	sql := readScope{}.andSQL(3)
	if sql.sql != "" || len(sql.params) != 0 {
		t.Error(sql)
	}
}

// Rust: scope.rs::the_owner_condition_is_what_it_was
func TestTheOwnerConditionIsWhatItWas(t *testing.T) {
	sql := readScope{ownerOnly: true, owner: "ann"}.andSQL(2)
	eq(t, sql.sql, " AND \"owner\" = $2::text", "sql")
	if len(sql.params) != 1 || sql.params[0] != "ann" {
		t.Error(sql.params)
	}
}

// Rust: scope.rs::names_and_links_are_bound_conditions_numbered_after_the_owner
func TestNamesAndLinksAreBoundConditionsNumberedAfterTheOwner(t *testing.T) {
	retail := []string{"Retail", "Wholesale"}
	rows := core.RowScope{
		Names: []string{"C-1"},
		Links: []core.LinkScope{
			{Field: "group", Target: "customer_group", Allowed: retail},
			{Field: "billing_group", Target: "customer_group", Allowed: retail},
		},
	}
	sql := readScope{ownerOnly: true, owner: "ann", rows: rows}.andSQL(2)
	eq(t, sql.sql, " AND \"owner\" = $2::text "+
		"AND \"name\" = ANY($3::text::text[]) "+
		"AND (\"group\" IS NULL OR \"group\" IN (SELECT t.\"id\" FROM \"doc_customer_group\" t WHERE t.\"name\" = ANY($4::text::text[]))) "+
		"AND (\"billing_group\" IS NULL OR \"billing_group\" IN (SELECT t.\"id\" FROM \"doc_customer_group\" t WHERE t.\"name\" = ANY($4::text::text[])))", "sql")
	// One parameter for the target both fields point at.
	if got := strings.Join(sql.params, "|"); got != `ann|{"C-1"}|{"Retail","Wholesale"}` {
		t.Error(got)
	}
}

// Rust: scope.rs::a_qualified_condition_names_the_table
func TestAQualifiedConditionNamesTheTable(t *testing.T) {
	rows := core.RowScope{Names: []string{"C-1"}}
	n := 0
	conditions := readScope{rows: rows}.conditions("\"doc_x\"", func(_, cast string) string {
		n++
		return "$" + string(rune('0'+n)) + "::" + cast
	})
	if len(conditions) != 1 || conditions[0] != "\"doc_x\".\"name\" = ANY($1::text::text[])" {
		t.Error(conditions)
	}
}

// Rust: value.rs::null_binds_as_sql_null_for_any_type
func TestNullBindsAsSQLNullForAnyType(t *testing.T) {
	for _, cast := range []string{"bigint", "text", "uuid", "timestamptz"} {
		arg, err := bindValue(cast, core.Null(), 1)
		if err != nil || arg != nil {
			t.Errorf("%s: %v %v", cast, arg, err)
		}
	}
}

// Rust: value.rs::wrong_kind_for_the_parameter_type_is_an_error
func TestWrongKindForTheParameterTypeIsAnError(t *testing.T) {
	if _, err := bindValue("bigint", core.Text("x"), 1); err == nil {
		t.Error("text as bigint")
	}
	if _, err := bindValue("text", core.Int(1), 1); err == nil {
		t.Error("int as text")
	}
	if _, err := bindValue("bigint", core.Int(1), 1); err != nil {
		t.Error(err)
	}
}

// Rust: value.rs::datetime_out_of_range_in_utc_is_an_error_not_a_panic
func TestDatetimeOutOfRangeInUTCIsAnErrorNotAPanicUnit(t *testing.T) {
	edge := time.Date(9999, 12, 31, 23, 0, 0, 0, time.FixedZone("", -5*3600))
	if _, err := bindValue("timestamptz", core.Datetime(edge), 1); err == nil {
		t.Error("year 10000 in UTC was bound")
	}
	if _, err := bindValue("timestamptz", core.Datetime(edge.Add(-11*time.Hour)), 1); err != nil {
		t.Error(err)
	}
}

// Rust: value.rs::link_params_bind_as_uuid_or_null
func TestLinkParamsBindAsUUIDOrNull(t *testing.T) {
	ft := core.FieldType{Kind: core.FieldLink, Target: "customer"}
	id := newID()
	links := map[string][16]byte{"customer": id}
	arg, err := sqlParam(ft, links, "customer", core.Text("ignored"), 1)
	if err != nil || arg != any([16]byte(id)) {
		t.Errorf("%v %v", arg, err)
	}
	arg, err = sqlParam(ft, links, "other", core.Text("ignored"), 1)
	if err != nil || arg != nil {
		t.Errorf("%v %v", arg, err)
	}
}

// Rust: value.rs::cast_types
func TestCastTypes(t *testing.T) {
	eq(t, castType(core.FieldType{Kind: core.FieldData, MaxLength: 5}), "varchar", "data")
	eq(t, castType(core.FieldType{Kind: core.FieldInt}), "bigint", "int")
	eq(t, castType(core.FieldType{Kind: core.FieldDatetime}), "timestamptz", "datetime")
	eq(t, castType(core.FieldType{Kind: core.FieldLink, Target: "customer"}), "uuid", "link")
}

// A Go addition: the truncation of a datetime to microseconds is toward the PostgreSQL epoch, as
// the Rust time crate sends it (Go's own encoding would floor).
func TestDatetimesAreTruncatedToMicrosecondsTowardTheEpoch(t *testing.T) {
	after := time.Date(2024, 1, 1, 0, 0, 0, 1_234_567, time.UTC)
	if got := truncateMicros(after); got.Nanosecond() != 1_234_000 {
		t.Error(got.Nanosecond())
	}
	before := time.Date(1999, 12, 31, 23, 59, 59, 999_999_999, time.UTC)
	if got := truncateMicros(before); !got.Equal(time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Error(got)
	}
	exact := time.Date(1999, 12, 31, 23, 59, 59, 999_999_000, time.UTC)
	if got := truncateMicros(exact); !got.Equal(exact) {
		t.Error(got)
	}
}

// A Go addition: a numeric column comes back as the text PostgreSQL writes it and keeps its scale
// (12.50, 0.00, 1200); NaN is refused.
func TestNumericColumnsKeepTheirScale(t *testing.T) {
	for _, c := range []string{"12.50", "0.00", "-0.05", "1200", "9999999999.99", "0"} {
		dest := &numericText{}
		dest.Text = pgtype.Text{String: c, Valid: true}
		v, err := fromColumn(dest)
		if err != nil || v.Dec.String() != c {
			t.Errorf("%s: %v %v", c, v.Dec, err)
		}
	}
	if v, err := fromColumn(&numericText{}); err != nil || v.Kind != core.KindNull {
		t.Errorf("NULL: %v %v", v, err)
	}
	for _, bad := range []string{"NaN", "Infinity", "-Infinity"} {
		dest := &numericText{}
		dest.Text = pgtype.Text{String: bad, Valid: true}
		if _, err := fromColumn(dest); err == nil {
			t.Errorf("%s was read", bad)
		}
	}
}
