package httpapi

import (
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tvstore/forgebase-go/internal/core"
	"github.com/tvstore/forgebase-go/internal/db"
	"github.com/tvstore/forgebase-go/internal/db/dbtest"
	"github.com/tvstore/forgebase-go/internal/decimal"
)

// The unit tests of transfer.go (the file formats of import and export), ported from the tests of
// crates/forgebase-http/src/transfer.rs, and the bound of an import summary (the tests of error.rs that
// P3.2 deferred until the summary existed).

const trThing = `
name = "thing"
label = "Thing"
[[fields]]
name = "title"
type = "data"
[[fields]]
name = "qty"
type = "int"
[[fields]]
name = "price"
type = "decimal"
precision = 12
scale = 2
[[fields]]
name = "due"
type = "date"
[[fields]]
name = "done"
type = "check"
[[fields]]
name = "lines"
type = "table"
target = "thing_line"
`

const trThingLine = "name = \"thing_line\"\nlabel = \"L\"\nis_child = true\n[[fields]]\nname = \"item\"\ntype = \"data\"\n"

func trRegistry(t *testing.T) (*core.Registry, *core.DocType) {
	t.Helper()
	registry := dbtest.Registry(t, trThingLine, trThing)
	return registry, registry.Get("thing")
}

func trCSV(t *testing.T, text string) (*ParsedImport, error) {
	t.Helper()
	registry, thing := trRegistry(t)
	return ParseImport(registry, thing, FormatCSV, db.ImportInsert, []byte(text), 100)
}

// Rust: transfer.rs::tests::csv_records_are_quoted_only_when_needed
func TestCSVRecordsAreQuotedOnlyWhenNeeded(t *testing.T) {
	var out strings.Builder
	AppendCSVRecord(&out, []string{"plain", "a,b", `say "hi"`, "two\r\nlines", "line\nfeed", " lead", "trail ", "mid dle", "", "héllo ✓"})
	want := "plain,\"a,b\",\"say \"\"hi\"\"\",\"two\r\nlines\",\"line\nfeed\",\" lead\",\"trail \",mid dle,,héllo ✓\r\n"
	if out.String() != want {
		t.Errorf("%q\nwant %q", out.String(), want)
	}
}

// Rust: transfer.rs::tests::only_text_values_are_neutralized
func TestOnlyTextValuesAreNeutralized(t *testing.T) {
	text := func(s string) string {
		got, err := CSVCell("f", core.Text(s))
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	for _, risky := range []string{"=1+1", "+1", "-1", "@SUM(A1)", "\tx", "\rx"} {
		if got := text(risky); got != "'"+risky {
			t.Errorf("%q: %q", risky, got)
		}
	}
	for in, want := range map[string]string{"safe": "safe", " =x": " =x", "": ""} {
		if got := text(in); got != want {
			t.Errorf("%q: %q", in, got)
		}
	}
	// Numbers, decimals, booleans and dates are never prefixed.
	d, err := decimal.Parse("-12.50")
	if err != nil {
		t.Fatal(err)
	}
	dec := core.Decimal(d)
	for value, want := range map[string]string{"int": "-5", "decimal": "-12.50", "bool": "true", "null": ""} {
		var v core.Value
		switch value {
		case "int":
			v = core.Int(-5)
		case "decimal":
			v = dec
		case "bool":
			v = core.Bool(true)
		}
		if got, err := CSVCell("f", v); err != nil || got != want {
			t.Errorf("%s: %q %v", value, got, err)
		}
	}
}

// Rust: transfer.rs::tests::a_text_is_guarded_whatever_else_is
func TestATextIsGuardedWhateverElseIs(t *testing.T) {
	if got, _ := CSVCell("f", core.Text("=1")); got != "'=1" {
		t.Error(got)
	}
	if got, _ := CSVCell("f", core.Int(-1)); got != "-1" {
		t.Error(got)
	}
}

// Rust: transfer.rs::tests::exporter_fields_are_checked
func TestExporterFieldsAreChecked(t *testing.T) {
	_, thing := trRegistry(t)
	fail := func(format Format, names ...string) string {
		t.Helper()
		_, err := NewExporter(thing, format, names)
		if err == nil {
			t.Fatalf("%v: no error", names)
		}
		return err.Error()
	}
	for _, c := range []struct {
		format Format
		names  []string
		want   string
	}{
		{FormatCSV, []string{"nope"}, "unknown field `nope`"},
		{FormatCSV, []string{"lines"}, "table field"},
		{FormatCSV, []string{"title", "title"}, "twice"},
		{FormatCSV, []string{}, "empty"},
		{FormatCSV, []string{"docstatus"}, "unknown field"},
	} {
		if got := fail(c.format, c.names...); !strings.Contains(got, c.want) {
			t.Errorf("%v: %q does not contain %q", c.names, got, c.want)
		}
	}
	if _, err := NewExporter(thing, FormatJSON, []string{"lines", "name"}); err != nil {
		t.Error(err)
	}
	if _, err := NewExporter(thing, FormatCSV, []string{"owner", "title"}); err != nil {
		t.Error(err)
	}
	def, err := NewExporter(thing, FormatCSV, nil)
	if err != nil || def.Start() != "name,title,qty,price,due,done\r\n" || def.WithTables() {
		t.Errorf("%q %v", def.Start(), err)
	}
}

// Rust: transfer.rs::tests::csv_cells_are_parsed_by_the_field_type_and_empty_cells_are_not_given
func TestCSVCellsAreParsedByTheFieldTypeAndEmptyCellsAreNotGiven(t *testing.T) {
	parsed, err := trCSV(t, "\ufeffname,title,qty,price,due,done\r\nA,Hello,3,1.50,2026-10-31,TRUE\r\n,,,,,\r\nB,x,,,,0\r\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.Problems) != 0 || len(parsed.Rows) != 3 {
		t.Fatalf("%v", parsed.Problems)
	}
	first := parsed.Rows[0]
	if first.Name != "A" || first.Line != 2 || first.Values["qty"] != core.Int(3) || first.Values["done"] != core.Bool(true) ||
		first.Values["price"].Kind != core.KindDecimal || first.Values["due"].Kind != core.KindDate {
		t.Errorf("%+v", first)
	}
	blank := parsed.Rows[1]
	if blank.Name != "" || len(blank.Values) != 0 || len(blank.Empty) != 5 {
		t.Errorf("%+v", blank)
	}
	last := parsed.Rows[2]
	if last.Row != 3 || last.Values["done"] != core.Bool(false) || !contains(last.Empty, "qty") {
		t.Errorf("%+v", last)
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// Rust: transfer.rs::tests::csv_quoted_cells_keep_commas_quotes_and_line_breaks
func TestCSVQuotedCellsKeepCommasQuotesAndLineBreaks(t *testing.T) {
	parsed, err := trCSV(t, "title\r\n\"a,b\"\r\n\"say \"\"hi\"\"\"\r\n\"two\r\nlines\"\r\n")
	if err != nil {
		t.Fatal(err)
	}
	var titles []string
	for _, r := range parsed.Rows {
		titles = append(titles, r.Values["title"].Text)
	}
	if strings.Join(titles, "|") != "a,b|say \"hi\"|two\r\nlines" {
		t.Errorf("%q", titles)
	}
	if parsed.Rows[2].Line != 4 {
		t.Errorf("line %d", parsed.Rows[2].Line)
	}
}

// Rust: transfer.rs::tests::a_bad_cell_is_a_row_problem_with_its_number
func TestABadCellIsARowProblemWithItsNumber(t *testing.T) {
	parsed, err := trCSV(t, "title,qty,due\nok,1,2026-01-01\nbad,x,31/10/2026\nok,2,2026-01-02\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.Rows) != 2 || len(parsed.Problems) != 1 {
		t.Fatalf("%d rows, %d problems", len(parsed.Rows), len(parsed.Problems))
	}
	p := parsed.Problems[0]
	if p.Row != 2 || p.Line != 3 || len(p.Details) != 2 || !strings.Contains(p.Details[0], "qty") || !strings.Contains(p.Details[1], "due") {
		t.Errorf("%+v", p)
	}
}

func trRefused(t *testing.T, text string) string {
	t.Helper()
	_, err := trCSV(t, text)
	var fe *FileError
	if !errors.As(err, &fe) || fe.TooLarge {
		t.Fatalf("expected a refusal of %q, got %v", text, err)
	}
	return strings.Join(fe.Problems, "; ")
}

// Rust: transfer.rs::tests::csv_files_with_bad_headers_are_refused_whole
func TestCSVFilesWithBadHeadersAreRefusedWhole(t *testing.T) {
	for text, want := range map[string]string{
		"title,nope\na,b\n":  "unknown column `nope`",
		"title,title\na,b\n": "duplicate column `title`",
		"title,qty\na\n":     "cannot be read",
		"":                   "no header",
	} {
		if got := trRefused(t, text); !strings.Contains(got, want) {
			t.Errorf("%q: %q does not contain %q", text, got, want)
		}
	}
	table := trRefused(t, "title,lines\na,b\n")
	if !strings.Contains(table, "child tables is not supported") || !strings.Contains(table, "use JSON") {
		t.Error(table)
	}
	// Several problems are reported together.
	all := trRefused(t, "x,y,y\n1,2,3\n")
	if !strings.Contains(all, "unknown column `x`") || !strings.Contains(all, "duplicate column `y`") {
		t.Error(all)
	}
	registry, thing := trRegistry(t)
	// An upsert needs the name column.
	_, err := ParseImport(registry, thing, FormatCSV, db.ImportUpsert, []byte("title\na\n"), 10)
	var fe *FileError
	if !errors.As(err, &fe) || !strings.Contains(fe.Problems[0], "`name` column") {
		t.Errorf("%v", err)
	}
	// Not UTF-8.
	_, err = ParseImport(registry, thing, FormatCSV, db.ImportInsert, []byte("title\n\xe9\n"), 10)
	if !errors.As(err, &fe) || !strings.Contains(fe.Problems[0], "UTF-8") || !strings.Contains(fe.Problems[0], "offset 6") {
		t.Errorf("%v", err)
	}
}

// Rust: transfer.rs::tests::the_row_limit_is_checked_while_reading
func TestTheRowLimitIsCheckedWhileReading(t *testing.T) {
	registry, thing := trRegistry(t)
	for format, body := range map[Format]string{FormatCSV: "title\na\nb\nc\n", FormatJSON: `[{"title":"a"},{"title":"b"},{"title":"c"}]`} {
		_, err := ParseImport(registry, thing, format, db.ImportInsert, []byte(body), 2)
		var fe *FileError
		if !errors.As(err, &fe) || !fe.TooLarge || fe.Limit != 2 {
			t.Errorf("%v: %v", format, err)
		}
	}
}

// Rust: transfer.rs::tests::json_rows_may_carry_child_rows_and_ignore_the_standard_keys_of_an_export
func TestJSONRowsMayCarryChildRowsAndIgnoreTheStandardKeysOfAnExport(t *testing.T) {
	registry, thing := trRegistry(t)
	parsed, err := ParseImport(registry, thing, FormatJSON, db.ImportInsert, []byte(`[{"id":"x","name":"N1","owner":"ann","version":3,"title":"t","qty":2,
                 "lines":[{"item":"a"},{"item":"b"}]}, 5, {"name": 7}, {"qty":"x"}]`), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.Rows) != 1 {
		t.Fatalf("%d rows", len(parsed.Rows))
	}
	row := parsed.Rows[0]
	if row.Name != "N1" || len(row.Tables["lines"]) != 2 || len(row.Values) != 2 {
		t.Errorf("%+v", row)
	}
	var rows []int
	for _, p := range parsed.Problems {
		rows = append(rows, p.Row)
	}
	if fmt.Sprint(rows) != "[2 3 4]" || !strings.Contains(parsed.Problems[0].Details[0], "object") {
		t.Errorf("%v %v", rows, parsed.Problems)
	}
	_, err = ParseImport(registry, thing, FormatJSON, db.ImportInsert, []byte("{}"), 10)
	var fe *FileError
	if !errors.As(err, &fe) {
		t.Errorf("%v", err)
	}
}

// Rust: transfer.rs::tests::a_header_with_too_many_columns_is_one_short_problem
func TestAHeaderWithTooManyColumnsIsOneShortProblem(t *testing.T) {
	header := strings.Repeat("a,", 99_999) + "a"
	_, err := trCSV(t, header+"\n")
	var fe *FileError
	if !errors.As(err, &fe) || len(fe.Problems) != 1 || !strings.Contains(fe.Problems[0], "100000 columns; at most") {
		t.Errorf("%v", err)
	}
}

// Rust: transfer.rs::tests::the_problems_of_a_header_are_capped
func TestTheProblemsOfAHeaderAreCapped(t *testing.T) {
	// As many columns as allowed (6 fields + name + 9 standard = 16 here), all unknown.
	var names []string
	for i := range 16 {
		names = append(names, fmt.Sprintf("x%d", i))
	}
	got := trRefused(t, strings.Join(names, ",")+"\n")
	if strings.Count(got, ";") != 15 {
		t.Errorf("%q", got)
	}
	many := make([]string, 250)
	for i := range many {
		many[i] = fmt.Sprint(i)
	}
	capped := capped(many, freshBudget())
	if len(capped) != db.MaxReportedErrors+1 || capped[len(capped)-1] != "and 150 more" {
		t.Errorf("%d %q", len(capped), capped[len(capped)-1])
	}
}

// Rust: transfer.rs::tests::csv_ignores_the_standard_columns_of_an_export
func TestCSVIgnoresTheStandardColumnsOfAnExport(t *testing.T) {
	parsed, err := trCSV(t, "name,owner,modified_at,version,title,docstatus\nN,ann,2026-01-01T00:00:00Z,3,Hello,0\n")
	if err != nil || len(parsed.Problems) != 0 {
		t.Fatalf("%v %v", err, parsed.Problems)
	}
	row := parsed.Rows[0]
	if row.Name != "N" || len(row.Values) != 1 || len(row.Empty) != 0 {
		t.Errorf("%+v", row)
	}
}

// Rust: transfer.rs::tests::child_rows_count_toward_the_row_limit
func TestChildRowsCountTowardTheRowLimit(t *testing.T) {
	registry, thing := trRegistry(t)
	file := []byte(`[{"title":"a","lines":[{"item":"1"},{"item":"2"}]},{"title":"b","lines":[{"item":"3"}]}]`)
	// 2 documents + 3 child rows = 5.
	if _, err := ParseImport(registry, thing, FormatJSON, db.ImportInsert, file, 5); err != nil {
		t.Error(err)
	}
	_, err := ParseImport(registry, thing, FormatJSON, db.ImportInsert, file, 4)
	var fe *FileError
	if !errors.As(err, &fe) || !fe.TooLarge || fe.Limit != 4 {
		t.Errorf("%v", err)
	}
}

// The reader of CSV is the one of the Rust csv crate with its defaults, not encoding/csv. Not in Rust: these
// are the cases where the two differ, and the ones the crate documents.
func TestTheCSVReaderBehavesAsTheRustCrate(t *testing.T) {
	read := func(text string) [][]string {
		r := &csvReader{text: text, line: 1}
		var out [][]string
		for {
			rec, ok := r.next()
			if !ok {
				return out
			}
			out = append(out, rec.fields)
		}
	}
	for _, c := range []struct{ in, want string }{
		{"a,b\r\nc,d\r\n", "[a b] [c d]"},
		{"a,b\nc,d", "[a b] [c d]"},
		{"a,b\rc,d\r", "[a b] [c d]"},
		{"\r\n\n a\n\n\nb\n", "[ a] [b]"},
		{"a,\n,\n", "[a ] [ ]"},
		{"a,", "[a ]"},
		{`"a""b",c`, `[a"b c]`},
		{"\"x\r\ny\",z", "[x\r\ny z]"},
		{`ab"c,d`, `[ab"c d]`},
		{`"ab"c,d`, "[abc d]"},
		{`"unterminated, still`, "[unterminated, still]"},
		{`""`, "[]"},
		{"a,b\n\"\"\n", "[a b] []"},
		{" \"a\",b", `[ "a" b]`},
	} {
		if got := fmt.Sprint(read(c.in)); got != "["+c.want+"]" {
			t.Errorf("%q: got %v, want [%s]", c.in, got, c.want)
		}
	}
}

// ---- the bound of a summary (error.rs) -----------------------------------------------------------

func jsonLen(t *testing.T, v any) int {
	t.Helper()
	b, err := marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return len(b)
}

func objs(n int, mk func(i int) map[string]any) []any {
	out := make([]any, n)
	for i := range out {
		out[i] = mk(i)
	}
	return out
}

// Rust: error.rs::bound_tests::nested_arrays_share_one_budget
func TestNestedArraysShareOneBudget(t *testing.T) {
	long := strings.Repeat("\U0001D11E", 10_000)
	value := map[string]any{
		"count": 5,
		"name":  long,
		"errors": objs(300, func(i int) map[string]any {
			details := make([]any, 300)
			for j := range details {
				details[j] = long
			}
			return map[string]any{"row": i, "details": details}
		}),
	}
	bounded := boundedJSON(value).(map[string]any)
	if bounded["count"] != 5 {
		t.Error(bounded["count"])
	}
	errs := bounded["errors"].([]any)
	if len(errs) > maxReportedErrors+1 {
		t.Errorf("%d entries", len(errs))
	}
	if n := jsonLen(t, bounded); n >= maxErrorBodyBytes+2048 {
		t.Errorf("%d bytes", n)
	}
	// The first row is kept first.
	if errs[0].(map[string]any)["row"] != 0 {
		t.Error(errs[0])
	}
}

// Rust: error.rs::bound_tests::the_counts_of_a_summary_survive_an_exhausted_budget
func TestTheCountsOfASummarySurviveAnExhaustedBudget(t *testing.T) {
	value := map[string]any{
		"errors": objs(100, func(int) map[string]any {
			details := make([]any, 100)
			for j := range details {
				details[j] = strings.Repeat("\x01", 600)
			}
			return map[string]any{"row": 3, "code": "validation_failed", "message": "bad", "name": "n", "details": details}
		}),
		"mode": "upsert", "inserted": 7, "updated": 2, "rows": 100, "error_count": 100, "dry_run": true,
	}
	bounded := boundedJSON(value).(map[string]any)
	if bounded["mode"] != "upsert" || bounded["dry_run"] != true {
		t.Errorf("%v", bounded)
	}
	for key, count := range map[string]int{"inserted": 7, "updated": 2, "rows": 100, "error_count": 100} {
		if bounded[key] != count {
			t.Errorf("%s: %v", key, bounded[key])
		}
	}
	// A row error keeps its code and message ahead of its details.
	first := bounded["errors"].([]any)[0].(map[string]any)
	if first["code"] != "validation_failed" || first["message"] != "bad" || first["row"] != 3 {
		t.Errorf("%v", first)
	}
	if n := jsonLen(t, bounded); n > maxErrorBodyBytes+2048 {
		t.Errorf("%d bytes", n)
	}
}

// Rust: error.rs::bound_tests::keys_are_cut_and_counted
func TestKeysAreCutAndCounted(t *testing.T) {
	bounded := boundedJSON(map[string]any{strings.Repeat("k", 100_000): 1}).(map[string]any)
	for key := range bounded {
		if n := len([]rune(key)); n != maxMessageChars {
			t.Errorf("%d characters", n)
		}
	}
}

// Rust: error.rs::bound_tests::a_response_body_is_small_whatever_the_error_holds (the import_failed half)
func TestAnImportFailedBodyIsSmallWhateverTheSummaryHolds(t *testing.T) {
	summary := map[string]any{
		"error_count": 3,
		"errors": objs(1000, func(int) map[string]any {
			details := make([]any, 500)
			for j := range details {
				details[j] = strings.Repeat("\x01", 900)
			}
			return map[string]any{"row": 1, "details": details}
		}),
	}
	e := errImportFailed(summary, 3)
	e.message = "m"
	rec := httptest.NewRecorder()
	e.write(rec)
	if n := rec.Body.Len(); n >= maxErrorBodyBytes+2048 {
		t.Errorf("%d bytes", n)
	}
	if rec.Code != 422 {
		t.Error(rec.Code)
	}
}

// Rust: error.rs (From<DbError> for ApiError): the export_too_large mapping.
func TestExportTooLargeIsA422WithTheCountAndTheLimit(t *testing.T) {
	e := fromDB(&db.Error{Kind: db.ErrExportTooLarge, Count: 4, Limit: 3})
	rec := httptest.NewRecorder()
	e.write(rec)
	want := `{"error":{"code":"export_too_large","details":{"count":4,"limit":3},"message":"the export would have 4 rows, over the limit of 3; narrow the filters"}}`
	if rec.Code != 422 || rec.Body.String() != want {
		t.Errorf("%d %s", rec.Code, rec.Body.String())
	}
}

// The line of a CSV row is the one the Rust code derives from the position of the csv crate (reader.rs of csv
// 1.x: the position of a record is taken before its read starts, so it is where the previous read stopped; a
// read that ends at CRLF stops after the CR), counting the LF before it (line_of in transfer.rs). Blank lines
// before a record are part of its read, so they are not counted: the line is then the one of the first blank
// line. The expected values are derived by hand from that rule, not from running Rust.
func TestTheLineOfACSVRowIsTheOneRustReports(t *testing.T) {
	for _, c := range []struct {
		name, file string
		lines      []uint64
	}{
		{"no blank line", "title\r\na\r\nb\r\n", []uint64{2, 3}},
		{"LF", "title\na\nb\n", []uint64{2, 3}},
		{"a blank CRLF line before a record", "title\r\n\r\na\r\n", []uint64{2}},
		{"a blank LF line before a record", "title\n\na\n", []uint64{2}},
		{"blank lines before the header", "\n\ntitle\nx\n", []uint64{4}},
		{"a blank line between records", "title\r\na\r\n\r\nb\r\n", []uint64{2, 3}},
		{"two blank LF lines between records", "title\na\n\n\nb\n", []uint64{2, 3}},
	} {
		parsed, err := trCSV(t, c.file)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		var got []uint64
		for _, r := range parsed.Rows {
			got = append(got, r.Line)
		}
		if fmt.Sprint(got) != fmt.Sprint(c.lines) {
			t.Errorf("%s: lines %v, want %v", c.name, got, c.lines)
		}
	}
}

// Rust: a JSON row with "name": "" has Some("") as its name (the empty text), not no name.
func TestAnEmptyJSONNameIsAGivenName(t *testing.T) {
	registry, thing := trRegistry(t)
	parsed, err := ParseImport(registry, thing, FormatJSON, db.ImportInsert, []byte(`[{"name":"","title":"x"},{"title":"y"},{"name":"","qty":"x"}]`), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.Rows) != 2 || !parsed.Rows[0].NameGiven || parsed.Rows[1].NameGiven {
		t.Errorf("%+v", parsed.Rows)
	}
	if len(parsed.Problems) != 1 || !parsed.Problems[0].NameGiven {
		t.Errorf("%+v", parsed.Problems)
	}
}

// A second byte order mark: Rust strips one in parse_import and csv-core strips another at the start of its
// first read (reader.rs strip_utf8_bom); the bytes still count in the offsets of the crate's messages.
func TestASecondByteOrderMarkIsStrippedByTheReader(t *testing.T) {
	parsed, err := trCSV(t, "\xEF\xBB\xBF\xEF\xBB\xBFtitle\r\nx")
	if err != nil || len(parsed.Rows) != 1 || parsed.Rows[0].Values["title"] != core.Text("x") {
		t.Fatalf("%+v %v", parsed, err)
	}
	// Three of them: the third is part of the first column name.
	if got := trRefused(t, "\xEF\xBB\xBF\xEF\xBB\xBF\xEF\xBB\xBFtitle\r\nx"); !strings.Contains(got, "unknown column") {
		t.Error(got)
	}
}

// Rust: cell_value lower-cases a check cell with to_ascii_lowercase: only A to Z.
func TestACheckCellIsLowerCasedAsASCIIOnly(t *testing.T) {
	_, err := trCSV(t, "done\nTRU\u00c9\n")
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := trCSV(t, "done\nTRU\u00c9\n")
	if got := strings.Join(parsed.Problems[0].Details, ";"); !strings.Contains(got, "`tru\u00c9` is not true, false, 1 or 0") {
		t.Error(got)
	}
}

// The error of a record of another length is the text of the csv crate, with the position it reports.
func TestTheErrorOfARecordOfAnotherLengthIsTheCratesText(t *testing.T) {
	for file, want := range map[string]string{
		"title\r\na,b\r\n": "the CSV file cannot be read: CSV error: record 1 (line: 1, byte: 6): found record with 2 fields, but the previous record has 1 fields",
		"title\na,b\n":     "the CSV file cannot be read: CSV error: record 1 (line: 2, byte: 6): found record with 2 fields, but the previous record has 1 fields",
	} {
		if got := trRefused(t, file); got != want {
			t.Errorf("%q: %q, want %q", file, got, want)
		}
	}
}
