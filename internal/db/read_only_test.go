package db_test

import (
	"testing"

	"github.com/tvstore/forgebase-go/internal/core"
	"github.com/tvstore/forgebase-go/internal/db"
	"github.com/tvstore/forgebase-go/internal/db/dbtest"
)

// read_only fields (UX.1) against a real PostgreSQL: a caller's input for them is refused before the
// hooks run, a hook may set them, and an amendment is not caller input. Ported from
// crates/forgebase-db/tests/read_only.rs.

const roSheet = `
name = "sheet"
label = "Sheet"
submittable = true
[[fields]]
name = "title"
type = "data"
required = true
[[fields]]
name = "total"
type = "int"
read_only = true
[[fields]]
name = "lines"
type = "table"
target = "sheet_line"
`

const roLine = `
name = "sheet_line"
label = "Sheet Line"
is_child = true
[[fields]]
name = "qty"
type = "int"
required = true
[[fields]]
name = "amount"
type = "int"
read_only = true
`

// roCompute computes amount (qty * 10) of every row and total (their sum), like an app's hook.
type roCompute struct{ core.BaseHooks }

func (roCompute) Validate(_ core.HookContext, doc *core.Document) error {
	total := int64(0)
	for _, r := range doc.Tables["lines"] {
		if qty := r["qty"]; qty.Kind == core.KindInt {
			amount := qty.Int * 10
			total += amount
			r["amount"] = core.Int(amount)
		}
	}
	doc.Values["total"] = core.Int(total)
	return nil
}

func roPlain(t *testing.T) *core.Registry { return registry(t, roSheet, roLine) }

func roComputing(t *testing.T) *core.Registry {
	t.Helper()
	reg := roPlain(t)
	if err := reg.AddHooks("sheet", roCompute{}); err != nil {
		t.Fatal(err)
	}
	return reg
}

func roLineRow(qty int64, amount *int64) core.Row {
	r := row("qty", core.Int(qty))
	if amount != nil {
		r["amount"] = core.Int(*amount)
	}
	return r
}

func i64(n int64) *int64 { return &n }

// roSheetDoc is a sheet document; total nil leaves the field out.
func roSheetDoc(total *core.Value, lines ...core.Row) core.Document {
	d := doc("title", text("s"))
	if total != nil {
		d.Values["total"] = *total
	}
	d.Tables["lines"] = lines
	return d
}

func roAnn() core.Caller { return sys("ann") }

func roReadOnly(field string) core.ValidationError {
	return core.ValidationError{Kind: core.ErrReadOnly, Field: field}
}

func roInRow(r int, field string) core.ValidationError {
	inner := roReadOnly(field)
	return core.ValidationError{Kind: core.ErrInRow, Field: "lines", Row: r, Inner: &inner}
}

func roRefused(t *testing.T, err error) []core.ValidationError {
	t.Helper()
	return wantKind(t, err, db.ErrValidation).Validation
}

func roSetup(t *testing.T, reg *core.Registry) *dbtest.DB {
	t.Helper()
	d := dbtest.Migrated(t)
	if _, err := d.Sync(reg); err != nil {
		t.Fatal(err)
	}
	return d
}

func valuePtr(v core.Value) *core.Value { return &v }

// Rust: read_only.rs::insert_refuses_caller_values_top_level_and_in_rows_and_writes_nothing
func TestInsertRefusesCallerValuesTopLevelAndInRowsAndWritesNothing(t *testing.T) {
	reg := roPlain(t)
	d := roSetup(t, reg)
	document := roSheetDoc(valuePtr(core.Int(5)), roLineRow(1, i64(2)), roLineRow(2, nil), roLineRow(3, i64(9)))
	_, err := db.Insert(bg, d.Pool, reg, "sheet", document, roAnn())
	deepWant(t, roRefused(t, err), []core.ValidationError{roReadOnly("total"), roInRow(1, "amount"), roInRow(3, "amount")}, "errors")
	if count(t, d, "doc_sheet") != 0 {
		t.Error("something was written")
	}
	// Null, or absent, is not a value.
	stored := mustInsert(t, d, reg, "sheet", roSheetDoc(valuePtr(core.Null()), roLineRow(1, nil)), roAnn())
	if stored.Values["total"].Kind != core.KindNull {
		t.Error(stored.Values["total"])
	}
}

// Rust: read_only.rs::the_refusal_comes_before_the_hooks_run
func TestTheRefusalComesBeforeTheHooksRun(t *testing.T) {
	// The hook would overwrite the value, but the caller's input is still refused.
	reg := roComputing(t)
	d := roSetup(t, reg)
	_, err := db.Insert(bg, d.Pool, reg, "sheet", roSheetDoc(valuePtr(core.Int(1)), roLineRow(1, nil)), roAnn())
	deepWant(t, roRefused(t, err), []core.ValidationError{roReadOnly("total")}, "errors")
}

// Rust: read_only.rs::a_hook_sets_read_only_fields_on_insert_and_update
func TestAHookSetsReadOnlyFieldsOnInsertAndUpdate(t *testing.T) {
	reg := roComputing(t)
	d := roSetup(t, reg)
	stored := mustInsert(t, d, reg, "sheet", roSheetDoc(nil, roLineRow(1, nil), roLineRow(2, nil)), roAnn())
	if stored.Values["total"].Int != 30 || stored.Tables["lines"][1]["amount"].Int != 20 {
		t.Fatal(describeDoc(stored))
	}
	// Replacing the table without the computed column: the hook fills it in and it is stored.
	c := core.Changes{Values: map[string]core.Value{}, Tables: map[string][]core.Row{"lines": {roLineRow(4, nil)}}}
	stored = mustUpdate(t, d, reg, "sheet", stored.Name, stored.Version, c, roAnn())
	if stored.Values["total"].Int != 40 || stored.Tables["lines"][0]["amount"].Int != 40 {
		t.Fatal(describeDoc(stored))
	}
	if read := mustGet(t, d, reg, "sheet", stored.Name, roAnn()); read.Values["total"].Int != 40 {
		t.Error(describeDoc(read))
	}
}

// Rust: read_only.rs::update_refuses_a_changed_value_and_a_row_value_but_accepts_the_stored_value
func TestUpdateRefusesAChangedValueAndARowValueButAcceptsTheStoredValue(t *testing.T) {
	reg := roComputing(t)
	d := roSetup(t, reg)
	stored := mustInsert(t, d, reg, "sheet", roSheetDoc(nil, roLineRow(1, nil)), roAnn())
	name, version := stored.Name, stored.Version
	set := func(total int64, rows []core.Row) core.Changes {
		c := core.Changes{Values: map[string]core.Value{"total": core.Int(total)}}
		if rows != nil {
			c.Tables = map[string][]core.Row{"lines": rows}
		}
		return c
	}
	_, err := db.Update(bg, d.Pool, reg, "sheet", name, version, set(99, nil), roAnn())
	deepWant(t, roRefused(t, err), []core.ValidationError{roReadOnly("total")}, "changed value")

	// A row with a computed value is refused even when it equals the stored one.
	_, err = db.Update(bg, d.Pool, reg, "sheet", name, version, set(10, []core.Row{roLineRow(1, i64(10))}), roAnn())
	deepWant(t, roRefused(t, err), []core.ValidationError{roInRow(1, "amount")}, "row value")

	// Sending the stored value back is no change.
	stored = mustUpdate(t, d, reg, "sheet", name, version, set(10, nil), roAnn())
	if stored.Version != 2 || stored.Values["total"].Int != 10 {
		t.Error(describeDoc(stored))
	}
}

// Rust: read_only.rs::update_refuses_null_for_a_stored_read_only_value
func TestUpdateRefusesNullForAStoredReadOnlyValue(t *testing.T) {
	reg := roComputing(t)
	d := roSetup(t, reg)
	stored := mustInsert(t, d, reg, "sheet", roSheetDoc(nil, roLineRow(1, nil)), roAnn())
	if stored.Values["total"].Int != 10 {
		t.Fatal(describeDoc(stored))
	}
	_, err := db.Update(bg, d.Pool, reg, "sheet", stored.Name, stored.Version,
		core.Changes{Values: map[string]core.Value{"total": core.Null()}}, roAnn())
	deepWant(t, roRefused(t, err), []core.ValidationError{roReadOnly("total")}, "null")
	read := mustGet(t, d, reg, "sheet", stored.Name, roAnn())
	if read.Version != 1 || read.Values["total"].Int != 10 {
		t.Error(describeDoc(read))
	}
}

// Rust: read_only.rs::amend_copies_read_only_values_without_refusing_them
func TestAmendCopiesReadOnlyValuesWithoutRefusingThem(t *testing.T) {
	reg := roComputing(t)
	d := roSetup(t, reg)
	stored := mustInsert(t, d, reg, "sheet", roSheetDoc(nil, roLineRow(2, nil)), roAnn())
	stored = mustSubmit(t, d, reg, "sheet", stored.Name, stored.Version, roAnn())
	stored = mustCancel(t, d, reg, "sheet", stored.Name, stored.Version, roAnn())
	if stored.Values["total"].Int != 20 {
		t.Fatal(describeDoc(stored))
	}
	amended := mustAmend(t, d, reg, "sheet", stored.Name, stored.Version, roAnn())
	if amended.AmendedFrom != stored.Name || amended.Values["total"].Int != 20 || amended.Tables["lines"][0]["amount"].Int != 20 {
		t.Error(describeDoc(amended))
	}
}
