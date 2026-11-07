package app

import (
	"errors"
	"fmt"
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/tvstore/forgebase-go/internal/db"
)

// Unit tests of JobOptions, JobError and the classification of errors. Ported from the #[cfg(test)] module of
// crates/forgebase/src/jobs.rs.

// Rust: jobs.rs::the_defaults_are_the_documented_ones
func TestTheJobDefaultsAreTheDocumentedOnes(t *testing.T) {
	o := NewJobOptions()
	if o.maxAttempts != 5 || o.timeout != 300*time.Second || o.backoffBase != 30*time.Second || o.backoffCap != time.Hour {
		t.Errorf("%+v", o)
	}
	if got := o.problems(); len(got) != 0 {
		t.Error(got)
	}
}

// Rust: jobs.rs::backoff_doubles_from_the_base_and_is_capped
func TestBackoffDoublesFromTheBaseAndIsCapped(t *testing.T) {
	o := NewJobOptions()
	var seconds []int
	for attempt := 1; attempt <= 9; attempt++ {
		seconds = append(seconds, int(o.backoffAfter(attempt).Seconds()))
	}
	if want := []int{30, 60, 120, 240, 480, 960, 1920, 3600, 3600}; !reflect.DeepEqual(seconds, want) {
		t.Errorf("%v", seconds)
	}
	// No overflow, however many attempts.
	if got := o.backoffAfter(math.MaxInt); got != time.Hour {
		t.Error(got)
	}
	big := NewJobOptions().Backoff(math.MaxInt64/2, math.MaxInt64)
	if got := big.backoffAfter(40); got != math.MaxInt64 {
		t.Error(got)
	}
	// attempt 0 is not used, but must not panic.
	if got := o.backoffAfter(0); got != 30*time.Second {
		t.Error(got)
	}
}

// Rust: jobs.rs::invalid_options_are_listed
func TestInvalidJobOptionsAreListed(t *testing.T) {
	o := NewJobOptions().MaxAttempts(0).Timeout(0).Backoff(10*time.Second, 5*time.Second)
	if got := o.problems(); len(got) != 3 {
		t.Error(got)
	}
	if got := NewJobOptions().Backoff(0, 0).problems(); len(got) != 1 {
		t.Error(got)
	}
	// The zero JobOptions is not the defaults: it is refused, not silently completed.
	if got := (JobOptions{}).problems(); len(got) != 3 {
		t.Error(got)
	}
}

// Rust: jobs.rs::options_above_the_upper_bounds_are_listed
func TestJobOptionsAboveTheUpperBoundsAreListed(t *testing.T) {
	day := 24 * time.Hour
	base := time.Second
	for _, ok := range []JobOptions{
		NewJobOptions().MaxAttempts(maxJobAttempts),
		NewJobOptions().Timeout(day),
		NewJobOptions().Backoff(base, 30*day),
	} {
		if got := ok.problems(); len(got) != 0 {
			t.Errorf("%+v: %v", ok, got)
		}
	}
	for _, c := range []struct {
		options JobOptions
		want    []string
	}{
		{NewJobOptions().MaxAttempts(maxJobAttempts + 1), []string{"max_attempts must be at most 1000"}},
		{NewJobOptions().Timeout(day + time.Second), []string{"timeout must be at most 24 hours"}},
		{NewJobOptions().Backoff(base, 30*day+time.Second), []string{"the backoff cap must be at most 30 days"}},
		// A base above the cap is refused (and a huge base above a huge cap as well).
		{NewJobOptions().Backoff(day, base), []string{"the backoff cap must be at least the backoff base"}},
	} {
		if got := c.options.problems(); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%v, want %v", got, c.want)
		}
	}
	if got := NewJobOptions().Timeout(math.MaxInt64).Backoff(math.MaxInt64, math.MaxInt64).problems(); len(got) != 2 {
		t.Error(got)
	}
}

// Rust: jobs.rs::database_errors_retry_and_refusals_do_not
func TestDatabaseErrorsRetryAndRefusalsDoNot(t *testing.T) {
	for _, kind := range []db.ErrorKind{db.ErrLockTimeout, db.ErrPostgres, db.ErrPool, db.ErrCreatePool, db.ErrChangedWhileReading} {
		if fromDB(&db.Error{Kind: kind}).Permanent {
			t.Errorf("kind %d is permanent", kind)
		}
	}
	for _, e := range []*db.Error{
		{Kind: db.ErrNotFound, DocType: "a", Name: "b"},
		{Kind: db.ErrConflict, DocType: "a", Name: "b", ExpectedVersion: 1, CurrentVersion: 2},
		{Kind: db.ErrNoChanges},
		{Kind: db.ErrValidation},
		{Kind: db.ErrJob, Message: "x"},
		{Kind: db.ErrUniqueViolation, Message: "x"},
		{Kind: db.ErrPermissionDenied},
	} {
		if !fromDB(e).Permanent {
			t.Errorf("%v is retryable", e)
		}
	}
	if e := NewPermanentJobError("x"); e.Message != "x" || !e.IsPermanent() {
		t.Error(e)
	}
	if e := NewJobError("y"); e.Error() != "y" || e.IsPermanent() {
		t.Error(e)
	}
}

func TestAHandlerErrorIsClassifiedByWhatItWraps(t *testing.T) {
	permanent := NewPermanentJobError("no")
	for _, c := range []struct {
		err       error
		permanent bool
		message   string
	}{
		{permanent, true, "no"},
		{fmt.Errorf("context: %w", permanent), true, "no"},
		{errors.New("plain"), false, "plain"},
		{&DocError{Err: &db.Error{Kind: db.ErrLockTimeout}}, false, ""},
		{fmt.Errorf("wrapped: %w", &DocError{Err: &db.Error{Kind: db.ErrNoChanges}}), true, "update needs at least one changed field"},
		{&db.Error{Kind: db.ErrNotFound, DocType: "a", Name: "b"}, true, "a `b` not found"},
	} {
		got := jobFailure(c.err)
		if got.Permanent != c.permanent || (c.message != "" && got.Message != c.message) {
			t.Errorf("%v: %+v", c.err, got)
		}
	}
}

func TestAnErrorThatIsNotADatabaseErrorBecomesOne(t *testing.T) {
	got := asDBError(errors.New("context canceled"))
	if got.Kind != db.ErrPostgres || got.Message != "context canceled" {
		t.Errorf("%+v", got)
	}
	original := &db.Error{Kind: db.ErrJob}
	if asDBError(fmt.Errorf("x: %w", original)) != original {
		t.Error("a database error was not found behind a wrap")
	}
}
