package db

import (
	"context"
	"fmt"
	"math"
	"strings"

	"github.com/tvstore/forgebase-go/internal/core"
)

// The list query: filter, sort and page the documents of one DocType (M3.2, ADR 0011; the Rust module
// list.rs).
//
// A ListQuery is a typed tree, not text: fields are looked up in the DocType's metadata (an unknown
// field is an error and is never put into SQL), identifiers are quoted, and every value is a bound
// parameter with an explicit cast taken from the field type. The caller's read access is part of the
// same statement ("owner" = $1 for an if_owner read, and the conditions of the caller's user
// permissions, M10.3, ADR 0025): rows the caller cannot read are never fetched, so no filter, sort or
// page can reveal one.
//
// Operators
//
//	Eq, Ne, In            every column field and standard column; a Link field takes the target's name
//	Lt, Le, Gt, Ge        Data, Text, Select, Int, Decimal, Date, Datetime and the text, time and number
//	                      standard columns (not Check, not Link)
//	Like                  text only: Data, Text, Select, name, owner, modified_by, workflow_state (a SQL
//	                      LIKE pattern: % any text, _ one character, \ escapes; case-sensitive)
//	IsNull                every column field and standard column
//
// Ne and the comparisons leave out rows where the field is null (SQL semantics); use IsNull(true) for
// those. Filters are combined with AND.
//
// Fields
//
// Filterable and sortable: every column field of the DocType (not Table fields, whose rows are not
// listed) and the standard columns name, owner, modified_by, created_at, modified_at, version and, for
// a submittable DocType, docstatus (an integer, 0, 1 or 2) and, for a DocType with a workflow,
// workflow_state (the name of the state; text, so like works). Sorting by a Link field sorts by the
// target's name. Nulls sort as PostgreSQL does: last in ascending, first in descending order.
//
// Paging
//
// Limit defaults to DefaultLimit (20) and may be at most MaxLimit (500): a larger value is an error,
// not silently reduced; 0 is an error too. Offset defaults to 0. The sort always ends with name
// ascending (it is unique), so the order is total and paging is stable.

const (
	// DefaultLimit is the number of documents List returns when ListQuery.Limit is nil.
	DefaultLimit uint32 = 20
	// MaxLimit is the largest allowed ListQuery.Limit.
	MaxLimit uint32 = 500
	// MaxInValues is the most values an In filter may have.
	MaxInValues = 500
)

// ListQuery is what List selects: filters (AND), a sort order and a page.
type ListQuery struct {
	// Filters are the conditions every returned document meets.
	Filters []Filter
	// Sort are the sort keys, most significant first. name ascending is always appended.
	Sort []Sort
	// Limit is the page size: at most MaxLimit, default DefaultLimit (nil).
	Limit *uint32
	// Offset is the number of documents to skip, default 0 (nil).
	Offset *uint64
}

// Filter is one filter: a field and a condition on it.
type Filter struct {
	// Field is a field name or standard column, see the package comment above.
	Field string
	// Op is the condition.
	Op FilterOp
}

// FilterOpKind is the operator of a FilterOp.
type FilterOpKind int

const (
	// OpEq is =.
	OpEq FilterOpKind = iota + 1
	// OpNe is <>.
	OpNe
	// OpLt is <.
	OpLt
	// OpLe is <=.
	OpLe
	// OpGt is >.
	OpGt
	// OpGe is >=.
	OpGe
	// OpIn is one of the values (1 to MaxInValues).
	OpIn
	// OpLike is a SQL LIKE pattern, text fields only.
	OpLike
	// OpIsNull is IS NULL with IsNull true, IS NOT NULL with false.
	OpIsNull
)

// FilterOp is a condition on a field. Operand values must have the kind of the field type (a text Value
// for Data, Text, Select and Link, an Int for Int, ...) and are never Null (use IsNull). Build one with
// Eq, Ne, Lt, Le, Gt, Ge, In, Like or IsNull.
type FilterOp struct {
	Kind FilterOpKind
	// Value is the operand of Eq, Ne, Lt, Le, Gt and Ge.
	Value core.Value
	// Values are the operands of In.
	Values []core.Value
	// Pattern is the operand of Like.
	Pattern string
	// Null is the argument of IsNull.
	Null bool
}

// Eq is =.
func Eq(v core.Value) FilterOp { return FilterOp{Kind: OpEq, Value: v} }

// Ne is <>.
func Ne(v core.Value) FilterOp { return FilterOp{Kind: OpNe, Value: v} }

// Lt is <.
func Lt(v core.Value) FilterOp { return FilterOp{Kind: OpLt, Value: v} }

// Le is <=.
func Le(v core.Value) FilterOp { return FilterOp{Kind: OpLe, Value: v} }

// Gt is >.
func Gt(v core.Value) FilterOp { return FilterOp{Kind: OpGt, Value: v} }

// Ge is >=.
func Ge(v core.Value) FilterOp { return FilterOp{Kind: OpGe, Value: v} }

// In is one of the values (1 to MaxInValues).
func In(values ...core.Value) FilterOp { return FilterOp{Kind: OpIn, Values: values} }

// Like is a SQL LIKE pattern, text fields only.
func Like(pattern string) FilterOp { return FilterOp{Kind: OpLike, Pattern: pattern} }

// IsNull is IS NULL with true, IS NOT NULL with false.
func IsNull(null bool) FilterOp { return FilterOp{Kind: OpIsNull, Null: null} }

// Sort is one sort key.
type Sort struct {
	// Field is a field name or standard column that is not a Table field.
	Field string
	// Descending is descending order.
	Descending bool
}

// Asc is ascending by field.
func Asc(field string) Sort { return Sort{Field: field} }

// Desc is descending by field.
func Desc(field string) Sort { return Sort{Field: field, Descending: true} }

// List returns the documents of doctype that the caller may read and that meet the query.
//
// The caller needs the read right: none at all is ErrPermissionDenied (that a DocType exists is not
// secret, and the answer does not depend on any document). With an if_owner read the statement is
// limited to the caller's own documents in SQL. An invalid query is ErrInvalidQuery; a name that is
// not a DocType is ErrUnknownDocType (nothing is put into SQL before it is found in the registry).
//
// The documents are as Get returns them (without the fields the caller cannot read, which a filter or
// sort may not name either: they are unknown, ADR 0024), except that Tables is empty: the rows of
// child tables are not listed. Link values are the targets' names. One statement, so the page is
// consistent without a transaction.
func List(ctx context.Context, q Queryer, registry *core.Registry, doctype string, caller core.Caller, query ListQuery) ([]*StoredDocument, error) {
	dt, err := documentDocType(registry, doctype)
	if err != nil {
		return nil, err
	}
	scope, err := readScopeOf(dt, caller)
	if err != nil {
		return nil, err
	}
	// A field the caller cannot read is unknown to the query (ADR 0024).
	dt = core.ReadableDocTypeFor(dt, caller)
	sql, binds, err := buildList(dt, scope, query)
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
	docs := []*StoredDocument{}
	for rows.Next() {
		stored, err := scanDocument(dt, rows)
		if err != nil {
			return nil, err
		}
		docs = append(docs, stored)
	}
	if err := rows.Err(); err != nil {
		return nil, wrap(err)
	}
	return docs, nil
}

// ColumnType is the type of a field the list query can filter or sort by (a column field or a
// standard column), and false for any other name, a Table field included. It lets callers map input
// values to the right Value kind.
func ColumnType(doctype *core.DocType, field string) (core.FieldType, bool) {
	col, err := listColumnOf(doctype, field)
	if err != nil {
		return core.FieldType{}, false
	}
	return col.fieldType, true
}

// listColumn is a filterable column: its qualified SQL expression and its type.
type listColumn struct {
	// expr is "doc_x"."field", quoted.
	expr      string
	fieldType core.FieldType
}

func invalidQuery(format string, args ...any) error {
	return &Error{Kind: ErrInvalidQuery, Message: fmt.Sprintf(format, args...)}
}

// listColumnOf looks field up in the metadata. Only names found here ever reach the SQL, quoted.
func listColumnOf(doctype *core.DocType, field string) (listColumn, error) {
	table := QuoteIdent(core.TableName(doctype.Name))
	var standard *core.FieldType
	text, datetime, integer := core.FieldType{Kind: core.FieldText}, core.FieldType{Kind: core.FieldDatetime}, core.FieldType{Kind: core.FieldInt}
	switch {
	case field == "name" || field == "owner" || field == "modified_by":
		standard = &text
	case field == "created_at" || field == "modified_at":
		standard = &datetime
	case field == "version":
		standard = &integer
	case field == "docstatus" && doctype.Submittable:
		standard = &integer
	// A text column of a DocType with a workflow (M8.1); filter and sort by the state name.
	case field == "workflow_state" && doctype.Workflow != nil:
		standard = &text
	}
	var fieldType core.FieldType
	f := doctype.Field(field)
	switch {
	case standard != nil:
		fieldType = *standard
	case f != nil && f.Type.Kind == core.FieldTable:
		return listColumn{}, invalidQuery("field %s is a table field: its rows cannot be filtered or sorted", core.DebugQuote(field))
	case f != nil:
		fieldType = f.Type
	default:
		return listColumn{}, invalidQuery("unknown field %s for DocType `%s`", core.DebugQuote(core.Shorten(field)), doctype.Name)
	}
	return listColumn{expr: table + "." + QuoteIdent(field), fieldType: fieldType}, nil
}

// listBinds collects the bound values and hands out numbered placeholders with a cast.
type listBinds struct {
	values []core.Value
	casts  []string
}

// add binds value to a new placeholder cast to cast and returns the placeholder. A cast such as
// text::text[] (the array literal of a user permission, see arrayLiteral) binds its value as the
// text the first type names.
func (b *listBinds) add(value core.Value, cast string) string {
	b.values = append(b.values, value)
	bound, _, _ := strings.Cut(cast, "::")
	b.casts = append(b.casts, bound)
	return fmt.Sprintf("$%d::%s", len(b.values), cast)
}

// args are the query arguments, each converted for the cast of its placeholder.
func (b *listBinds) args() ([]any, error) {
	out := make([]any, len(b.values))
	for i, v := range b.values {
		arg, err := bindValue(b.casts[i], v, i+1)
		if err != nil {
			return nil, err
		}
		out[i] = arg
	}
	return out, nil
}

// listConditions are the WHERE conditions of a read: the read scope first (the owner and the user
// permissions, all bound, so nothing can be injected), then one per filter. The list, the group-by
// summary and the export build theirs here.
func listConditions(doctype *core.DocType, scope readScope, filters []Filter, binds *listBinds) ([]string, error) {
	table := QuoteIdent(core.TableName(doctype.Name))
	conditions := scope.conditions(table, func(value, cast string) string {
		return binds.add(core.Text(value), cast)
	})
	for _, filter := range filters {
		col, err := listColumnOf(doctype, filter.Field)
		if err != nil {
			return nil, err
		}
		cond, err := condition(binds, filter.Field, col, filter.Op)
		if err != nil {
			return nil, err
		}
		conditions = append(conditions, cond)
	}
	return conditions, nil
}

// buildList builds the statement and its bound values. scope limits it to what the caller may read.
func buildList(doctype *core.DocType, scope readScope, query ListQuery) (string, *listBinds, error) {
	limit := DefaultLimit
	if query.Limit != nil {
		switch n := *query.Limit; {
		case n == 0:
			return "", nil, invalidQuery("limit must be at least 1")
		case n > MaxLimit:
			return "", nil, invalidQuery("limit %d is above the maximum of %d", n, MaxLimit)
		default:
			limit = n
		}
	}
	var offset uint64
	if query.Offset != nil {
		offset = *query.Offset
	}
	if offset > math.MaxInt64 {
		return "", nil, invalidQuery("offset is too large")
	}

	table := QuoteIdent(core.TableName(doctype.Name))
	binds := &listBinds{}
	conditions, err := listConditions(doctype, scope, query.Filters, binds)
	if err != nil {
		return "", nil, err
	}
	order, err := orderKeys(doctype, query.Sort)
	if err != nil {
		return "", nil, err
	}
	var sb strings.Builder
	sb.WriteString("SELECT " + selectList(doctype) + " FROM " + table)
	if len(conditions) > 0 {
		sb.WriteString(" WHERE " + strings.Join(conditions, " AND "))
	}
	sb.WriteString(" ORDER BY " + strings.Join(order, ", "))
	limitP := binds.add(core.Int(int64(limit)), "bigint")
	offsetP := binds.add(core.Int(int64(offset)), "bigint")
	sb.WriteString(" LIMIT " + limitP + " OFFSET " + offsetP)
	return sb.String(), binds, nil
}

// orderKeys are the ORDER BY keys of a sort: one per sort key, then name ascending (unique, so the
// order is total). A Link column sorts by the target's name.
func orderKeys(doctype *core.DocType, sorts []Sort) ([]string, error) {
	table := QuoteIdent(core.TableName(doctype.Name))
	var order []string
	for _, s := range sorts {
		col, err := listColumnOf(doctype, s.Field)
		if err != nil {
			return nil, err
		}
		key := col.expr
		if col.fieldType.Kind == core.FieldLink {
			// A Link column holds the target's id; order by what the user sees, its name.
			key = fmt.Sprintf("(SELECT t.\"name\" FROM %s t WHERE t.\"id\" = %s)",
				QuoteIdent(core.TableName(col.fieldType.Target)), col.expr)
		}
		direction := "ASC"
		if s.Descending {
			direction = "DESC"
		}
		order = append(order, key+" "+direction)
	}
	return append(order, table+".\"name\" ASC"), nil
}

// condition is the SQL condition of one filter; values are bound.
func condition(binds *listBinds, field string, col listColumn, op FilterOp) (string, error) {
	ft := col.fieldType
	expr := col.expr
	cast := castType(ft)
	link := ""
	if ft.Kind == core.FieldLink {
		link = QuoteIdent(core.TableName(ft.Target))
	}
	switch op.Kind {
	case OpIsNull:
		if op.Null {
			return expr + " IS NULL", nil
		}
		return expr + " IS NOT NULL", nil
	case OpEq, OpNe:
		sqlOp := "<>"
		if op.Kind == OpEq {
			sqlOp = "="
		}
		if err := checkOperand(field, ft, op.Value); err != nil {
			return "", err
		}
		if link != "" {
			// The target's id for a name; a name that does not exist matches nothing for = and every
			// non-null link for <>.
			p := binds.add(op.Value, "text")
			sub := fmt.Sprintf("(SELECT t.\"id\" FROM %s t WHERE t.\"name\" = %s)", link, p)
			if sqlOp == "=" {
				return expr + " IN " + sub, nil
			}
			// NOT IN an empty set is true even for NULL: leave unlinked rows out explicitly, as <>
			// does for every other field.
			return fmt.Sprintf("(%s IS NOT NULL AND %s NOT IN %s)", expr, expr, sub), nil
		}
		return fmt.Sprintf("%s %s %s", expr, sqlOp, binds.add(op.Value, cast)), nil
	case OpLt, OpLe, OpGt, OpGe:
		sqlOp := map[FilterOpKind]string{OpLt: "<", OpLe: "<=", OpGt: ">", OpGe: ">="}[op.Kind]
		if ft.Kind == core.FieldCheck || ft.Kind == core.FieldLink {
			return "", invalidQuery("operator %s does not apply to %s field %s", sqlOp, ft.Name(), core.DebugQuote(field))
		}
		if err := checkOperand(field, ft, op.Value); err != nil {
			return "", err
		}
		return fmt.Sprintf("%s %s %s", expr, sqlOp, binds.add(op.Value, cast)), nil
	case OpIn:
		if len(op.Values) == 0 || len(op.Values) > MaxInValues {
			return "", invalidQuery("`in` on field %s needs 1 to %d values, got %d", core.DebugQuote(field), MaxInValues, len(op.Values))
		}
		placeholders := make([]string, 0, len(op.Values))
		for _, v := range op.Values {
			if err := checkOperand(field, ft, v); err != nil {
				return "", err
			}
			c := cast
			if link != "" {
				c = "text"
			}
			placeholders = append(placeholders, binds.add(v, c))
		}
		list := strings.Join(placeholders, ", ")
		if link != "" {
			return fmt.Sprintf("%s IN (SELECT t.\"id\" FROM %s t WHERE t.\"name\" IN (%s))", expr, link, list), nil
		}
		return fmt.Sprintf("%s IN (%s)", expr, list), nil
	case OpLike:
		if ft.Kind != core.FieldData && ft.Kind != core.FieldText && ft.Kind != core.FieldSelect {
			return "", invalidQuery("`like` applies to text fields only, but field %s is %s", core.DebugQuote(field), ft.Name())
		}
		if strings.ContainsRune(op.Pattern, 0) {
			return "", invalidQuery("the pattern for field %s must not contain a NUL character", core.DebugQuote(field))
		}
		return fmt.Sprintf("%s LIKE %s", expr, binds.add(core.Text(op.Pattern), "text")), nil
	}
	return "", invalidQuery("unknown operator")
}

// checkOperand: an operand must be a non-null value of the kind the field type takes.
func checkOperand(field string, ft core.FieldType, value core.Value) error {
	if value.Kind == core.KindNull {
		return invalidQuery("field %s: a filter value cannot be null (use `is null`)", core.DebugQuote(field))
	}
	switch {
	case value.Kind == core.KindText && (ft.Kind == core.FieldData || ft.Kind == core.FieldText || ft.Kind == core.FieldSelect || ft.Kind == core.FieldLink):
		if strings.ContainsRune(value.Text, 0) {
			return invalidQuery("field %s: a value must not contain a NUL character", core.DebugQuote(field))
		}
		return nil
	case value.Kind == core.KindDatetime && ft.Kind == core.FieldDatetime:
		if y := value.Time.UTC().Year(); y < minYear || y > maxYear {
			return invalidQuery("field %s: the datetime is out of range once converted to UTC", core.DebugQuote(field))
		}
		return nil
	case value.Kind == core.KindInt && ft.Kind == core.FieldInt,
		value.Kind == core.KindDecimal && ft.Kind == core.FieldDecimal,
		value.Kind == core.KindBool && ft.Kind == core.FieldCheck,
		value.Kind == core.KindDate && ft.Kind == core.FieldDate:
		return nil
	}
	return invalidQuery("field %s is %s: expected a %s value, found %s", core.DebugQuote(field), ft.Name(), ft.Name(), value.KindName())
}
