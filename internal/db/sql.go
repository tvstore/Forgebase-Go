package db

import (
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/tvstore/forgebase-go/internal/core"
)

// The statements of the document operations and the decoding of their rows. SQL is built only from
// validated, quoted identifiers and numeric placeholders with explicit casts; values are always
// bound parameters.

// standardColumns are the standard columns, in the order they are selected (plan.go: the DDL has
// the same).
var standardColumns = [7]string{"id", "name", "owner", "created_at", "modified_at", "modified_by", "version"}

// columnList is `"id", "name", ..., "<field>", ...`: the columns of the table, in decode order.
func columnList(doctype *core.DocType) string {
	items := make([]string, 0, len(standardColumns)+len(doctype.Fields))
	for _, c := range standardColumns {
		items = append(items, QuoteIdent(c))
	}
	for _, f := range doctype.ColumnFields() {
		items = append(items, QuoteIdent(f.Name))
	}
	return strings.Join(items, ", ")
}

// fieldItems is what every statement returns for the fields, in decode order: the column, except
// that a Link column is replaced by the target document's name (a scalar subquery on the target's
// id, aliased to the field name; the outer column is qualified with the table name so a self-link
// resolves).
func fieldItems(doctype *core.DocType) []string {
	table := QuoteIdent(core.TableName(doctype.Name))
	var items []string
	for _, f := range doctype.ColumnFields() {
		column := QuoteIdent(f.Name)
		if f.Type.Kind == core.FieldLink {
			items = append(items, fmt.Sprintf("(SELECT t.\"name\" FROM %s t WHERE t.\"id\" = %s.%s) AS %s",
				QuoteIdent(core.TableName(f.Type.Target)), table, column, column))
		} else {
			items = append(items, column)
		}
	}
	return items
}

// statusColumns are the columns after the standard ones that only a submittable DocType's table
// has.
func statusColumns(doctype *core.DocType) []string {
	if doctype.Submittable {
		return []string{"docstatus", "amended_from"}
	}
	return nil
}

// workflowSelectColumns is the column after the statusColumns that only the table of a DocType with a
// workflow has.
func workflowSelectColumns(doctype *core.DocType) []string {
	if doctype.Workflow != nil {
		return []string{core.WorkflowColumn}
	}
	return nil
}

// selectList is what every statement on a document table returns, in decode order: the standard
// columns, the statusColumns, the workflowSelectColumns, then fieldItems.
func selectList(doctype *core.DocType) string {
	var items []string
	for _, c := range standardColumns {
		items = append(items, QuoteIdent(c))
	}
	for _, c := range statusColumns(doctype) {
		items = append(items, QuoteIdent(c))
	}
	for _, c := range workflowSelectColumns(doctype) {
		items = append(items, QuoteIdent(c))
	}
	items = append(items, fieldItems(doctype)...)
	return strings.Join(items, ", ")
}

// getSQL is the statement for one document by name ($1); scope is the " AND ..." conditions of the
// caller's read scope (readScope.andSQL(2): if_owner and user permissions), empty for a caller who
// reads every document.
func getSQL(doctype *core.DocType, scope string) string {
	return fmt.Sprintf("SELECT %s FROM %s WHERE \"name\" = $1::text%s",
		selectList(doctype), QuoteIdent(core.TableName(doctype.Name)), scope)
}

// fieldPlaceholders are $1::<type>, $2::<type>, ... for the column fields, numbered from first.
func fieldPlaceholders(doctype *core.DocType, first int) []string {
	var out []string
	for i, f := range doctype.ColumnFields() {
		out = append(out, fmt.Sprintf("$%d::%s", first+i, castType(f.Type)))
	}
	return out
}

// insertSQL has placeholders $1 (id), $2 (name) and $3 (actor, used twice), then one per column
// field, then, for an amendment, amended_from, then, for a DocType with a workflow, the initial
// state. docstatus is not written: its default (0) is a draft.
func insertSQL(doctype *core.DocType, amended bool) string {
	values := []string{"$1::uuid", "$2::text", "$3::text", "now()", "now()", "$3::text", "1"}
	values = append(values, fieldPlaceholders(doctype, 4)...)
	columns := columnList(doctype)
	fields := len(doctype.ColumnFields())
	if amended {
		columns += ", " + QuoteIdent("amended_from")
		values = append(values, fmt.Sprintf("$%d::text", 4+fields))
	}
	if doctype.Workflow != nil {
		n := 4 + fields
		if amended {
			n++
		}
		columns += ", " + QuoteIdent(core.WorkflowColumn)
		values = append(values, fmt.Sprintf("$%d::text", n))
	}
	return fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s) RETURNING %s",
		QuoteIdent(core.TableName(doctype.Name)), columns, strings.Join(values, ", "), selectList(doctype))
}

// childInsertSQL has placeholders $1 (row id), $2 (parent id) and $3 (idx), then one per field of
// the child.
func childInsertSQL(child *core.DocType) string {
	columns := []string{QuoteIdent("id"), QuoteIdent("parent_id"), QuoteIdent("idx")}
	for _, f := range child.ColumnFields() {
		columns = append(columns, QuoteIdent(f.Name))
	}
	values := []string{"$1::uuid", "$2::uuid", "$3::integer"}
	values = append(values, fieldPlaceholders(child, 4)...)
	return fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)",
		QuoteIdent(core.TableName(child.Name)), strings.Join(columns, ", "), strings.Join(values, ", "))
}

// childSelectSQL are the rows of a parent ($1 = its id) in order, as fieldItems.
func childSelectSQL(child *core.DocType) string {
	return fmt.Sprintf("SELECT %s FROM %s WHERE \"parent_id\" = $1::uuid ORDER BY \"idx\"",
		strings.Join(fieldItems(child), ", "), QuoteIdent(core.TableName(child.Name)))
}

// updateSQL has placeholders $1..$n for the changes (in key order), $n+1 actor, $n+2 name, $n+3
// expected version. With status the new docstatus is set too (a constant of the enum, not a value
// from outside). changes has passed validation, so every key is a field of the DocType (the text
// fallback cast is unreachable; an unknown column would be rejected by the database, never
// interpolated).
func updateSQL(doctype *core.DocType, changes map[string]core.Value, status *core.DocStatus) string {
	keys := sortedKeys(changes)
	sets := make([]string, 0, len(keys)+3)
	for i, name := range keys {
		cast := "text"
		if f := doctype.Field(name); f != nil {
			cast = castType(f.Type)
		}
		sets = append(sets, fmt.Sprintf("%s = $%d::%s", QuoteIdent(name), i+1, cast))
	}
	n := len(keys)
	sets = append(sets, "\"modified_at\" = now()")
	sets = append(sets, fmt.Sprintf("\"modified_by\" = $%d::text", n+1))
	sets = append(sets, "\"version\" = \"version\" + 1")
	if status != nil {
		sets = append(sets, fmt.Sprintf("\"docstatus\" = %d", status.Int16()))
	}
	return fmt.Sprintf("UPDATE %s SET %s WHERE \"name\" = $%d::text AND \"version\" = $%d::bigint RETURNING %s",
		QuoteIdent(core.TableName(doctype.Name)), strings.Join(sets, ", "), n+2, n+3, selectList(doctype))
}

// fieldDests are scan destinations for the column fields of doctype, in order.
func fieldDests(doctype *core.DocType) []any {
	fields := doctype.ColumnFields()
	dests := make([]any, len(fields))
	for i, f := range fields {
		dests[i] = columnDest(f.Type)
	}
	return dests
}

// decodeFields turns what fieldDests scanned into the values of the column fields of doctype.
func decodeFields(doctype *core.DocType, dests []any) (map[string]core.Value, error) {
	values := make(map[string]core.Value, len(dests))
	for i, f := range doctype.ColumnFields() {
		v, err := fromColumn(dests[i])
		if err != nil {
			return nil, &Error{Kind: ErrDecode, DocType: doctype.Name, Field: f.Name, Message: err.Error()}
		}
		values[f.Name] = v
	}
	return values, nil
}

// rowScanner is what both a pgx.Row and a pgx.Rows can do.
type rowScanner interface {
	Scan(dest ...any) error
}

// scanDocument reads a row of selectList. Tables is left empty (a non-nil map) for the caller to
// fill. pgx.ErrNoRows is returned as it is.
func scanDocument(doctype *core.DocType, row rowScanner) (*StoredDocument, error) {
	var (
		id                    [16]byte
		name, owner, modBy    string
		createdAt, modifiedAt pgtype.Timestamptz
		version               int64
		docstatus             int16
		amendedFrom, state    pgtype.Text
	)
	dests := []any{&id, &name, &owner, &createdAt, &modifiedAt, &modBy, &version}
	if doctype.Submittable {
		dests = append(dests, &docstatus, &amendedFrom)
	}
	if doctype.Workflow != nil {
		dests = append(dests, &state)
	}
	fields := fieldDests(doctype)
	dests = append(dests, fields...)
	if err := row.Scan(dests...); err != nil {
		if err == pgx.ErrNoRows {
			return nil, err
		}
		return nil, wrap(err)
	}
	stored := &StoredDocument{
		ID:         id,
		Name:       name,
		Owner:      owner,
		ModifiedBy: modBy,
		Version:    version,
		Tables:     map[string][]core.Row{},
	}
	for _, t := range []struct {
		src pgtype.Timestamptz
		dst *time.Time
	}{{createdAt, &stored.CreatedAt}, {modifiedAt, &stored.ModifiedAt}} {
		if !t.src.Valid || t.src.InfinityModifier != pgtype.Finite {
			return nil, &Error{Kind: ErrPostgres, Message: "cannot read a timestamp of the document: it is NULL or infinite"}
		}
		*t.dst = t.src.Time.UTC()
	}
	if doctype.Submittable {
		status, ok := core.DocStatusFromInt16(docstatus)
		if !ok {
			return nil, &Error{Kind: ErrDecode, DocType: doctype.Name, Field: "docstatus",
				Message: fmt.Sprintf("%d is not a docstatus (0, 1 or 2)", docstatus)}
		}
		stored.DocStatus = &status
		if amendedFrom.Valid {
			stored.AmendedFrom = amendedFrom.String
		}
	}
	if doctype.Workflow != nil && state.Valid {
		stored.WorkflowState = state.String
	}
	values, err := decodeFields(doctype, fields)
	if err != nil {
		return nil, err
	}
	stored.Values = values
	return stored, nil
}
