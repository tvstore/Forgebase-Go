package db_test

import (
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/tvstore/forgebase-go/internal/core"
	"github.com/tvstore/forgebase-go/internal/db"
	"github.com/tvstore/forgebase-go/internal/db/dbtest"
)

// Document CRUD against a real PostgreSQL. Ported from crates/forgebase-db/tests/documents.rs.

// namedAccount is a DocType whose name is the value of email.
const namedAccount = `
name = "account"
label = "Account"
naming = { field = "email" }
[[fields]]
name = "email"
type = "data"
required = true
unique = true
[[fields]]
name = "points"
type = "int"
`

func fullCustomer(email string) core.Document {
	return doc(
		"email", text(email),
		"notes", text("line one\nline two \U0001F600"),
		"points", core.Int(-42),
		"credit_limit", dec("1234.50"),
		"active", core.Bool(true),
		"joined_on", date(2024, 3, 15),
		"last_seen", datetime(0),
		"tier", text("gold"),
	)
}

// Rust: documents.rs::round_trips_every_field_type
func TestRoundTripsEveryFieldType(t *testing.T) {
	d, reg := setup(t, dbtest.Customer)
	stored := mustInsert(t, d, reg, "customer", fullCustomer("a@example.com"), sys("alice"))
	for field, expected := range map[string]core.Value{
		"email":        text("a@example.com"),
		"points":       core.Int(-42),
		"credit_limit": dec("1234.50"),
		"active":       core.Bool(true),
		"joined_on":    date(2024, 3, 15),
		"last_seen":    datetime(0),
		"tier":         text("gold"),
	} {
		if !stored.Values[field].Equal(expected) {
			t.Errorf("%s: %v, want %v", field, describeValue(stored.Values[field]), describeValue(expected))
		}
	}
	if stored.Values["credit_limit"].Dec.String() != "1234.50" {
		t.Errorf("the decimal lost its scale: %s", stored.Values["credit_limit"].Dec)
	}
	if len(stored.Values) != 8 {
		t.Errorf("%d values", len(stored.Values))
	}
	if stored.Owner != "alice" || stored.ModifiedBy != "alice" || stored.Version != 1 || !stored.CreatedAt.Equal(stored.ModifiedAt) {
		t.Errorf("%+v", stored)
	}
	fetched := mustGet(t, d, reg, "customer", stored.Name, sys("cli"))
	sameDocument(t, fetched, stored, "fetched")
}

// Rust: documents.rs::absent_and_null_values_are_null
func TestAbsentAndNullValuesAreNull(t *testing.T) {
	d, reg := setup(t, dbtest.Customer)
	stored := mustInsert(t, d, reg, "customer", doc("email", text("n@example.com"), "points", core.Null()), sys("cli"))
	if len(stored.Values) != 8 {
		t.Errorf("%d values", len(stored.Values))
	}
	for field, value := range stored.Values {
		if field != "email" && value.Kind != core.KindNull {
			t.Errorf("%s: %v", field, describeValue(value))
		}
	}
}

// Rust: documents.rs::boundary_values_round_trip
func TestBoundaryValuesRoundTrip(t *testing.T) {
	d, reg := setup(t, dbtest.Customer)
	// Data at max_length 100, counted in characters (multi-byte characters).
	longest := strings.Repeat("é", 100)
	for i, c := range [][2]string{
		{longest, "9999999999.99"},
		{"neg", "-9999999999.99"},
		{"tiny", "0.01"},
		{"zero", "0"},
	} {
		stored := mustInsert(t, d, reg, "customer", doc("email", text(c[0]), "credit_limit", dec(c[1])), sys("cli"))
		if stored.Values["email"].Text != c[0] {
			t.Errorf("%d: email", i)
		}
		if !stored.Values["credit_limit"].Equal(dec(c[1])) {
			t.Errorf("%d: %s", i, describeValue(stored.Values["credit_limit"]))
		}
	}
	// i64 extremes.
	for _, n := range []int64{math.MinInt64, math.MaxInt64, 0} {
		stored := mustInsert(t, d, reg, "customer",
			doc("email", text(fmt.Sprintf("int%d", n)), "points", core.Int(n)), sys("cli"))
		if stored.Values["points"].Int != n {
			t.Errorf("%d: %d", n, stored.Values["points"].Int)
		}
	}
	// A non-UTC offset is stored as the same instant (and comes back in UTC).
	local := datetime(7)
	stored := mustInsert(t, d, reg, "customer", doc("email", text("tz"), "last_seen", local), sys("cli"))
	back := stored.Values["last_seen"]
	if back.Kind != core.KindDatetime || !back.Time.Equal(local.Time) || back.Time.UnixNano() != local.Time.UnixNano() {
		t.Errorf("%v", back)
	}
}

// Rust: documents.rs::random_naming_uses_the_id
func TestRandomNamingUsesTheID(t *testing.T) {
	d, reg := setup(t, dbtest.Customer)
	stored := mustInsert(t, d, reg, "customer", fullCustomer("r@example.com"), sys("cli"))
	if stored.Name != stored.ID.String() || stored.ID.Version() != 7 {
		t.Errorf("%s %s %d", stored.Name, stored.ID, stored.ID.Version())
	}
	again := mustGet(t, d, reg, "customer", stored.Name, sys("cli"))
	if again.ID != stored.ID {
		t.Error("another id")
	}
}

// Rust: documents.rs::explicit_name_is_rejected_for_random_naming
func TestExplicitNameIsRejectedForRandomNaming(t *testing.T) {
	d, reg := setup(t, dbtest.Customer)
	document := fullCustomer("x@example.com")
	document.Name = "my-name"
	_, err := db.Insert(bg, d.Pool, reg, "customer", document, sys("cli"))
	wantKind(t, err, db.ErrNaming)
	if count(t, d, "doc_customer") != 0 {
		t.Error("a document was written")
	}
}

// Rust: documents.rs::field_naming_uses_the_field_value
func TestFieldNamingUsesTheFieldValue(t *testing.T) {
	d, reg := setup(t, namedAccount)
	stored := mustInsert(t, d, reg, "account", doc("email", text("a@b.c")), sys("cli"))
	if stored.Name != "a@b.c" {
		t.Error(stored.Name)
	}
	// An explicit name that equals the field value is accepted, a different one is not.
	same := doc("email", text("same"))
	same.Name = "same"
	if got := mustInsert(t, d, reg, "account", same, sys("cli")).Name; got != "same" {
		t.Error(got)
	}
	different := doc("email", text("other"))
	different.Name = "nope"
	_, err := db.Insert(bg, d.Pool, reg, "account", different, sys("cli"))
	wantKind(t, err, db.ErrNaming)
	if got, err := db.Get(bg, d.Pool, reg, "account", "other", sys("cli")); err != nil || got != nil {
		t.Errorf("%v %v", got, err)
	}
}

// Rust: documents.rs::validation_errors_are_collected_and_nothing_is_written
func TestValidationErrorsAreCollectedAndNothingIsWritten(t *testing.T) {
	d, reg := setup(t, dbtest.Customer)
	_, err := db.Insert(bg, d.Pool, reg, "customer", doc(
		"email", text("   "), "bogus", core.Int(1), "tier", text("platinum"), "points", text("many")), sys("cli"))
	errs := validationOf(t, err)
	if len(errs) != 4 {
		t.Fatalf("%v", errs)
	}
	if !hasProblem(errs, core.ValidationError{Kind: core.ErrRequired, Field: "email"}) ||
		!hasProblem(errs, core.ValidationError{Kind: core.ErrUnknownField, Field: "bogus"}) {
		t.Errorf("%v", errs)
	}
	shown := err.Error()
	if !strings.Contains(shown, "bogus") || !strings.Contains(shown, "platinum") {
		t.Error(shown)
	}
	if count(t, d, "doc_customer") != 0 {
		t.Error("a document was written")
	}
	// A required field that is missing entirely.
	_, err = db.Insert(bg, d.Pool, reg, "customer", doc(), sys("cli"))
	if errs := validationOf(t, err); len(errs) != 1 {
		t.Error(errs)
	}
}

// Rust: documents.rs::invalid_actor_is_rejected
func TestInvalidActorIsRejected(t *testing.T) {
	d, reg := setup(t, dbtest.Customer)
	for _, actor := range []string{"", "a\x00b"} {
		_, err := db.Insert(bg, d.Pool, reg, "customer", fullCustomer("a@x.y"), sys(actor))
		wantKind(t, err, db.ErrInvalidActor)
		_, err = db.Update(bg, d.Pool, reg, "customer", "n", 1, changes("points", core.Int(1)), sys(actor))
		wantKind(t, err, db.ErrInvalidActor)
	}
	if count(t, d, "doc_customer") != 0 {
		t.Error("a document was written")
	}
}

// Rust: documents.rs::get_missing_is_none
func TestGetMissingIsNone(t *testing.T) {
	d, reg := setup(t, dbtest.Customer)
	got, err := db.Get(bg, d.Pool, reg, "customer", "nothing", sys("cli"))
	if err != nil || got != nil {
		t.Errorf("%v %v", got, err)
	}
}

// Rust: documents.rs::update_changes_values_and_bumps_version
func TestUpdateChangesValuesAndBumpsVersion(t *testing.T) {
	d, reg := setup(t, dbtest.Customer)
	created := mustInsert(t, d, reg, "customer", fullCustomer("u@example.com"), sys("alice"))
	time.Sleep(20 * time.Millisecond)

	c := changes("points", core.Int(7), "credit_limit", dec("0.5"), "notes", core.Null())
	updated := mustUpdate(t, d, reg, "customer", created.Name, 1, c, sys("bob"))
	if updated.Version != 2 || updated.ModifiedBy != "bob" || !updated.ModifiedAt.After(created.ModifiedAt) ||
		!updated.CreatedAt.Equal(created.CreatedAt) || updated.Owner != "alice" || updated.ID != created.ID {
		t.Errorf("%+v", updated)
	}
	if updated.Values["points"].Int != 7 || updated.Values["credit_limit"].Dec.String() != "0.50" ||
		updated.Values["notes"].Kind != core.KindNull {
		t.Errorf("%v", describeDoc(updated))
	}
	// Untouched fields stay.
	if updated.Values["tier"].Text != "gold" || !updated.Values["email"].Equal(created.Values["email"]) {
		t.Errorf("%v", describeDoc(updated))
	}
	sameDocument(t, mustGet(t, d, reg, "customer", created.Name, sys("cli")), updated, "fetched")
	third := mustUpdate(t, d, reg, "customer", created.Name, 2, c, sys("bob"))
	if third.Version != 3 {
		t.Error(third.Version)
	}
}

// Rust: documents.rs::update_rejects_bad_changes
func TestUpdateRejectsBadChanges(t *testing.T) {
	d, reg := setup(t, dbtest.Customer, namedAccount)
	created := mustInsert(t, d, reg, "customer", fullCustomer("v@example.com"), sys("cli"))

	// Nothing to change.
	_, err := db.Update(bg, d.Pool, reg, "customer", created.Name, 1, core.Changes{}, sys("cli"))
	wantKind(t, err, db.ErrNoChanges)
	// Validation (unknown field, required cleared).
	_, err = db.Update(bg, d.Pool, reg, "customer", created.Name, 1,
		changes("bogus", core.Int(1), "email", core.Null()), sys("cli"))
	if errs := validationOf(t, err); len(errs) != 2 {
		t.Error(errs)
	}

	// The naming field cannot change, even to the same value.
	named := mustInsert(t, d, reg, "account", doc("email", text("old")), sys("cli"))
	for _, v := range []string{"new", "old"} {
		_, err := db.Update(bg, d.Pool, reg, "account", "old", 1, changes("email", text(v)), sys("cli"))
		wantKind(t, err, db.ErrNaming)
	}
	sameDocument(t, mustGet(t, d, reg, "account", "old", sys("cli")), named, "named")

	// Unknown name.
	_, err = db.Update(bg, d.Pool, reg, "customer", "missing", 1, changes("points", core.Int(1)), sys("cli"))
	if e := wantKind(t, err, db.ErrNotFound); e.DocType != "customer" || e.Name != "missing" {
		t.Error(e)
	}
	// Nothing changed.
	sameDocument(t, mustGet(t, d, reg, "customer", created.Name, sys("cli")), created, "unchanged")
}

// Rust: documents.rs::delete_removes_the_document
func TestDeleteRemovesTheDocument(t *testing.T) {
	d, reg := setup(t, dbtest.Customer)
	stored := mustInsert(t, d, reg, "customer", fullCustomer("d@example.com"), sys("cli"))
	if err := db.Delete(bg, d.Pool, reg, "customer", stored.Name, 1, sys("ann")); err != nil {
		t.Fatal(err)
	}
	if got, err := db.Get(bg, d.Pool, reg, "customer", stored.Name, sys("cli")); err != nil || got != nil {
		t.Errorf("%v %v", got, err)
	}
	wantKind(t, db.Delete(bg, d.Pool, reg, "customer", stored.Name, 1, sys("ann")), db.ErrNotFound)
}

// Rust: documents.rs::duplicate_unique_values_are_unique_violations
func TestDuplicateUniqueValuesAreUniqueViolations(t *testing.T) {
	d, reg := setup(t, dbtest.Customer, namedAccount)
	mustInsert(t, d, reg, "customer", fullCustomer("dup@example.com"), sys("cli"))
	_, err := db.Insert(bg, d.Pool, reg, "customer", fullCustomer("dup@example.com"), sys("cli"))
	e := wantKind(t, err, db.ErrUniqueViolation)
	if !strings.Contains(e.Message, "unique") || !strings.Contains(e.Message, "23505") {
		t.Error(e.Message)
	}
	if count(t, d, "doc_customer") != 1 {
		t.Error("count")
	}
	// Update into a duplicate.
	other := mustInsert(t, d, reg, "customer", fullCustomer("other@example.com"), sys("cli"))
	_, err = db.Update(bg, d.Pool, reg, "customer", other.Name, 1, changes("email", text("dup@example.com")), sys("cli"))
	wantKind(t, err, db.ErrUniqueViolation)
	// Field naming: the same name twice.
	mustInsert(t, d, reg, "account", doc("email", text("n")), sys("cli"))
	_, err = db.Insert(bg, d.Pool, reg, "account", doc("email", text("n")), sys("cli"))
	wantKind(t, err, db.ErrUniqueViolation)
}

// Rust: documents.rs::injection_like_strings_are_stored_literally
func TestInjectionLikeStringsAreStoredLiterally(t *testing.T) {
	d, reg := setup(t, dbtest.Customer)
	nasty := []string{
		"x'); DROP TABLE doc_customer; --",
		"\"; DROP TABLE doc_customer; --",
		"$1 $2 ${x} \\' %s",
	}
	for i, value := range nasty {
		stored := mustInsert(t, d, reg, "customer",
			doc("email", text(fmt.Sprintf("%d@x.y", i)), "notes", text(value)), sys(value))
		if stored.Values["notes"].Text != value || stored.Owner != value {
			t.Errorf("%v", describeDoc(stored))
		}
		updated := mustUpdate(t, d, reg, "customer", stored.Name, 1, changes("notes", text(value)), sys(value))
		if updated.ModifiedBy != value {
			t.Error(updated.ModifiedBy)
		}
		// Looking up by a hostile name finds nothing and breaks nothing.
		if got, err := db.Get(bg, d.Pool, reg, "customer", value, sys("cli")); err != nil || got != nil {
			t.Errorf("%v %v", got, err)
		}
		wantKind(t, db.Delete(bg, d.Pool, reg, "customer", value, 1, sys("ann")), db.ErrNotFound)
	}
	if count(t, d, "doc_customer") != 3 {
		t.Error("count")
	}
}

// Rust: documents.rs::operations_in_a_rolled_back_transaction_leave_nothing
func TestOperationsInARolledBackTransactionLeaveNothing(t *testing.T) {
	d, reg := setup(t, dbtest.Customer)
	tx, err := d.Pool.Begin(bg)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := db.Insert(bg, tx, reg, "customer", fullCustomer("t@example.com"), sys("cli"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Update(bg, tx, reg, "customer", stored.Name, 1, changes("points", core.Int(9)), sys("cli")); err != nil {
		t.Fatal(err)
	}
	// Visible inside the transaction.
	if got, err := db.Get(bg, tx, reg, "customer", stored.Name, sys("cli")); err != nil || got == nil {
		t.Errorf("%v %v", got, err)
	}
	if err := tx.Rollback(bg); err != nil {
		t.Fatal(err)
	}
	if count(t, d, "doc_customer") != 0 {
		t.Error("a document survived the rollback")
	}

	// A committed transaction keeps the document.
	tx, err = d.Pool.Begin(bg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Insert(bg, tx, reg, "customer", fullCustomer("t@example.com"), sys("cli")); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(bg); err != nil {
		t.Fatal(err)
	}
	if count(t, d, "doc_customer") != 1 {
		t.Error("the committed document is missing")
	}
}

// Rust: documents.rs::stored_document_columns_match_the_table
func TestStoredDocumentColumnsMatchTheTable(t *testing.T) {
	// Documents the standard-column order of the generated DDL. Decoding does not depend on it
	// (every statement selects an explicit column list); this only pins the table layout.
	d, _ := setup(t, dbtest.Customer)
	var names []string
	for _, c := range d.Columns("doc_customer")[:7] {
		names = append(names, c[0])
	}
	want(t, names, []string{"id", "name", "owner", "created_at", "modified_at", "modified_by", "version"}, "columns")
}

// Rust: documents.rs::datetime_out_of_range_in_utc_is_an_error_not_a_panic
func TestDatetimeOutOfRangeInUTCIsAnErrorNotAPanic(t *testing.T) {
	d, reg := setup(t, dbtest.Customer)
	// 9999-12-31T23:00:00-05:00 is year 10000 in UTC.
	edge := core.Datetime(time.Date(9999, 12, 31, 23, 0, 0, 0, time.FixedZone("", -5*3600)))
	_, err := db.Insert(bg, d.Pool, reg, "customer", doc("email", text("e@x.y"), "last_seen", edge), sys("cli"))
	wantKind(t, err, db.ErrPostgres)
	if count(t, d, "doc_customer") != 0 {
		t.Error("a document was written")
	}
}

// Rust: documents.rs::unreadable_stored_values_give_an_error_naming_the_field
func TestUnreadableStoredValuesGiveAnErrorNamingTheField(t *testing.T) {
	d, reg := setup(t, dbtest.Customer)
	stored := mustInsert(t, d, reg, "customer", fullCustomer("raw@example.com"), sys("cli"))
	// Another writer stores a date the time package of the Rust implementation cannot represent.
	d.Exec("UPDATE doc_customer SET joined_on = date '5874897-12-31'")
	_, err := db.Get(bg, d.Pool, reg, "customer", stored.Name, sys("cli"))
	wantKind(t, err, db.ErrDecode)
	if m := err.Error(); !strings.Contains(m, "`joined_on`") || !strings.Contains(m, "`customer`") {
		t.Error(m)
	}
	// Same for timestamptz infinity.
	d.Exec("UPDATE doc_customer SET joined_on = NULL, last_seen = 'infinity'")
	_, err = db.Get(bg, d.Pool, reg, "customer", stored.Name, sys("cli"))
	if err == nil || !strings.Contains(err.Error(), "`last_seen`") {
		t.Error(err)
	}
}
