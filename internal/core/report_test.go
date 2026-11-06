package core

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tvstore/forgebase-go/internal/decimal"
)

func reportParam(name string, kind ParamKind, required bool) ReportParam {
	return ReportParam{Name: name, Label: name, Kind: kind, Required: required}
}

func testReport() *ReportDef {
	return &ReportDef{
		App: "sales", Name: "totals", Label: "Totals", SQL: "SELECT 1",
		Roles: []string{"Manager"},
		Params: []ReportParam{
			reportParam("from", ParamKind{Kind: ParamDate}, true),
			reportParam("note", ParamKind{Kind: ParamData}, false),
			reportParam("kind", ParamKind{Kind: ParamSelect, Options: []string{"a", "b"}}, false),
		},
		Columns: []ReportColumn{{Name: "n", Label: "N", Kind: ColumnKind{Kind: ColumnInt}}},
		Timeout: DefaultTimeout,
		MaxRows: DefaultMaxRows,
	}
}

func reportDate() Value { return NewDate(2026, time.October, 30) }

func given(pairs ...pair) map[string]Value {
	m := map[string]Value{}
	for _, p := range pairs {
		m[p.k] = p.v
	}
	return m
}

func errorTexts(errs []ValidationError) []string {
	var out []string
	for _, e := range errs {
		out = append(out, e.Error())
	}
	return out
}

// Rust: report.rs::a_valid_report_has_no_problems_and_a_full_name
func TestAValidReportHasNoProblemsAndAFullName(t *testing.T) {
	if p := testReport().Problems(); len(p) != 0 {
		t.Errorf("problems: %v", p)
	}
	if testReport().FullName() != "sales.totals" {
		t.Error("full name")
	}
}

// Rust: report.rs::every_definition_problem_is_reported
func TestEveryDefinitionProblemIsReported(t *testing.T) {
	r := testReport()
	r.Name = "Bad Name"
	r.SQL = "  "
	r.Roles = nil
	r.Columns = nil
	r.Timeout = 301 * time.Second
	r.MaxRows = 100_001
	text := strings.Join(r.Problems(), "\n")
	for _, needle := range []string{"name \"Bad Name\"", "the SQL is empty", "no roles", "no columns", "timeout", "max_rows 100001"} {
		if !strings.Contains(text, needle) {
			t.Errorf("%q not in %s", needle, text)
		}
	}
	// The timeout text is the Rust Debug text of a Duration.
	if !strings.Contains(text, "timeout 301s must be more than zero and at most 300s") {
		t.Errorf("timeout text in %s", text)
	}
	r = testReport()
	r.Timeout = 0
	r.MaxRows = 0
	if n := len(r.Problems()); n != 2 {
		t.Errorf("%d problems", n)
	}
	if got := r.Problems()[0]; got != "timeout 0ns must be more than zero and at most 300s" {
		t.Errorf("zero timeout text %q", got)
	}
}

// Rust: report.rs::the_limits_may_be_raised_up_to_their_caps
func TestTheLimitsMayBeRaisedUpToTheirCaps(t *testing.T) {
	r := testReport()
	r.Timeout = MaxTimeout
	r.MaxRows = MaxMaxRows
	if p := r.Problems(); len(p) != 0 {
		t.Errorf("problems: %v", p)
	}
}

func TestDurationDebug(t *testing.T) {
	// Rust: the Debug text of std::time::Duration.
	for d, want := range map[time.Duration]string{
		0: "0ns", 1: "1ns", 1500: "1.5µs", 2 * time.Millisecond: "2ms", 1500 * time.Millisecond: "1.5s",
		30 * time.Second: "30s", 301 * time.Second: "301s", time.Second + time.Nanosecond: "1.000000001s",
		250 * time.Microsecond: "250µs", 999999999 * time.Nanosecond: "999.999999ms",
	} {
		if got := durationDebug(d); got != want {
			t.Errorf("%d: got %q, want %q", d, got, want)
		}
	}
}

// Rust: report.rs::roles_params_columns_and_options_are_checked
func TestRolesParamsColumnsAndOptionsAreChecked(t *testing.T) {
	r := testReport()
	r.Roles = []string{"  "}
	r.Params = append(r.Params,
		reportParam("from", ParamKind{Kind: ParamInt}, false),
		reportParam("Bad", ParamKind{Kind: ParamInt}, false),
		reportParam("s", ParamKind{Kind: ParamSelect, Options: []string{"x", "x"}}, false),
		reportParam("e", ParamKind{Kind: ParamSelect, Options: []string{}}, false),
	)
	r.Columns = append(r.Columns, r.Columns[0])
	text := strings.Join(r.Problems(), "\n")
	for _, needle := range []string{
		"invalid role \"  \"",
		"param `from` is declared more than once",
		"param name \"Bad\"",
		"param `s`: select options must be unique",
		"param `e`: a select needs options",
		"column `n` is declared more than once",
	} {
		if !strings.Contains(text, needle) {
			t.Errorf("%q not in %s", needle, text)
		}
	}
}

// Rust: report.rs::a_default_must_fit_its_param
func TestADefaultMustFitItsParam(t *testing.T) {
	r := testReport()
	one, zzz := Int(1), Text("zzz")
	r.Params[1].Default = &one
	r.Params[2].Default = &zzz
	text := strings.Join(r.Problems(), "\n")
	if !strings.Contains(text, "param `note`: invalid default value") || !strings.Contains(text, "param `kind`: invalid default value") {
		t.Errorf("problems: %s", text)
	}
	x, a := Text("x"), Text("a")
	r.Params[1].Default = &x
	r.Params[2].Default = &a
	if p := r.Problems(); len(p) != 0 {
		t.Errorf("problems: %v", p)
	}
}

// Rust: report.rs::link_targets_name_params_and_columns
func TestLinkTargetsNameParamsAndColumns(t *testing.T) {
	r := testReport()
	r.Params = append(r.Params, reportParam("customer", ParamKind{Kind: ParamLink, DocType: "customer"}, false))
	r.Columns = append(r.Columns, ReportColumn{Name: "c", Label: "C", Kind: ColumnKind{Kind: ColumnLink, DocType: "item"}})
	sameValue(t, r.LinkTargets(), []LinkTarget{{"customer", "param `customer`"}, {"item", "column `c`"}}, "link targets")
}

// Rust: report.rs::params_are_bound_in_declared_order_with_null_for_absent_ones
func TestParamsAreBoundInDeclaredOrderWithNullForAbsentOnes(t *testing.T) {
	values, errs := testReport().BindParams(given(pair{"from", reportDate()}))
	if errs != nil {
		t.Fatal(errs)
	}
	if len(values) != 3 || !values[0].Equal(reportDate()) || values[1].Kind != KindNull || values[2].Kind != KindNull {
		t.Errorf("values = %v", values)
	}
}

// Rust: report.rs::a_default_fills_an_absent_param_but_not_an_explicit_null
func TestADefaultFillsAnAbsentParamButNotAnExplicitNull(t *testing.T) {
	r := testReport()
	dflt := Text("dflt")
	r.Params[1].Default = &dflt
	values, errs := r.BindParams(given(pair{"from", reportDate()}))
	if errs != nil || !values[1].Equal(Text("dflt")) {
		t.Errorf("default: %v %v", values, errs)
	}
	values, errs = r.BindParams(given(pair{"from", reportDate()}, pair{"note", Null()}))
	if errs != nil || values[1].Kind != KindNull {
		t.Errorf("explicit null: %v %v", values, errs)
	}
}

// Rust: report.rs::every_param_problem_is_reported_by_param
func TestEveryParamProblemIsReportedByParam(t *testing.T) {
	_, errs := testReport().BindParams(given(pair{"note", Int(1)}, pair{"kind", Text("c")}, pair{"extra", Null()}))
	text := errorTexts(errs)
	for _, want := range []string{
		"unknown field `extra`",
		"field `from` is required",
		"field `note`: expected text, found int",
		"field `kind`: `c` is not one of the allowed options",
	} {
		if !slices.Contains(text, want) {
			t.Errorf("%q not in %v", want, text)
		}
	}
	if len(errs) != 4 {
		t.Errorf("%d errors", len(errs))
	}
}

// Rust: report.rs::text_params_refuse_nul_and_long_values_and_blank_required_text
func TestTextParamsRefuseNulAndLongValuesAndBlankRequiredText(t *testing.T) {
	r := testReport()
	r.Params = append(r.Params, reportParam("must", ParamKind{Kind: ParamData}, true))
	_, errs := r.BindParams(given(pair{"from", reportDate()}, pair{"note", Text("a\x00b")}, pair{"must", Text("   ")}))
	sameValue(t, errorTexts(errs), []string{"field `note`: value must not contain a NUL character", "field `must` is required"}, "nul and blank")
	_, errs = r.BindParams(given(pair{"from", reportDate()}, pair{"note", Text(strings.Repeat("x", 141))}, pair{"must", Text("ok")}))
	if got := errs[0].Error(); got != "field `note`: 141 characters exceeds max_length 140" {
		t.Errorf("long: %q", got)
	}
}

// Rust: report.rs::each_kind_takes_its_own_value_only
func TestEachKindTakesItsOwnValueOnly(t *testing.T) {
	r := testReport()
	r.Params = []ReportParam{
		reportParam("i", ParamKind{Kind: ParamInt}, false),
		reportParam("d", ParamKind{Kind: ParamDecimal}, false),
		reportParam("b", ParamKind{Kind: ParamCheck}, false),
		reportParam("l", ParamKind{Kind: ParamLink, DocType: "customer"}, false),
	}
	ok, errs := r.BindParams(given(
		pair{"i", Int(3)},
		pair{"d", Decimal(decimal.New(125, 2))},
		pair{"b", Bool(false)},
		pair{"l", Text("CUST-1")},
	))
	if errs != nil || len(ok) != 4 {
		t.Errorf("valid: %v %v", ok, errs)
	}
	_, errs = r.BindParams(given(pair{"i", Text("3")}, pair{"d", Int(1)}, pair{"b", Int(0)}, pair{"l", Int(1)}))
	if len(errs) != 4 {
		t.Errorf("invalid: %d errors", len(errs))
	}
}
