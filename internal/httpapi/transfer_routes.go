package httpapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/tvstore/forgebase-go/internal/core"
	"github.com/tvstore/forgebase-go/internal/db"
)

// The routes of import and export (M9.3, ADR 0021; the Rust module transfer_routes.rs): GET
// /api/v1/export/{doctype}, POST /api/v1/report/{app}.{name}/export and POST /api/v1/import/{doctype}. The
// formats are in transfer.go; the rights, the snapshot and the transaction are in internal/db. These handlers
// map requests to them.

// formatParam is the format parameter: csv (the default) or json.
func formatParam(query map[string]string) (Format, error) {
	text, ok := query["format"]
	if !ok {
		return FormatCSV, nil
	}
	format, ok := ParseFormat(text)
	if !ok {
		return 0, errInvalidQuery("`format` must be csv or json")
	}
	return format, nil
}

// disposition is attachment; filename="<name>-<UTC timestamp>.<ext>". The name is a DocType or report name:
// lower-case letters, digits, _ and .
func disposition(name string, format Format) string {
	stamp := time.Now().UTC().Format("20060102T150405Z")
	return `attachment; filename="` + name + "-" + stamp + "." + format.Extension() + `"`
}

// fileHeaders sets the head of a file download: the type, the name, and no-store.
func fileHeaders(w http.ResponseWriter, name string, format Format) {
	h := w.Header()
	h.Set("Content-Type", format.ContentType())
	h.Set("Content-Disposition", disposition(name, format))
	h.Set("Cache-Control", "no-store")
}

// ---- export of documents --------------------------------------------------------------------------

// handleExport is GET /api/v1/export/{doctype} with format (csv or json), filters, sort and fields (comma
// separated). The caller needs the read and the export right. The answer is a streamed file from one
// REPEATABLE READ snapshot; the rows are counted first, and more than Config.ExportMaxRows is 422
// export_too_large before any byte is sent.
func handleExport(s *State, w http.ResponseWriter, r *request) error {
	auth, err := s.authenticate(r)
	if err != nil {
		return err
	}
	path, err := r.pathParams(1)
	if err != nil {
		return err
	}
	registry := s.snapshot()
	doctype, err := topLevel(registry, path[0])
	if err != nil {
		return err
	}
	caller := auth.caller()
	if err := require(doctype, caller, []core.Right{core.RightRead}, core.RightRead); err != nil {
		return err
	}
	if err := require(doctype, caller, []core.Right{core.RightExport}, core.RightExport); err != nil {
		return err
	}
	query, err := params(r, "format", "filters", "sort", "fields")
	if err != nil {
		return err
	}
	format, err := formatParam(query)
	if err != nil {
		return err
	}
	// The columns, the filters, the sort and fields follow what the caller can read: a hidden field is
	// unknown (ADR 0024).
	readable := core.ReadableDocTypeFor(doctype, caller)
	var filters []db.Filter
	var sorts []db.Sort
	if text, ok := query["filters"]; ok {
		if filters, err = ParseFilters(readable, text); err != nil {
			return syntaxToQuery(err)
		}
	}
	if text, ok := query["sort"]; ok {
		if sorts, err = ParseSort(strings.Split(text, ",")); err != nil {
			return syntaxToQuery(err)
		}
	}
	var fields []string
	if text, ok := query["fields"]; ok {
		fields = strings.Split(text, ",")
	}
	exporter, err := NewExporter(readable, format, fields)
	if err != nil {
		return syntaxToQuery(err)
	}
	ctx := r.Context()
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return errInternal(err)
	}
	defer conn.Release()
	tx, err := db.BeginExport(ctx, conn)
	if err != nil {
		return err
	}
	// Rolling back ends the snapshot; it must not depend on the request's context.
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	export, err := db.StartExport(ctx, tx, registry, caller, db.ExportQuery{DocType: doctype.Name, Filters: filters,
		Sort: sorts, Limit: s.config.ExportMaxRows, WithTables: exporter.WithTables()})
	if err != nil {
		return err
	}
	fileHeaders(w, doctype.Name, format)
	w.Header().Set("X-Forgebase-Row-Count", strconv.FormatUint(export.Count(), 10))
	// From here on the answer has started: a failure cannot be an error response, it aborts the body (the
	// connection is closed and the client sees an incomplete body, so a client must treat that as a failure).
	out := &chunkWriter{w: w, rc: http.NewResponseController(w), timeout: s.config.ExportSendTimeout}
	defer out.done()
	if !out.send(exporter.Start()) {
		return nil
	}
	for {
		page, err := export.NextPage(ctx)
		if err == nil && page == nil {
			break
		}
		var text string
		if err == nil {
			text, err = exporter.Rows(page)
		}
		if err != nil {
			// A client that went away cancels the request, which fails the query: not worth a log line.
			if ctx.Err() == nil {
				logInternal("export failed halfway: %v\n", err)
			}
			panic(http.ErrAbortHandler)
		}
		// The client went away or stopped reading: the snapshot ends with the handler.
		if !out.send(text) {
			return nil
		}
	}
	out.send(exporter.Finish())
	return nil
}

// chunkWriter writes the chunks of a streamed body, waiting at most timeout for the client to take each one.
// A client that is gone or too slow ends the export: the body is aborted (the client sees an incomplete
// download) and the handler's connection and snapshot are released when it returns.
type chunkWriter struct {
	w       http.ResponseWriter
	rc      *http.ResponseController
	timeout time.Duration
}

// send writes text and flushes it; false when the export must end. The abort is a panic of
// http.ErrAbortHandler, which the server answers by closing the connection without ending the body.
func (c *chunkWriter) send(text string) bool {
	if text == "" {
		return true
	}
	// Not every writer has deadlines (a recorder in a test): then the write cannot time out.
	_ = c.rc.SetWriteDeadline(time.Now().Add(c.timeout))
	if _, err := io.WriteString(c.w, text); err != nil {
		panic(http.ErrAbortHandler)
	}
	if err := c.rc.Flush(); err != nil && !errors.Is(err, http.ErrNotSupported) {
		panic(http.ErrAbortHandler)
	}
	return true
}

// done clears the deadline, so that it does not reach the next request of the connection.
func (c *chunkWriter) done() { _ = c.rc.SetWriteDeadline(time.Time{}) }

// ---- export of a report ---------------------------------------------------------------------------

// handleReportExport is POST /api/v1/report/{app}.{name}/export?format=csv|json with {"params": {..}}: runs the
// report as POST /report/{name} does (its roles, its row limit and timeout) and answers a file. Not streamed:
// the result is already bounded. CSV has the column names as its header; JSON is {"columns": [..], "rows":
// [[..]], "truncated": bool}. A result cut at the row limit has the header X-Forgebase-Truncated: true.
func handleReportExport(s *State, w http.ResponseWriter, r *request) error {
	auth, err := s.authenticate(r)
	if err != nil {
		return err
	}
	body, err := s.readJSON(w, r)
	if err != nil {
		return err
	}
	path, err := r.pathParams(1)
	if err != nil {
		return err
	}
	caller := auth.caller()
	registry := s.snapshot()
	report, err := findReport(registry, path[0], caller)
	if err != nil {
		return err
	}
	query, err := params(r, "format")
	if err != nil {
		return err
	}
	format, err := formatParam(query)
	if err != nil {
		return err
	}
	values, err := reportParams(report, body)
	if err != nil {
		return err
	}
	conn, err := db.GetReportConnection(r.Context(), s.pool)
	if err != nil {
		return err
	}
	defer conn.Close(r.Context())
	result, err := db.RunReport(r.Context(), conn.Conn(), report, caller, values)
	if err != nil {
		return err
	}
	var text string
	if format == FormatCSV {
		var out strings.Builder
		header := make([]string, len(report.Columns))
		for i, c := range report.Columns {
			header[i] = c.Name
		}
		AppendCSVRecord(&out, header)
		for _, row := range result.Rows {
			cells := make([]string, len(row))
			for i, value := range row {
				if cells[i], err = CSVCell(report.Columns[i].Name, value); err != nil {
					return err
				}
			}
			AppendCSVRecord(&out, cells)
		}
		text = out.String()
	} else {
		rows := make([]any, len(result.Rows))
		for i, row := range result.Rows {
			cells := make([]any, len(row))
			for j, value := range row {
				if cells[j], err = db.ValueToJSON(report.Columns[j].Name, value); err != nil {
					return err
				}
			}
			rows[i] = cells
		}
		raw, err := marshal(map[string]any{"columns": columnsJSON(report), "rows": rows, "truncated": result.Truncated})
		if err != nil {
			return errInternal(err)
		}
		text = string(raw) + "\n"
	}
	fileHeaders(w, report.FullName(), format)
	if result.Truncated {
		w.Header().Set("X-Forgebase-Truncated", "true")
	}
	_, _ = io.WriteString(w, text)
	return nil
}

// ---- import ---------------------------------------------------------------------------------------

// mediaType is the media type of a Content-Type header, lower-cased, without parameters; "" when the header is
// missing or repeated.
func mediaType(h http.Header) string {
	values := h.Values("Content-Type")
	if len(values) != 1 {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(strings.SplitN(values[0], ";", 2)[0]))
}

// handleImport is POST /api/v1/import/{doctype}?format=csv|json&mode=insert|upsert&dry_run=1; the body is the
// file. It needs the import and create rights (and write for an upsert). All rows run in one transaction, each
// in a savepoint; see db.Import. 200 with the summary, or 422 import_failed with the same summary in details
// when a row failed (nothing is kept either way for a dry run).
func handleImport(s *State, w http.ResponseWriter, r *request) error {
	auth, err := s.authenticate(r)
	if err != nil {
		return err
	}
	path, err := r.pathParams(1)
	if err != nil {
		return err
	}
	registry := s.snapshot()
	doctype, err := topLevel(registry, path[0])
	if err != nil {
		return err
	}
	caller := auth.caller()
	if err := require(doctype, caller, []core.Right{core.RightImport}, core.RightImport); err != nil {
		return err
	}
	if err := require(doctype, caller, []core.Right{core.RightCreate}, core.RightCreate); err != nil {
		return err
	}
	query, err := params(r, "format", "mode", "dry_run")
	if err != nil {
		return err
	}
	text, ok := query["format"]
	if !ok {
		return errInvalidQuery("`format` (csv or json) is required")
	}
	format, ok := ParseFormat(text)
	if !ok {
		return errInvalidQuery("`format` must be csv or json")
	}
	mode := db.ImportInsert
	switch given, ok := query["mode"]; {
	case !ok || given == "insert":
	case given == "upsert":
		mode = db.ImportUpsert
	default:
		return errInvalidQuery("`mode` must be insert or upsert")
	}
	if mode == db.ImportUpsert {
		if err := require(doctype, caller, []core.Right{core.RightWrite}, core.RightWrite); err != nil {
			return err
		}
	}
	dryRun := false
	switch dry, given := query["dry_run"]; {
	case !given || dry == "0":
	case dry == "1":
		dryRun = true
	default:
		return errInvalidQuery("`dry_run` must be 1")
	}
	expected := "text/csv"
	if format == FormatJSON {
		expected = "application/json"
	}
	if mediaType(r.Header) != expected {
		return newError(http.StatusUnsupportedMediaType, "unsupported_media_type",
			"the body of a "+format.Extension()+" import must be "+expected)
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, s.config.ImportBodyLimitBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return newError(http.StatusRequestEntityTooLarge, "payload_too_large", "the request body is too large")
		}
		return errBadRequest("the body cannot be read")
	}
	readable := core.ReadableDocTypeFor(doctype, caller)
	parsed, err := ParseImport(registry, readable, format, mode, data, s.config.ImportMaxRows)
	if err != nil {
		var fe *FileError
		if errors.As(err, &fe) {
			if fe.TooLarge {
				return errImportTooLarge(fe.Limit)
			}
			return errInvalidImport(fe.Problems)
		}
		return err
	}
	summary, err := RunImport(r.Context(), s.pool, registry, doctype.Name, caller, parsed, mode, dryRun)
	if err != nil {
		return err
	}
	if summary.ErrorCount > 0 && !dryRun {
		return errImportFailed(summary.JSON().(map[string]any), summary.ErrorCount)
	}
	writeData(w, http.StatusOK, summary.JSON())
	return nil
}
