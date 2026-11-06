package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/tvstore/forgebase-go/internal/core"
)

// Links and child tables: resolving Link names to ids on the way in, writing and reading the rows
// of Table fields.

// tableWrite is the rows to write for one Table field, with their Link values already resolved.
type tableWrite struct {
	child *core.DocType
	rows  []rowWrite
}

type rowWrite struct {
	row   core.Row
	links map[string][16]byte
}

// sqlParam is the bound parameter for one field: the resolved id for a Link, else the value
// itself. position is the placeholder number, for the message of a bad value.
func sqlParam(ft core.FieldType, links map[string][16]byte, field string, v core.Value, position int) (any, error) {
	if ft.Kind == core.FieldLink {
		if id, ok := links[field]; ok {
			return id, nil
		}
		return nil, nil
	}
	arg, err := bindValue(castType(ft), v, position)
	if err != nil {
		return nil, &Error{Kind: ErrPostgres, Message: err.Error(), Err: err}
	}
	return arg, nil
}

// resolve resolves the Links of the document's values and of every row. It returns the ids of the
// document's Links and the rows to write; a Link name that does not exist is added to errs. A
// target the caller cannot read counts as missing (decision 9, see resolveLinks).
//
// With stored (update and submit), a Link value that is already stored is kept: a document Link
// that equals the stored one, and a row Link whose name is among the stored values of the same
// field in the same table, is resolved with full access (the caller already sees that name through
// Get). Only new or changed Link values get the caller's read check.
func resolve(ctx context.Context, c Queryer, registry *core.Registry, caller core.Caller, doctype *core.DocType,
	values map[string]core.Value, tables map[string][]core.Row, stored *StoredDocument, errs *[]core.ValidationError,
) (map[string][16]byte, []tableWrite, error) {
	// Fields that already have a problem are not looked up (their value may not even be sendable
	// to the database, for example text with NUL).
	skip := map[string]bool{}
	for _, e := range *errs {
		if e.Kind != core.ErrInRow {
			skip[e.FieldName()] = true
		}
	}
	keptValue := func(field, link string) bool {
		if stored == nil {
			return false
		}
		v, ok := stored.Values[field]
		return ok && v.Equal(core.Text(link))
	}
	links, found, err := resolveLinks(ctx, c, registry, caller, doctype, values, skip, keptValue)
	if err != nil {
		return nil, nil, err
	}
	*errs = append(*errs, found...)

	var writes []tableWrite
	for _, name := range sortedKeys(tables) {
		rows := tables[name]
		f := doctype.Field(name)
		if f == nil || f.Type.Kind != core.FieldTable {
			continue // reported by validation
		}
		child, err := lookupDocType(registry, f.Type.Target)
		if err != nil {
			return nil, nil, err
		}
		resolved := make([]rowWrite, 0, len(rows))
		for i, row := range rows {
			position := i + 1
			rowSkip := map[string]bool{}
			for _, e := range *errs {
				if e.Kind == core.ErrInRow && e.Field == name && e.Row == position {
					rowSkip[e.Inner.FieldName()] = true
				}
			}
			keptRow := func(field, link string) bool {
				want := core.Text(link)
				if stored == nil {
					return false
				}
				for _, r := range stored.Tables[name] {
					if v, ok := r[field]; ok && v.Equal(want) {
						return true
					}
				}
				return false
			}
			ids, found, err := resolveLinks(ctx, c, registry, caller, child, row, rowSkip, keptRow)
			if err != nil {
				return nil, nil, err
			}
			for _, e := range found {
				inner := e
				*errs = append(*errs, core.ValidationError{Kind: core.ErrInRow, Field: name, Row: position, Inner: &inner})
			}
			resolved = append(resolved, rowWrite{row: row, links: ids})
		}
		writes = append(writes, tableWrite{child: child, rows: resolved})
	}
	return links, writes, nil
}

// resolveLinks resolves the Link values in values (name to target id), locking each target row
// against deletion. A name that does not exist is returned as core.ErrLinkNotFound. Fields in skip
// are not looked up. A value for which kept(field, name) is true is already stored on the
// document: it is resolved with full access, without the caller's read check.
//
// Decision 9. The lookup uses the caller's read access on the target DocType, so a target the
// caller cannot read gives exactly the error of a missing one: with no read access to the target
// DocType nothing is looked up at all; with an if_owner read the query has AND "owner" = $2
// (bound, in SQL). The error is built in one place for both cases.
func resolveLinks(ctx context.Context, c Queryer, registry *core.Registry, caller core.Caller, doctype *core.DocType,
	values map[string]core.Value, skip map[string]bool, kept func(field, name string) bool,
) (map[string][16]byte, []core.ValidationError, error) {
	ids := map[string][16]byte{}
	var errs []core.ValidationError
	for _, field := range sortedKeys(values) {
		value := values[field]
		f := doctype.Field(field)
		if f == nil || f.Type.Kind != core.FieldLink || value.Kind != core.KindText {
			continue
		}
		if skip[field] {
			continue
		}
		target := f.Type.Target
		targetName := value.Text
		targetType, err := lookupDocType(registry, target)
		if err != nil {
			return nil, nil, err
		}
		// A kept value is resolved with full access: no read right and no user permission is
		// asked for what the document already holds (ADR 0011, ADR 0025).
		var scope *readScope
		if kept(field, targetName) {
			scope = &readScope{}
		} else {
			// The same scope as for reading the target: its if_owner and its user permissions.
			access := core.Evaluate(targetType, caller, core.RightRead)
			if access != core.AccessDenied {
				owner, owned := ownerScope(access, caller)
				scope = &readScope{ownerOnly: owned, owner: owner, rows: core.RowScopeOf(targetType, caller)}
			}
		}
		found := false
		var id [16]byte
		if scope != nil {
			scoped := scope.andSQL(2)
			sql := fmt.Sprintf("SELECT \"id\" FROM %s WHERE \"name\" = $1::text%s FOR KEY SHARE",
				QuoteIdent(core.TableName(target)), scoped.sql)
			args := append([]any{targetName}, scoped.args()...)
			err := c.QueryRow(ctx, sql, args...).Scan(&id)
			switch {
			case err == nil:
				found = true
			case errors.Is(err, pgx.ErrNoRows):
			default:
				return nil, nil, wrap(err)
			}
		}
		if found {
			ids[field] = id
		} else {
			errs = append(errs, core.ValidationError{Kind: core.ErrLinkNotFound, Field: field, Target: target, Name: targetName})
		}
	}
	return ids, errs, nil
}

// writeRows inserts the rows of every table, one INSERT per row, numbering them from 1.
func writeRows(ctx context.Context, c Queryer, parentID [16]byte, writes []tableWrite) error {
	for _, write := range writes {
		sql := childInsertSQL(write.child)
		fields := write.child.ColumnFields()
		for i, rw := range write.rows {
			args := []any{[16]byte(newID()), parentID, int32(i + 1)}
			for j, f := range fields {
				arg, err := sqlParam(f.Type, rw.links, f.Name, rw.row[f.Name], 4+j)
				if err != nil {
					return err
				}
				args = append(args, arg)
			}
			if _, err := c.Exec(ctx, sql, args...); err != nil {
				return wrap(err)
			}
		}
	}
	return nil
}

// loadTables reads the rows of every Table field of a document, in order.
func loadTables(ctx context.Context, c Queryer, registry *core.Registry, doctype *core.DocType, parentID [16]byte) (map[string][]core.Row, error) {
	tables := map[string][]core.Row{}
	for _, tf := range doctype.TableFields() {
		child, err := lookupDocType(registry, tf.Target)
		if err != nil {
			return nil, err
		}
		rows, err := c.Query(ctx, childSelectSQL(child), withFormats(parentID)...)
		if err != nil {
			return nil, wrap(err)
		}
		list, err := scanRows(child, rows)
		if err != nil {
			return nil, err
		}
		tables[tf.Field.Name] = list
	}
	return tables, nil
}

// scanRows reads every row of a child table query and closes rows.
func scanRows(child *core.DocType, rows pgx.Rows) ([]core.Row, error) {
	defer rows.Close()
	list := []core.Row{}
	for rows.Next() {
		dests := fieldDests(child)
		if err := rows.Scan(dests...); err != nil {
			return nil, wrap(err)
		}
		values, err := decodeFields(child, dests)
		if err != nil {
			return nil, err
		}
		list = append(list, core.Row(values))
	}
	if err := rows.Err(); err != nil {
		return nil, wrap(err)
	}
	return list, nil
}
