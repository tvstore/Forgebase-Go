package db_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/tvstore/forgebase-go/internal/core"
	"github.com/tvstore/forgebase-go/internal/db"
	"github.com/tvstore/forgebase-go/internal/db/dbtest"
)

// Optimistic concurrency: expected_version on update and delete. Ported from
// crates/forgebase-db/tests/concurrency.rs.

func customerWith(email string) core.Document { return doc("email", text(email)) }

func points(n int64) core.Changes { return changes("points", core.Int(n)) }

// Rust: concurrency.rs::update_with_a_stale_version_is_a_conflict_and_changes_nothing
func TestUpdateWithAStaleVersionIsAConflictAndChangesNothing(t *testing.T) {
	d, reg := setup(t, dbtest.Customer)
	created := mustInsert(t, d, reg, "customer", customerWith("a@x.y"), sys("cli"))
	v2 := mustUpdate(t, d, reg, "customer", created.Name, 1, points(1), sys("cli"))
	if v2.Version != 2 {
		t.Error(v2.Version)
	}
	// Reusing the old version fails and reports the current one.
	_, err := db.Update(bg, d.Pool, reg, "customer", created.Name, 1, points(2), sys("cli"))
	e := wantKind(t, err, db.ErrConflict)
	if e.DocType != "customer" || e.Name != created.Name || e.ExpectedVersion != 1 || e.CurrentVersion != 2 {
		t.Errorf("%+v", e)
	}
	want(t, err.Error(), fmt.Sprintf("customer `%s` was changed by someone else: expected version 1, current version 2", created.Name), "message")
	// A version from the future is a conflict too.
	_, err = db.Update(bg, d.Pool, reg, "customer", created.Name, 9, points(2), sys("cli"))
	if e := wantKind(t, err, db.ErrConflict); e.ExpectedVersion != 9 || e.CurrentVersion != 2 {
		t.Errorf("%+v", e)
	}
	sameDocument(t, mustGet(t, d, reg, "customer", created.Name, sys("cli")), v2, "unchanged")
}

// Rust: concurrency.rs::delete_with_a_stale_version_is_a_conflict_and_deletes_nothing
func TestDeleteWithAStaleVersionIsAConflictAndDeletesNothing(t *testing.T) {
	d, reg := setup(t, dbtest.Customer)
	created := mustInsert(t, d, reg, "customer", customerWith("d@x.y"), sys("cli"))
	mustUpdate(t, d, reg, "customer", created.Name, 1, points(1), sys("cli"))
	err := db.Delete(bg, d.Pool, reg, "customer", created.Name, 1, sys("ann"))
	if e := wantKind(t, err, db.ErrConflict); e.ExpectedVersion != 1 || e.CurrentVersion != 2 {
		t.Errorf("%+v", e)
	}
	if got, err := db.Get(bg, d.Pool, reg, "customer", created.Name, sys("cli")); err != nil || got == nil {
		t.Errorf("%v %v", got, err)
	}
	if err := db.Delete(bg, d.Pool, reg, "customer", created.Name, 2, sys("ann")); err != nil {
		t.Fatal(err)
	}
}

// Rust: concurrency.rs::a_missing_document_is_not_found_not_a_conflict
func TestAMissingDocumentIsNotFoundNotAConflict(t *testing.T) {
	d, reg := setup(t, dbtest.Customer)
	_, err := db.Update(bg, d.Pool, reg, "customer", "nobody", 1, points(1), sys("cli"))
	wantKind(t, err, db.ErrNotFound)
	wantKind(t, db.Delete(bg, d.Pool, reg, "customer", "nobody", 1, sys("ann")), db.ErrNotFound)
}

// Rust: concurrency.rs::a_conflict_inside_a_transaction_does_not_abort_it
func TestAConflictInsideATransactionDoesNotAbortIt(t *testing.T) {
	d, reg := setup(t, dbtest.Customer)
	tx, err := d.Pool.Begin(bg)
	if err != nil {
		t.Fatal(err)
	}
	created, err := db.Insert(bg, tx, reg, "customer", customerWith("t@x.y"), sys("cli"))
	if err != nil {
		t.Fatal(err)
	}
	// The failed update ran in a savepoint, so the transaction stays usable.
	_, err = db.Update(bg, tx, reg, "customer", created.Name, 5, points(1), sys("cli"))
	wantKind(t, err, db.ErrConflict)
	if _, err := db.Update(bg, tx, reg, "customer", created.Name, 1, points(1), sys("cli")); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(bg); err != nil {
		t.Fatal(err)
	}
}

// Rust: concurrency.rs::a_second_writer_waits_then_gets_a_conflict
func TestASecondWriterWaitsThenGetsAConflict(t *testing.T) {
	d, reg := setup(t, dbtest.Customer)
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
	created, err := db.Insert(bg, first, reg, "customer", customerWith("c@x.y"), sys("cli"))
	if err != nil {
		t.Fatal(err)
	}
	// The first writer updates inside a transaction and has not committed yet.
	tx, err := first.Begin(bg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Update(bg, tx, reg, "customer", created.Name, 1, points(1), sys("one")); err != nil {
		t.Fatal(err)
	}
	// The second writer, holding the same version, blocks on the row lock ...
	done := make(chan error, 1)
	go func() {
		_, err := db.Update(bg, second, reg, "customer", created.Name, 1, points(2), sys("two"))
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("the second update should wait for the first transaction: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	// ... and once the first commits, the version no longer matches.
	if err := tx.Commit(bg); err != nil {
		t.Fatal(err)
	}
	if e := wantKind(t, <-done, db.ErrConflict); e.ExpectedVersion != 1 || e.CurrentVersion != 2 {
		t.Errorf("%+v", e)
	}
	stored := mustGet(t, d, reg, "customer", created.Name, sys("cli"))
	if stored.Version != 2 || stored.ModifiedBy != "one" || stored.Values["points"].Int != 1 {
		t.Error(describeDoc(stored))
	}
}

// Rust: concurrency.rs::racing_updates_with_the_same_version_succeed_exactly_once
func TestRacingUpdatesWithTheSameVersionSucceedExactlyOnce(t *testing.T) {
	d, reg := setup(t, dbtest.Customer)
	for round := range 10 {
		created := mustInsert(t, d, reg, "customer", customerWith(fmt.Sprintf("race%d@x.y", round)), sys("cli"))
		outcomes := race(
			func() error {
				_, err := db.Update(bg, d.Pool, reg, "customer", created.Name, 1, points(1), sys("a"))
				return err
			},
			func() error {
				_, err := db.Update(bg, d.Pool, reg, "customer", created.Name, 1, points(2), sys("b"))
				return err
			})
		ok := countErrors(outcomes, func(e error) bool { return e == nil })
		conflicts := countErrors(outcomes, func(e error) bool {
			var de *db.Error
			return e != nil && errorsAs(e, &de) && de.Kind == db.ErrConflict && de.CurrentVersion == 2
		})
		if ok != 1 || conflicts != 1 {
			t.Errorf("round %d: %v", round, outcomes)
		}
		if got := mustGet(t, d, reg, "customer", created.Name, sys("cli")).Version; got != 2 {
			t.Errorf("round %d: version %d", round, got)
		}
	}
}

// Rust: concurrency.rs::racing_delete_and_update_with_the_same_version_succeed_exactly_once
func TestRacingDeleteAndUpdateWithTheSameVersionSucceedExactlyOnce(t *testing.T) {
	d, reg := setup(t, dbtest.Customer)
	created := mustInsert(t, d, reg, "customer", customerWith("dr@x.y"), sys("cli"))
	outcomes := race(
		func() error {
			_, err := db.Update(bg, d.Pool, reg, "customer", created.Name, 1, points(1), sys("a"))
			return err
		},
		func() error { return db.Delete(bg, d.Pool, reg, "customer", created.Name, 1, sys("ann")) })
	// Either the update won (the delete then sees version 2) or the delete won (the update then
	// finds nothing); never both.
	updated, deleted := outcomes[0], outcomes[1]
	var de *db.Error
	updateWon := updated == nil && errorsAs(deleted, &de) && de.Kind == db.ErrConflict && de.CurrentVersion == 2
	deleteWon := deleted == nil && errorsAs(updated, &de) && de.Kind == db.ErrNotFound
	if !updateWon && !deleteWon {
		t.Errorf("unexpected outcome: update %v, delete %v", updated, deleted)
	}
}
