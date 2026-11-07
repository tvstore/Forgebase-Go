package db

import (
	"context"
	"fmt"
	"strings"

	"github.com/tvstore/forgebase-go/internal/core"
	"github.com/tvstore/forgebase-go/internal/decimal"
)

// The group-by summary of a list (M8.3, architecture section 11): documents grouped by one field, with a
// count and optional sum, average, minimum and maximum per group (the Rust module summary.rs).
//
// It is a read like List, and takes everything that decides what is read from the list: the read scope
// (readScopeOf, so an if_owner read counts the caller's own documents only and a missing read right is
// ErrPermissionDenied) and the filters (listConditions, so a filter is checked and bound exactly as in a
// list). No second permission check exists. A summary gives no right: it sees only rows the list sees.
//
// Fields
//
//   - Group by one of owner, modified_by, docstatus (submittable DocTypes), workflow_state (DocTypes with
//     a workflow), or a Data, Select, Link, Check, Date or Int field. A Link groups by the target's name,
//     as a filter takes it. Not Text, Table, Decimal or Datetime fields, and not name, created_at,
//     modified_at or version (nearly every document would be a group of its own). Documents with no value
//     form one group whose key is Null.
//   - Aggregates: count is always there. sum, avg, min and max apply to Int and Decimal fields; min and max
//     also to Date fields. At most MaxAggregates, none twice. The result key is <function>_<field>. sum
//     and min/max of an Int are integers (a sum beyond 64 bits is a decimal); a Decimal field's results
//     are decimals; avg is a decimal rounded to the field's scale plus 2 digits (6 digits for an Int).
//     Groups where every value is null have a null aggregate.
//
// Groups are ordered by count descending, then by key (byte order for text, nulls last), and at most
// MaxGroups are returned; Truncated says there were more. A child DocType is refused, as in a list.

const (
	// MaxGroups is the most groups a summary returns.
	MaxGroups = 500
	// MaxAggregates is the most aggregates besides the count.
	MaxAggregates = 6
)

// AggregateFn is an aggregate function (the count is always included and is not listed).
type AggregateFn int

const (
	// AggSum is the total of an Int or Decimal field.
	AggSum AggregateFn = iota + 1
	// AggAvg is the average of an Int or Decimal field.
	AggAvg
	// AggMin is the smallest value of an Int, Decimal or Date field.
	AggMin
	// AggMax is the largest value of an Int, Decimal or Date field.
	AggMax
)

// Name is sum, avg, min or max: the prefix of the result key.
func (f AggregateFn) Name() string {
	switch f {
	case AggSum:
		return "sum"
	case AggAvg:
		return "avg"
	case AggMin:
		return "min"
	case AggMax:
		return "max"
	}
	return "unknown"
}

// AggregateFromName is the function with this name.
func AggregateFromName(name string) (AggregateFn, bool) {
	for _, f := range []AggregateFn{AggSum, AggAvg, AggMin, AggMax} {
		if f.Name() == name {
			return f, true
		}
	}
	return 0, false
}

// Aggregate is a function and the field it is applied to.
type Aggregate struct {
	Fn    AggregateFn
	Field string
}

// SummaryQuery is what Summary computes.
type SummaryQuery struct {
	// Filters are conditions every counted document meets (as in a list).
	Filters []Filter
	// GroupBy is the field to group by, see the comment above.
	GroupBy string
	// Aggregates are the aggregates besides the count.
	Aggregates []Aggregate
}

// Group is one group.
type Group struct {
	// Key is the value of the group-by field; Null for documents without one.
	Key core.Value
	// Count is the number of documents.
	Count int64
	// Values are the aggregates, by <function>_<field>.
	Values map[string]core.Value
}

// SummaryResult is the result of Summary.
type SummaryResult struct {
	// Groups are at most MaxGroups groups, largest first.
	Groups []Group
	// Truncated is true when there were more groups than are returned.
	Truncated bool
}

// selected is how an aggregate is read back.
type selected struct {
	name      string
	fieldType core.FieldType
	fn        AggregateFn
}

// Summary groups the documents of doctype that the caller may read and that meet the filters. See the
// comment above. An invalid query is ErrInvalidQuery; no read right is ErrPermissionDenied; an unknown or
// child DocType is an error as in a list.
func Summary(ctx context.Context, q Queryer, registry *core.Registry, doctype string, caller core.Caller, query SummaryQuery) (*SummaryResult, error) {
	dt, err := documentDocType(registry, doctype)
	if err != nil {
		return nil, err
	}
	scope, err := readScopeOf(dt, caller)
	if err != nil {
		return nil, err
	}
	// A group-by, an aggregate or a filter on a field the caller cannot read is an unknown field (ADR
	// 0024).
	dt = core.ReadableDocTypeFor(dt, caller)
	sql, binds, keyType, items, err := buildSummary(dt, scope, query)
	if err != nil {
		return nil, err
	}
	args, err := binds.args()
	if err != nil {
		return nil, wrap(err)
	}
	rows, err := q.Query(ctx, sql, withFormats(args...)...)
	if err != nil {
		return nil, wrap(err)
	}
	defer rows.Close()
	result := &SummaryResult{Groups: []Group{}}
	for rows.Next() {
		if len(result.Groups) == MaxGroups {
			result.Truncated = true
			break
		}
		keyDest := columnDest(keyType)
		dests := []any{keyDest, new(int64)}
		aggDests := make([]any, len(items))
		for i, item := range items {
			aggDests[i] = aggregateDest(item)
			dests = append(dests, aggDests[i])
		}
		if err := rows.Scan(dests...); err != nil {
			return nil, wrap(err)
		}
		key, err := fromColumn(keyDest)
		if err != nil {
			return nil, &Error{Kind: ErrDecode, DocType: dt.Name, Field: query.GroupBy, Message: err.Error()}
		}
		group := Group{Key: key, Count: *dests[1].(*int64), Values: map[string]core.Value{}}
		for i, item := range items {
			v, err := readAggregate(aggDests[i], item)
			if err != nil {
				return nil, &Error{Kind: ErrDecode, DocType: dt.Name, Field: item.name, Message: err.Error()}
			}
			group.Values[item.name] = v
		}
		result.Groups = append(result.Groups, group)
	}
	if err := rows.Err(); err != nil {
		return nil, wrap(err)
	}
	return result, nil
}

// aggregateDest is the scan destination of an aggregate: a sum or an average is numeric (as text, to keep
// the scale), a minimum or maximum has the type of the field.
func aggregateDest(item selected) any {
	if item.fn == AggSum || item.fn == AggAvg {
		return new(numericText)
	}
	return columnDest(item.fieldType)
}

func readAggregate(dest any, item selected) (core.Value, error) {
	if item.fn == AggMin || item.fn == AggMax {
		return fromColumn(dest)
	}
	n := dest.(*numericText)
	if !n.Valid {
		return core.Null(), nil
	}
	d, err := decimal.Parse(n.String)
	if err != nil {
		return core.Null(), err
	}
	// Computed as numeric in SQL: an Int total is an integer unless it outgrows 64 bits.
	if item.fn == AggSum && item.fieldType.Kind == core.FieldInt && d.Scale() == 0 && d.Mantissa().IsInt64() {
		return core.Int(d.Mantissa().Int64()), nil
	}
	return core.Decimal(d), nil
}

// groupKey is the group-by column: the SQL of the key, its type and whether it is text (ordered by bytes).
func groupKey(doctype *core.DocType, name string) (string, core.FieldType, bool, error) {
	if name == "name" || name == "created_at" || name == "modified_at" || name == "version" {
		return "", core.FieldType{}, false, invalidQuery("cannot group by %s: almost every document has its own value", core.DebugQuote(name))
	}
	col, err := listColumnOf(doctype, name)
	if err != nil {
		return "", core.FieldType{}, false, err
	}
	// A field is looked up first: a Text field is refused, but the standard text columns (owner,
	// modified_by, workflow_state) are fine.
	isField := doctype.Field(name) != nil
	switch ft := col.fieldType; {
	case ft.Kind == core.FieldData || ft.Kind == core.FieldSelect:
		return col.expr, ft, true, nil
	case ft.Kind == core.FieldText && !isField:
		return col.expr, ft, true, nil
	case ft.Kind == core.FieldLink:
		return fmt.Sprintf("(SELECT t.\"name\" FROM %s t WHERE t.\"id\" = %s)", QuoteIdent(core.TableName(ft.Target)), col.expr), ft, true, nil
	case ft.Kind == core.FieldInt:
		return col.expr + "::bigint", ft, false, nil
	case ft.Kind == core.FieldCheck || ft.Kind == core.FieldDate:
		return col.expr, ft, false, nil
	default:
		return "", core.FieldType{}, false, invalidQuery("cannot group by %s field %s (use a data, select, link, check, date or int field)", ft.Name(), core.DebugQuote(name))
	}
}

func buildSummary(doctype *core.DocType, scope readScope, query SummaryQuery) (string, *listBinds, core.FieldType, []selected, error) {
	fail := func(err error) (string, *listBinds, core.FieldType, []selected, error) {
		return "", nil, core.FieldType{}, nil, err
	}
	table := QuoteIdent(core.TableName(doctype.Name))
	key, keyType, textKey, err := groupKey(doctype, query.GroupBy)
	if err != nil {
		return fail(err)
	}
	if len(query.Aggregates) > MaxAggregates {
		return fail(invalidQuery("at most %d aggregates, got %d", MaxAggregates, len(query.Aggregates)))
	}
	var exprs []string
	var items []selected
	for _, agg := range query.Aggregates {
		name := agg.Fn.Name() + "_" + agg.Field
		for _, s := range items {
			if s.name == name {
				return fail(invalidQuery("the aggregate %s is listed twice", core.DebugQuote(core.Shorten(name))))
			}
		}
		// Only fields: the standard columns are not summed.
		f := doctype.Field(agg.Field)
		switch {
		case f == nil:
			return fail(invalidQuery("unknown field %s for DocType `%s` in %s", core.DebugQuote(core.Shorten(agg.Field)),
				doctype.Name, core.DebugQuote(core.Shorten(name))))
		case f.Type.Kind == core.FieldTable:
			return fail(invalidQuery("field %s is a table field: its rows cannot be summarised", core.DebugQuote(agg.Field)))
		}
		fieldType := f.Type
		fits := fieldType.Kind == core.FieldInt || fieldType.Kind == core.FieldDecimal ||
			(fieldType.Kind == core.FieldDate && (agg.Fn == AggMin || agg.Fn == AggMax))
		if !fits {
			return fail(invalidQuery("%s cannot be applied to %s field %s (sum and avg take int and decimal fields, min and max also date fields)",
				agg.Fn.Name(), fieldType.Name(), core.DebugQuote(agg.Field)))
		}
		col, err := listColumnOf(doctype, agg.Field)
		if err != nil {
			return fail(err)
		}
		switch agg.Fn {
		case AggSum:
			exprs = append(exprs, fmt.Sprintf("sum(%s)::numeric", col.expr))
		case AggAvg:
			scale := 6
			if fieldType.Kind == core.FieldDecimal {
				scale = int(fieldType.Scale) + 2
			}
			exprs = append(exprs, fmt.Sprintf("round(avg(%s), %d)::numeric", col.expr, scale))
		case AggMin:
			exprs = append(exprs, fmt.Sprintf("min(%s)", col.expr))
		case AggMax:
			exprs = append(exprs, fmt.Sprintf("max(%s)", col.expr))
		}
		items = append(items, selected{name: name, fieldType: fieldType, fn: agg.Fn})
	}

	binds := &listBinds{}
	conditions, err := listConditions(doctype, scope, query.Filters, binds)
	if err != nil {
		return fail(err)
	}
	selectList := append([]string{key + " AS \"key\"", "count(*)"}, exprs...)
	sql := "SELECT " + strings.Join(selectList, ", ") + " FROM " + table
	if len(conditions) > 0 {
		sql += " WHERE " + strings.Join(conditions, " AND ")
	}
	orderKey := "(" + key + ")"
	if textKey {
		orderKey = "(" + key + ") COLLATE \"C\""
	}
	// One more than the cap, to know that there are more.
	sql += fmt.Sprintf(" GROUP BY 1 ORDER BY count(*) DESC, %s ASC LIMIT %d", orderKey, MaxGroups+1)
	return sql, binds, keyType, items, nil
}
