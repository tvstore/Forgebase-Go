package db_test

import (
	"strings"
	"testing"
	"time"

	"github.com/tvstore/forgebase-go/internal/core"
	"github.com/tvstore/forgebase-go/internal/db"
	"github.com/tvstore/forgebase-go/internal/db/dbtest"
)

// Link fields: resolution of names, errors, foreign key behaviour. Ported from
// crates/forgebase-db/tests/links.rs.

func linkSetup(t *testing.T) (*dbtest.DB, *core.Registry) {
	return setup(t, dbtest.Person, dbtest.SalesOrder)
}

func personDoc(email string) core.Document { return doc("email", text(email)) }

// orderDoc is a sales order with the required approver and the given text fields.
func orderDoc(kv ...string) core.Document {
	d := doc("approver", text("boss@x.y"))
	for i := 0; i < len(kv); i += 2 {
		d.Values[kv[i]] = text(kv[i+1])
	}
	return d
}

// Rust: links.rs::link_values_are_names_in_the_api_and_ids_in_the_table
func TestLinkValuesAreNamesInTheAPIAndIDsInTheTable(t *testing.T) {
	d, reg := linkSetup(t)
	ann := mustInsert(t, d, reg, "person", personDoc("ann@x.y"), sys("cli"))
	mustInsert(t, d, reg, "person", personDoc("boss@x.y"), sys("cli"))
	created := mustInsert(t, d, reg, "sales_order", orderDoc("customer", "ann@x.y", "title", "T"), sys("cli"))
	if created.Values["customer"].Text != "ann@x.y" || created.Values["approver"].Text != "boss@x.y" ||
		created.Values["follows"].Kind != core.KindNull {
		t.Errorf("%s", describeDoc(created))
	}
	sameDocument(t, mustGet(t, d, reg, "sales_order", created.Name, sys("cli")), created, "fetched")
	// The table holds the target's id, not its name.
	want(t, d.Rows("SELECT customer::text, pg_typeof(customer)::text FROM doc_sales_order"),
		[][]string{{ann.ID.String(), "uuid"}}, "stored")
}

// Rust: links.rs::missing_target_is_a_validation_error_and_nothing_is_written
func TestMissingTargetIsAValidationErrorAndNothingIsWritten(t *testing.T) {
	d, reg := linkSetup(t)
	mustInsert(t, d, reg, "person", personDoc("boss@x.y"), sys("cli"))
	_, err := db.Insert(bg, d.Pool, reg, "sales_order", orderDoc("customer", "nobody@x.y"), sys("cli"))
	errs := validationOf(t, err)
	want(t, errs, []core.ValidationError{{Kind: core.ErrLinkNotFound, Field: "customer", Target: "person", Name: "nobody@x.y"}}, "errors")
	want(t, err.Error(), "validation failed: field `customer`: person `nobody@x.y` does not exist", "text")
	if count(t, d, "doc_sales_order") != 0 {
		t.Error("written")
	}
	// Link errors are collected with the other problems, one per field, in one pass.
	bad := doc("customer", text("nobody@x.y"), "follows", text("no-such-order"), "bogus", core.Int(1))
	bad.Values["approver"] = text("missing@x.y")
	_, err = db.Insert(bg, d.Pool, reg, "sales_order", bad, sys("cli"))
	errs = validationOf(t, err)
	if len(errs) != 4 || !slicesAny(errs, func(e core.ValidationError) bool { return e.Kind == core.ErrUnknownField }) {
		t.Errorf("%v", errs)
	}
	if count(t, d, "doc_sales_order") != 0 {
		t.Error("written")
	}
}

func slicesAny[T any](s []T, f func(T) bool) bool {
	for _, v := range s {
		if f(v) {
			return true
		}
	}
	return false
}

// Rust: links.rs::values_that_failed_validation_are_not_looked_up
func TestValuesThatFailedValidationAreNotLookedUp(t *testing.T) {
	d, reg := linkSetup(t)
	mustInsert(t, d, reg, "person", personDoc("boss@x.y"), sys("cli"))
	// NUL cannot be sent to PostgreSQL; the validation error is reported and no query is made.
	_, err := db.Insert(bg, d.Pool, reg, "sales_order", orderDoc("customer", "a\x00b"), sys("cli"))
	if errs := validationOf(t, err); len(errs) != 1 || errs[0].Kind != core.ErrContainsNul {
		t.Errorf("%v", errs)
	}
	// A blank required link is "required", not "not found".
	_, err = db.Insert(bg, d.Pool, reg, "sales_order", doc("approver", text("  ")), sys("cli"))
	if errs := validationOf(t, err); len(errs) != 1 || errs[0].Kind != core.ErrRequired || errs[0].Field != "approver" {
		t.Errorf("%v", errs)
	}
	// A required link may not be missing, null or cleared.
	_, err = db.Insert(bg, d.Pool, reg, "sales_order", doc(), sys("cli"))
	wantKind(t, err, db.ErrValidation)
	created := mustInsert(t, d, reg, "sales_order", orderDoc(), sys("cli"))
	_, err = db.Update(bg, d.Pool, reg, "sales_order", created.Name, 1, changes("approver", core.Null()), sys("cli"))
	wantKind(t, err, db.ErrValidation)
}

// Rust: links.rs::optional_links_may_be_null_or_absent
func TestOptionalLinksMayBeNullOrAbsent(t *testing.T) {
	d, reg := linkSetup(t)
	mustInsert(t, d, reg, "person", personDoc("boss@x.y"), sys("cli"))
	withNull := orderDoc()
	withNull.Values["customer"] = core.Null()
	created := mustInsert(t, d, reg, "sales_order", withNull, sys("cli"))
	if created.Values["customer"].Kind != core.KindNull || created.Values["follows"].Kind != core.KindNull {
		t.Error(describeDoc(created))
	}
	want(t, d.Rows("SELECT (customer IS NULL AND follows IS NULL)::text FROM doc_sales_order"), [][]string{{"true"}}, "nulls")
}

// Rust: links.rs::updating_links_resolves_and_validates_names
func TestUpdatingLinksResolvesAndValidatesNames(t *testing.T) {
	d, reg := linkSetup(t)
	for _, email := range []string{"boss@x.y", "ann@x.y", "bob@x.y"} {
		mustInsert(t, d, reg, "person", personDoc(email), sys("cli"))
	}
	created := mustInsert(t, d, reg, "sales_order", orderDoc("customer", "ann@x.y"), sys("cli"))
	updated := mustUpdate(t, d, reg, "sales_order", created.Name, 1, changes("customer", text("bob@x.y")), sys("cli"))
	if updated.Version != 2 || updated.Values["customer"].Text != "bob@x.y" {
		t.Error(describeDoc(updated))
	}
	_, err := db.Update(bg, d.Pool, reg, "sales_order", created.Name, 2, changes("customer", text("nobody@x.y")), sys("cli"))
	if errs := validationOf(t, err); len(errs) != 1 || errs[0].Kind != core.ErrLinkNotFound {
		t.Errorf("%v", errs)
	}
	sameDocument(t, mustGet(t, d, reg, "sales_order", created.Name, sys("cli")), updated, "unchanged")
	updated = mustUpdate(t, d, reg, "sales_order", created.Name, 2, changes("customer", core.Null()), sys("cli"))
	if updated.Values["customer"].Kind != core.KindNull || updated.Version != 3 {
		t.Error(describeDoc(updated))
	}
}

// Rust: links.rs::self_links_work
func TestSelfLinksWork(t *testing.T) {
	d, reg := linkSetup(t)
	mustInsert(t, d, reg, "person", personDoc("boss@x.y"), sys("cli"))
	first := mustInsert(t, d, reg, "sales_order", orderDoc(), sys("cli"))
	second := mustInsert(t, d, reg, "sales_order", orderDoc("follows", first.Name), sys("cli"))
	if second.Values["follows"].Text != first.Name {
		t.Error(describeDoc(second))
	}
	// An update may point a document at itself.
	updated := mustUpdate(t, d, reg, "sales_order", first.Name, 1, changes("follows", text(first.Name)), sys("cli"))
	if updated.Values["follows"].Text != first.Name {
		t.Error(describeDoc(updated))
	}
}

// Rust: links.rs::deleting_a_linked_document_names_the_linking_doctype
func TestDeletingALinkedDocumentNamesTheLinkingDoctype(t *testing.T) {
	d, reg := linkSetup(t)
	boss := mustInsert(t, d, reg, "person", personDoc("boss@x.y"), sys("cli"))
	created := mustInsert(t, d, reg, "sales_order", orderDoc(), sys("cli"))

	err := db.Delete(bg, d.Pool, reg, "person", boss.Name, 1, sys("ann"))
	e := wantKind(t, err, db.ErrLinkedDocumentExists)
	if e.DocType != "person" || e.Name != "boss@x.y" || e.ReferencedBy == nil || *e.ReferencedBy != "sales_order" {
		t.Errorf("%+v", e)
	}
	want(t, err.Error(), "person `boss@x.y` cannot be deleted: it is linked from sales_order", "text")
	if count(t, d, "doc_person") != 1 {
		t.Error("deleted")
	}
	// A stale version is reported as a conflict before anything is attempted.
	wantKind(t, db.Delete(bg, d.Pool, reg, "person", boss.Name, 7, sys("ann")), db.ErrConflict)
	// Deleting the linking document first releases the target.
	if err := db.Delete(bg, d.Pool, reg, "sales_order", created.Name, 1, sys("ann")); err != nil {
		t.Fatal(err)
	}
	if err := db.Delete(bg, d.Pool, reg, "person", boss.Name, 1, sys("ann")); err != nil {
		t.Fatal(err)
	}
	if count(t, d, "doc_person") != 0 {
		t.Error("not deleted")
	}
}

// Rust: links.rs::a_linked_delete_in_a_transaction_fails_cleanly_and_leaves_it_usable
func TestALinkedDeleteInATransactionFailsCleanlyAndLeavesItUsable(t *testing.T) {
	d, reg := linkSetup(t)
	tx, err := d.Pool.Begin(bg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Insert(bg, tx, reg, "person", personDoc("boss@x.y"), sys("cli")); err != nil {
		t.Fatal(err)
	}
	// A target inserted earlier in the same transaction can be linked.
	if _, err := db.Insert(bg, tx, reg, "sales_order", orderDoc(), sys("cli")); err != nil {
		t.Fatal(err)
	}
	err = db.Delete(bg, tx, reg, "person", "boss@x.y", 1, sys("ann"))
	if e := wantKind(t, err, db.ErrLinkedDocumentExists); e.ReferencedBy == nil || *e.ReferencedBy != "sales_order" {
		t.Errorf("%+v", e)
	}
	// The failed delete ran in a savepoint: the transaction is not aborted and still has its work.
	if boss, err := db.Get(bg, tx, reg, "person", "boss@x.y", sys("cli")); err != nil || boss == nil {
		t.Errorf("%v %v", boss, err)
	}
	if err := tx.Rollback(bg); err != nil {
		t.Fatal(err)
	}
	if count(t, d, "doc_person") != 0 || count(t, d, "doc_sales_order") != 0 {
		t.Error("rows survived the rollback")
	}
}

// Rust: links.rs::a_link_inside_a_rolled_back_transaction_leaves_nothing
func TestALinkInsideARolledBackTransactionLeavesNothing(t *testing.T) {
	d, reg := linkSetup(t)
	tx, err := d.Pool.Begin(bg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Insert(bg, tx, reg, "person", personDoc("boss@x.y"), sys("cli")); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Insert(bg, tx, reg, "sales_order", orderDoc(), sys("cli")); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(bg); err != nil {
		t.Fatal(err)
	}
	if count(t, d, "doc_person") != 0 || count(t, d, "doc_sales_order") != 0 {
		t.Error("rows survived the rollback")
	}
	// The target no longer exists, so linking to it fails.
	_, err = db.Insert(bg, d.Pool, reg, "sales_order", orderDoc(), sys("cli"))
	wantKind(t, err, db.ErrValidation)
}

// Rust: links.rs::hostile_names_are_data_not_sql
func TestHostileNamesAreDataNotSQL(t *testing.T) {
	d, reg := linkSetup(t)
	nasty := "x'); DROP TABLE doc_person; --"
	mustInsert(t, d, reg, "person", personDoc("boss@x.y"), sys("cli"))
	_, err := db.Insert(bg, d.Pool, reg, "sales_order", orderDoc("customer", nasty), sys("cli"))
	wantKind(t, err, db.ErrValidation)
	// The same string works as the name of a real target.
	mustInsert(t, d, reg, "person", personDoc(nasty), sys("cli"))
	created := mustInsert(t, d, reg, "sales_order", orderDoc("customer", nasty), sys("cli"))
	if created.Values["customer"].Text != nasty {
		t.Error(describeDoc(created))
	}
	if count(t, d, "doc_person") != 2 {
		t.Error("count")
	}
}

// Rust: links.rs::link_target_with_random_naming_is_addressed_by_its_generated_name
func TestLinkTargetWithRandomNamingIsAddressedByItsGeneratedName(t *testing.T) {
	d, reg := setup(t,
		"name = \"note\"\nlabel = \"N\"\n",
		"name = \"tag\"\nlabel = \"T\"\n[[fields]]\nname = \"note\"\ntype = \"link\"\ntarget = \"note\"\n")
	n := mustInsert(t, d, reg, "note", doc(), sys("cli"))
	tag := mustInsert(t, d, reg, "tag", doc("note", text(n.Name)), sys("cli"))
	if tag.Values["note"].Text != n.Name {
		t.Error(describeDoc(tag))
	}
}

const badge = `
name = "badge"
label = "Badge"
[[fields]]
name = "holder"
type = "link"
target = "person"
unique = true
`

// Rust: links.rs::a_duplicate_unique_link_names_the_field_not_the_internal_id
func TestADuplicateUniqueLinkNamesTheFieldNotTheInternalID(t *testing.T) {
	d, reg := setup(t, dbtest.Person, badge)
	ann := mustInsert(t, d, reg, "person", personDoc("ann@x.y"), sys("cli"))
	mustInsert(t, d, reg, "badge", doc("holder", text(ann.Name)), sys("cli"))
	_, err := db.Insert(bg, d.Pool, reg, "badge", doc("holder", text(ann.Name)), sys("cli"))
	e := wantKind(t, err, db.ErrUniqueViolation)
	if !strings.Contains(e.Message, "`holder`") {
		t.Error(e.Message)
	}
	if strings.Contains(e.Message, "detail") || strings.Contains(e.Message, "Key (") {
		t.Error(e.Message)
	}
	if id := d.Rows("SELECT id::text FROM doc_person")[0][0]; strings.Contains(e.Message, id) {
		t.Error(e.Message)
	}
	if count(t, d, "doc_badge") != 1 {
		t.Error("count")
	}
}

// Rust: links.rs::the_link_lookup_lock_blocks_deleting_the_target_until_the_transaction_ends
func TestTheLinkLookupLockBlocksDeletingTheTargetUntilTheTransactionEnds(t *testing.T) {
	d, reg := linkSetup(t)
	boss := mustInsert(t, d, reg, "person", personDoc("boss@x.y"), sys("cli"))
	first, err := d.Pool.Acquire(bg)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Release()
	second, err := d.Pool.Acquire(bg)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Release()
	if _, err := second.Exec(bg, "SET lock_timeout = '300ms'"); err != nil {
		t.Fatal(err)
	}
	// The first transaction links to the boss: the lookup holds FOR KEY SHARE on the row.
	tx, err := first.Begin(bg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Insert(bg, tx, reg, "sales_order", orderDoc(), sys("cli")); err != nil {
		t.Fatal(err)
	}
	// A concurrent delete of the target waits and then times out.
	wantKind(t, db.Delete(bg, second, reg, "person", boss.Name, 1, sys("ann")), db.ErrLockTimeout)
	if count(t, d, "doc_person") != 1 {
		t.Error("deleted")
	}
	// After the rollback nothing links to the boss, so the delete succeeds.
	if err := tx.Rollback(bg); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := db.Delete(bg, second, reg, "person", boss.Name, 1, sys("ann")); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 5*time.Second {
		t.Error("the delete waited")
	}
	if count(t, d, "doc_person") != 0 {
		t.Error("not deleted")
	}
}
