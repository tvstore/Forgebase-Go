package db

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/tvstore/forgebase-go/internal/core"
)

// Export: the documents of one DocType that the caller may read and export, in pages from one
// consistent snapshot (M9.3, ADR 0021; the Rust module export.rs).
//
// An export runs in one read-only REPEATABLE READ transaction (BeginExport), so the count, every page and
// the child rows of every page see the same data. StartExport checks the rights and counts the rows with
// the same WHERE as the list query (the if_owner scope and the user permissions of the caller included,
// ADR 0025); a count over the limit is ErrExportTooLarge and nothing is read. NextPage then fetches
// PageRows documents at a time from a server-side cursor (Rust: a portal), so the memory in use does not
// depend on the size of the result. Formatting the pages (CSV or JSON) is the job of the caller
// (internal/httpapi, transfer.go).
//
// Rights. The caller needs read and export. Each right is decided by the one evaluator
// (core.Evaluate); if either comes from an if_owner rule, only the caller's own documents are exported.

// PageRows is how many documents one page of an export has.
const PageRows = 1000

// exportCursor is the name of the cursor of an export; a transaction has at most one export.
const exportCursor = "fb_export"

// ExportBeginner is what BeginExport needs: a *pgxpool.Pool, a *pgxpool.Conn or a *pgx.Conn.
type ExportBeginner interface {
	BeginTx(ctx context.Context, txOptions pgx.TxOptions) (pgx.Tx, error)
}

// BeginExport starts the transaction an export runs in: read-only, REPEATABLE READ. Rolling it back (or
// ending the connection) ends it; nothing is ever written.
func BeginExport(ctx context.Context, client ExportBeginner) (pgx.Tx, error) {
	tx, err := client.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	return tx, wrap(err)
}

// ExportQuery says what to export.
type ExportQuery struct {
	// DocType is the DocType (not a child DocType).
	DocType string
	// Filters are the conditions every document meets (AND), as for the list query.
	Filters []Filter
	// Sort are the sort keys, most significant first; name ascending is always appended.
	Sort []Sort
	// Limit is the most rows allowed: more is ErrExportTooLarge.
	Limit uint64
	// WithTables says whether the pages carry the rows of the Table fields.
	WithTables bool
}

// Export is an export in progress: the cursor of the rows and what is needed to read the next page.
type Export struct {
	tx         pgx.Tx
	registry   *core.Registry
	doctype    *core.DocType
	count      uint64
	withTables bool
	done       bool
}

// StartExport checks the caller's rights, counts the rows and opens the cursor. tx is the transaction of
// BeginExport.
//
// Errors: ErrPermissionDenied (read first, then export), ErrInvalidQuery for a bad filter or sort,
// ErrExportTooLarge, and database errors.
func StartExport(ctx context.Context, tx pgx.Tx, registry *core.Registry, caller core.Caller, query ExportQuery) (*Export, error) {
	doctype, err := documentDocType(registry, query.DocType)
	if err != nil {
		return nil, err
	}
	if err := checkActor(caller.Name()); err != nil {
		return nil, err
	}
	read, err := accessTo(doctype, caller, core.RightRead)
	if err != nil {
		return nil, err
	}
	export, err := accessTo(doctype, caller, core.RightExport)
	if err != nil {
		return nil, err
	}
	scope := readScope{rows: core.RowScopeOf(doctype, caller)}
	if read == core.AccessOwnerOnly || export == core.AccessOwnerOnly {
		scope.ownerOnly, scope.owner = true, caller.Name()
	}
	// The columns, the filters, the sort and the child rows follow the fields the caller can read: a hidden
	// field is not exported and is unknown to the query (ADR 0024).
	doctype = core.ReadableDocTypeFor(doctype, caller)
	rowsSQL, countSQL, binds, err := buildExport(doctype, scope, query.Filters, query.Sort)
	if err != nil {
		return nil, err
	}
	args, err := binds.args()
	if err != nil {
		return nil, wrap(err)
	}
	var count int64
	if err := tx.QueryRow(ctx, countSQL, args...).Scan(&count); err != nil {
		return nil, wrap(err)
	}
	n := uint64(max(count, 0))
	if n > query.Limit {
		return nil, &Error{Kind: ErrExportTooLarge, Count: n, Limit: query.Limit}
	}
	if _, err := tx.Exec(ctx, "DECLARE "+exportCursor+" NO SCROLL CURSOR FOR "+rowsSQL, args...); err != nil {
		return nil, wrap(err)
	}
	return &Export{tx: tx, registry: registry, doctype: doctype, count: n, withTables: query.WithTables, done: n == 0}, nil
}

// buildExport builds the statements of an export: the rows (filters and sort, no page) and their count,
// which share one list of bound values. They are built from the same conditions and sort as the list.
func buildExport(doctype *core.DocType, scope readScope, filters []Filter, sort []Sort) (string, string, *listBinds, error) {
	table := QuoteIdent(core.TableName(doctype.Name))
	binds := &listBinds{}
	conditions, err := listConditions(doctype, scope, filters, binds)
	if err != nil {
		return "", "", nil, err
	}
	order, err := orderKeys(doctype, sort)
	if err != nil {
		return "", "", nil, err
	}
	filter := ""
	if len(conditions) > 0 {
		filter = " WHERE " + strings.Join(conditions, " AND ")
	}
	rows := fmt.Sprintf("SELECT %s FROM %s%s ORDER BY %s", selectList(doctype), table, filter, strings.Join(order, ", "))
	count := fmt.Sprintf("SELECT count(*) FROM %s%s", table, filter)
	return rows, count, binds, nil
}

// Count is the number of documents the export has (counted before the first page).
func (e *Export) Count() uint64 { return e.count }

// NextPage is the next page of at most PageRows documents in the order of the query, or nil when there are
// no more. With tables, one query per child table reads the rows of the whole page.
func (e *Export) NextPage(ctx context.Context) ([]*StoredDocument, error) {
	if e.done {
		return nil, nil
	}
	// The simple protocol, so that the statement is not cached by its text: the shape of a cursor's rows
	// depends on the DocType, and the name of the cursor is always the same. Every column comes back as text,
	// which is what the reading of numeric columns wants anyway.
	rows, err := e.tx.Query(ctx, fmt.Sprintf("FETCH %d FROM %s", PageRows, exportCursor), pgx.QueryExecModeSimpleProtocol)
	if err != nil {
		return nil, wrap(err)
	}
	defer rows.Close()
	var documents []*StoredDocument
	for rows.Next() {
		stored, err := scanDocument(e.doctype, rows)
		if err != nil {
			return nil, err
		}
		documents = append(documents, stored)
	}
	if err := rows.Err(); err != nil {
		return nil, wrap(err)
	}
	rows.Close()
	if len(documents) < PageRows {
		e.done = true
	}
	if len(documents) == 0 {
		return nil, nil
	}
	if e.withTables && len(e.doctype.TableFields()) > 0 {
		ids := make([][16]byte, len(documents))
		for i, d := range documents {
			ids[i] = d.ID
		}
		tables, err := loadTablesFor(ctx, e.tx, e.registry, e.doctype, ids)
		if err != nil {
			return nil, err
		}
		for _, d := range documents {
			d.Tables = tables[d.ID]
		}
	}
	return documents, nil
}

// loadTablesFor reads the rows of every Table field of the documents with the ids parents, in order, one
// query per child table (Rust: load_tables_for). Every parent has an entry, with an empty list for a table
// without rows.
func loadTablesFor(ctx context.Context, c Queryer, registry *core.Registry, doctype *core.DocType, parents [][16]byte) (map[uuid.UUID]map[string][]core.Row, error) {
	all := make(map[uuid.UUID]map[string][]core.Row, len(parents))
	for _, id := range parents {
		tables := map[string][]core.Row{}
		for _, tf := range doctype.TableFields() {
			tables[tf.Field.Name] = []core.Row{}
		}
		all[id] = tables
	}
	for _, tf := range doctype.TableFields() {
		child, err := lookupDocType(registry, tf.Target)
		if err != nil {
			return nil, err
		}
		sql := fmt.Sprintf("SELECT \"parent_id\", %s FROM %s WHERE \"parent_id\" = ANY($1::uuid[]) ORDER BY \"idx\"",
			strings.Join(fieldItems(child), ", "), QuoteIdent(core.TableName(child.Name)))
		if err := readChildRows(ctx, c, sql, parents, child, func(parent uuid.UUID, row core.Row) {
			if tables, ok := all[parent]; ok {
				tables[tf.Field.Name] = append(tables[tf.Field.Name], row)
			}
		}); err != nil {
			return nil, err
		}
	}
	return all, nil
}

func readChildRows(ctx context.Context, c Queryer, sql string, parents [][16]byte, child *core.DocType, add func(uuid.UUID, core.Row)) error {
	rows, err := c.Query(ctx, sql, withFormats(parents)...)
	if err != nil {
		return wrap(err)
	}
	defer rows.Close()
	for rows.Next() {
		var parent [16]byte
		dests := fieldDests(child)
		if err := rows.Scan(append([]any{&parent}, dests...)...); err != nil {
			return wrap(err)
		}
		values, err := decodeFields(child, dests)
		if err != nil {
			return err
		}
		add(parent, core.Row(values))
	}
	return wrap(rows.Err())
}
