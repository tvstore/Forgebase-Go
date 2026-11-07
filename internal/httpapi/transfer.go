package httpapi

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/tvstore/forgebase-go/internal/core"
	"github.com/tvstore/forgebase-go/internal/db"
)

// The file formats of import and export (M9.3, ADR 0021; the Rust module transfer.rs), shared by the HTTP
// API and the command-line tool, so both read and write identically.
//
// # Export
//
// Exporter turns pages of stored documents (from db.Export) into text.
//
//   - CSV is written by hand (RFC 4180): UTF-8 without a BOM, CRLF line ends, a header row of the field
//     keys, never child tables. A cell is quoted when it contains a comma, a quote, CR or LF or has leading
//     or trailing spaces, and a quote inside is doubled. Dates and datetimes are ISO 8601 as in the JSON
//     API, decimals are their exact strings, booleans true / false, links the target's name, null an empty
//     cell.
//   - Formula injection. A spreadsheet may run a cell that starts with =, +, -, @, TAB or CR as a formula.
//     A value of a text kind (Data, Text, Select, Link, and the text standard columns) that starts with one
//     of them gets a leading '. Numbers, decimals, dates, datetimes and booleans are never prefixed, so -5
//     stays -5. This is always on: to keep exact text, round-trip through JSON (CSV import does not strip
//     the ').
//   - JSON is a top-level array of documents, written as db.DocumentToJSON writes them (child rows
//     included), so a JSON export can be imported again.
//
// # Import
//
// ParseImport maps a file to db.ImportRows. CSV is read by the reader below (the behaviour of the Rust csv
// crate): the first row is the header of field keys (a name column is allowed; a UTF-8 BOM is stripped), a
// cell is parsed by the type of its field (a date YYYY-MM-DD, a datetime RFC 3339, an integer, a decimal, a
// boolean true/false/1/0, anything else text), and an empty cell is "not given". Unknown, duplicate and table
// columns are refused for the whole file. JSON is an array of objects as in POST /resource (child rows as
// arrays); the standard keys of an exported document (id, owner, created_at, modified_at, modified_by,
// version, docstatus, amended_from, and workflow_state only when the DocType has a workflow) are ignored, so
// an export can be imported again. A value that cannot be parsed is an error of its row, reported with the
// others. RunImport runs the rows with db.Import and builds the summary.

// Format is the file format of an import or export.
type Format int

const (
	// FormatCSV is text/csv.
	FormatCSV Format = iota
	// FormatJSON is application/json.
	FormatJSON
)

// ParseFormat is csv or json.
func ParseFormat(text string) (Format, bool) {
	switch text {
	case "csv":
		return FormatCSV, true
	case "json":
		return FormatJSON, true
	}
	return 0, false
}

// Extension is the file extension, without the dot.
func (f Format) Extension() string {
	if f == FormatCSV {
		return "csv"
	}
	return "json"
}

// ContentType is the Content-Type of an export.
func (f Format) ContentType() string {
	if f == FormatCSV {
		return "text/csv; charset=utf-8"
	}
	return "application/json"
}

// ---- CSV writing ----------------------------------------------------------------------------------

// CSVCell is a value as the text of a CSV cell, with the formula mitigation for text values (a value is text
// exactly when its field is of a text kind). Null is an empty cell.
func CSVCell(name string, v core.Value) (string, error) {
	var text string
	switch v.Kind {
	case core.KindNull:
		return "", nil
	case core.KindText:
		text = v.Text
	default:
		j, err := db.ValueToJSON(name, v)
		if err != nil {
			return "", err
		}
		switch x := j.(type) {
		case string:
			text = x
		case int64:
			text = strconv.FormatInt(x, 10)
		case bool:
			text = strconv.FormatBool(x)
		}
	}
	// Fail safe: only the known non-text kinds skip the guard, so a value kind added later is guarded until
	// someone decides it cannot be a formula.
	switch v.Kind {
	case core.KindInt, core.KindDecimal, core.KindBool, core.KindDate, core.KindDatetime:
		return text, nil
	}
	return neutralize(text), nil
}

// neutralize prefixes ' to a text that a spreadsheet could take for a formula.
func neutralize(text string) string {
	if text != "" && strings.IndexByte("=+-@\t\r", text[0]) >= 0 {
		return "'" + text
	}
	return text
}

// AppendCSVRecord appends one CSV record (cells already formatted) and its CRLF to out.
func AppendCSVRecord(out *strings.Builder, cells []string) {
	for i, cell := range cells {
		if i > 0 {
			out.WriteByte(',')
		}
		if strings.ContainsAny(cell, ",\"\r\n") || strings.HasPrefix(cell, " ") || strings.HasSuffix(cell, " ") {
			out.WriteByte('"')
			out.WriteString(strings.ReplaceAll(cell, `"`, `""`))
			out.WriteByte('"')
		} else {
			out.WriteString(cell)
		}
	}
	out.WriteString("\r\n")
}

// ---- export ---------------------------------------------------------------------------------------

// Exporter writes the pages of an export as CSV or JSON text. See the comment at the top of the file.
type Exporter struct {
	format Format
	// columns are the CSV columns; empty for JSON.
	columns []string
	// keep are the JSON keys to keep; nil for the whole document.
	keep  []string
	wrote bool
	// workflow: the DocType has a workflow, so workflow_state is a standard column.
	workflow bool
}

// standardColumn: the standard columns a fields list may name, besides the fields of the DocType.
func standardColumn(doctype *core.DocType, name string) bool {
	switch name {
	case "name", "owner", "modified_by", "created_at", "modified_at", "version":
		return true
	case "docstatus":
		return doctype.Submittable
	case "workflow_state":
		return doctype.Workflow != nil
	}
	return false
}

// NewExporter is an exporter of doctype's documents. fields are the keys to write (the field names, and the
// standard columns name, owner, modified_by, created_at, modified_at, version, and docstatus / workflow_state
// where the DocType has them); nil is name and every field for CSV, and the whole document for JSON. A Table
// field can be written to JSON only. A bad list is a JSONQuery error.
func NewExporter(doctype *core.DocType, format Format, fields []string) (*Exporter, error) {
	bad := func(text string) error { return queryError("invalid fields: " + text) }
	e := &Exporter{format: format, workflow: doctype.Workflow != nil}
	if fields == nil {
		if format == FormatCSV {
			e.columns = []string{"name"}
			for _, f := range doctype.ColumnFields() {
				e.columns = append(e.columns, f.Name)
			}
		}
		return e, nil
	}
	if len(fields) == 0 {
		return nil, bad("the list is empty")
	}
	seen := map[string]bool{}
	for _, field := range fields {
		if seen[field] {
			return nil, bad(fmt.Sprintf("`%s` is listed twice", core.Shorten(field)))
		}
		seen[field] = true
		f := doctype.Field(field)
		switch {
		case standardColumn(doctype, field):
		case f != nil && f.Type.Kind == core.FieldTable && format == FormatCSV:
			return nil, bad(fmt.Sprintf("`%s` is a table field: a CSV export has no child tables, use JSON", field))
		case f != nil:
		default:
			return nil, bad(fmt.Sprintf("unknown field `%s` for DocType `%s`", core.Shorten(field), doctype.Name))
		}
	}
	if format == FormatCSV {
		e.columns = slices.Clone(fields)
	} else {
		e.keep = slices.Clone(fields)
	}
	return e, nil
}

// Format is the format.
func (e *Exporter) Format() Format { return e.format }

// WithTables says whether the pages must carry the rows of the Table fields.
func (e *Exporter) WithTables() bool { return e.format == FormatJSON }

// Start is the text that starts the file: the CSV header, or [.
func (e *Exporter) Start() string {
	if e.format == FormatCSV {
		var out strings.Builder
		AppendCSVRecord(&out, e.columns)
		return out.String()
	}
	return "["
}

// Rows is the text of a page of documents.
func (e *Exporter) Rows(documents []*db.StoredDocument) (string, error) {
	var out strings.Builder
	for _, document := range documents {
		if e.format == FormatCSV {
			cells := make([]string, 0, len(e.columns))
			for _, column := range e.columns {
				cell, err := CSVCell(column, standardOrField(document, column, e.workflow))
				if err != nil {
					return "", err
				}
				cells = append(cells, cell)
			}
			AppendCSVRecord(&out, cells)
			continue
		}
		object, err := db.DocumentToJSON(document)
		if err != nil {
			return "", err
		}
		if e.keep != nil {
			for key := range object {
				if !slices.Contains(e.keep, key) {
					delete(object, key)
				}
			}
		}
		text, err := marshal(object)
		if err != nil {
			return "", err
		}
		if e.wrote {
			out.WriteByte(',')
		}
		out.WriteByte('\n')
		out.Write(text)
		e.wrote = true
	}
	return out.String(), nil
}

// Finish is the text that ends the file: nothing for CSV, ] for JSON.
func (e *Exporter) Finish() string {
	if e.format == FormatCSV {
		return ""
	}
	return "\n]\n"
}

// standardOrField is the value of a column of a document: a standard column or a field (Null when absent).
func standardOrField(document *db.StoredDocument, key string, workflow bool) core.Value {
	switch key {
	case "name":
		return core.Text(document.Name)
	case "owner":
		return core.Text(document.Owner)
	case "modified_by":
		return core.Text(document.ModifiedBy)
	case "created_at":
		return core.Datetime(document.CreatedAt)
	case "modified_at":
		return core.Datetime(document.ModifiedAt)
	case "version":
		return core.Int(document.Version)
	case "docstatus":
		if document.DocStatus == nil {
			return core.Null()
		}
		return core.Int(int64(document.DocStatus.Int16()))
	case "workflow_state":
		if workflow {
			if document.WorkflowState == "" {
				return core.Null()
			}
			return core.Text(document.WorkflowState)
		}
	}
	if v, ok := document.Values[key]; ok {
		return v
	}
	return core.Null()
}

// ---- import ---------------------------------------------------------------------------------------

// RowProblem is a row whose values could not be mapped; reported like a row the database refused.
type RowProblem struct {
	// Row is the 1-based data row number.
	Row int
	// Line is the line of a CSV file; 0 for JSON.
	Line uint64
	// Name is the name of the row, if any; NameGiven says it is the empty text (Rust: Some("")).
	Name      string
	NameGiven bool
	// Details is what is wrong, one entry per problem.
	Details []string
}

// ParsedImport is a file that was read.
type ParsedImport struct {
	// Rows are the rows that were mapped.
	Rows []db.ImportRow
	// Problems are the rows that could not be mapped.
	Problems []RowProblem
}

// Total is the number of data rows in the file.
func (p *ParsedImport) Total() int { return len(p.Rows) + len(p.Problems) }

// FileError is why a file cannot be imported at all: nothing runs.
type FileError struct {
	// Problems are the problems of an unusable file, one entry per problem; empty for TooLarge.
	Problems []string
	// TooLarge says the file has more rows than Limit.
	TooLarge bool
	// Limit is the limit of rows.
	Limit int
}

func (e *FileError) Error() string {
	if e.TooLarge {
		return fmt.Sprintf("the file has more than %d rows; split it", e.Limit)
	}
	return "the file cannot be imported: " + strings.Join(e.Problems, "; ")
}

func invalidFile(problem string) *FileError { return &FileError{Problems: []string{problem}} }

// standardKeys are the keys of an exported document that are not fields: ignored in a JSON import.
var standardKeys = []string{"id", "owner", "created_at", "modified_at", "modified_by", "version", "docstatus",
	"amended_from", "workflow_state"}

// isStandardKey says whether key is a standard key of an exported document that import ignores: the keys of
// standardKeys, except that workflow_state is one only for a DocType with a workflow (on another it may be
// a field of the DocType's own, which import must keep).
func isStandardKey(doctype *core.DocType, key string) bool {
	return slices.Contains(standardKeys, key) && (key != "workflow_state" || doctype.Workflow != nil)
}

// ParseImport reads body as a file of format for doctype. At most maxRows data rows. The error is a
// *FileError for what is refused for the whole file; what is refused per row is in the result.
func ParseImport(registry *core.Registry, doctype *core.DocType, format Format, mode db.ImportMode, body []byte,
	maxRows int) (*ParsedImport, error) {
	if !utf8.Valid(body) {
		valid := 0
		for valid < len(body) {
			r, size := utf8.DecodeRune(body[valid:])
			if r == utf8.RuneError && size <= 1 {
				break
			}
			valid += size
		}
		return nil, invalidFile(fmt.Sprintf("the file is not valid UTF-8 (invalid byte at offset %d)", valid))
	}
	text := strings.TrimPrefix(string(body), "\ufeff")
	if format == FormatCSV {
		return parseCSV(doctype, mode, text, maxRows)
	}
	return parseJSONImport(registry, doctype, text, maxRows)
}

func parseJSONImport(registry *core.Registry, doctype *core.DocType, text string, maxRows int) (*ParsedImport, error) {
	v, err := parseJSON(text)
	if err != nil {
		return nil, invalidFile("the file is not valid JSON: " + err.Error())
	}
	items, ok := v.([]any)
	if !ok {
		return nil, invalidFile("the JSON must be an array of document objects")
	}
	// The row limit counts the documents and their child rows together: a small file of documents must not
	// carry millions of rows.
	total := len(items)
	for _, item := range items {
		if object, ok := item.(map[string]any); ok {
			for _, tf := range doctype.TableFields() {
				if rows, ok := object[tf.Field.Name].([]any); ok {
					total += len(rows)
				}
			}
		}
	}
	if total > maxRows {
		return nil, &FileError{TooLarge: true, Limit: maxRows}
	}
	parsed := &ParsedImport{}
	for i, item := range items {
		row := i + 1
		nameGiven := false
		problem := func(name, detail string) {
			parsed.Problems = append(parsed.Problems, RowProblem{Row: row, Name: name, NameGiven: nameGiven && name == "", Details: []string{detail}})
		}
		object, ok := item.(map[string]any)
		if !ok {
			problem("", "the row must be a JSON object")
			continue
		}
		for _, key := range standardKeys {
			if isStandardKey(doctype, key) {
				delete(object, key)
			}
		}
		name := ""
		given := object["name"]
		delete(object, "name")
		switch n := given.(type) {
		case nil:
		case string:
			name, nameGiven = n, true
		default:
			problem("", "`name` must be a string")
			continue
		}
		input, err := InputFromJSON(registry, doctype, object)
		if err != nil {
			problem(name, err.Error())
			continue
		}
		name, nameGiven = knownName(doctype, name, nameGiven, input.Values)
		parsed.Rows = append(parsed.Rows, db.ImportRow{Row: row, Name: name, NameGiven: nameGiven && name == "",
			Values: input.Values, Tables: input.Tables})
	}
	return parsed, nil
}

// knownName is the name of a row: the one it gives, else, for a DocType named by a field, the value of that
// field (the document's name will be exactly that), so an error can name the document and an upsert can find
// it.
func knownName(doctype *core.DocType, name string, given bool, values map[string]core.Value) (string, bool) {
	if given || doctype.Naming.Kind != core.NamingField {
		return name, given
	}
	if v, ok := values[doctype.Naming.Field]; ok && v.Kind == core.KindText {
		return v.Text, true
	}
	return "", false
}

// ---- reading CSV ----------------------------------------------------------------------------------

// csvReader reads CSV as the Rust csv crate does with its defaults (comma, double quote, no comment,
// records ended by CR, LF or CRLF): empty lines are skipped; a quote that starts a field opens a quoted
// field, in which a doubled quote is a quote and line breaks are kept as they are (encoding/csv would turn
// CRLF into LF there, which would change the data); a quote in the middle of an unquoted field is a
// character; a quote that ends a quoted field followed by anything but a comma or a line end goes on as an
// unquoted field. There is no syntax error: the only way a file is refused is a record of another length.
type csvReader struct {
	text string
	pos  int
	// line is the 1-based line of text[pos]: the number of LF consumed so far, plus one.
	line uint64
}

// csvRecord is a record and where it starts.
type csvRecord struct {
	fields []string
	// byteOffset and line are the position the Rust csv crate reports for the record (csv::Position): where the
	// previous read ended (0 for the first record, or 3 after a second byte order mark), and the line count there. Blank lines before the record are
	// inside the read, so they are before the position.
	byteOffset int
	line       uint64
	// reportedLine is the line the Rust code derives from that position (line_of in transfer.rs).
	reportedLine uint64
}

func (r *csvReader) next() (csvRecord, bool) {
	// The crate sets the position of a record before the read starts, so it is where the last read stopped. A
	// read that ended at a record terminator stopped after its first byte: after "\r" of a CRLF, leaving the "\n".
	// Rust's line_of counts the LF before that offset (cumulatively), and first moves it past an LF that follows a
	// CR, "because the crate reports the start of a record after a CRLF one line early".
	start, newlines := r.pos, r.line-1
	reported := newlines + 1
	if start > 0 && start < len(r.text) && r.text[start] == '\n' && r.text[start-1] == '\r' {
		reported++
	}
	for r.pos < len(r.text) && (r.text[r.pos] == '\r' || r.text[r.pos] == '\n') {
		if r.text[r.pos] == '\n' {
			r.line++
		}
		r.pos++
	}
	if r.pos >= len(r.text) {
		return csvRecord{}, false
	}
	rec := csvRecord{byteOffset: start, line: newlines + 1, reportedLine: reported}
	const (
		startField = iota
		inField
		inQuoted
		afterQuote
	)
	state := startField
	var field []byte
	endField := func() {
		rec.fields = append(rec.fields, string(field))
		field = field[:0]
	}
	for r.pos < len(r.text) {
		c := r.text[r.pos]
		r.pos++
		if c == '\n' {
			r.line++
		}
		switch state {
		case startField:
			switch c {
			case '"':
				state = inQuoted
			case ',':
				endField()
			case '\r', '\n':
				endField()
				return rec, true
			default:
				state = inField
				field = append(field, c)
			}
		case inField:
			switch c {
			case ',':
				endField()
				state = startField
			case '\r', '\n':
				endField()
				return rec, true
			default:
				field = append(field, c)
			}
		case inQuoted:
			if c == '"' {
				state = afterQuote
			} else {
				field = append(field, c)
			}
		case afterQuote:
			switch c {
			case '"':
				field = append(field, '"')
				state = inQuoted
			case ',':
				endField()
				state = startField
			case '\r', '\n':
				endField()
				return rec, true
			default:
				state = inField
				field = append(field, c)
			}
		}
	}
	endField()
	return rec, true
}

// csvColumn is a column of a CSV file: the name, a field of the DocType, or a standard column of an exported
// file (owner, modified_at, ...) that is read and not used.
type csvColumn struct {
	kind  int
	field core.FieldType
}

const (
	columnName = iota
	columnField
	columnIgnored
)

func parseCSV(doctype *core.DocType, mode db.ImportMode, text string, maxRows int) (*ParsedImport, error) {
	reader := &csvReader{text: text, line: 1}
	// Rust strips one BOM before the crate sees the text; the crate strips a second one at the start of its
	// first read (csv-core strip_utf8_bom). Its bytes still count in the byte offsets, so the reader starts
	// after them instead of the text being cut.
	if strings.HasPrefix(text, "\ufeff") {
		reader.pos = len("\ufeff")
	}
	header, ok := reader.next()
	// No record at all, or one empty field.
	if !ok || (len(header.fields) == 1 && header.fields[0] == "") {
		return nil, invalidFile("the CSV file has no header row")
	}
	headers := header.fields
	// More columns than the DocType can have is refused before any column is looked at: the list of problems
	// must not grow with the file.
	possible := len(doctype.Fields) + 1 + len(standardKeys)
	if len(headers) > possible {
		return nil, invalidFile(fmt.Sprintf("the header has %d columns; at most %d are possible", len(headers), possible))
	}
	var problems []string
	seen := map[string]bool{}
	columns := make([]csvColumn, 0, len(headers))
	for _, h := range headers {
		if seen[h] {
			problems = append(problems, fmt.Sprintf("duplicate column `%s`", core.Shorten(h)))
		}
		seen[h] = true
		if h == "name" {
			columns = append(columns, csvColumn{kind: columnName})
			continue
		}
		if isStandardKey(doctype, h) {
			columns = append(columns, csvColumn{kind: columnIgnored})
			continue
		}
		switch f := doctype.Field(h); {
		case f != nil && f.Type.Kind == core.FieldTable:
			problems = append(problems, fmt.Sprintf("column `%s` is a table field: CSV import of child tables is not supported; use JSON", h))
		case f != nil:
			columns = append(columns, csvColumn{kind: columnField, field: f.Type})
		default:
			problems = append(problems, fmt.Sprintf("unknown column `%s` for DocType `%s`", core.Shorten(h), doctype.Name))
		}
	}
	if mode == db.ImportUpsert && !seen["name"] {
		problems = append(problems, "an upsert needs a `name` column")
	}
	if len(problems) > 0 {
		b := freshBudget()
		return nil, &FileError{Problems: capped(problems, b)}
	}
	parsed := &ParsedImport{}
	for index := 1; ; index++ {
		rec, ok := reader.next()
		if !ok {
			break
		}
		if len(rec.fields) != len(headers) {
			return nil, invalidFile(fmt.Sprintf(
				"the CSV file cannot be read: CSV error: record %d (line: %d, byte: %d): found record with %d fields, but the previous record has %d fields",
				index, rec.line, rec.byteOffset, len(rec.fields), len(headers)))
		}
		if parsed.Total() >= maxRows {
			return nil, &FileError{TooLarge: true, Limit: maxRows}
		}
		row := parsed.Total() + 1
		item := db.ImportRow{Row: row, Line: rec.reportedLine, Values: map[string]core.Value{}}
		var details []string
		for i, cell := range rec.fields {
			switch column := columns[i]; column.kind {
			case columnName:
				if cell != "" {
					item.Name = cell
				}
			case columnField:
				if cell == "" {
					item.Empty = append(item.Empty, headers[i])
					continue
				}
				value, err := cellValue(headers[i], column.field, cell)
				if err != nil {
					details = append(details, err.Error())
					continue
				}
				item.Values[headers[i]] = value
			}
		}
		item.Name, item.NameGiven = knownName(doctype, item.Name, item.Name != "", item.Values)
		item.NameGiven = item.NameGiven && item.Name == ""
		if len(details) == 0 {
			parsed.Rows = append(parsed.Rows, item)
		} else {
			parsed.Problems = append(parsed.Problems, RowProblem{Row: row, Line: rec.reportedLine, Name: item.Name, NameGiven: item.NameGiven, Details: details})
		}
	}
	return parsed, nil
}

// cellValue is a CSV cell as a value of its field's type, mapped like the JSON API maps the same value.
func cellValue(name string, ft core.FieldType, cell string) (core.Value, error) {
	switch ft.Kind {
	case core.FieldData, core.FieldText, core.FieldSelect, core.FieldLink:
		return core.Text(cell), nil
	case core.FieldInt:
		trimmed := strings.TrimSpace(cell)
		n, err := strconv.ParseInt(trimmed, 10, 64)
		if err != nil {
			return core.Value{}, bad(name, fmt.Sprintf("`%s` is not an integer", core.Shorten(trimmed)))
		}
		return core.Int(n), nil
	case core.FieldCheck:
		switch lower := asciiLower(strings.TrimSpace(cell)); lower {
		case "true", "1":
			return core.Bool(true), nil
		case "false", "0":
			return core.Bool(false), nil
		default:
			return core.Value{}, bad(name, fmt.Sprintf("`%s` is not true, false, 1 or 0", core.Shorten(lower)))
		}
	case core.FieldDecimal, core.FieldDate, core.FieldDatetime:
		return toValue(name, ft, strings.TrimSpace(cell))
	}
	return core.Value{}, bad(name, "a table field is not a CSV column")
}

// ---- running an import and its summary ------------------------------------------------------------

// RowReport is one row error of an import, as it is reported.
type RowReport struct {
	// Row is the 1-based data row number.
	Row int
	// Line is the line of a CSV file; 0 for JSON.
	Line uint64
	// Name is the document name, if the row gave one; NameGiven says it is the empty text.
	Name      string
	NameGiven bool
	// Code is the error code, as the API names it (validation_failed, hook_rejected, ...).
	Code string
	// Message is the message.
	Message string
	// Details is one entry per problem, for a validation error.
	Details []string
}

// ImportSummary is what an import did, or, for a dry run or a failed one, would have done (nothing is kept
// then).
type ImportSummary struct {
	DryRun bool
	Mode   db.ImportMode
	// Rows is the data rows of the file.
	Rows int
	// Inserted and Updated are the rows that inserted and updated a document.
	Inserted, Updated uint64
	// Errors are the first db.MaxReportedErrors errors, in row order.
	Errors []RowReport
	// ErrorCount is the number of rows with an error.
	ErrorCount uint64
}

// JSON is the summary as JSON: {"dry_run", "mode", "rows", "inserted", "updated", "errors", "error_count"},
// within the total bound of an answer (about 64 KiB, see error.go): the HTTP route and the CLI print this, so
// neither can grow with the file.
func (s *ImportSummary) JSON() any { return boundedJSON(s.fullJSON()) }

func (s *ImportSummary) fullJSON() map[string]any {
	errors := make([]any, len(s.Errors))
	for i, e := range s.Errors {
		m := map[string]any{"row": e.Row, "code": e.Code, "message": e.Message}
		if e.Line > 0 {
			m["line"] = e.Line
		}
		if e.Name != "" || e.NameGiven {
			m["name"] = e.Name
		} else {
			m["name"] = nil
		}
		if len(e.Details) > 0 {
			details := make([]any, len(e.Details))
			for j, d := range e.Details {
				details[j] = d
			}
			m["details"] = details
		}
		errors[i] = m
	}
	mode := "insert"
	if s.Mode == db.ImportUpsert {
		mode = "upsert"
	}
	return map[string]any{
		"dry_run":     s.DryRun,
		"mode":        mode,
		"rows":        s.Rows,
		"inserted":    s.Inserted,
		"updated":     s.Updated,
		"errors":      errors,
		"error_count": s.ErrorCount,
	}
}

// RunImport runs a parsed file as caller: all rows in one transaction, committed only when dryRun is false
// and no row (parse problems included) has an error. See db.Import.
func RunImport(ctx context.Context, client db.TxStarter, registry *core.Registry, doctype string, caller core.Caller,
	parsed *ParsedImport, mode db.ImportMode, dryRun bool) (*ImportSummary, error) {
	total := parsed.Total()
	commit := !dryRun && len(parsed.Problems) == 0
	outcome, err := db.Import(ctx, client, registry, doctype, caller, parsed.Rows, mode, commit)
	if err != nil {
		return nil, err
	}
	var errors []RowReport
	for _, p := range parsed.Problems {
		errors = append(errors, RowReport{Row: p.Row, Line: p.Line, Name: core.Shorten(p.Name), NameGiven: p.NameGiven, Code: "validation_failed",
			Message: "validation failed", Details: capped(p.Details, freshBudget())})
	}
	problems := uint64(len(errors))
	for _, e := range outcome.Errors {
		api := fromError(e.Err)
		errors = append(errors, RowReport{Row: e.Row, Line: e.Line, Name: core.Shorten(e.Name), NameGiven: e.NameGiven, Code: api.code,
			Message: cutText(api.message), Details: capped(api.details, freshBudget())})
	}
	sort.SliceStable(errors, func(i, j int) bool { return errors[i].Row < errors[j].Row })
	if len(errors) > db.MaxReportedErrors {
		errors = errors[:db.MaxReportedErrors]
	}
	return &ImportSummary{DryRun: dryRun, Mode: mode, Rows: total, Inserted: outcome.Inserted, Updated: outcome.Updated,
		Errors: errors, ErrorCount: problems + outcome.ErrorCount}, nil
}

// asciiLower lower-cases A to Z only, as Rust's to_ascii_lowercase does.
func asciiLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
}
