package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tvstore/forgebase-go/internal/core"
	"github.com/tvstore/forgebase-go/internal/db"
)

// Ported from the unit tests of crates/forgebase-http/src/error.rs: the bounds of an error body and the
// mapping of the errors of the layers below.

func newBudget() *budget { b := budget(maxErrorBodyBytes); return &b }

func jsonSize(t *testing.T, v any) int {
	t.Helper()
	b, err := marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return len(b)
}

// Rust: error.rs::bound_tests::a_long_message_is_511_characters_and_an_ellipsis
func TestALongMessageIs511CharactersAndAnEllipsis(t *testing.T) {
	if got := cut("short", newBudget()); got != "short" {
		t.Error(got)
	}
	exactly := strings.Repeat("é", maxMessageChars)
	if got := cut(exactly, newBudget()); got != exactly {
		t.Error("512 characters are kept whole")
	}
	got := cut(strings.Repeat("é", 5000), newBudget())
	runes := []rune(got)
	if len(runes) != maxMessageChars || runes[len(runes)-1] != '…' || strings.Count(got, "é") != maxMessageChars-1 {
		t.Errorf("%d characters", len(runes))
	}
}

// Rust: error.rs::bound_tests::control_bytes_and_four_byte_characters_are_counted_as_they_are_serialized
func TestControlBytesAndFourByteCharactersAreCountedAsTheyAreSerialized(t *testing.T) {
	controls := strings.Repeat("\x01", 10000)
	out := cut(controls, newBudget())
	if n := len([]rune(out)); n != maxMessageChars {
		t.Errorf("%d characters", n)
	}
	if n := jsonSize(t, out); n > 6*maxMessageChars+8 {
		t.Errorf("%d bytes", n)
	}
	wide := strings.Repeat("\U0001F600", 10000)
	if n := len([]rune(cut(wide, newBudget()))); n != maxMessageChars {
		t.Errorf("%d characters", n)
	}
	// A short budget cuts earlier.
	small := budget(100)
	if n := jsonSize(t, cut(controls, &small)); n > 100 {
		t.Errorf("the budget does not hold: %d bytes", n)
	}
}

// Rust: error.rs::bound_tests::a_long_list_keeps_a_hundred_entries_and_counts_the_rest
func TestALongListKeepsAHundredEntriesAndCountsTheRest(t *testing.T) {
	many := make([]string, 1_000_000)
	for i := range many {
		many[i] = "x"
	}
	got := capped(many, newBudget())
	if len(got) != maxReportedErrors+1 || got[len(got)-1] != "and 999900 more" {
		t.Errorf("%d entries, last %q", len(got), got[len(got)-1])
	}
	few := capped([]string{"a", "b"}, newBudget())
	if strings.Join(few, ",") != "a,b" {
		t.Error(few)
	}
	// Long entries run out of the budget before the count.
	long := make([]string, 1000)
	for i := range long {
		long[i] = strings.Repeat("x", 2000)
	}
	got = capped(long, newBudget())
	if len(got) > maxReportedErrors+1 || !strings.HasPrefix(got[len(got)-1], "and ") {
		t.Errorf("%d entries", len(got))
	}
	if n := jsonSize(t, got); n > maxErrorBodyBytes+100 {
		t.Errorf("%d bytes", n)
	}
}

// Rust: error.rs::bound_tests::a_response_body_is_small_whatever_the_error_holds
func TestAResponseBodyIsSmallWhateverTheErrorHolds(t *testing.T) {
	details := make([]string, 1000)
	for i := range details {
		details[i] = strings.Repeat("z", 1_000_000)
	}
	e := errValidation(details)
	e.message = strings.Repeat("m", 1_000_000)
	rec := httptest.NewRecorder()
	e.write(rec)
	if n := rec.Body.Len(); n >= maxErrorBodyBytes+2000 {
		t.Errorf("%d bytes", n)
	}
	if rec.Code != http.StatusUnprocessableEntity {
		t.Error(rec.Code)
	}
}

// The envelope without details has no details key, and sorts its keys like serde_json.
func TestTheEnvelopeShape(t *testing.T) {
	rec := httptest.NewRecorder()
	errNotFound("no such route").write(rec)
	if got := rec.Body.String(); got != `{"error":{"code":"not_found","message":"no such route"}}` {
		t.Error(got)
	}
	rec = httptest.NewRecorder()
	errValidation([]string{"a", "b"}).write(rec)
	if got := rec.Body.String(); got != `{"error":{"code":"validation_failed","details":["a","b"],"message":"validation failed"}}` {
		t.Error(got)
	}
}

// Rust: error.rs::lock_tests::a_busy_lock_is_a_503_with_retry_after
func TestABusyLockIsA503WithRetryAfter(t *testing.T) {
	e := fromDB(&db.Error{Kind: db.ErrLockTimeout})
	if e.status != http.StatusServiceUnavailable || e.code != "lock_timeout" {
		t.Error(e)
	}
	rec := httptest.NewRecorder()
	e.write(rec)
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") != "5" {
		t.Error(rec.Header())
	}
}

// The mapping of the errors of internal/db to status and code (the Rust impl From<DbError>).
func TestTheMappingOfDatabaseErrors(t *testing.T) {
	for _, c := range []struct {
		err    *db.Error
		status int
		code   string
	}{
		{&db.Error{Kind: db.ErrNotFound, DocType: "d", Name: "n"}, 404, "not_found"},
		{&db.Error{Kind: db.ErrUnknownDocType, Name: "d"}, 404, "not_found"},
		{&db.Error{Kind: db.ErrChildDocType, DocType: "d"}, 404, "not_found"},
		{&db.Error{Kind: db.ErrPermissionDenied, DocType: "d", Right: core.RightRead}, 403, "permission_denied"},
		{&db.Error{Kind: db.ErrEditDenied, DocType: "d", State: "s"}, 403, "permission_denied"},
		{&db.Error{Kind: db.ErrWorkflowRequired, DocType: "d", Operation: "submit"}, 409, "workflow_required"},
		{&db.Error{Kind: db.ErrInvalidQuery, Message: "m"}, 400, "invalid_query"},
		{&db.Error{Kind: db.ErrNoChanges}, 400, "bad_request"},
		{&db.Error{Kind: db.ErrConflict}, 409, "conflict"},
		{&db.Error{Kind: db.ErrChangedWhileReading}, 409, "conflict"},
		{&db.Error{Kind: db.ErrInvalidDocStatus}, 409, "invalid_docstatus"},
		{&db.Error{Kind: db.ErrAlreadyAmended}, 409, "already_amended"},
		{&db.Error{Kind: db.ErrFieldLocked}, 409, "field_locked"},
		{&db.Error{Kind: db.ErrLinkedDocumentExists}, 409, "linked_document_exists"},
		{&db.Error{Kind: db.ErrUniqueViolation, Message: "secret constraint"}, 409, "unique_violation"},
		{&db.Error{Kind: db.ErrNotSubmittable}, 422, "not_submittable"},
		{&db.Error{Kind: db.ErrHook, Hook: "validate", Message: "no"}, 422, "hook_rejected"},
		{&db.Error{Kind: db.ErrNaming, Message: "bad name"}, 422, "validation_failed"},
		{&db.Error{Kind: db.ErrPostgres, Message: "relation doc_x does not exist"}, 500, "internal"},
		{&db.Error{Kind: db.ErrJob, Message: "j"}, 500, "internal"},
		{&db.Error{Kind: db.ErrDecode}, 500, "internal"},
		{&db.Error{Kind: db.ErrPool, Message: "closed"}, 500, "internal"},
	} {
		logMu.Lock()
		old := logWriter
		logWriter = &strings.Builder{}
		logMu.Unlock()
		got := fromError(c.err)
		logMu.Lock()
		logWriter = old
		logMu.Unlock()
		if got.status != c.status || got.code != c.code {
			t.Errorf("%v: %d %s, want %d %s", c.err, got.status, got.code, c.status, c.code)
		}
		if got.code == "unique_violation" && strings.Contains(got.message, "secret") {
			t.Error("the constraint leaks")
		}
		if got.code == "internal" && got.message != "internal server error" {
			t.Errorf("message %q", got.message)
		}
	}
	// Auth errors.
	if e := fromError(&db.AuthError{Kind: db.AuthInvalidCredentials}); e.status != 401 || e.message != "invalid username or password" {
		t.Error(e)
	}
	if e := fromError(&db.AuthError{Kind: db.AuthDB, Err: &db.Error{Kind: db.ErrLockTimeout}}); e.code != "lock_timeout" {
		t.Error(e)
	}
	// A field the caller can read but not write is field_not_permitted.
	e := fromError(&db.Error{Kind: db.ErrValidation, Validation: []core.ValidationError{{Kind: core.ErrNotPermitted, Field: "f"}}})
	if e.code != "field_not_permitted" || e.status != 422 || len(e.details) != 1 {
		t.Error(e)
	}
}
