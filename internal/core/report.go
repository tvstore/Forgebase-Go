package core

import (
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// Query reports (M8.4, ADR 0016): the definition of an app-defined SQL report, and the checks that
// need no database.
//
// A report is a SQL statement the app compiled in (trusted code, like an app migration), the roles
// that may run it, typed parameters and typed result columns. This file is pure data and rules: it
// never runs SQL. internal/db runs a report (read-only, with a timeout and a row limit) and
// internal/httpapi serves it.
//
// The SQL parameters are positional: $1..$n are the declared parameters in order. A parameter that
// is absent and not required has no value (SQL NULL).

// DefaultTimeout is the statement timeout of a report that sets none.
const DefaultTimeout = 30 * time.Second

// MaxTimeout is the longest statement timeout a report may ask for.
const MaxTimeout = 300 * time.Second

// DefaultMaxRows is the row limit of a report that sets none.
const DefaultMaxRows uint32 = 10_000

// MaxMaxRows is the largest row limit a report may ask for.
const MaxMaxRows uint32 = 100_000

// MaxParamsAndColumns is the most parameters or columns a report may have (a PostgreSQL statement
// has at most 65 535 parameters; a report this wide is a mistake).
const MaxParamsAndColumns = 64

// ParamKindKind is the variant of a ParamKind.
type ParamKindKind int

const (
	// ParamData is text of at most 140 characters.
	ParamData ParamKindKind = iota + 1
	// ParamInt is a 64-bit integer.
	ParamInt
	// ParamDecimal is a fixed-point number.
	ParamDecimal
	// ParamDate is a calendar date.
	ParamDate
	// ParamCheck is a boolean.
	ParamCheck
	// ParamLink is the name of a document of DocType. Not checked for existence: it is only bound.
	ParamLink
	// ParamSelect is one of a fixed list of strings.
	ParamSelect
)

// ParamKind is the kind of a report parameter, which decides how a value is checked and bound.
type ParamKind struct {
	Kind ParamKindKind
	// DocType is the target DocType, for ParamLink.
	DocType string
	// Options are the allowed values, for ParamSelect.
	Options []string
}

// Name is the name used in the API: data, int, decimal, date, check, link, select.
func (k ParamKind) Name() string {
	switch k.Kind {
	case ParamData:
		return "data"
	case ParamInt:
		return "int"
	case ParamDecimal:
		return "decimal"
	case ParamDate:
		return "date"
	case ParamCheck:
		return "check"
	case ParamLink:
		return "link"
	case ParamSelect:
		return "select"
	}
	return "unknown"
}

// expected is the kind of Value a parameter of this kind takes.
func (k ParamKind) expected() string {
	switch k.Kind {
	case ParamData, ParamLink, ParamSelect:
		return "text"
	case ParamInt:
		return "int"
	case ParamDecimal:
		return "decimal"
	case ParamDate:
		return "date"
	}
	return "bool"
}

// ColumnKindKind is the variant of a ColumnKind.
type ColumnKindKind int

const (
	// ColumnData is text (text or varchar).
	ColumnData ColumnKindKind = iota + 1
	// ColumnInt is an integer (int2, int4 or int8).
	ColumnInt
	// ColumnDecimal is a fixed-point number (numeric).
	ColumnDecimal
	// ColumnDate is a calendar date (date).
	ColumnDate
	// ColumnDatetime is a point in time (timestamptz).
	ColumnDatetime
	// ColumnCheck is a boolean (bool).
	ColumnCheck
	// ColumnLink is the name of a document of DocType (text or varchar).
	ColumnLink
)

// ColumnKind is the kind of a result column. The SQL type must fit it; forgebase migrate checks
// that.
type ColumnKind struct {
	Kind ColumnKindKind
	// DocType is the target DocType, for ColumnLink.
	DocType string
}

// Name is the name used in the API: data, int, decimal, date, datetime, check, link.
func (k ColumnKind) Name() string {
	switch k.Kind {
	case ColumnData:
		return "data"
	case ColumnInt:
		return "int"
	case ColumnDecimal:
		return "decimal"
	case ColumnDate:
		return "date"
	case ColumnDatetime:
		return "datetime"
	case ColumnCheck:
		return "check"
	case ColumnLink:
		return "link"
	}
	return "unknown"
}

// ReportParam is one declared parameter of a report.
type ReportParam struct {
	// Name is the key in the request. Same rule as a DocType name.
	Name string
	// Label is the label the desk shows.
	Label string
	// Kind is the kind.
	Kind ParamKind
	// Required: a value must be given (a Default counts).
	Required bool
	// Default is the value used when the request does not mention the parameter; nil for none. An
	// explicit null is not replaced.
	Default *Value
}

// ReportColumn is one declared result column of a report.
type ReportColumn struct {
	// Name is the key in the API's column list. Same rule as a DocType name.
	Name string
	// Label is the label the desk shows.
	Label string
	// Kind is the kind.
	Kind ColumnKind
}

// ReportDef is a report: SQL, roles, parameters and columns. Plain data with exported fields, like
// Document; check it with Problems.
type ReportDef struct {
	// App is the app that defines it.
	App string
	// Name is unique within the app. Same rule as a DocType name.
	Name string
	// Label is the label the desk shows.
	Label string
	// SQL is the statement, with $1..$n for the parameters. No trailing ";".
	SQL string
	// Roles may run it. Never empty in a valid report.
	Roles []string
	// Params are in the order of $1..$n.
	Params []ReportParam
	// Columns are in the order the statement returns them.
	Columns []ReportColumn
	// Timeout is the statement timeout.
	Timeout time.Duration
	// MaxRows is the most rows returned; one more row than this only sets truncated.
	MaxRows uint32
}

// FullName is <app>.<name>: how the report is known in the API and the desk.
func (r *ReportDef) FullName() string { return r.App + "." + r.Name }

// Problems is what is wrong with the definition itself, one message per problem. Whether a Link
// DocType exists is checked by the caller that has the registry (LinkTargets).
func (r *ReportDef) Problems() []string {
	var problems []string
	add := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }
	if err := ValidateDocTypeName(r.Name); err != nil {
		add("name %s %s", debugQuote(r.Name), err)
	}
	if strings.TrimSpace(r.SQL) == "" {
		add("the SQL is empty")
	}
	if strings.TrimSpace(r.Label) == "" {
		add("the label is empty")
	}
	if len(r.Roles) == 0 {
		add("no roles: name the roles that may run it with .roles(&[..]); there is no \"any user\" report")
	}
	for _, role := range r.Roles {
		if reason := CheckRoleName(role); reason != "" {
			add("invalid role %s: %s", debugQuote(role), reason)
		}
	}
	if len(r.Columns) == 0 {
		add("no columns: declare the result columns with .column(..)")
	}
	if len(r.Params) > MaxParamsAndColumns {
		add("%d parameters; at most %d", len(r.Params), MaxParamsAndColumns)
	}
	if len(r.Columns) > MaxParamsAndColumns {
		add("%d columns; at most %d", len(r.Columns), MaxParamsAndColumns)
	}
	if r.Timeout <= 0 || r.Timeout > MaxTimeout {
		add("timeout %s must be more than zero and at most %s", durationDebug(r.Timeout), durationDebug(MaxTimeout))
	}
	if r.MaxRows == 0 || r.MaxRows > MaxMaxRows {
		add("max_rows %d must be 1 to %d", r.MaxRows, MaxMaxRows)
	}
	seen := map[string]bool{}
	for i := range r.Params {
		param := &r.Params[i]
		name := param.Name
		if err := ValidateDocTypeName(name); err != nil {
			add("param name %s %s", debugQuote(name), err)
		} else if seen[name] {
			add("param `%s` is declared more than once", name)
		} else {
			seen[name] = true
		}
		if param.Kind.Kind == ParamSelect {
			options := param.Kind.Options
			unique := map[string]bool{}
			allUnique := true
			for _, o := range options {
				if unique[o] {
					allUnique = false
				}
				unique[o] = true
			}
			blank := slices.ContainsFunc(options, func(o string) bool { return strings.TrimSpace(o) == "" })
			if len(options) == 0 || blank {
				add("param `%s`: a select needs options, none of them empty", name)
			} else if !allUnique {
				add("param `%s`: select options must be unique", name)
			}
		}
		if param.Default != nil {
			for _, e := range checkParam(param, *param.Default, nil) {
				add("param `%s`: invalid default value: %s", name, e)
			}
		}
	}
	seen = map[string]bool{}
	for _, column := range r.Columns {
		name := column.Name
		if err := ValidateDocTypeName(name); err != nil {
			add("column name %s %s", debugQuote(name), err)
		} else if seen[name] {
			add("column `%s` is declared more than once", name)
		} else {
			seen[name] = true
		}
	}
	return problems
}

// LinkTarget is a DocType that a Link param or column names, with what names it (for messages).
type LinkTarget struct {
	// DocType is the target DocType.
	DocType string
	// Place is what names it: "param `customer`" or "column `c`".
	Place string
}

// LinkTargets are the DocTypes that Link params and Link columns name, params first, in
// declaration order.
func (r *ReportDef) LinkTargets() []LinkTarget {
	var out []LinkTarget
	for _, p := range r.Params {
		if p.Kind.Kind == ParamLink {
			out = append(out, LinkTarget{DocType: p.Kind.DocType, Place: fmt.Sprintf("param `%s`", p.Name)})
		}
	}
	for _, c := range r.Columns {
		if c.Kind.Kind == ColumnLink {
			out = append(out, LinkTarget{DocType: c.Kind.DocType, Place: fmt.Sprintf("column `%s`", c.Name)})
		}
	}
	return out
}

// BindParams checks the values a caller gave and returns the value of every declared parameter, in
// declared order (Null for a parameter without a value), ready to be bound as $1..$n. On problems it
// returns them (and no values).
//
// Every problem is reported, naming the parameter: a key that is not a declared parameter, a
// missing required parameter (an empty or blank text counts as missing), a value of the wrong kind,
// a text with a NUL character or longer than 140 characters (Data, Link), a Select value that is
// not an option. A parameter that is absent gets its default. Link values are not checked against
// the documents.
func (r *ReportDef) BindParams(given map[string]Value) ([]Value, []ValidationError) {
	var errs []ValidationError
	for _, key := range sortedKeys(given) {
		if !slices.ContainsFunc(r.Params, func(p ReportParam) bool { return p.Name == key }) {
			errs = append(errs, ValidationError{Kind: ErrUnknownField, Field: key})
		}
	}
	values := make([]Value, 0, len(r.Params))
	for i := range r.Params {
		param := &r.Params[i]
		value, ok := given[param.Name]
		if !ok {
			value = Null()
			if param.Default != nil {
				value = *param.Default
			}
		}
		errs = checkParam(param, value, errs)
		values = append(values, value)
	}
	if len(errs) > 0 {
		return nil, errs
	}
	return values, nil
}

// checkParam checks one value against its parameter, like the same kind of document field, and
// appends the problems to errs.
func checkParam(param *ReportParam, value Value, errs []ValidationError) []ValidationError {
	field := param.Name
	if value.Kind == KindNull {
		if param.Required {
			errs = append(errs, ValidationError{Kind: ErrRequired, Field: field})
		}
		return errs
	}
	if value.Kind == KindText {
		if strings.ContainsRune(value.Text, 0) {
			return append(errs, ValidationError{Kind: ErrContainsNul, Field: field})
		}
		if param.Required && strings.TrimSpace(value.Text) == "" {
			return append(errs, ValidationError{Kind: ErrRequired, Field: field})
		}
	}
	kind := param.Kind
	switch {
	case (kind.Kind == ParamData || kind.Kind == ParamLink) && value.Kind == KindText:
		if n := utf8.RuneCountInString(value.Text); n > int(DefaultMaxLength) {
			errs = append(errs, ValidationError{Kind: ErrTooLong, Field: field, Len: n, Max: DefaultMaxLength})
		}
	case kind.Kind == ParamSelect && value.Kind == KindText:
		if !slices.Contains(kind.Options, value.Text) {
			errs = append(errs, ValidationError{Kind: ErrInvalidOption, Field: field, Value: value.Text})
		}
	case kind.Kind == ParamInt && value.Kind == KindInt,
		kind.Kind == ParamDecimal && value.Kind == KindDecimal,
		kind.Kind == ParamDate && value.Kind == KindDate,
		kind.Kind == ParamCheck && value.Kind == KindBool:
	default:
		errs = append(errs, ValidationError{Kind: ErrTypeMismatch, Field: field, Expected: kind.expected(), Found: value.KindName()})
	}
	return errs
}
