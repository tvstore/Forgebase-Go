package db_test

import (
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5/pgconn"
	"strings"
	"testing"
	"time"

	"github.com/tvstore/forgebase-go/internal/core"
	"github.com/tvstore/forgebase-go/internal/db"
	"github.com/tvstore/forgebase-go/internal/db/dbtest"
	"github.com/tvstore/forgebase-go/internal/decimal"
)

// Tests of the Go form of the document API: what has no Rust test because Rust's ownership rules make
// it impossible (a hook that changes what it was only to read, the caller's document that a hook
// changes), the JSON form, and the texts of the errors.

// vandal changes everything it is given, in every hook that takes a document.
type vandal struct {
	core.BaseHooks
	seen []string
}

func scribble(doc *core.Document) {
	doc.Values["title"] = text("vandalised")
	doc.Name = "vandalised"
	for _, rows := range doc.Tables {
		for _, r := range rows {
			r["note"] = text("vandalised")
		}
	}
}

func (v *vandal) BeforeDelete(_ core.HookContext, doc *core.Document) error {
	v.seen = append(v.seen, doc.Values["title"].Text)
	scribble(doc)
	return nil
}

func (v *vandal) BeforeCancel(_ core.HookContext, doc *core.Document) error {
	v.seen = append(v.seen, doc.Values["title"].Text)
	scribble(doc)
	return nil
}

// Go: a hook of BeforeDelete and BeforeCancel is given a read-only view (core documents that the
// pipeline ignores what it does to it): the next hook, the stored document and the row that is
// written do not see the change. In Rust the view is a shared reference.
func TestChangesAHookMakesToTheViewOfADeleteOrCancelAreIgnored(t *testing.T) {
	d, _ := quoteSetup(t)
	reg := registry(t, quoteDoc, quoteLine, plainNote)
	first, second := &vandal{}, &vandal{}
	for _, h := range []core.DocTypeHooks{first, second} {
		if err := reg.AddHooks("quote", h); err != nil {
			t.Fatal(err)
		}
	}
	draft := mustInsert(t, d, reg, "quote", quote("q", line(1, "a")), sys("ann"))
	mustSubmit(t, d, reg, "quote", draft.Name, 1, sys("ann"))
	cancelled := mustCancel(t, d, reg, "quote", draft.Name, 2, sys("ann"))
	want(t, first.seen, []string{"q"}, "first hook at cancel")
	want(t, second.seen, []string{"q"}, "second hook at cancel")
	sameDocument(t, reread(t, d, reg, draft.Name), cancelled, "stored after cancel")
	if err := db.Delete(bg, d.Pool, reg, "quote", draft.Name, cancelled.Version, sys("ann")); err != nil {
		t.Fatal(err)
	}
	want(t, second.seen, []string{"q", "q"}, "second hook at delete")
}

// seeker changes the document it is given in the hooks that may.
type seeker struct{ core.BaseHooks }

func (seeker) BeforeInsert(_ core.HookContext, doc *core.Document) error {
	doc.Values["title"] = text("from the hook")
	doc.Tables["lines"] = append(doc.Tables["lines"], line(9, "hook"))
	doc.Name = ""
	return nil
}

// Go: Insert works on a copy of the document: a hook that changes it does not change the caller's
// (in Rust the document is moved).
func TestInsertDoesNotChangeTheCallersDocument(t *testing.T) {
	d, _ := quoteSetup(t)
	reg := registry(t, quoteDoc, quoteLine, plainNote)
	if err := reg.AddHooks("quote", seeker{}); err != nil {
		t.Fatal(err)
	}
	given := quote("mine", line(1, "a"))
	stored := mustInsert(t, d, reg, "quote", given, sys("ann"))
	if stored.Values["title"].Text != "from the hook" || len(stored.Tables["lines"]) != 2 {
		t.Error(describeDoc(stored))
	}
	if given.Values["title"].Text != "mine" || len(given.Tables["lines"]) != 1 {
		t.Errorf("the caller's document changed: %v", given)
	}
	// The same for the changes of an update.
	c := core.Changes{Values: pairs("remarks", text("r")), Tables: map[string][]core.Row{"lines": {line(2, "b")}}}
	mustUpdate(t, d, reg, "quote", stored.Name, 1, c, sys("ann"))
	if len(c.Tables["lines"]) != 1 || len(c.Values) != 1 {
		t.Errorf("the caller's changes changed: %v", c)
	}
}

// Go: the Rust Document is moved and its tables are BTreeMaps; a Go document may carry nil maps, which
// are the same as empty ones.
func TestADocumentWithNilMapsIsAnEmptyDocument(t *testing.T) {
	d, reg := setup(t, plainNote)
	stored := mustInsert(t, d, reg, "note", core.Document{Values: nil, Tables: nil}, sys("ann"))
	if len(stored.Values) != 1 || stored.Values["body"].Kind != core.KindNull {
		t.Error(describeDoc(stored))
	}
	if _, err := db.Update(bg, d.Pool, reg, "note", stored.Name, 1, core.Changes{Values: nil, Tables: nil}, sys("ann")); err == nil {
		t.Error("empty changes were accepted")
	}
}

// Go: Datetimes keep the microseconds PostgreSQL has and no more: the finer digits are cut like the
// Rust time crate cuts them, also before the PostgreSQL epoch.
func TestDatetimesAreCutToMicroseconds(t *testing.T) {
	d, reg := setup(t, dbtest.Customer)
	for i, c := range []struct {
		at   time.Time
		want string
	}{
		{time.Date(2024, 3, 15, 10, 20, 30, 123_456_789, time.UTC), "2024-03-15T10:20:30.123456Z"},
		{time.Date(1999, 12, 31, 23, 59, 59, 999_999_999, time.UTC), "2000-01-01T00:00:00Z"},
		{time.Date(1960, 1, 1, 0, 0, 0, 1_500, time.UTC), "1960-01-01T00:00:00.000002Z"},
		{time.Date(2024, 3, 15, 10, 20, 30, 500_000_000, time.FixedZone("", 9*3600)), "2024-03-15T01:20:30.5Z"},
	} {
		stored := mustInsert(t, d, reg, "customer", doc("email", text(fmt.Sprintf("t%d", i)), "last_seen", core.Datetime(c.at)), sys("cli"))
		got, err := db.UTCRFC3339(stored.Values["last_seen"].Time)
		if err != nil || got != c.want {
			t.Errorf("%v: %s (%v), want %s", c.at, got, err, c.want)
		}
	}
}

// Go: a decimal keeps its scale through the database: the scale of the column, not the one it was
// written with.
func TestDecimalsComeBackWithTheScaleOfTheColumn(t *testing.T) {
	d, reg := setup(t, dbtest.Customer)
	for i, c := range []struct{ in, out string }{
		{"1", "1.00"}, {"0.5", "0.50"}, {"-12.3", "-12.30"}, {"0", "0.00"}, {"1234.567", ""},
	} {
		in, err := decimal.Parse(c.in)
		if err != nil {
			t.Fatal(err)
		}
		stored, err := db.Insert(bg, d.Pool, reg, "customer", doc("email", text(fmt.Sprintf("d%d", i)), "credit_limit", core.Decimal(in)), sys("cli"))
		if c.out == "" {
			// More decimal places than the scale is a validation error, not a rounding.
			wantKind(t, err, db.ErrValidation)
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if got := stored.Values["credit_limit"].Dec.String(); got != c.out {
			t.Errorf("%s: %s, want %s", c.in, got, c.out)
		}
	}
}

// Go: the JSON form of a document, as the API and the webhooks write it.
func TestDocumentToJSON(t *testing.T) {
	d, reg := setup(t, quoteDoc, quoteLine, plainNote, dbtest.Customer)
	stored := mustInsert(t, d, reg, "quote", quote("a", line(1, "x")), sys("ann"))
	got, err := db.DocumentToJSON(stored)
	if err != nil {
		t.Fatal(err)
	}
	if got["id"] != stored.ID.String() || got["name"] != stored.Name || got["owner"] != "ann" || got["version"] != int64(1) ||
		got["docstatus"] != int64(0) || got["amended_from"] != nil || got["title"] != "a" || got["total"] != nil {
		t.Errorf("%v", got)
	}
	if _, ok := got["workflow_state"]; ok {
		t.Error("a DocType without a workflow has no state")
	}
	lines, ok := got["lines"].([]any)
	if !ok || len(lines) != 1 || lines[0].(map[string]any)["qty"] != int64(1) {
		t.Errorf("%v", got["lines"])
	}
	for _, key := range []string{"created_at", "modified_at"} {
		if s, ok := got[key].(string); !ok || !strings.HasSuffix(s, "Z") {
			t.Errorf("%s: %v", key, got[key])
		}
	}
	// A plain DocType has no docstatus; values are written as their kinds say.
	customer := mustInsert(t, d, reg, "customer", fullCustomer("j@x.y"), sys("ann"))
	got, err = db.DocumentToJSON(customer)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got["docstatus"]; ok {
		t.Error("docstatus")
	}
	if got["credit_limit"] != "1234.50" || got["joined_on"] != "2024-03-15" || got["last_seen"] != "2024-03-15T10:20:30.123456Z" ||
		got["active"] != true || got["points"] != int64(-42) {
		t.Errorf("%v", got)
	}
}

// Go: a value that has no JSON form is an OutputError naming the field (and the row).
func TestAValueWithNoJSONFormIsAnOutputError(t *testing.T) {
	doc := &db.StoredDocument{
		Values: map[string]core.Value{"at": core.Datetime(time.Date(-1, 1, 1, 0, 0, 0, 0, time.UTC))},
		Tables: map[string][]core.Row{"rows": {{"d": core.NewDate(12000, 1, 1)}}},
	}
	_, err := db.DocumentToJSON(doc)
	var out *db.OutputError
	if !errorsAs2(err, &out) || out.Field != "at" || !strings.Contains(err.Error(), "cannot format field `at` as JSON") {
		t.Errorf("%v", err)
	}
	delete(doc.Values, "at")
	_, err = db.DocumentToJSON(doc)
	if !errorsAs2(err, &out) || out.Field != "rows row 1: d" {
		t.Errorf("%v", err)
	}
}

func errorsAs2(err error, target **db.OutputError) bool {
	e, ok := err.(*db.OutputError)
	if ok {
		*target = e
	}
	return ok
}

// Go: the texts of the document errors are the Rust Display texts (those that no other test pins).
func TestDocumentErrorTexts(t *testing.T) {
	by := "invoice"
	for _, c := range []struct {
		err  db.Error
		text string
	}{
		{db.Error{Kind: db.ErrUnknownDocType, Name: "nope"}, "unknown DocType `nope`"},
		{db.Error{Kind: db.ErrChildDocType, DocType: "line"}, "DocType `line` is a child DocType: its rows belong to a parent document and are read and written through the parent"},
		{db.Error{Kind: db.ErrChangedWhileReading, DocType: "invoice", Name: "I-1"}, "invoice `I-1` was changed while it was being read; retry"},
		{db.Error{Kind: db.ErrNotFound, DocType: "invoice", Name: "I-1"}, "invoice `I-1` not found"},
		{db.Error{Kind: db.ErrLinkedDocumentExists, DocType: "person", Name: "p", ReferencedBy: &by}, "person `p` cannot be deleted: it is linked from invoice"},
		{db.Error{Kind: db.ErrLinkedDocumentExists, DocType: "person", Name: "p"}, "person `p` cannot be deleted: it is linked from another document"},
		{db.Error{Kind: db.ErrPermissionDenied, DocType: "invoice", Right: core.RightSubmit}, "permission denied: no `submit` right on DocType `invoice`"},
		{db.Error{Kind: db.ErrInvalidActor}, "actor must be a non-empty string without NUL characters"},
		{db.Error{Kind: db.ErrDecode, DocType: "customer", Field: "joined_on", Message: "m"}, "cannot read field `joined_on` (column \"joined_on\") of DocType `customer`: m"},
		{db.Error{Kind: db.ErrNoChanges}, "update needs at least one changed field"},
		{db.Error{Kind: db.ErrInvalidDocStatus, DocType: "quote", Name: "q", DocStatus: core.Cancelled, Operation: "update"}, "cannot update quote `q`: it is cancelled"},
		{db.Error{Kind: db.ErrNotSubmittable, DocType: "note", Operation: "amend"}, "cannot amend: DocType `note` is not submittable (`submittable = true`)"},
		{db.Error{Kind: db.ErrFieldLocked, DocType: "quote", Name: "q", Field: "title"}, "quote `q` is submitted: field `title` cannot be changed (only fields marked allow_on_submit can; tables are locked)"},
		{db.Error{Kind: db.ErrAlreadyAmended, DocType: "quote", Name: "q", AmendedBy: "q-1"}, "quote `q` was already amended by `q-1`"},
		{db.Error{Kind: db.ErrWorkflowRequired, DocType: "order", Operation: "cancel"}, "DocType `order` has a workflow; use a transition (cannot cancel)"},
		{db.Error{Kind: db.ErrEditDenied, DocType: "order", State: "Review"}, "permission denied: a `order` document in state `Review` can be edited only by the roles of that state"},
		{db.Error{Kind: db.ErrJob, Message: "boom"}, "job: boom"},
		{db.Error{Kind: db.ErrHook, DocType: "invoice", Hook: "validate", Message: "no"}, "validate hook of DocType `invoice` rejected the operation: no"},
	} {
		if got := c.err.Error(); got != c.text {
			t.Errorf("kind %d: %q, want %q", c.err.Kind, got, c.text)
		}
	}
}

// Go: dates with a negative year are written like the Rust time crate: a sign, then four digits.
func TestFormatDateNegativeYears(t *testing.T) {
	for _, c := range []struct {
		year int
		want string
	}{{-1, "-0001-03-05"}, {-9999, "-9999-03-05"}, {0, "0000-03-05"}, {9999, "9999-03-05"}} {
		got, err := db.FormatDate(time.Date(c.year, 3, 5, 0, 0, 0, 0, time.UTC))
		if err != nil || got != c.want {
			t.Errorf("%d: %q %v", c.year, got, err)
		}
	}
	if _, err := db.FormatDate(time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)); err == nil {
		t.Error("year 10000")
	}
	// A datetime before year 0 has no RFC 3339 form (the Rust time crate refuses it too).
	for _, y := range []int{-1, -9999} {
		if _, err := db.UTCRFC3339(time.Date(y, 1, 1, 0, 0, 0, 0, time.UTC)); err == nil {
			t.Errorf("datetime year %d", y)
		}
	}
	if got, err := db.UTCRFC3339(time.Date(0, 1, 1, 0, 0, 0, 0, time.UTC)); err != nil || got != "0000-01-01T00:00:00Z" {
		t.Errorf("%q %v", got, err)
	}
}

// Go: a hidden referencing DocType and a hidden field are not reachable through the driver error.
func TestHiddenNamesAreNotReachableThroughUnwrap(t *testing.T) {
	d, reg := permSetup(t)
	party := makeParty(t, d, reg, "p@x.y", sys("sys"))
	mustInsert(t, d, reg, "ticket", ticketLinking("p@x.y", ""), sys("sys"))
	err := db.Delete(bg, d.Pool, reg, "party", party.Name, party.Version, user("u", "PartyAdmin"))
	e := wantKind(t, err, db.ErrLinkedDocumentExists)
	var pg *pgconn.PgError
	if e.ReferencedBy != nil || errors.As(err, &pg) || errors.Unwrap(err) != nil {
		t.Errorf("%+v", e)
	}
}
