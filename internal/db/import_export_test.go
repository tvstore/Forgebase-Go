package db_test

import (
	"fmt"
	"reflect"
	"slices"
	"sort"
	"testing"

	"github.com/tvstore/forgebase-go/internal/core"
	"github.com/tvstore/forgebase-go/internal/db"
	"github.com/tvstore/forgebase-go/internal/db/dbtest"
)

// Import and export (M9.3, ADR 0021) against a real PostgreSQL: the rights, the snapshot and the pages of an
// export, and the all-or-nothing import with savepoints, its dry run, upsert and per-row errors. Ported from
// crates/forgebase-db/tests/import_export.rs.

const ieParcel = `
name = "parcel"
label = "Parcel"
[[fields]]
name = "title"
type = "data"
required = true
[[fields]]
name = "qty"
type = "int"
[[fields]]
name = "note"
type = "text"
[[fields]]
name = "lines"
type = "table"
target = "parcel_line"

[[permissions]]
role = "Reader"
read = true
[[permissions]]
role = "Plain Reader"
read = true
[[permissions]]
role = "Exporter"
export = true
[[permissions]]
role = "Reader Exporter"
read = true
export = true
[[permissions]]
role = "Own Reader Exporter"
read = true
export = true
if_owner = true
[[permissions]]
role = "Reader"
export = true
if_owner = true
[[permissions]]
role = "Importer"
import = true
create = true
[[permissions]]
role = "Import Only"
import = true
[[permissions]]
role = "Create Only"
create = true
[[permissions]]
role = "Upserter"
import = true
create = true
write = true
read = true
`

const ieParcelLine = `
name = "parcel_line"
label = "Parcel Line"
is_child = true
[[fields]]
name = "item"
type = "data"
`

// ieBadge is named by its code.
const ieBadge = `
name = "badge"
label = "Badge"
naming = { field = "code" }
[[fields]]
name = "code"
type = "data"
required = true
unique = true
[[fields]]
name = "level"
type = "int"
[[permissions]]
role = "Upserter"
import = true
create = true
write = true
read = true
[[permissions]]
role = "Own Upserter"
import = true
create = true
write = true
read = true
if_owner = true
`

// ieRules sets the note of a parcel titled `hook-me`, refuses `reject` and enqueues an audit job.
type ieRules struct{ core.BaseHooks }

func (ieRules) Validate(ctx core.HookContext, doc *core.Document) error {
	if v := doc.Values["title"]; v.Kind == core.KindText {
		switch v.Text {
		case "reject":
			return core.NewHookError("rejected")
		case "hook-me":
			doc.Values["note"] = text("hooked")
		}
	}
	_, has := doc.Values["title"]
	return ctx.Enqueue("app.audit", map[string]any{"title": has})
}

func ieSetup(t *testing.T) (*dbtest.DB, *core.Registry) {
	t.Helper()
	d := dbtest.Migrated(t)
	reg := registry(t, ieParcel, ieParcelLine, ieBadge)
	if err := reg.AddHooks("parcel", ieRules{}); err != nil {
		t.Fatal(err)
	}
	if !reg.AddJob("app.audit") || !reg.AddJob(db.DeliverWebhookJob) {
		t.Fatal("jobs")
	}
	mustSync(t, d, reg)
	return d, reg
}

func ieOther(roles ...string) core.Caller { return user("someone-else", roles...) }

func ieRow(n int, title string) db.ImportRow {
	return db.ImportRow{Row: n, Line: uint64(n) + 1, Values: pairs("title", text(title))}
}

func ieNamed(n int, name string, kv ...any) db.ImportRow {
	return db.ImportRow{Row: n, Name: name, Values: pairs(kv...)}
}

func ieAddParcel(t *testing.T, d *dbtest.DB, reg *core.Registry, by core.Caller, title string) *db.StoredDocument {
	t.Helper()
	document := core.Document{Values: pairs("title", text(title)), Tables: map[string][]core.Row{"lines": {
		row("item", text(title+"-a")), row("item", text(title+"-b")),
	}}}
	return mustInsert(t, d, reg, "parcel", document, by)
}

type exported struct {
	docs  []*db.StoredDocument
	pages int
	count uint64
}

// ieExportAll is every document of an export, page by page, with the number of pages.
func ieExportAll(t *testing.T, d *dbtest.DB, reg *core.Registry, caller core.Caller, filters []db.Filter, sorts []db.Sort,
	limit uint64) (exported, error) {
	t.Helper()
	tx, err := db.BeginExport(bg, d.Pool)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(bg) }()
	export, err := db.StartExport(bg, tx, reg, caller, db.ExportQuery{DocType: "parcel", Filters: filters, Sort: sorts, Limit: limit, WithTables: true})
	if err != nil {
		return exported{}, err
	}
	out := exported{count: export.Count()}
	for {
		page, err := export.NextPage(bg)
		if err != nil {
			return exported{}, err
		}
		if page == nil {
			return out, nil
		}
		out.pages++
		out.docs = append(out.docs, page...)
	}
}

func ieTitles(docs []*db.StoredDocument) []string {
	out := []string{}
	for _, d := range docs {
		out = append(out, d.Values["title"].Text)
	}
	return out
}

// Rust: import_export.rs::export_needs_read_and_export_and_follows_if_owner
func TestExportNeedsReadAndExportAndFollowsIfOwner(t *testing.T) {
	d, reg := ieSetup(t)
	ieAddParcel(t, d, reg, sys("ann"), "ann-1")
	ieAddParcel(t, d, reg, sys("bob"), "bob-1")
	run := func(caller core.Caller) (exported, error) { return ieExportAll(t, d, reg, caller, nil, nil, 100) }
	denied := func(caller core.Caller, right string) {
		t.Helper()
		_, err := run(caller)
		if e := wantKind(t, err, db.ErrPermissionDenied); e.Right.Name() != right {
			t.Errorf("denied %s, got %s", right, e.Right.Name())
		}
	}
	// Read without export, export without read, and neither: refused, read first.
	denied(user("u", "Plain Reader"), "export")
	denied(user("u", "Exporter"), "read")
	denied(user("u"), "read")
	denied(user("u", "Create Only"), "read")
	// `Reader` reads everything but may export only its own: allowed, limited to own rows.
	got, err := run(user("u", "Reader"))
	if err != nil || len(got.docs) != 0 || got.count != 0 {
		t.Fatalf("%v %v", got, err)
	}
	// Both rights: everything.
	got, err = run(user("u", "Reader Exporter"))
	if err != nil || !reflect.DeepEqual(ieTitles(got.docs), []string{"ann-1", "bob-1"}) || got.count != 2 {
		t.Fatalf("%v %v", ieTitles(got.docs), err)
	}
	// Read on everything, export on own documents only: the export is limited to own documents.
	got, err = run(user("ann", "Reader"))
	if err != nil || !reflect.DeepEqual(ieTitles(got.docs), []string{"ann-1"}) || got.count != 1 {
		t.Fatalf("%v %v", ieTitles(got.docs), err)
	}
	// Read and export both for own documents only.
	got, err = run(user("bob", "Own Reader Exporter"))
	if err != nil || !reflect.DeepEqual(ieTitles(got.docs), []string{"bob-1"}) || got.count != 1 {
		t.Fatalf("%v %v", ieTitles(got.docs), err)
	}
	if got, err = run(ieOther("Own Reader Exporter")); err != nil || len(got.docs) != 0 {
		t.Errorf("%v %v", got, err)
	}
	// Administrator and the system context need no rule.
	if got, err = run(user("root", "Administrator")); err != nil || len(got.docs) != 2 {
		t.Errorf("%v %v", got, err)
	}
	if got, err = run(sys("cli")); err != nil || len(got.docs) != 2 {
		t.Errorf("%v %v", got, err)
	}
}

// Rust: import_export.rs::export_applies_filters_and_sort_and_counts_the_same_rows
func TestExportAppliesFiltersAndSortAndCountsTheSameRows(t *testing.T) {
	d, reg := ieSetup(t)
	system := sys("t")
	for _, c := range []struct {
		title string
		qty   int64
	}{{"b", 2}, {"a", 5}, {"c", 2}, {"d", 9}} {
		mustInsert(t, d, reg, "parcel", doc("title", text(c.title), "qty", core.Int(c.qty)), system)
	}
	filters := []db.Filter{{Field: "qty", Op: db.Lt(core.Int(9))}}
	sorts := []db.Sort{db.Desc("qty"), db.Asc("title")}
	got, err := ieExportAll(t, d, reg, system, filters, sorts, 100)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ieTitles(got.docs), []string{"a", "b", "c"}) || got.pages != 1 || got.count != 3 {
		t.Errorf("%v %d %d", ieTitles(got.docs), got.pages, got.count)
	}
	// A bad filter is an invalid query, nothing is read.
	_, err = ieExportAll(t, d, reg, system, []db.Filter{{Field: "nope", Op: db.IsNull(true)}}, nil, 100)
	wantKind(t, err, db.ErrInvalidQuery)
}

// Rust: import_export.rs::export_over_the_limit_is_refused_with_the_count
func TestExportOverTheLimitIsRefusedWithTheCount(t *testing.T) {
	d, reg := ieSetup(t)
	system := sys("t")
	for i := range 5 {
		ieAddParcel(t, d, reg, system, fmt.Sprintf("p%d", i))
	}
	_, err := ieExportAll(t, d, reg, system, nil, nil, 4)
	if e := wantKind(t, err, db.ErrExportTooLarge); e.Count != 5 || e.Limit != 4 {
		t.Errorf("%d of %d", e.Count, e.Limit)
	}
	if got, err := ieExportAll(t, d, reg, system, nil, nil, 5); err != nil || got.count != 5 {
		t.Errorf("at the limit: %v %v", got.count, err)
	}
}

// Rust: import_export.rs::export_streams_several_pages_with_their_child_rows_from_one_snapshot
func TestExportStreamsSeveralPagesWithTheirChildRowsFromOneSnapshot(t *testing.T) {
	d, reg := ieSetup(t)
	system := sys("t")
	// 2500 documents (three pages), each with two child rows, in one statement.
	d.Exec("INSERT INTO doc_parcel (id, name, owner, created_at, modified_at, modified_by, title) " +
		"SELECT gen_random_uuid(), 'P' || lpad(g::text, 5, '0'), 'seed', now(), now(), 'seed', 'T' || g FROM generate_series(1, 2500) g; " +
		"INSERT INTO doc_parcel_line (id, parent_id, idx, item) " +
		"SELECT gen_random_uuid(), p.id, i, p.name || '/' || i FROM doc_parcel p, generate_series(1, 2) i;")
	tx, err := db.BeginExport(bg, d.Pool)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(bg) }()
	export, err := db.StartExport(bg, tx, reg, system, db.ExportQuery{DocType: "parcel", Limit: 100_000, WithTables: true})
	if err != nil {
		t.Fatal(err)
	}
	if export.Count() != 2500 {
		t.Fatal(export.Count())
	}
	// A write that commits after the snapshot started is not seen.
	d.Exec("INSERT INTO doc_parcel (id, name, owner, created_at, modified_at, modified_by, title) " +
		"VALUES (gen_random_uuid(), 'P99999', 'seed', now(), now(), 'seed', 'late')")
	var sizes []int
	var names []string
	for {
		page, err := export.NextPage(bg)
		if err != nil {
			t.Fatal(err)
		}
		if page == nil {
			break
		}
		sizes = append(sizes, len(page))
		for _, doc := range page {
			rows := doc.Tables["lines"]
			if len(rows) != 2 || rows[0]["item"] != text(doc.Name+"/1") || rows[1]["item"] != text(doc.Name+"/2") {
				t.Fatalf("%s: %v", doc.Name, rows)
			}
			names = append(names, doc.Name)
		}
	}
	if want := []int{db.PageRows, db.PageRows, 2500 - 2*db.PageRows}; !reflect.DeepEqual(sizes, want) {
		t.Errorf("%v, want %v", sizes, want)
	}
	if len(names) != 2500 || !sort.StringsAreSorted(names) || slices.Contains(names, "P99999") {
		t.Errorf("%d names, sorted %v", len(names), sort.StringsAreSorted(names))
	}
}

func ieImport(t *testing.T, d *dbtest.DB, reg *core.Registry, doctype string, caller core.Caller, rows []db.ImportRow,
	mode db.ImportMode, commit bool) (*db.ImportOutcome, error) {
	t.Helper()
	return db.Import(bg, d.Pool, reg, doctype, caller, rows, mode, commit)
}

func mustImport(t *testing.T, d *dbtest.DB, reg *core.Registry, doctype string, caller core.Caller, rows []db.ImportRow,
	mode db.ImportMode, commit bool) *db.ImportOutcome {
	t.Helper()
	outcome, err := ieImport(t, d, reg, doctype, caller, rows, mode, commit)
	if err != nil {
		t.Fatal(err)
	}
	return outcome
}

// Rust: import_export.rs::import_needs_import_and_create_and_for_an_upsert_write
func TestImportNeedsImportAndCreateAndForAnUpsertWrite(t *testing.T) {
	d, reg := ieSetup(t)
	rows := []db.ImportRow{ieRow(1, "x")}
	denied := func(err error, right string) {
		t.Helper()
		if e := wantKind(t, err, db.ErrPermissionDenied); e.Right.Name() != right {
			t.Errorf("denied %s, got %s", right, e.Right.Name())
		}
	}
	try := func(mode db.ImportMode, roles ...string) (*db.ImportOutcome, error) {
		return ieImport(t, d, reg, "parcel", user("u", roles...), rows, mode, true)
	}
	_, err := try(db.ImportInsert)
	denied(err, "import")
	_, err = try(db.ImportInsert, "Create Only")
	denied(err, "import")
	_, err = try(db.ImportInsert, "Import Only")
	denied(err, "create")
	// Import and create inserts; the same caller may not upsert.
	if o, err := try(db.ImportInsert, "Importer"); err != nil || o.Inserted != 1 {
		t.Fatalf("%v %v", o, err)
	}
	_, err = try(db.ImportUpsert, "Importer")
	denied(err, "write")
	// `parcel` names are random: a given name is refused as a row error, not a right problem.
	o := mustImport(t, d, reg, "parcel", user("u", "Upserter"), []db.ImportRow{ieNamed(1, "N1", "title", text("y"))}, db.ImportUpsert, true)
	if o.ErrorCount != 1 {
		t.Errorf("%d", o.ErrorCount)
	}
	// The administrator and the system context need no rule.
	o = mustImport(t, d, reg, "parcel", user("root", "Administrator"), rows, db.ImportInsert, true)
	if !o.Committed {
		t.Error("not committed")
	}
	// A rights refusal wrote nothing: only the two committed rows exist.
	if n := count(t, d, "doc_parcel"); n != 2 {
		t.Errorf("%d documents", n)
	}
}

// Rust: import_export.rs::one_bad_row_rolls_back_everything_and_every_error_has_its_row
func TestOneBadRowRollsBackEverythingAndEveryErrorHasItsRow(t *testing.T) {
	d, reg := ieSetup(t)
	var rows []db.ImportRow
	for n := 1; n <= 5; n++ {
		rows = append(rows, ieRow(n, fmt.Sprintf("ok%d", n)))
	}
	rows = append(rows, ieRow(6, "reject"))                                             // a hook refuses
	rows = append(rows, db.ImportRow{Row: 7, Line: 8, Values: map[string]core.Value{}}) // title is required
	rows = append(rows, db.ImportRow{Row: 8, Values: pairs("title", core.Int(3))})      // wrong kind
	rows = append(rows, ieRow(9, "fine"))
	o := mustImport(t, d, reg, "parcel", sys("t"), rows, db.ImportInsert, true)
	if o.Committed || o.Inserted != 6 || o.ErrorCount != 3 {
		t.Fatalf("%+v", o)
	}
	var numbers []int
	for _, e := range o.Errors {
		numbers = append(numbers, e.Row)
	}
	if !reflect.DeepEqual(numbers, []int{6, 7, 8}) || o.Errors[1].Line != 8 {
		t.Errorf("%v", numbers)
	}
	wantKind(t, o.Errors[0].Err, db.ErrHook)
	wantKind(t, o.Errors[1].Err, db.ErrValidation)
	// Nothing was kept: no documents, no child rows, no audit jobs.
	if count(t, d, "doc_parcel") != 0 || count(t, d, "fb_job") != 0 {
		t.Error("something was kept")
	}
}

// Rust: import_export.rs::a_dry_run_changes_nothing_not_even_jobs_or_webhook_deliveries
func TestADryRunChangesNothingNotEvenJobsOrWebhookDeliveries(t *testing.T) {
	d, reg := ieSetup(t)
	system := sys("t")
	if _, err := db.AddWebhook(bg, d.Pool, reg, db.NewWebhook{Name: "wh", DocType: "parcel", Events: []string{"insert"},
		URL: "https://example.com/hook"}, nil); err != nil {
		t.Fatal(err)
	}
	rows := []db.ImportRow{ieRow(1, "a"), ieRow(2, "b"), ieRow(3, "hook-me")}
	dry := mustImport(t, d, reg, "parcel", system, rows, db.ImportInsert, false)
	if dry.Inserted != 3 || dry.ErrorCount != 0 || dry.Committed {
		t.Fatalf("%+v", dry)
	}
	if count(t, d, "doc_parcel") != 0 || count(t, d, "fb_job") != 0 {
		t.Error("a dry run kept something")
	}
	// The same file for real: the documents, the audit jobs (one per row) and one webhook delivery per row
	// exist, all from the one commit, and the hook ran.
	if real := mustImport(t, d, reg, "parcel", system, rows, db.ImportInsert, true); !real.Committed {
		t.Fatal("not committed")
	}
	if n := count(t, d, "doc_parcel"); n != 3 {
		t.Errorf("%d documents", n)
	}
	jobs := d.Rows("SELECT name || '/' || count(*) FROM fb_job GROUP BY name ORDER BY name")
	if !reflect.DeepEqual(jobs, [][]string{{"app.audit/3"}, {"forgebase.deliver_webhook/3"}}) {
		t.Errorf("%v", jobs)
	}
	if notes := d.Rows("SELECT coalesce(note, '-') FROM doc_parcel WHERE title = 'hook-me'"); !reflect.DeepEqual(notes, [][]string{{"hooked"}}) {
		t.Errorf("%v", notes)
	}
	// A failed import (any bad row) commits no webhook delivery either.
	before := count(t, d, "fb_job")
	failed := mustImport(t, d, reg, "parcel", system, []db.ImportRow{ieRow(1, "c"), ieRow(2, "reject")}, db.ImportInsert, true)
	if failed.ErrorCount != 1 || count(t, d, "fb_job") != before {
		t.Errorf("%d errors, %d jobs", failed.ErrorCount, count(t, d, "fb_job"))
	}
}

// Rust: import_export.rs::errors_are_capped_but_all_are_counted
func TestErrorsAreCappedButAllAreCounted(t *testing.T) {
	d, reg := ieSetup(t)
	var rows []db.ImportRow
	for n := 1; n <= 150; n++ {
		rows = append(rows, ieRow(n, "reject"))
	}
	o := mustImport(t, d, reg, "parcel", sys("t"), rows, db.ImportInsert, true)
	if o.ErrorCount != 150 || len(o.Errors) != db.MaxReportedErrors || o.Errors[99].Row != 100 {
		t.Errorf("%d errors, %d kept", o.ErrorCount, len(o.Errors))
	}
}

func ieLevels(d *dbtest.DB) [][]string {
	return d.Rows("SELECT name || '=' || coalesce(level::text, 'null') FROM doc_badge ORDER BY name")
}

// Rust: import_export.rs::upsert_updates_what_exists_inserts_what_does_not_and_sets_empty_cells_to_null
func TestUpsertUpdatesWhatExistsInsertsWhatDoesNotAndSetsEmptyCellsToNull(t *testing.T) {
	d, reg := ieSetup(t)
	system := sys("t")
	seed := func(code string, level int64) {
		mustInsert(t, d, reg, "badge", core.Document{Name: code, Values: pairs("code", text(code), "level", core.Int(level))}, sys("seed"))
	}
	seed("A", 1)
	seed("B", 2)
	empty := ieNamed(2, "B")
	empty.Empty = []string{"level"}
	rows := []db.ImportRow{
		// Exists: the level changes; the naming field repeats the name and is left out.
		ieNamed(1, "A", "code", text("A"), "level", core.Int(10)),
		// Exists, an empty `level` cell: set to null.
		empty,
		// Does not exist: inserted with that name.
		ieNamed(3, "C", "code", text("C"), "level", core.Int(3)),
		// The row's naming field names another document: a rename, refused for this row.
		ieNamed(4, "A", "code", text("Z")),
		// No name in an upsert.
		ieRow(5, "x"),
	}
	o := mustImport(t, d, reg, "badge", system, rows, db.ImportUpsert, true)
	if o.Updated != 2 || o.Inserted != 1 || o.ErrorCount != 2 {
		t.Fatalf("%+v", o)
	}
	if o.Errors[0].Row != 4 || o.Errors[1].Row != 5 {
		t.Errorf("%v", o.Errors)
	}
	for _, e := range o.Errors {
		if k := wantKind0(e.Err); k != db.ErrNaming && k != db.ErrValidation {
			t.Errorf("row %d: %v", e.Row, e.Err)
		}
	}
	// Rows 4 and 5 failed, so nothing was kept.
	if o.Committed {
		t.Error("committed")
	}
	if got := ieLevels(d); !reflect.DeepEqual(got, [][]string{{"A=1"}, {"B=2"}}) {
		t.Errorf("%v", got)
	}
	// Without the bad rows it commits.
	o = mustImport(t, d, reg, "badge", system, rows[:3], db.ImportUpsert, true)
	if !o.Committed {
		t.Fatal("not committed")
	}
	if got := ieLevels(d); !reflect.DeepEqual(got, [][]string{{"A=10"}, {"B=null"}, {"C=3"}}) {
		t.Errorf("%v", got)
	}
}

func wantKind0(err error) db.ErrorKind {
	if e, ok := err.(*db.Error); ok {
		return e.Kind
	}
	return 0
}

// Rust: import_export.rs::insert_mode_leaves_empty_cells_out_and_refuses_a_name_where_naming_is_automatic
func TestInsertModeLeavesEmptyCellsOutAndRefusesANameWhereNamingIsAutomatic(t *testing.T) {
	d, reg := ieSetup(t)
	system := sys("t")
	plain := ieRow(1, "plain")
	plain.Empty = []string{"note", "qty"}
	if o := mustImport(t, d, reg, "parcel", system, []db.ImportRow{plain}, db.ImportInsert, true); !o.Committed {
		t.Fatal("not committed")
	}
	stored := d.Rows("SELECT coalesce(note, 'null') || '/' || coalesce(qty::text, 'null') FROM doc_parcel")
	if !reflect.DeepEqual(stored, [][]string{{"null/null"}}) {
		t.Errorf("%v", stored)
	}
	// Random naming: a given name is a row error.
	o := mustImport(t, d, reg, "parcel", system, []db.ImportRow{ieNamed(1, "mine", "title", text("t"))}, db.ImportInsert, true)
	wantKind(t, o.Errors[0].Err, db.ErrNaming)
	if o.Errors[0].Name != "mine" {
		t.Errorf("%q", o.Errors[0].Name)
	}
	// A naming-field DocType takes the given name when it is the field's value.
	if o := mustImport(t, d, reg, "badge", system, []db.ImportRow{ieNamed(1, "K1", "code", text("K1"))}, db.ImportInsert, true); !o.Committed {
		t.Fatal("not committed")
	}
	o = mustImport(t, d, reg, "badge", system, []db.ImportRow{ieNamed(1, "K2", "code", text("other"))}, db.ImportInsert, true)
	wantKind(t, o.Errors[0].Err, db.ErrNaming)
}

// Rust: import_export.rs::an_import_with_child_rows_round_trips_through_the_export
func TestAnImportWithChildRowsRoundTripsThroughTheExport(t *testing.T) {
	d, reg := ieSetup(t)
	system := sys("t")
	ieAddParcel(t, d, reg, system, "one")
	ieAddParcel(t, d, reg, system, "two")
	first, err := ieExportAll(t, d, reg, system, nil, []db.Sort{db.Asc("title")}, 100)
	if err != nil {
		t.Fatal(err)
	}
	// Import the exported documents again (insert, new names) and compare values and rows.
	var rows []db.ImportRow
	for i, doc := range first.docs {
		rows = append(rows, db.ImportRow{Row: i + 1, Values: doc.Values, Tables: doc.Tables})
	}
	if o := mustImport(t, d, reg, "parcel", system, rows, db.ImportInsert, true); !o.Committed {
		t.Fatalf("%+v", o.Errors)
	}
	again, err := ieExportAll(t, d, reg, system, nil, []db.Sort{db.Asc("title"), db.Asc("name")}, 100)
	if err != nil || again.count != 4 {
		t.Fatalf("%v %d", err, again.count)
	}
	for _, original := range first.docs {
		var copies []*db.StoredDocument
		for _, c := range again.docs {
			if c.Values["title"] == original.Values["title"] {
				copies = append(copies, c)
			}
		}
		if len(copies) != 2 {
			t.Fatalf("%d copies", len(copies))
		}
		for _, c := range copies {
			if !reflect.DeepEqual(c.Values, original.Values) || !reflect.DeepEqual(c.Tables, original.Tables) {
				t.Errorf("%v != %v", c, original)
			}
		}
	}
	// The documents of the first export are untouched.
	if still := mustGet(t, d, reg, "parcel", first.docs[0].Name, system); still.Version != 1 {
		t.Errorf("version %d", still.Version)
	}
}

// Rust: import_export.rs::an_upsert_replaces_the_child_rows_it_is_given_and_keeps_the_others
func TestAnUpsertReplacesTheChildRowsItIsGivenAndKeepsTheOthers(t *testing.T) {
	d, reg := ieSetup(t)
	system := sys("t")
	// `parcel` is randomly named, so use the stored name of an existing document.
	stored := ieAddParcel(t, d, reg, system, "keep")
	newRows := []core.Row{row("item", text("only"))}
	rows := []db.ImportRow{{Row: 1, Name: stored.Name, Values: pairs("qty", core.Int(4)), Tables: map[string][]core.Row{"lines": newRows}}}
	if o := mustImport(t, d, reg, "parcel", system, rows, db.ImportUpsert, true); o.Updated != 1 {
		t.Fatalf("%+v", o.Errors)
	}
	after := mustGet(t, d, reg, "parcel", stored.Name, system)
	if !reflect.DeepEqual(after.Tables["lines"], newRows) || after.Values["qty"] != core.Int(4) ||
		after.Values["title"] != text("keep") || after.Version != 2 {
		t.Errorf("%+v", after)
	}
	// An upsert without the table leaves the rows alone.
	rows = []db.ImportRow{{Row: 1, Name: stored.Name, Values: pairs("qty", core.Int(5))}}
	mustImport(t, d, reg, "parcel", system, rows, db.ImportUpsert, true)
	after = mustGet(t, d, reg, "parcel", stored.Name, system)
	if !reflect.DeepEqual(after.Tables["lines"], newRows) {
		t.Errorf("%v", after.Tables)
	}
}

// A caller under an if_owner read does not see a document of somebody else: an upsert of its name is an
// insert, which collides (a row error, as POST /resource gives a unique violation); the other user's
// document is not changed.
//
// Rust: import_export.rs::an_upsert_of_a_document_the_caller_cannot_read_changes_nothing
func TestAnUpsertOfADocumentTheCallerCannotReadChangesNothing(t *testing.T) {
	d, reg := ieSetup(t)
	mustInsert(t, d, reg, "badge", core.Document{Name: "A", Values: pairs("code", text("A"), "level", core.Int(1))}, sys("alice"))
	rows := []db.ImportRow{ieNamed(1, "A", "code", text("A"), "level", core.Int(9))}
	o := mustImport(t, d, reg, "badge", user("bob", "Own Upserter"), rows, db.ImportUpsert, true)
	if o.Updated != 0 || o.Inserted != 0 || o.ErrorCount != 1 || o.Committed {
		t.Fatalf("%+v", o)
	}
	wantKind(t, o.Errors[0].Err, db.ErrUniqueViolation)
	if got := d.Rows("SELECT name || '=' || level::text || '/' || owner FROM doc_badge"); !reflect.DeepEqual(got, [][]string{{"A=1/alice"}}) {
		t.Errorf("%v", got)
	}
}
