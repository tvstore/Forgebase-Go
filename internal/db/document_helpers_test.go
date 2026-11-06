package db_test

import (
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/tvstore/forgebase-go/internal/core"
	"github.com/tvstore/forgebase-go/internal/db"
	"github.com/tvstore/forgebase-go/internal/db/dbtest"
	"github.com/tvstore/forgebase-go/internal/decimal"
)

// Helpers of the document tests (the Rust tests have their own copies in every file; they are
// shared here). The tests are ported from crates/forgebase-db/tests/.

// setup is a migrated database with the DocTypes in texts synced.
func setup(t *testing.T, texts ...string) (*dbtest.DB, *core.Registry) {
	t.Helper()
	d := dbtest.Migrated(t)
	r := registry(t, texts...)
	if _, err := d.Sync(r); err != nil {
		t.Fatalf("sync: %v", err)
	}
	return d, r
}

func text(s string) core.Value { return core.Text(s) }

func dec(s string) core.Value {
	d, err := decimal.Parse(s)
	if err != nil {
		panic(err)
	}
	return core.Decimal(d)
}

func date(y int, m time.Month, d int) core.Value { return core.NewDate(y, m, d) }

// datetime is 2024-03-15 10:20:30.123456 UTC at the given offset in hours.
func datetime(offsetHours int) core.Value {
	utc := time.Unix(0, 1_710_498_030_123_456_000).UTC()
	return core.Datetime(utc.In(time.FixedZone("", offsetHours*3600)))
}

func sys(actor string) core.Caller { return core.SystemCaller(actor) }

func user(name string, roles ...string) core.Caller { return core.UserCaller(name, roles) }

// pairs turns "name", value, "name", value ... into a map of values.
func pairs(kv ...any) map[string]core.Value {
	m := map[string]core.Value{}
	for i := 0; i < len(kv); i += 2 {
		m[kv[i].(string)] = kv[i+1].(core.Value)
	}
	return m
}

// doc is a document with the given field values.
func doc(kv ...any) core.Document {
	return core.Document{Values: pairs(kv...), Tables: map[string][]core.Row{}}
}

// changes are changes to field values only.
func changes(kv ...any) core.Changes { return core.Changes{Values: pairs(kv...)} }

func row(kv ...any) core.Row { return core.Row(pairs(kv...)) }

func count(t *testing.T, d *dbtest.DB, table string) int64 {
	t.Helper()
	n, err := strconv.ParseInt(d.Rows("SELECT count(*)::text FROM " + table)[0][0], 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func mustInsert(t *testing.T, d *dbtest.DB, r *core.Registry, doctype string, document core.Document, caller core.Caller) *db.StoredDocument {
	t.Helper()
	stored, err := db.Insert(bg, d.Pool, r, doctype, document, caller)
	if err != nil {
		t.Fatalf("insert %s: %v", doctype, err)
	}
	return stored
}

func mustUpdate(t *testing.T, d *dbtest.DB, r *core.Registry, doctype, name string, version int64, c core.Changes, caller core.Caller) *db.StoredDocument {
	t.Helper()
	stored, err := db.Update(bg, d.Pool, r, doctype, name, version, c, caller)
	if err != nil {
		t.Fatalf("update %s %s: %v", doctype, name, err)
	}
	return stored
}

func mustGet(t *testing.T, d *dbtest.DB, r *core.Registry, doctype, name string, caller core.Caller) *db.StoredDocument {
	t.Helper()
	stored, err := db.Get(bg, d.Pool, r, doctype, name, caller)
	if err != nil {
		t.Fatalf("get %s %s: %v", doctype, name, err)
	}
	if stored == nil {
		t.Fatalf("get %s %s: not found", doctype, name)
	}
	return stored
}

func mustSubmit(t *testing.T, d *dbtest.DB, r *core.Registry, doctype, name string, version int64, caller core.Caller) *db.StoredDocument {
	t.Helper()
	stored, err := db.Submit(bg, d.Pool, r, doctype, name, version, caller)
	if err != nil {
		t.Fatalf("submit %s %s: %v", doctype, name, err)
	}
	return stored
}

func mustCancel(t *testing.T, d *dbtest.DB, r *core.Registry, doctype, name string, version int64, caller core.Caller) *db.StoredDocument {
	t.Helper()
	stored, err := db.Cancel(bg, d.Pool, r, doctype, name, version, caller)
	if err != nil {
		t.Fatalf("cancel %s %s: %v", doctype, name, err)
	}
	return stored
}

func mustAmend(t *testing.T, d *dbtest.DB, r *core.Registry, doctype, name string, version int64, caller core.Caller) *db.StoredDocument {
	t.Helper()
	stored, err := db.Amend(bg, d.Pool, r, doctype, name, version, caller)
	if err != nil {
		t.Fatalf("amend %s %s: %v", doctype, name, err)
	}
	return stored
}

// wantKind checks that err is a *db.Error of kind and returns it.
func wantKind(t *testing.T, err error, kind db.ErrorKind) *db.Error {
	t.Helper()
	if err == nil {
		t.Fatalf("expected an error of kind %d, got nil", kind)
	}
	var e *db.Error
	if !errors.As(err, &e) {
		t.Fatalf("expected a *db.Error, got %T: %v", err, err)
	}
	if e.Kind != kind {
		t.Fatalf("expected an error of kind %d, got kind %d: %v", kind, e.Kind, err)
	}
	return e
}

// validationOf is the problems of an ErrValidation.
func validationOf(t *testing.T, err error) []core.ValidationError {
	t.Helper()
	return wantKind(t, err, db.ErrValidation).Validation
}

func hasProblem(problems []core.ValidationError, p core.ValidationError) bool {
	for _, q := range problems {
		if reflect.DeepEqual(p, q) {
			return true
		}
	}
	return false
}

// sameDocument is Rust's assert_eq! of two StoredDocuments: every field equal, values compared as
// values (decimals as numbers, datetimes as instants).
func sameDocument(t *testing.T, got, want *db.StoredDocument, label string) {
	t.Helper()
	if got == nil || want == nil {
		t.Fatalf("%s: got %v, want %v", label, got, want)
	}
	if a, b := describeDoc(got), describeDoc(want); a != b {
		t.Errorf("%s:\n got %s\nwant %s", label, a, b)
	}
}

func describeDoc(s *db.StoredDocument) string {
	status, from := "none", s.AmendedFrom
	if s.DocStatus != nil {
		status = s.DocStatus.String()
	}
	out := fmt.Sprintf("%s %s owner=%s by=%s v=%d created=%s modified=%s status=%s from=%q state=%q",
		s.ID, s.Name, s.Owner, s.ModifiedBy, s.Version,
		s.CreatedAt.UTC().Format(time.RFC3339Nano), s.ModifiedAt.UTC().Format(time.RFC3339Nano), status, from, s.WorkflowState)
	for _, k := range sortedKeys(s.Values) {
		out += fmt.Sprintf(" %s=%s", k, describeValue(s.Values[k]))
	}
	for _, k := range sortedTableKeys(s.Tables) {
		out += fmt.Sprintf(" %s=[", k)
		for _, r := range s.Tables[k] {
			out += "{"
			for _, f := range sortedKeys(r) {
				out += fmt.Sprintf("%s=%s ", f, describeValue(r[f]))
			}
			out += "}"
		}
		out += "]"
	}
	return out
}

func describeValue(v core.Value) string {
	switch v.Kind {
	case core.KindText:
		return strconv.Quote(v.Text)
	case core.KindInt:
		return strconv.FormatInt(v.Int, 10)
	case core.KindDecimal:
		return v.Dec.Normalize().String() + "d"
	case core.KindBool:
		return strconv.FormatBool(v.Bool)
	case core.KindDate:
		return v.Time.Format("2006-01-02")
	case core.KindDatetime:
		return v.Time.UTC().Format(time.RFC3339Nano)
	}
	return "null"
}

func sortedKeys[V any](m map[string]V) []string { return slices.Sorted(maps.Keys(m)) }

func sortedTableKeys(m map[string][]core.Row) []string { return sortedKeys(m) }

// deepWant is reflect.DeepEqual with a message (want compares printed values, which differ for
// pointers).
func deepWant(t *testing.T, got, want any, label string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s:\n got %#v\nwant %#v", label, got, want)
	}
}

func errorsAs(err error, target **db.Error) bool { return errors.As(err, target) }
