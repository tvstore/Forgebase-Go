package db_test

import (
	"strings"
	"testing"

	"github.com/tvstore/forgebase-go/internal/db"
	"github.com/tvstore/forgebase-go/internal/db/dbtest"
)

// Schema sync of submittable DocTypes (M2.4): the docstatus and amended_from columns, adding them to
// an existing table, drift conflicts and turning submittable off. Ported from
// crates/forgebase-db/tests/docstatus_schema.rs.

const plainMemo = `
name = "memo"
label = "Memo"
[[fields]]
name = "title"
type = "data"
`

const submittableMemo = `
name = "memo"
label = "Memo"
submittable = true
[[fields]]
name = "title"
type = "data"
`

func columnType(columns []dbtest.Column, name string) (string, bool) {
	for _, c := range columns {
		if c[0] == name {
			return c[1], true
		}
	}
	return "", false
}

// Rust: docstatus_schema.rs::a_new_submittable_table_has_the_docstatus_columns
func TestANewSubmittableTableHasTheDocstatusColumns(t *testing.T) {
	d := dbtest.Migrated(t)
	report := mustSync(t, d, registry(t, submittableMemo))
	if len(report.Steps) != 3 {
		t.Fatalf("%v", texts(report.Steps))
	}
	if report.Steps[0].Kind != db.StepCreateTable ||
		!strings.Contains(report.Steps[0].SQL, "\"docstatus\" smallint NOT NULL DEFAULT 0,\n    \"amended_from\" text,\n") {
		t.Errorf("%s", report.Steps[0].SQL)
	}
	columns := d.Columns("doc_memo")
	if typ, _ := columnType(columns, "docstatus"); typ != "smallint/NO" {
		t.Errorf("docstatus: %s", typ)
	}
	if typ, _ := columnType(columns, "amended_from"); typ != "text/YES" {
		t.Errorf("amended_from: %s", typ)
	}
	want(t, d.Rows("SELECT column_default::text FROM information_schema.columns "+
		"WHERE table_name = 'doc_memo' AND column_name = 'docstatus'"), [][]string{{"0"}}, "default")
	// The standard columns come first; a second sync is a no-op.
	if columns[7][0] != "docstatus" {
		t.Errorf("%v", columns)
	}
	if len(mustSync(t, d, registry(t, submittableMemo)).Steps) != 0 {
		t.Error("a re-sync did something")
	}
}

// Rust: docstatus_schema.rs::a_plain_table_has_no_docstatus_columns
func TestAPlainTableHasNoDocstatusColumns(t *testing.T) {
	d := dbtest.Migrated(t)
	mustSync(t, d, registry(t, plainMemo))
	columns := d.Columns("doc_memo")
	if _, ok := columnType(columns, "docstatus"); ok {
		t.Error("docstatus")
	}
	if _, ok := columnType(columns, "amended_from"); ok {
		t.Error("amended_from")
	}
}

// Rust: docstatus_schema.rs::making_an_existing_doctype_submittable_adds_the_columns_additively
//
// The Rust test reads the document back with get() and expects a draft. Document reads arrive in
// P2; here the stored row is read with SQL (docstatus 0, amended_from NULL), and the get() part
// is ported with P2.
func TestMakingAnExistingDoctypeSubmittableAddsTheColumnsAdditively(t *testing.T) {
	d := dbtest.Migrated(t)
	mustSync(t, d, registry(t, plainMemo))
	d.Exec("INSERT INTO doc_memo (id, name, owner, created_at, modified_at, modified_by, title) " +
		"VALUES (gen_random_uuid(), 'old', 'u', now(), now(), 'u', 'before')")

	submittable := registry(t, submittableMemo)
	report := mustSync(t, d, submittable)
	if len(report.Warnings) != 0 {
		t.Errorf("%v", report.Warnings)
	}
	if len(report.Steps) != 2 {
		t.Fatalf("%v", texts(report.Steps))
	}
	if s := report.Steps[0]; s.Kind != db.StepAddColumn || s.Column != "docstatus" ||
		s.SQL != `ALTER TABLE "doc_memo" ADD COLUMN "docstatus" smallint NOT NULL DEFAULT 0` {
		t.Errorf("%v", s)
	}
	if s := report.Steps[1]; s.Kind != db.StepAddColumn || s.Column != "amended_from" ||
		s.SQL != `ALTER TABLE "doc_memo" ADD COLUMN "amended_from" text` {
		t.Errorf("%v", s)
	}
	columns := d.Columns("doc_memo")
	if typ, _ := columnType(columns, "docstatus"); typ != "smallint/NO" {
		t.Errorf("docstatus: %s", typ)
	}
	if typ, _ := columnType(columns, "amended_from"); typ != "text/YES" {
		t.Errorf("amended_from: %s", typ)
	}

	// The document that was there is now a draft.
	want(t, d.Rows("SELECT docstatus::text, coalesce(amended_from, 'null') FROM doc_memo WHERE name = 'old'"),
		[][]string{{"0", "null"}}, "the old document")
	if len(mustSync(t, d, submittable).Steps) != 0 {
		t.Error("a re-sync did something")
	}
}

// Rust: docstatus_schema.rs::a_dry_run_shows_the_alter_statements_and_changes_nothing
func TestADryRunShowsTheAlterStatementsAndChangesNothing(t *testing.T) {
	d := dbtest.Migrated(t)
	mustSync(t, d, registry(t, plainMemo))
	report, err := db.Sync(bg, d.Pool, registry(t, submittableMemo), db.SyncOptions{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Steps) != 2 {
		t.Errorf("%v", texts(report.Steps))
	}
	if _, ok := columnType(d.Columns("doc_memo"), "docstatus"); ok {
		t.Error("a dry run added the column")
	}
}

// Rust: docstatus_schema.rs::turning_submittable_off_is_a_warning_and_keeps_the_columns
//
// The Rust test ends by inserting a document of the plain DocType through insert() and checking that
// it has no docstatus; that part needs the document API (P2). Here a row is inserted with SQL: the
// column has a default, so the plain DocType can still be written.
func TestTurningSubmittableOffIsAWarningAndKeepsTheColumns(t *testing.T) {
	d := dbtest.Migrated(t)
	mustSync(t, d, registry(t, submittableMemo))
	report := mustSync(t, d, registry(t, plainMemo))
	if len(report.Steps) != 0 {
		t.Errorf("%v", texts(report.Steps))
	}
	for _, column := range []string{"docstatus", "amended_from"} {
		found := false
		for _, w := range report.Warnings {
			if w == (db.Warning{Kind: db.WarningColumnNotInMetadata, Table: "doc_memo", Column: column}) {
				found = true
			}
		}
		if !found {
			t.Errorf("no warning for %s: %v", column, report.Warnings)
		}
	}
	if _, ok := columnType(d.Columns("doc_memo"), "docstatus"); !ok {
		t.Error("the columns are kept")
	}
	// Documents of the plain DocType can still be written (the column has a default).
	d.Exec("INSERT INTO doc_memo (id, name, owner, created_at, modified_at, modified_by, title) " +
		"VALUES (gen_random_uuid(), 'new', 'ann', now(), now(), 'ann', 'after')")
	want(t, d.Rows("SELECT docstatus::text FROM doc_memo WHERE name = 'new'"), [][]string{{"0"}}, "default docstatus")
}

// Rust: docstatus_schema.rs::drift_in_the_docstatus_columns_is_a_conflict
func TestDriftInTheDocstatusColumnsIsAConflict(t *testing.T) {
	cases := []struct{ drift, column, expected, actual string }{
		{"ALTER TABLE doc_memo ALTER COLUMN docstatus DROP DEFAULT, ALTER COLUMN docstatus TYPE bigint",
			"docstatus", "smallint NOT NULL", "bigint NOT NULL"},
		{"ALTER TABLE doc_memo ALTER COLUMN docstatus DROP NOT NULL",
			"docstatus", "smallint NOT NULL", "smallint"},
		{"ALTER TABLE doc_memo ALTER COLUMN amended_from SET NOT NULL",
			"amended_from", "text", "text NOT NULL"},
		{"ALTER TABLE doc_memo ALTER COLUMN amended_from TYPE character varying(10)",
			"amended_from", "text", "character varying(10)"},
	}
	for _, c := range cases {
		d := dbtest.Migrated(t)
		reg := registry(t, submittableMemo)
		mustSync(t, d, reg)
		d.Exec(c.drift)
		e := syncError(t, d, reg)
		want(t, e.Conflicts, []db.Conflict{{
			Kind: db.ConflictColumnMismatch, Table: "doc_memo", Column: c.column, Expected: c.expected, Actual: c.actual,
		}}, c.drift)
	}
}

// Rust: docstatus_schema.rs::a_dropped_docstatus_column_is_added_again
func TestADroppedDocstatusColumnIsAddedAgain(t *testing.T) {
	d := dbtest.Migrated(t)
	reg := registry(t, submittableMemo)
	mustSync(t, d, reg)
	d.Exec("ALTER TABLE doc_memo DROP COLUMN amended_from")
	report := mustSync(t, d, reg)
	if len(report.Steps) != 1 || report.Steps[0].Kind != db.StepAddColumn || report.Steps[0].Column != "amended_from" {
		t.Errorf("%v", texts(report.Steps))
	}
}
