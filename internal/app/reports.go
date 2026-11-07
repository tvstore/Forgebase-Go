package app

import (
	"strings"
	"time"

	"github.com/tvstore/forgebase-go/internal/core"
)

// Query reports (M8.4, ADR 0016): Report, Param and Column, the builders an app uses with App.Report (the
// Rust module reports.rs of the crate forgebase).

// Report is an app-defined SQL report, for [App.Report]: the SQL, the roles that may run it, typed
// parameters and typed result columns. Build one with NewReport.
//
//	//go:embed reports/sales_by_customer.sql
//	var salesByCustomerSQL string
//
//	forgebase.NewApp("sales").Report("sales_by_customer",
//		forgebase.NewReport(salesByCustomerSQL).
//			Label("Sales by customer").
//			Roles("Sales Manager").
//			Param(forgebase.DateParam("from").Label("From").Required()).
//			Param(forgebase.DateParam("to").Label("To").Required()).
//			Param(forgebase.LinkParam("customer", "customer")).
//			Column(forgebase.LinkColumn("customer", "customer")).
//			Column(forgebase.IntColumn("orders")).
//			Column(forgebase.DecimalColumn("total")))
//
// The SQL is trusted code compiled into the binary, like an app migration. Rules (ADR 0016):
//
//   - Parameters are positional: $1..$n are the declared params, in the order of the Param calls. They
//     are always bound, never put into the text. A param that is absent and not required is bound as
//     NULL, so the SQL handles it: WHERE ($3::text IS NULL OR customer = $3). Cast a placeholder
//     ($1::date) when PostgreSQL cannot infer its type.
//   - Columns are positional too: the statement returns the declared columns in order, and the SQL type
//     must fit the kind (int2, int4 or int8 for IntColumn, numeric for DecimalColumn, text or varchar for
//     DataColumn and LinkColumn, date, timestamptz, bool). `forgebase migrate` prepares every report and
//     refuses one that does not fit; forgebase/testing.CheckReports runs the same check in an app's
//     tests.
//   - Run as a read-only statement with a statement timeout and a row limit (Timeout, MaxRows). A
//     statement that writes fails.
//   - Only the roles named here may run it (the Administrator role always may). Row-level rules do not
//     apply inside a report: it shows what its SQL selects, to every role that may run it. The caller is
//     not available to the SQL in this version.
//
// A trailing ; and whitespace are stripped from the SQL. A report is only data: everything wrong with it
// (an invalid name, no roles, a Link to an unknown DocType, ...) is reported with the other problems when
// the binary starts (StartupError).
type Report struct {
	sql     string
	label   string
	hasName bool
	roles   []string
	params  []*Param
	columns []*Column
	timeout time.Duration
	maxRows uint32
}

// NewReport is a report that runs this SQL, usually an embedded file. Add the roles, params and columns
// with the other methods.
func NewReport(sql string) *Report {
	return &Report{sql: sql, timeout: core.DefaultTimeout, maxRows: core.DefaultMaxRows}
}

// Label is the label the desk shows. Default: the report's name, humanized.
func (r *Report) Label(label string) *Report {
	r.label, r.hasName = label, true
	return r
}

// Roles adds the roles that may run the report. At least one is required: there is no "any user" report.
// The Administrator role and the system context always may.
func (r *Report) Roles(roles ...string) *Report {
	r.roles = append(r.roles, roles...)
	return r
}

// Param adds a parameter. The order of the calls is the order of $1..$n.
func (r *Report) Param(p *Param) *Report {
	r.params = append(r.params, p)
	return r
}

// Column adds a result column. The order of the calls is the order of the statement's columns.
func (r *Report) Column(c *Column) *Report {
	r.columns = append(r.columns, c)
	return r
}

// Timeout is the statement timeout. Default 30 seconds, at most 300 seconds; a larger value is a startup
// error.
func (r *Report) Timeout(timeout time.Duration) *Report {
	r.timeout = timeout
	return r
}

// MaxRows is the most rows returned. Default 10 000, at most 100 000; a larger value is a startup error.
// When there are more rows the result is cut and marked truncated.
func (r *Report) MaxRows(rows uint32) *Report {
	r.maxRows = rows
	return r
}

// definition is the definition for app and name, with the SQL trimmed of whitespace and a trailing ;.
func (r *Report) definition(app, name string) core.ReportDef {
	sql := strings.TrimRight(r.sql, whitespace)
	for strings.HasSuffix(sql, ";") {
		sql = strings.TrimRight(strings.TrimSuffix(sql, ";"), whitespace)
	}
	label := r.label
	if !r.hasName {
		label = core.Humanize(name)
	}
	def := core.ReportDef{
		App: app, Name: name, Label: label,
		SQL:     strings.TrimLeft(sql, whitespace),
		Roles:   append([]string(nil), r.roles...),
		Timeout: r.timeout, MaxRows: r.maxRows,
	}
	for _, p := range r.params {
		def.Params = append(def.Params, p.definition())
	}
	for _, c := range r.columns {
		def.Columns = append(def.Columns, c.definition())
	}
	return def
}

// whitespace is what Rust's trim removes: Unicode White_Space, which for the ASCII range and the usual
// extras is this set.
const whitespace = " \t\n\v\f\r\u0085                 　"

// Param is a parameter of a [Report]: a typed value the caller gives. Values are checked like the fields
// of the same kind and always bound to the statement, never interpolated.
type Param struct {
	name     string
	label    string
	kind     core.ParamKind
	required bool
	def      *core.Value
}

func newParam(name string, kind core.ParamKind) *Param {
	return &Param{name: name, label: core.Humanize(name), kind: kind}
}

// DataParam is text of at most 140 characters. The name follows the identifier rule (lower-case ASCII
// letters, digits and underscores, starting with a letter) and is the key in the request.
func DataParam(name string) *Param { return newParam(name, core.ParamKind{Kind: core.ParamData}) }

// IntParam is a 64-bit integer.
func IntParam(name string) *Param { return newParam(name, core.ParamKind{Kind: core.ParamInt}) }

// DecimalParam is a fixed-point number, a string in the API ("12.50").
func DecimalParam(name string) *Param {
	return newParam(name, core.ParamKind{Kind: core.ParamDecimal})
}

// DateParam is a calendar date, YYYY-MM-DD in the API.
func DateParam(name string) *Param { return newParam(name, core.ParamKind{Kind: core.ParamDate}) }

// CheckParam is a boolean.
func CheckParam(name string) *Param { return newParam(name, core.ParamKind{Kind: core.ParamCheck}) }

// LinkParam is the name of a document of doctype (a DocType of the app or of a dependency). Only bound: it
// is not checked for existence, and the caller's read rules do not apply.
func LinkParam(name, doctype string) *Param {
	return newParam(name, core.ParamKind{Kind: core.ParamLink, DocType: doctype})
}

// SelectParam is one of options (not empty, no duplicates).
func SelectParam(name string, options ...string) *Param {
	return newParam(name, core.ParamKind{Kind: core.ParamSelect, Options: append([]string(nil), options...)})
}

// Label is the label the desk shows. Default: the name, humanized.
func (p *Param) Label(label string) *Param {
	p.label = label
	return p
}

// Required says a value must be given (the default counts). Without it, an absent value is NULL.
func (p *Param) Required() *Param {
	p.required = true
	return p
}

// DefaultValue is the value used when the request does not mention the param; the desk fills it in. It
// must fit the kind (a startup error otherwise).
func (p *Param) DefaultValue(value core.Value) *Param {
	p.def = &value
	return p
}

func (p *Param) definition() core.ReportParam {
	return core.ReportParam{Name: p.name, Label: p.label, Kind: p.kind, Required: p.required, Default: p.def}
}

// Column is a result column of a [Report]. The columns are positional: the statement returns them in the
// order they are declared, and the SQL type must fit the kind.
type Column struct {
	name  string
	label string
	kind  core.ColumnKind
}

func newColumn(name string, kind core.ColumnKind) *Column {
	return &Column{name: name, label: core.Humanize(name), kind: kind}
}

// DataColumn is text (text or varchar). The name follows the identifier rule and need not be the SQL
// column's name.
func DataColumn(name string) *Column { return newColumn(name, core.ColumnKind{Kind: core.ColumnData}) }

// IntColumn is an integer (int2, int4 or int8).
func IntColumn(name string) *Column { return newColumn(name, core.ColumnKind{Kind: core.ColumnInt}) }

// DecimalColumn is a fixed-point number (numeric), a string in the API.
func DecimalColumn(name string) *Column {
	return newColumn(name, core.ColumnKind{Kind: core.ColumnDecimal})
}

// DateColumn is a calendar date (date).
func DateColumn(name string) *Column { return newColumn(name, core.ColumnKind{Kind: core.ColumnDate}) }

// DatetimeColumn is a point in time (timestamptz), RFC 3339 in UTC in the API.
func DatetimeColumn(name string) *Column {
	return newColumn(name, core.ColumnKind{Kind: core.ColumnDatetime})
}

// CheckColumn is a boolean (bool).
func CheckColumn(name string) *Column {
	return newColumn(name, core.ColumnKind{Kind: core.ColumnCheck})
}

// LinkColumn is the name of a document of doctype (text or varchar). The desk shows it as a link.
func LinkColumn(name, doctype string) *Column {
	return newColumn(name, core.ColumnKind{Kind: core.ColumnLink, DocType: doctype})
}

// Label is the label the desk shows. Default: the name, humanized.
func (c *Column) Label(label string) *Column {
	c.label = label
	return c
}

func (c *Column) definition() core.ReportColumn {
	return core.ReportColumn{Name: c.name, Label: c.label, Kind: c.kind}
}
