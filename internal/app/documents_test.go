package app

import (
	"strings"
	"testing"

	"github.com/tvstore/forgebase-go/internal/core"
	"github.com/tvstore/forgebase-go/internal/db"
)

// Unit tests of Documents (no database). Ported from the #[cfg(test)] module of
// crates/forgebase/src/documents.rs.

// Rust: documents.rs::a_doc_error_maps_to_a_job_error_as_the_database_error_did
func TestADocErrorMapsToAJobErrorAsTheDatabaseErrorDid(t *testing.T) {
	retryable := docError(&db.Error{Kind: db.ErrLockTimeout}).(*DocError)
	if !retryable.IsRetryable() || jobFailure(retryable).Permanent {
		t.Error("a lock timeout is not retryable")
	}
	for _, e := range []*db.Error{
		{Kind: db.ErrNoChanges},
		{Kind: db.ErrValidation},
		{Kind: db.ErrNotFound, DocType: "a", Name: "b"},
	} {
		de := docError(e).(*DocError)
		if de.IsRetryable() {
			t.Errorf("%v is retryable", de)
		}
		job := jobFailure(de)
		if !job.Permanent || job.Message != de.Error() {
			t.Errorf("%+v for %q", job, de.Error())
		}
	}
	if docError(nil) != nil {
		t.Error("nil became an error")
	}
}

// Rust: documents.rs::a_bad_like_filter_is_an_invalid_query_with_the_old_message
func TestABadLikeFilterIsAnInvalidQueryWithTheOldMessage(t *testing.T) {
	_, err := NewListQuery().Filter("title", FilterLike, core.Int(1)).toDB()
	de, ok := err.(*DocError)
	if !ok || de.IsRetryable() {
		t.Fatalf("%v", err)
	}
	if de.Error() != "invalid list query: the `like` filter on `title` needs a text value" {
		t.Errorf("%q", de.Error())
	}
	if de.Err.Kind != db.ErrInvalidQuery {
		t.Errorf("%v", de.Err.Kind)
	}
}

func TestAListQueryIsAValueEachMethodCopies(t *testing.T) {
	base := NewListQuery().Filter("a", FilterEq, core.Text("x"))
	one := base.Filter("b", FilterGt, core.Int(1))
	two := base.OrderBy("a", true).Limit(7)
	q1, err := one.toDB()
	if err != nil {
		t.Fatal(err)
	}
	q2, err := two.toDB()
	if err != nil {
		t.Fatal(err)
	}
	if len(q1.Filters) != 2 || len(q1.Sort) != 0 || q1.Limit != nil {
		t.Errorf("%+v", q1)
	}
	if len(q2.Filters) != 1 || len(q2.Sort) != 1 || !q2.Sort[0].Descending || q2.Limit == nil || *q2.Limit != 7 {
		t.Errorf("%+v", q2)
	}
	if q, err := base.toDB(); err != nil || len(q.Filters) != 1 {
		t.Errorf("the base changed: %+v %v", q, err)
	}
	if _, err := (ListQuery{filters: []listFilter{{"f", 0, core.Text("x")}}}).toDB(); err == nil || !strings.Contains(err.Error(), "no operator") {
		t.Errorf("%v", err)
	}
	for op, kind := range map[FilterOp]db.FilterOpKind{FilterEq: db.OpEq, FilterNe: db.OpNe, FilterLt: db.OpLt, FilterLe: db.OpLe, FilterGt: db.OpGt, FilterGe: db.OpGe} {
		q, err := NewListQuery().Filter("f", op, core.Int(1)).toDB()
		if err != nil || q.Filters[0].Op.Kind != kind {
			t.Errorf("%d: %+v %v", op, q, err)
		}
	}
	q, err := NewListQuery().Filter("f", FilterLike, core.Text("a%")).toDB()
	if err != nil || q.Filters[0].Op.Kind != db.OpLike || q.Filters[0].Op.Pattern != "a%" {
		t.Errorf("%+v %v", q, err)
	}
}
