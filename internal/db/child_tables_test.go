package db_test

import (
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"errors"
	"github.com/tvstore/forgebase-go/internal/core"
	"github.com/tvstore/forgebase-go/internal/db"
	"github.com/tvstore/forgebase-go/internal/db/dbtest"
)

// Child tables (Table fields): schema sync and document CRUD against a real PostgreSQL. Ported from
// crates/forgebase-db/tests/child_tables.rs.

const invoiceItem = `
name = "invoice_item"
label = "Invoice Item"
is_child = true
[[fields]]
name = "qty"
type = "int"
required = true
[[fields]]
name = "rate"
type = "decimal"
precision = 10
scale = 2
[[fields]]
name = "description"
type = "data"
max_length = 50
[[fields]]
name = "supplier"
type = "link"
target = "person"
`

func invoiceTOML(required bool) string {
	return fmt.Sprintf("name = \"invoice\"\nlabel = \"Invoice\"\n"+
		"[[fields]]\nname = \"title\"\ntype = \"data\"\n"+
		"[[fields]]\nname = \"items\"\ntype = \"table\"\ntarget = \"invoice_item\"\nrequired = %t\n", required)
}

func invoiceRegistry(t *testing.T, required bool) *core.Registry {
	return registry(t, dbtest.Person, invoiceTOML(required), invoiceItem)
}

func childSetup(t *testing.T) (*dbtest.DB, *core.Registry) {
	t.Helper()
	d := dbtest.Migrated(t)
	reg := invoiceRegistry(t, false)
	mustSync(t, d, reg)
	return d, reg
}

func item(qty int64, description string) core.Row {
	return row("qty", core.Int(qty), "description", text(description))
}

// invoice has a title and the rows of its items table.
func invoice(title string, rows ...core.Row) core.Document {
	d := doc("title", text(title))
	if rows == nil {
		rows = []core.Row{}
	}
	d.Tables["items"] = rows
	return d
}

func rowsChange(rows ...core.Row) core.Changes {
	if rows == nil {
		rows = []core.Row{}
	}
	return core.Changes{Tables: map[string][]core.Row{"items": rows}}
}

func descriptions(t *testing.T, rows []core.Row) []string {
	t.Helper()
	out := []string{}
	for _, r := range rows {
		if r["description"].Kind != core.KindText {
			t.Fatalf("%v", r)
		}
		out = append(out, r["description"].Text)
	}
	return out
}

// Rust: child_tables.rs::parent_and_child_tables_are_created_in_one_sync
func TestParentAndChildTablesAreCreatedInOneSync(t *testing.T) {
	d := dbtest.Migrated(t)
	reg := invoiceRegistry(t, false)
	report := mustSync(t, d, reg)
	if !slices.ContainsFunc(report.Steps, func(s db.Step) bool { return s.Kind == db.StepCreateTable && s.DocType == "invoice_item" }) {
		t.Error("no CREATE TABLE for the child")
	}
	// The child has the child standard columns and no name, owner, version or timestamps.
	want(t, d.Columns("doc_invoice_item"), []dbtest.Column{
		{"id", "uuid/NO"}, {"parent_id", "uuid/NO"}, {"idx", "integer/NO"}, {"qty", "bigint/YES"},
		{"rate", "numeric(10,2)/YES"}, {"description", "character varying(50)/YES"}, {"supplier", "uuid/YES"},
	}, "child columns")
	// The Table field is not a column of the parent.
	parent := d.Columns("doc_invoice")
	if !columnNamed(parent, "title") || columnNamed(parent, "items") {
		t.Errorf("%v", parent)
	}
	want(t, foreignKeys(d, "doc_invoice_item"), []string{
		"FOREIGN KEY (parent_id) REFERENCES doc_invoice(id) ON DELETE CASCADE",
		"FOREIGN KEY (supplier) REFERENCES doc_person(id) ON DELETE RESTRICT",
	}, "foreign keys")
	want(t, indexedColumns(d, "doc_invoice_item"), []string{"id (unique)", "parent_id (plain)", "supplier (plain)"}, "indexes")
	want(t, d.Rows("SELECT name::text FROM fb_doctype ORDER BY 1"), [][]string{{"invoice"}, {"invoice_item"}, {"person"}}, "registered")
	// A second sync has nothing to do.
	emptyReport(t, mustSync(t, d, reg), "second sync")
}

// Rust: child_tables.rs::a_table_field_and_its_child_can_be_added_later
func TestATableFieldAndItsChildCanBeAddedLater(t *testing.T) {
	d := dbtest.Migrated(t)
	plain := "name = \"invoice\"\nlabel = \"Invoice\"\n[[fields]]\nname = \"title\"\ntype = \"data\"\n"
	mustSync(t, d, registry(t, dbtest.Person, plain))
	report := mustSync(t, d, invoiceRegistry(t, false))
	// The parent table is untouched: no column for the Table field.
	want(t, kinds(report.Steps), []db.StepKind{db.StepCreateTable, db.StepAddKeys, db.StepAddForeignKeys,
		db.StepCreateIndex, db.StepCreateIndex, db.StepRegisterDocType}, "steps")
	if got := foreignKeys(d, "doc_invoice_item")[0]; got != "FOREIGN KEY (parent_id) REFERENCES doc_invoice(id) ON DELETE CASCADE" {
		t.Error(got)
	}
}

// Rust: child_tables.rs::a_dropped_or_changed_parent_key_is_a_conflict
func TestADroppedOrChangedParentKeyIsAConflict(t *testing.T) {
	d := dbtest.Migrated(t)
	reg := invoiceRegistry(t, false)
	mustSync(t, d, reg)
	constraint := d.Rows("SELECT conname::text FROM pg_constraint WHERE conrelid = 'doc_invoice_item'::regclass " +
		"AND contype = 'f' AND pg_get_constraintdef(oid) LIKE 'FOREIGN KEY (parent_id)%'")[0][0]

	// ON DELETE changed from CASCADE to RESTRICT.
	d.Exec("ALTER TABLE doc_invoice_item DROP CONSTRAINT " + constraint + "; " +
		"ALTER TABLE doc_invoice_item ADD FOREIGN KEY (parent_id) REFERENCES doc_invoice (id) ON DELETE RESTRICT")
	_, err := d.Sync(reg)
	got := conflictTexts(t, asDbError(t, err))
	if len(got) != 1 || !strings.Contains(got[0], "doc_invoice_item.parent_id") ||
		!strings.Contains(got[0], "ON DELETE RESTRICT") || !strings.Contains(got[0], "ON DELETE CASCADE") {
		t.Errorf("%v", got)
	}
	// The key is gone.
	d.Exec("ALTER TABLE doc_invoice_item DROP CONSTRAINT doc_invoice_item_parent_id_fkey")
	_, err = d.Sync(reg)
	got = conflictTexts(t, asDbError(t, err))
	if len(got) != 1 || !strings.Contains(got[0], "is missing in the database") {
		t.Errorf("%v", got)
	}
	// Pointing at the wrong table.
	d.Exec("ALTER TABLE doc_invoice_item ADD FOREIGN KEY (parent_id) REFERENCES doc_person (id) ON DELETE CASCADE")
	_, err = d.Sync(reg)
	got = conflictTexts(t, asDbError(t, err))
	if len(got) != 1 || !strings.Contains(got[0], "doc_person") {
		t.Errorf("%v", got)
	}
}

// Rust: child_tables.rs::a_missing_parent_index_is_added
func TestAMissingParentIndexIsAdded(t *testing.T) {
	d := dbtest.Migrated(t)
	reg := invoiceRegistry(t, false)
	mustSync(t, d, reg)
	d.Exec("DROP INDEX doc_invoice_item_parent_id_idx")
	report := mustSync(t, d, reg)
	if len(report.Steps) != 1 || report.Steps[0].Kind != db.StepCreateIndex || report.Steps[0].Column != "parent_id" {
		t.Errorf("%v", texts(report.Steps))
	}
	want(t, indexedColumns(d, "doc_invoice_item"), []string{"id (unique)", "parent_id (plain)", "supplier (plain)"}, "indexes")
}

// Rust: child_tables.rs::a_missing_child_standard_column_is_a_conflict
func TestAMissingChildStandardColumnIsAConflict(t *testing.T) {
	d := dbtest.Migrated(t)
	reg := invoiceRegistry(t, false)
	mustSync(t, d, reg)
	d.Exec("ALTER TABLE doc_invoice_item ALTER COLUMN idx TYPE bigint")
	_, err := d.Sync(reg)
	got := conflictTexts(t, asDbError(t, err))
	if len(got) != 1 || !strings.Contains(got[0], "doc_invoice_item.idx is bigint NOT NULL") ||
		!strings.Contains(got[0], "requires integer NOT NULL") {
		t.Errorf("%v", got)
	}
}

// Rust: child_tables.rs::a_leftover_name_column_of_a_child_is_only_a_warning
func TestALeftoverNameColumnOfAChildIsOnlyAWarning(t *testing.T) {
	d := dbtest.Migrated(t)
	reg := invoiceRegistry(t, false)
	mustSync(t, d, reg)
	d.Exec("ALTER TABLE doc_invoice_item ADD COLUMN name text")
	report := mustSync(t, d, reg)
	if len(report.Steps) != 0 {
		t.Error(texts(report.Steps))
	}
	want(t, texts(report.Warnings), []string{"column doc_invoice_item.name is not in the metadata; kept"}, "warnings")
}

// Rust: child_tables.rs::switching_a_doctype_between_child_and_not_child_is_a_conflict
func TestSwitchingADoctypeBetweenChildAndNotChildIsAConflict(t *testing.T) {
	d := dbtest.Migrated(t)
	// invoice_item first exists as a normal DocType ...
	plainItem := strings.Replace(invoiceItem, "is_child = true\n", "", 1)
	plainInvoice := "name = \"invoice\"\nlabel = \"Invoice\"\n[[fields]]\nname = \"title\"\ntype = \"data\"\n"
	mustSync(t, d, registry(t, dbtest.Person, plainInvoice, plainItem))
	// ... and is then declared a child: refused, nothing applied.
	_, err := d.Sync(invoiceRegistry(t, false))
	got := conflictTexts(t, asDbError(t, err))
	if len(got) != 2 || !strings.Contains(got[0], "doc_invoice_item.parent_id is missing") ||
		!strings.Contains(got[1], "doc_invoice_item.idx is missing") {
		t.Errorf("%v", got)
	}
	if n := len(d.Columns("doc_invoice_item")); n != 7+4 {
		t.Errorf("the table is unchanged: %d columns", n)
	}
	_, err = d.Sync(invoiceRegistry(t, false))
	if e := wantKind(t, err, db.ErrSyncConflicts); e.Conflicts[0].Kind != db.ConflictColumnMismatch {
		t.Errorf("%v", e.Conflicts)
	}

	// The other way round: a child table that is declared a normal DocType.
	d = dbtest.Migrated(t)
	mustSync(t, d, invoiceRegistry(t, false))
	_, err = d.Sync(registry(t, dbtest.Person, plainInvoice, plainItem))
	got = conflictTexts(t, asDbError(t, err))
	if !slices.ContainsFunc(got, func(s string) bool { return strings.Contains(s, "doc_invoice_item.name is missing") }) {
		t.Errorf("%v", got)
	}
}

// Rust: child_tables.rs::an_unused_child_is_refused_before_the_database_is_touched
func TestAnUnusedChildIsRefusedBeforeTheDatabaseIsTouched(t *testing.T) {
	d := dbtest.Migrated(t)
	_, err := db.Sync(bg, d.Pool, registry(t, dbtest.Person, invoiceItem), db.DefaultSyncOptions())
	e := wantKind(t, err, db.ErrInvalidMetadata)
	if len(e.Problems) != 1 {
		t.Error(e.Problems)
	}
	want(t, err.Error(), "invalid DocType metadata: child doctype `invoice_item` is not used by any table field", "text")
	want(t, d.Rows("SELECT count(*)::text FROM fb_doctype")[0][0], "0", "count")
}

// Rust: child_tables.rs::dry_run_shows_the_sql_of_a_parent_and_its_child
func TestDryRunShowsTheSQLOfAParentAndItsChild(t *testing.T) {
	d := dbtest.Migrated(t)
	report, err := db.Sync(bg, d.Pool, invoiceRegistry(t, false), db.SyncOptions{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	steps := texts(report.Steps)
	for _, expected := range []string{
		"CREATE TABLE \"doc_invoice_item\" (\n    \"id\" uuid NOT NULL,\n    \"parent_id\" uuid NOT NULL,\n    " +
			"\"idx\" integer NOT NULL,\n    \"qty\" bigint,\n    \"rate\" numeric(10,2),\n    " +
			"\"description\" character varying(50),\n    \"supplier\" uuid\n);",
		"ALTER TABLE \"doc_invoice_item\" ADD FOREIGN KEY (\"parent_id\") REFERENCES \"doc_invoice\" (\"id\") ON DELETE CASCADE, " +
			"ADD FOREIGN KEY (\"supplier\") REFERENCES \"doc_person\" (\"id\") ON DELETE RESTRICT;",
		"CREATE INDEX ON \"doc_invoice_item\" (\"parent_id\");",
	} {
		if !slices.Contains(steps, expected) {
			t.Errorf("missing %s in %q", expected, steps)
		}
	}
	want(t, d.Rows("SELECT coalesce(to_regclass('doc_invoice_item')::text, 'none')")[0][0], "none", "dry run table")
}

// Rust: child_tables.rs::insert_with_rows_and_get_return_the_rows_in_order
func TestInsertWithRowsAndGetReturnTheRowsInOrder(t *testing.T) {
	d, reg := childSetup(t)
	mustInsert(t, d, reg, "person", personDoc("sam@x.y"), sys("cli"))
	second := item(2, "second")
	second["rate"] = dec("12.50")
	second["supplier"] = text("sam@x.y")
	// The third row omits every optional field.
	third := row("qty", core.Int(3))
	created := mustInsert(t, d, reg, "invoice", invoice("INV-1", item(1, "first"), second, third), sys("cli"))

	if created.Version != 1 || created.Values["title"].Text != "INV-1" {
		t.Error(describeDoc(created))
	}
	if _, ok := created.Values["items"]; ok {
		t.Error("the table field is a value")
	}
	items := created.Tables["items"]
	if len(items) != 3 {
		t.Fatal(describeDoc(created))
	}
	want(t, descriptions(t, items[:2]), []string{"first", "second"}, "descriptions")
	if items[1]["rate"].Dec.String() != "12.50" {
		t.Error(items[1]["rate"])
	}
	// Links come back as names; absent fields are null; every row has every field.
	if items[1]["supplier"].Text != "sam@x.y" || items[0]["supplier"].Kind != core.KindNull ||
		items[2]["description"].Kind != core.KindNull || items[2]["qty"].Int != 3 {
		t.Error(describeDoc(created))
	}
	for _, r := range items {
		if len(r) != 4 {
			t.Errorf("%v", r)
		}
	}
	sameDocument(t, mustGet(t, d, reg, "invoice", created.Name, sys("cli")), created, "fetched")

	// Stored as: row ids (UUID v7, all different), the parent's id, idx 1, 2, 3.
	want(t, d.Rows("SELECT i.idx::text, (i.parent_id = p.id)::text, (i.id <> p.id)::text "+
		"FROM doc_invoice_item i, doc_invoice p ORDER BY i.idx"),
		[][]string{{"1", "true", "true"}, {"2", "true", "true"}, {"3", "true", "true"}}, "stored")
	want(t, d.Rows("SELECT count(DISTINCT id)::text FROM doc_invoice_item")[0][0], "3", "distinct ids")
}

// Rust: child_tables.rs::a_document_without_rows_has_an_empty_list
func TestADocumentWithoutRowsHasAnEmptyList(t *testing.T) {
	d, reg := childSetup(t)
	document := invoice("empty")
	document.Tables = nil
	created := mustInsert(t, d, reg, "invoice", document, sys("cli"))
	if rows, ok := created.Tables["items"]; !ok || len(rows) != 0 {
		t.Errorf("%v", created.Tables)
	}
	fetched := mustGet(t, d, reg, "invoice", created.Name, sys("cli"))
	if rows, ok := fetched.Tables["items"]; !ok || len(rows) != 0 {
		t.Errorf("%v", fetched.Tables)
	}
	if got, err := db.Get(bg, d.Pool, reg, "invoice", "nothing", sys("cli")); err != nil || got != nil {
		t.Errorf("%v %v", got, err)
	}
}

func inRow(row int, e core.ValidationError) core.ValidationError {
	return core.ValidationError{Kind: core.ErrInRow, Field: "items", Row: row, Inner: &e}
}

// Rust: child_tables.rs::row_validation_errors_carry_positions_and_write_nothing
func TestRowValidationErrorsCarryPositionsAndWriteNothing(t *testing.T) {
	d, reg := childSetup(t)
	mustInsert(t, d, reg, "person", personDoc("sam@x.y"), sys("cli"))
	linked := item(2, "b")
	linked["supplier"] = text("nobody@x.y")
	badValue := item(3, "c")
	badValue["qty"] = text("three")
	_, err := db.Insert(bg, d.Pool, reg, "invoice",
		invoice("X", item(1, "a"), linked, badValue, row("description", text("no qty"))), sys("cli"))
	errs := validationOf(t, err)
	for _, expected := range []core.ValidationError{
		inRow(2, core.ValidationError{Kind: core.ErrLinkNotFound, Field: "supplier", Target: "person", Name: "nobody@x.y"}),
		inRow(3, core.ValidationError{Kind: core.ErrTypeMismatch, Field: "qty", Expected: "int", Found: "text"}),
		inRow(4, core.ValidationError{Kind: core.ErrRequired, Field: "qty"}),
	} {
		if !hasProblem(errs, expected) {
			t.Errorf("missing %v in %v", expected, errs)
		}
	}
	if len(errs) != 3 {
		t.Errorf("%v", errs)
	}
	if !strings.Contains(err.Error(), "field `items` row 2: field `supplier`: person `nobody@x.y` does not exist") {
		t.Error(err)
	}
	if count(t, d, "doc_invoice") != 0 || count(t, d, "doc_invoice_item") != 0 {
		t.Error("something was written")
	}
}

// Rust: child_tables.rs::a_required_table_needs_rows
func TestARequiredTableNeedsRows(t *testing.T) {
	d := dbtest.Migrated(t)
	reg := invoiceRegistry(t, true)
	mustSync(t, d, reg)
	absent := invoice("absent")
	absent.Tables = nil
	for _, document := range []core.Document{invoice("none"), absent} {
		_, err := db.Insert(bg, d.Pool, reg, "invoice", document, sys("cli"))
		want(t, validationOf(t, err), []core.ValidationError{{Kind: core.ErrRequired, Field: "items"}}, "errors")
	}
	if count(t, d, "doc_invoice") != 0 {
		t.Error("written")
	}
	created := mustInsert(t, d, reg, "invoice", invoice("ok", item(1, "a")), sys("cli"))
	// An update may not remove the last rows either.
	_, err := db.Update(bg, d.Pool, reg, "invoice", created.Name, 1, rowsChange(), sys("cli"))
	wantKind(t, err, db.ErrValidation)
	if count(t, d, "doc_invoice_item") != 1 {
		t.Error("rows removed")
	}
}

// Rust: child_tables.rs::unknown_table_and_misused_table_values_are_validation_errors
func TestUnknownTableAndMisusedTableValuesAreValidationErrors(t *testing.T) {
	d, reg := childSetup(t)
	document := invoice("x")
	document.Tables["ghosts"] = []core.Row{}
	document.Values["items"] = core.Int(1)
	document.Tables["title"] = []core.Row{item(1, "a")}
	_, err := db.Insert(bg, d.Pool, reg, "invoice", document, sys("cli"))
	errs := validationOf(t, err)
	if len(errs) != 3 ||
		!hasProblem(errs, core.ValidationError{Kind: core.ErrUnknownField, Field: "ghosts"}) ||
		!hasProblem(errs, core.ValidationError{Kind: core.ErrTypeMismatch, Field: "items", Expected: "rows", Found: "int"}) {
		t.Errorf("%v", errs)
	}
	if count(t, d, "doc_invoice") != 0 {
		t.Error("written")
	}
}

// Rust: child_tables.rs::update_replaces_rows_and_keeps_them_when_not_mentioned
func TestUpdateReplacesRowsAndKeepsThemWhenNotMentioned(t *testing.T) {
	d, reg := childSetup(t)
	created := mustInsert(t, d, reg, "invoice", invoice("INV", item(1, "a"), item(2, "b"), item(3, "c")), sys("alice"))
	oldIDs := d.Rows("SELECT id::text FROM doc_invoice_item ORDER BY idx")

	// Replace with two rows in another order: count and order follow the new list.
	v2 := mustUpdate(t, d, reg, "invoice", created.Name, 1, rowsChange(item(9, "z"), item(8, "y")), sys("bob"))
	if v2.Version != 2 || v2.ModifiedBy != "bob" || v2.Values["title"].Text != "INV" {
		t.Error(describeDoc(v2))
	}
	want(t, descriptions(t, v2.Tables["items"]), []string{"z", "y"}, "descriptions")
	if count(t, d, "doc_invoice_item") != 2 {
		t.Error("count")
	}
	// The rows differ, so the old rows were deleted and new ones inserted (new ids).
	newIDs := d.Rows("SELECT id::text FROM doc_invoice_item ORDER BY idx")
	for _, id := range newIDs {
		if slices.ContainsFunc(oldIDs, func(o []string) bool { return o[0] == id[0] }) {
			t.Errorf("row id %s survived", id[0])
		}
	}
	want(t, d.Rows("SELECT idx::text FROM doc_invoice_item ORDER BY idx"), [][]string{{"1"}, {"2"}}, "idx")

	// A values-only update leaves the rows alone.
	v3 := mustUpdate(t, d, reg, "invoice", created.Name, 2, changes("title", text("INV-2")), sys("bob"))
	if v3.Version != 3 {
		t.Error(v3.Version)
	}
	want(t, describeDoc(&db.StoredDocument{Tables: v3.Tables}), describeDoc(&db.StoredDocument{Tables: v2.Tables}), "tables")
	want(t, d.Rows("SELECT id::text FROM doc_invoice_item ORDER BY idx"), newIDs, "untouched rows keep their ids")

	// Values and rows in one update.
	both := core.Changes{Values: pairs("title", text("INV-3")), Tables: map[string][]core.Row{"items": {item(5, "only")}}}
	v4 := mustUpdate(t, d, reg, "invoice", created.Name, 3, both, sys("bob"))
	if v4.Values["title"].Text != "INV-3" {
		t.Error(describeDoc(v4))
	}
	want(t, descriptions(t, v4.Tables["items"]), []string{"only"}, "descriptions")

	// An empty list removes every row, and an update of the table alone is a valid change.
	v5 := mustUpdate(t, d, reg, "invoice", created.Name, 4, rowsChange(), sys("bob"))
	if rows, ok := v5.Tables["items"]; v5.Version != 5 || !ok || len(rows) != 0 || count(t, d, "doc_invoice_item") != 0 {
		t.Error(describeDoc(v5))
	}
	// Nothing at all to change is still an error.
	_, err := db.Update(bg, d.Pool, reg, "invoice", created.Name, 5, core.Changes{}, sys("bob"))
	wantKind(t, err, db.ErrNoChanges)
}

// Rust: child_tables.rs::update_with_a_stale_version_or_bad_rows_leaves_the_rows_alone
func TestUpdateWithAStaleVersionOrBadRowsLeavesTheRowsAlone(t *testing.T) {
	d, reg := childSetup(t)
	created := mustInsert(t, d, reg, "invoice", invoice("INV", item(1, "keep")), sys("cli"))
	_, err := db.Update(bg, d.Pool, reg, "invoice", created.Name, 7, rowsChange(item(2, "lost")), sys("cli"))
	if e := wantKind(t, err, db.ErrConflict); e.ExpectedVersion != 7 || e.CurrentVersion != 1 {
		t.Errorf("%+v", e)
	}
	_, err = db.Update(bg, d.Pool, reg, "invoice", created.Name, 1, rowsChange(item(2, "ok"), row()), sys("cli"))
	wantKind(t, err, db.ErrValidation)
	_, err = db.Update(bg, d.Pool, reg, "invoice", "nothing", 1, rowsChange(item(2, "ok")), sys("cli"))
	wantKind(t, err, db.ErrNotFound)
	sameDocument(t, mustGet(t, d, reg, "invoice", created.Name, sys("cli")), created, "unchanged")
}

// Rust: child_tables.rs::delete_removes_the_rows_with_the_document
func TestDeleteRemovesTheRowsWithTheDocument(t *testing.T) {
	d, reg := childSetup(t)
	a := mustInsert(t, d, reg, "invoice", invoice("A", item(1, "a1"), item(2, "a2")), sys("cli"))
	mustInsert(t, d, reg, "invoice", invoice("B", item(1, "b1")), sys("cli"))
	if count(t, d, "doc_invoice_item") != 3 {
		t.Error("count")
	}
	// A stale delete removes nothing.
	if db.Delete(bg, d.Pool, reg, "invoice", a.Name, 9, sys("ann")) == nil {
		t.Error("a stale delete worked")
	}
	if count(t, d, "doc_invoice_item") != 3 {
		t.Error("count")
	}
	if err := db.Delete(bg, d.Pool, reg, "invoice", a.Name, 1, sys("ann")); err != nil {
		t.Fatal(err)
	}
	if count(t, d, "doc_invoice") != 1 {
		t.Error("count")
	}
	want(t, d.Rows("SELECT description::text FROM doc_invoice_item"), [][]string{{"b1"}}, "rows")
}

// Rust: child_tables.rs::a_person_linked_from_a_row_cannot_be_deleted_and_the_error_names_the_owner
func TestAPersonLinkedFromARowCannotBeDeletedAndTheErrorNamesTheOwner(t *testing.T) {
	d, reg := childSetup(t)
	sam := mustInsert(t, d, reg, "person", personDoc("sam@x.y"), sys("cli"))
	r := item(1, "a")
	r["supplier"] = text("sam@x.y")
	created := mustInsert(t, d, reg, "invoice", invoice("INV", r), sys("cli"))
	e := wantKind(t, db.Delete(bg, d.Pool, reg, "person", sam.Name, 1, sys("ann")), db.ErrLinkedDocumentExists)
	if e.ReferencedBy == nil || *e.ReferencedBy != "invoice" {
		t.Errorf("%+v", e)
	}
	// Once the invoice (and so its rows) is gone, the person can be deleted.
	if err := db.Delete(bg, d.Pool, reg, "invoice", created.Name, 1, sys("ann")); err != nil {
		t.Fatal(err)
	}
	if err := db.Delete(bg, d.Pool, reg, "person", sam.Name, 1, sys("ann")); err != nil {
		t.Fatal(err)
	}
}

// Rust: child_tables.rs::child_doctypes_have_no_crud_of_their_own
func TestChildDoctypesHaveNoCrudOfTheirOwn(t *testing.T) {
	d, reg := childSetup(t)
	isChild := func(err error) {
		t.Helper()
		if e := wantKind(t, err, db.ErrChildDocType); e.DocType != "invoice_item" || !strings.Contains(err.Error(), "child DocType") {
			t.Error(err)
		}
	}
	_, err := db.Insert(bg, d.Pool, reg, "invoice_item", doc("qty", core.Int(1)), sys("cli"))
	isChild(err)
	_, err = db.Get(bg, d.Pool, reg, "invoice_item", "x", sys("cli"))
	isChild(err)
	_, err = db.Update(bg, d.Pool, reg, "invoice_item", "x", 1, changes("qty", core.Int(1)), sys("cli"))
	isChild(err)
	isChild(db.Delete(bg, d.Pool, reg, "invoice_item", "x", 1, sys("ann")))
	_, err = db.Get(bg, d.Pool, reg, "nope", "x", sys("cli"))
	if e := wantKind(t, err, db.ErrUnknownDocType); e.Name != "nope" {
		t.Error(e)
	}
	if count(t, d, "doc_invoice_item") != 0 {
		t.Error("written")
	}
}

func pgCode(err error) string {
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		return pg.Code
	}
	return ""
}

// Rust: child_tables.rs::a_failing_row_write_leaves_no_parent_on_a_pooled_client
func TestAFailingRowWriteLeavesNoParentOnAPooledClient(t *testing.T) {
	d, reg := childSetup(t)
	// Rows with qty 13 pass validation but the database refuses them: the parent row is inserted
	// first, so only a transaction around both statements keeps it out.
	d.Exec("ALTER TABLE doc_invoice_item ADD CONSTRAINT no_thirteen CHECK (qty <> 13)")
	_, err := db.Insert(bg, d.Pool, reg, "invoice", invoice("INV", item(1, "fine"), item(13, "unlucky")), sys("cli"))
	wantKind(t, err, db.ErrPostgres)
	if pgCode(err) != "23514" {
		t.Errorf("%v", err)
	}
	if count(t, d, "doc_invoice") != 0 || count(t, d, "doc_invoice_item") != 0 {
		t.Error("something was written")
	}
	// The same for an update: the old rows and the old version stay.
	created := mustInsert(t, d, reg, "invoice", invoice("INV", item(1, "old")), sys("cli"))
	c := core.Changes{Values: pairs("title", text("changed")), Tables: map[string][]core.Row{"items": {item(2, "new"), item(13, "boom")}}}
	if _, err := db.Update(bg, d.Pool, reg, "invoice", created.Name, 1, c, sys("cli")); err == nil {
		t.Error("the update worked")
	}
	sameDocument(t, mustGet(t, d, reg, "invoice", created.Name, sys("cli")), created, "unchanged")
}

// Rust: child_tables.rs::operations_in_a_caller_transaction_roll_back_with_it_and_a_failure_does_not_abort_it
func TestOperationsInACallerTransactionRollBackWithItAndAFailureDoesNotAbortIt(t *testing.T) {
	d, reg := childSetup(t)
	d.Exec("ALTER TABLE doc_invoice_item ADD CONSTRAINT no_thirteen CHECK (qty <> 13)")
	tx, err := d.Pool.Begin(bg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Insert(bg, tx, reg, "invoice", invoice("kept", item(1, "a")), sys("cli")); err != nil {
		t.Fatal(err)
	}
	// This call fails on the database after its parent row was written ...
	if _, err := db.Insert(bg, tx, reg, "invoice", invoice("lost", item(13, "b")), sys("cli")); err == nil {
		t.Fatal("the insert worked")
	}
	// ... but only its own work is undone: the transaction is usable and still has the first.
	rows, err := tx.Query(bg, "SELECT title::text FROM doc_invoice ORDER BY 1")
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for rows.Next() {
		n++
	}
	rows.Close()
	if rows.Err() != nil || n != 1 {
		t.Errorf("%d %v", n, rows.Err())
	}
	if err := tx.Commit(bg); err != nil {
		t.Fatal(err)
	}
	if count(t, d, "doc_invoice") != 1 || count(t, d, "doc_invoice_item") != 1 {
		t.Error("count")
	}
	tx, err = d.Pool.Begin(bg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Insert(bg, tx, reg, "invoice", invoice("rolled back", item(2, "c")), sys("cli")); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(bg); err != nil {
		t.Fatal(err)
	}
	if count(t, d, "doc_invoice") != 1 || count(t, d, "doc_invoice_item") != 1 {
		t.Error("count")
	}
}

// Rust: child_tables.rs::a_unique_violation_in_the_parent_writes_no_rows
func TestAUniqueViolationInTheParentWritesNoRows(t *testing.T) {
	d := dbtest.Migrated(t)
	invoiceUnique := strings.Replace(invoiceTOML(false), "name = \"title\"\ntype = \"data\"\n",
		"name = \"title\"\ntype = \"data\"\nunique = true\n", 1)
	reg := registry(t, dbtest.Person, invoiceUnique, invoiceItem)
	mustSync(t, d, reg)
	mustInsert(t, d, reg, "invoice", invoice("same", item(1, "a")), sys("cli"))
	_, err := db.Insert(bg, d.Pool, reg, "invoice", invoice("same", item(2, "b")), sys("cli"))
	wantKind(t, err, db.ErrUniqueViolation)
	if count(t, d, "doc_invoice_item") != 1 {
		t.Error("rows of the failed insert were written")
	}
}

// Rust: child_tables.rs::a_read_never_mixes_a_document_with_the_rows_of_another_version
func TestAReadNeverMixesADocumentWithTheRowsOfAnotherVersion(t *testing.T) {
	d, reg := childSetup(t)
	created := mustInsert(t, d, reg, "invoice", invoice("INV", item(1, "v1")), sys("cli"))
	name := created.Name
	// Every version of the document has exactly one row, "v<version>": a reader that sees version N
	// together with another version's row has read two states.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for version := int64(1); version <= 40; version++ {
			if _, err := db.Update(bg, d.Pool, reg, "invoice", name, version,
				rowsChange(item(1, fmt.Sprintf("v%d", version+1))), sys("cli")); err != nil {
				t.Errorf("update %d: %v", version, err)
				return
			}
		}
	}()
	deadline := time.Now().Add(20 * time.Second)
	seen := int64(0)
	for seen < 40 && time.Now().Before(deadline) {
		got, err := db.Get(bg, d.Pool, reg, "invoice", name, sys("cli"))
		var e *db.Error
		switch {
		case errors.As(err, &e) && e.Kind == db.ErrChangedWhileReading:
			// Allowed when a writer keeps winning the race; the data is never mixed.
		case err != nil:
			t.Fatal(err)
		case got == nil:
			t.Fatal("the document disappeared")
		default:
			rows := got.Tables["items"]
			if len(rows) != 1 || descriptions(t, rows)[0] != fmt.Sprintf("v%d", got.Version) {
				t.Fatalf("version %d with another version's rows: %v", got.Version, descriptions(t, rows))
			}
			seen = got.Version
		}
	}
	wg.Wait()
}

// Rust: child_tables.rs::concurrent_row_replacements_with_one_version_have_one_winner
func TestConcurrentRowReplacementsWithOneVersionHaveOneWinner(t *testing.T) {
	d, reg := childSetup(t)
	created := mustInsert(t, d, reg, "invoice", invoice("INV", item(1, "start")), sys("cli"))
	one := rowsChange(item(1, "one-a"), item(2, "one-b"))
	two := rowsChange(item(3, "two-a"))
	type result struct {
		doc *db.StoredDocument
		err error
	}
	var ra, rb result
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		ra.doc, ra.err = db.Update(bg, d.Pool, reg, "invoice", created.Name, 1, one, sys("a"))
	}()
	go func() {
		defer wg.Done()
		rb.doc, rb.err = db.Update(bg, d.Pool, reg, "invoice", created.Name, 1, two, sys("b"))
	}()
	wg.Wait()
	winner, loser := ra, rb
	if ra.err != nil {
		winner, loser = rb, ra
	}
	if winner.err != nil || loser.err == nil {
		t.Fatalf("exactly one update should win: %v / %v", ra.err, rb.err)
	}
	if e := wantKind(t, loser.err, db.ErrConflict); e.ExpectedVersion != 1 || e.CurrentVersion != 2 {
		t.Errorf("%+v", e)
	}
	fetched := mustGet(t, d, reg, "invoice", created.Name, sys("cli"))
	sameDocument(t, fetched, winner.doc, "winner")
	if int64(len(fetched.Tables["items"])) != count(t, d, "doc_invoice_item") {
		t.Error("the rows of the loser were written")
	}
}
