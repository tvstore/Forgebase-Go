package db

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgproto3"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/tvstore/forgebase-go/internal/core"
)

// Running query reports (M8.4, ADR 0016): read-only, with a statement timeout and a row limit (the Rust
// module reports.rs).
//
// The SQL is the app's own, compiled into the binary, so it is trusted code: what this file guards
// against is a mistake (a statement that writes, one that never ends, one that returns millions of
// rows) and input (parameters are bound, never put into the text).
//
//   - Access. Only the report's roles, an administrator and the system context (core.CanRunReport).
//     Anyone else gets ErrReportNotFound, the same error as for a report that does not exist.
//   - Row-level rules do not apply inside the SQL: it shows what it selects, to every role that may
//     run it.
//   - Execution. One READ ONLY transaction; SET LOCAL statement_timeout (the report's timeout) and SET
//     LOCAL lock_timeout; the statement is prepared, bound and run through a portal that returns at
//     most max_rows + 1 rows, so the database stops producing rows after that. A write fails with
//     SQLSTATE 25006 (ErrReportReadOnly), the timeout with 57014 (ErrReportTimeout).
//   - Parameters are the declared ones in order, $1..$n; an absent optional one is NULL. They are sent in
//     text form, so a parameter whose type PostgreSQL infers as text (for example $1::text) takes any
//     kind of value as its text, and an integer fits int2, int4, int8 and numeric (a value outside the
//     range of the inferred type is a database error, not a wrap).
//   - Never in an error: the SQL text.

// reportLockTimeoutMS is how long a statement may wait for a lock, in milliseconds. A report that has to
// wait this long is answered like one that is too slow.
const reportLockTimeoutMS = 5000

// ReportResult is the rows of a report, in the declared column order, as the same Values the documents
// use.
type ReportResult struct {
	// Rows are at most MaxRows rows. Every row has one value per declared column.
	Rows [][]core.Value
	// Truncated says there were more rows than MaxRows: the rest is not returned.
	Truncated bool
}

// ReportConnection is a connection for one report run. Close ends the session instead of returning the
// connection to the pool: a report's SQL is the app's own, and it may leave session state behind (a
// session-level set_config, a PREPARE, a temporary table, LISTEN, an advisory lock) that the next
// request on a pooled connection would inherit. Closing the connection is the one way that cannot miss a
// kind of state. The cost is a new connection for the next checkout, which is acceptable for a report
// (it is heavier than a document read anyway).
//
// Conn also serves what the handler does after the run (the titles of Link columns) on the same
// connection.
type ReportConnection struct {
	conn *pgx.Conn
}

// GetReportConnection takes a connection out of the pool.
func GetReportConnection(ctx context.Context, pool *pgxpool.Pool) (*ReportConnection, error) {
	acquired, err := pool.Acquire(ctx)
	if err != nil {
		return nil, &Error{Kind: ErrPool, Message: err.Error(), Err: err}
	}
	return &ReportConnection{conn: acquired.Hijack()}, nil
}

// Conn is the connection.
func (c *ReportConnection) Conn() *pgx.Conn { return c.conn }

// Close ends the session. It is safe to call twice.
func (c *ReportConnection) Close(ctx context.Context) {
	if c.conn != nil {
		_ = c.conn.Close(context.WithoutCancel(ctx))
		c.conn = nil
	}
}

// reportError is the report's error for a database error: the timeout, the read-only refusal, or the
// ordinary database error.
func reportError(report *core.ReportDef, err error) error {
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		switch pg.Code {
		case "57014", "55P03":
			return &Error{Kind: ErrReportTimeout, Report: report.FullName()}
		case "25006":
			return &Error{Kind: ErrReportReadOnly, Report: report.FullName()}
		}
	}
	return wrap(err)
}

// RunReport runs report for caller with the parameter values params (by parameter name; see
// core.ReportDef.BindParams for what is checked) and returns its rows.
//
// Errors: ErrReportNotFound when the caller may not run it; ErrValidation with every parameter problem;
// ErrReportTimeout when the statement ran over its timeout or waited too long for a lock;
// ErrReportReadOnly when it tried to write; ErrReport when the prepared statement does not have the
// declared parameters or columns; any other database error as it is. Nothing is ever changed.
func RunReport(ctx context.Context, conn *pgx.Conn, report *core.ReportDef, caller core.Caller, params map[string]core.Value) (*ReportResult, error) {
	if !core.CanRunReport(report.Roles, caller) {
		return nil, &Error{Kind: ErrReportNotFound, Report: report.FullName()}
	}
	values, errs := report.BindParams(params)
	if len(errs) > 0 {
		return nil, &Error{Kind: ErrValidation, Validation: errs}
	}
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, wrap(err)
	}
	// Read-only: nothing to keep either way. A failed statement leaves the transaction aborted, and the
	// rollback ends it.
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	// The numbers come from validated definitions (a Duration and a uint32), never from input.
	timeoutMS := max(report.Timeout.Milliseconds(), 1)
	if _, err := tx.Exec(ctx, "SET LOCAL statement_timeout = "+strconv.FormatInt(timeoutMS, 10)); err != nil {
		return nil, wrap(err)
	}
	if _, err := tx.Exec(ctx, "SET LOCAL lock_timeout = "+strconv.Itoa(reportLockTimeoutMS)); err != nil {
		return nil, wrap(err)
	}
	// Dates and timestamps are read from the text output: ISO, whatever the DateStyle of the database, the
	// role or the connection (Rust reads them in binary and does not depend on it).
	if _, err := tx.Exec(ctx, "SET LOCAL DateStyle = ISO"); err != nil {
		return nil, wrap(err)
	}
	rows, columns, err := fetchReport(ctx, conn, report, values)
	if err != nil {
		return nil, err
	}
	truncated := len(rows) > int(report.MaxRows)
	if truncated {
		rows = rows[:report.MaxRows]
	}
	result := &ReportResult{Rows: make([][]core.Value, len(rows)), Truncated: truncated}
	tm := conn.TypeMap()
	for i, raw := range rows {
		row := make([]core.Value, len(report.Columns))
		for j, column := range report.Columns {
			v, err := decodeReportColumn(tm, columns[j].DataTypeOID, raw[j], column)
			if err != nil {
				return nil, wrap(err)
			}
			row[j] = v
		}
		result.Rows[i] = row
	}
	return result, nil
}

// fetchReport prepares the statement, checks its shape, binds values and reads at most max_rows + 1 rows
// through a portal (the Execute message of the extended protocol has a row limit; pgx does not expose
// it, so the messages are sent and read here). The values of the rows are in the text format.
func fetchReport(ctx context.Context, conn *pgx.Conn, report *core.ReportDef, values []core.Value) ([][][]byte, []pgconn.FieldDescription, error) {
	// The unnamed statement: nothing is left in the connection's statement cache.
	statement, err := conn.PgConn().Prepare(ctx, "", report.SQL, nil)
	if err != nil {
		return nil, nil, reportError(report, err)
	}
	// The migrate check says the same earlier; this stops a statement that was never checked (a COMMIT, a
	// wrong number of columns) before it runs.
	if len(statement.ParamOIDs) != len(report.Params) || len(statement.Fields) != len(report.Columns) {
		return nil, nil, &Error{Kind: ErrReport, Message: fmt.Sprintf(
			"report `%s`: the SQL has %d parameter(s) and %d column(s), but %d and %d are declared",
			report.FullName(), len(statement.ParamOIDs), len(statement.Fields), len(report.Params), len(report.Columns))}
	}
	args := make([][]byte, len(values))
	for i, v := range values {
		text, ok, err := reportArgText(v)
		if err != nil {
			return nil, nil, wrap(err)
		}
		if ok {
			args[i] = []byte(text)
		}
	}
	limit := report.MaxRows + 1
	fe := conn.PgConn().Frontend()
	fe.SendBind(&pgproto3.Bind{
		ParameterFormatCodes: []int16{pgtype.TextFormatCode},
		Parameters:           args,
		ResultFormatCodes:    []int16{pgtype.TextFormatCode},
	})
	fe.SendExecute(&pgproto3.Execute{MaxRows: limit})
	fe.SendSync(&pgproto3.Sync{})
	if err := fe.Flush(); err != nil {
		return nil, nil, wrap(err)
	}
	// If the context ends while the statement runs, ask the server to cancel it.
	stop := context.AfterFunc(ctx, func() { _ = conn.PgConn().CancelRequest(context.WithoutCancel(ctx)) })
	defer stop()
	var rows [][][]byte
	var failure error
	for {
		msg, err := fe.Receive()
		if err != nil {
			return nil, nil, wrap(err)
		}
		switch m := msg.(type) {
		case *pgproto3.DataRow:
			row := make([][]byte, len(m.Values))
			for i, v := range m.Values {
				if v != nil {
					row[i] = append([]byte{}, v...)
				}
			}
			rows = append(rows, row)
		case *pgproto3.ErrorResponse:
			if failure == nil {
				failure = reportError(report, pgconn.ErrorResponseToPgError(m))
			}
		case *pgproto3.ReadyForQuery:
			if failure != nil {
				return nil, nil, failure
			}
			return rows, statement.Fields, nil
		}
	}
}

// reportArgText is the text form of a parameter value; false for NULL.
func reportArgText(v core.Value) (string, bool, error) {
	switch v.Kind {
	case core.KindNull:
		return "", false, nil
	case core.KindText:
		return v.Text, true, nil
	case core.KindInt:
		return strconv.FormatInt(v.Int, 10), true, nil
	case core.KindDecimal:
		return v.Dec.String(), true, nil
	case core.KindBool:
		return strconv.FormatBool(v.Bool), true, nil
	case core.KindDate:
		s, err := FormatDate(v.Time)
		return s, true, err
	}
	return "", false, fmt.Errorf("error serializing parameter: cannot bind a %s value", v.KindName())
}

// decodeReportColumn reads a column of the declared kind from its text form. A column of another SQL
// type is an error (the check at migrate finds that earlier), not a wrong value.
func decodeReportColumn(tm *pgtype.Map, oid uint32, data []byte, column core.ReportColumn) (core.Value, error) {
	if !columnFits(column.Kind, oid) {
		return core.Null(), fmt.Errorf("error deserializing column `%s`: the SQL returns %s, but %s is declared",
			column.Name, oidName(tm, oid), column.Kind.Name())
	}
	if data == nil {
		return core.Null(), nil
	}
	kinds := map[core.ColumnKindKind]core.FieldType{
		core.ColumnData:     {Kind: core.FieldData},
		core.ColumnLink:     {Kind: core.FieldData},
		core.ColumnInt:      {Kind: core.FieldInt},
		core.ColumnDecimal:  {Kind: core.FieldDecimal},
		core.ColumnDate:     {Kind: core.FieldDate},
		core.ColumnDatetime: {Kind: core.FieldDatetime},
		core.ColumnCheck:    {Kind: core.FieldCheck},
	}
	dest := columnDest(kinds[column.Kind.Kind])
	if err := tm.Scan(oid, pgtype.TextFormatCode, data, dest); err != nil {
		return core.Null(), fmt.Errorf("error deserializing column `%s`: %w", column.Name, err)
	}
	v, err := fromColumn(dest)
	if err != nil {
		return core.Null(), fmt.Errorf("error deserializing column `%s`: %w", column.Name, err)
	}
	return v, nil
}

func oidName(tm *pgtype.Map, oid uint32) string {
	if t, ok := tm.TypeForOID(oid); ok {
		return t.Name
	}
	return "oid " + strconv.FormatUint(uint64(oid), 10)
}

// ReportLinkTitles are the titles for the Link columns of a result, under the caller's read rules like
// LinkTitles (a target the caller cannot read gets no entry). At most MaxLimit distinct names per target
// DocType are resolved; the others show as their names.
func ReportLinkTitles(ctx context.Context, q Queryer, registry *core.Registry, caller core.Caller, report *core.ReportDef, rows [][]core.Value) (LinkTitleMap, error) {
	found := map[string]map[string]struct{}{}
	for i, column := range report.Columns {
		if column.Kind.Kind != core.ColumnLink {
			continue
		}
		names := found[column.Kind.DocType]
		if names == nil {
			names = map[string]struct{}{}
			found[column.Kind.DocType] = names
		}
		for _, row := range rows {
			if i < len(row) && row[i].Kind == core.KindText && len(names) < int(MaxLimit) {
				names[row[i].Text] = struct{}{}
			}
		}
	}
	return resolveLinkTitles(ctx, q, registry, caller, found)
}

// paramFits says whether a placeholder PostgreSQL typed as oid can take a parameter of kind.
func paramFits(kind core.ParamKind, oid uint32) bool {
	// Text (and the unknown type) take every kind, as the text form of the value.
	switch oid {
	case pgtype.TextOID, pgtype.VarcharOID, pgtype.BPCharOID, pgtype.NameOID, pgtype.UnknownOID:
		return true
	}
	switch kind.Kind {
	case core.ParamInt:
		return oid == pgtype.Int2OID || oid == pgtype.Int4OID || oid == pgtype.Int8OID || oid == pgtype.NumericOID
	case core.ParamDecimal:
		return oid == pgtype.NumericOID
	case core.ParamDate:
		return oid == pgtype.DateOID
	case core.ParamCheck:
		return oid == pgtype.BoolOID
	}
	return false
}

// columnFits says whether a result column of SQL type oid fits the declared kind.
func columnFits(kind core.ColumnKind, oid uint32) bool {
	switch kind.Kind {
	case core.ColumnData, core.ColumnLink:
		return oid == pgtype.TextOID || oid == pgtype.VarcharOID || oid == pgtype.BPCharOID
	case core.ColumnInt:
		return oid == pgtype.Int2OID || oid == pgtype.Int4OID || oid == pgtype.Int8OID
	case core.ColumnDecimal:
		return oid == pgtype.NumericOID
	case core.ColumnDate:
		return oid == pgtype.DateOID
	case core.ColumnDatetime:
		return oid == pgtype.TimestamptzOID
	case core.ColumnCheck:
		return oid == pgtype.BoolOID
	}
	return false
}

// CheckReports prepares every report's statement, in a READ ONLY transaction that is rolled back, and
// returns one message per problem (empty when all fit). Nothing is run. It checks that
//
//  1. the SQL parses and its tables and columns exist;
//  2. it has as many parameters as declared;
//  3. it returns as many columns as declared;
//  4. each column's type fits its kind: int2, int4 or int8 for int, numeric for decimal, text or varchar
//     (and bpchar) for data and link, date, timestamptz for datetime, bool for check;
//  5. each parameter's type, as PostgreSQL infers it, fits its kind: int takes the integer types and
//     numeric, decimal numeric, date date, check bool, and data, link and select text or varchar. A
//     parameter inferred as text or unknown is accepted for every kind (the value is bound as its text
//     form), so $1::text is always fine.
//
// A database error that is not about the statement (a lost connection) is returned as an error.
func CheckReports(ctx context.Context, conn *pgx.Conn, reports []*core.ReportDef) ([]string, error) {
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, wrap(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	tm := conn.TypeMap()
	var problems []string
	for _, report := range reports {
		name := report.FullName()
		// A savepoint per report: a statement that fails to prepare aborts the transaction.
		if _, err := tx.Exec(ctx, "SAVEPOINT fb_report_check"); err != nil {
			return nil, wrap(err)
		}
		statement, prepareErr := conn.PgConn().Prepare(ctx, "", report.SQL, nil)
		if _, err := tx.Exec(ctx, "ROLLBACK TO SAVEPOINT fb_report_check"); err != nil {
			return nil, wrap(err)
		}
		if prepareErr != nil {
			var pg *pgconn.PgError
			if errors.As(prepareErr, &pg) {
				problems = append(problems, fmt.Sprintf("report `%s`: the SQL does not prepare: %s", name, pg.Message))
				continue
			}
			return nil, wrap(prepareErr)
		}
		if len(statement.ParamOIDs) != len(report.Params) {
			problems = append(problems, fmt.Sprintf("report `%s`: the SQL has %d parameter(s) but %d are declared",
				name, len(statement.ParamOIDs), len(report.Params)))
		} else {
			for i, param := range report.Params {
				if oid := statement.ParamOIDs[i]; !paramFits(param.Kind, oid) {
					problems = append(problems, fmt.Sprintf("report `%s`: parameter $%d (`%s`) is declared %s, but the SQL expects %s",
						name, i+1, param.Name, param.Kind.Name(), oidName(tm, oid)))
				}
			}
		}
		if len(statement.Fields) != len(report.Columns) {
			problems = append(problems, fmt.Sprintf("report `%s`: the SQL returns %d column(s) but %d are declared",
				name, len(statement.Fields), len(report.Columns)))
		} else {
			for i, column := range report.Columns {
				if oid := statement.Fields[i].DataTypeOID; !columnFits(column.Kind, oid) {
					problems = append(problems, fmt.Sprintf("report `%s`: column %d (`%s`) is declared %s, but the SQL returns %s",
						name, i+1, column.Name, column.Kind.Name(), oidName(tm, oid)))
				}
			}
		}
	}
	return problems, nil
}
