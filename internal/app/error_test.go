package app

import (
	"errors"
	"testing"

	"github.com/tvstore/forgebase-go/internal/core"
	"github.com/tvstore/forgebase-go/internal/db"
)

// Rust: error.rs::validation_errors_are_listed_one_per_line
func TestValidationErrorsAreListedOnePerLine(t *testing.T) {
	err := &db.Error{Kind: db.ErrValidation, Validation: []core.ValidationError{
		{Kind: core.ErrRequired, Field: "a"},
		{Kind: core.ErrUnknownField, Field: "b"},
	}}
	want := "validation failed:\n  - field `a` is required\n  - unknown field `b`"
	if got := describeError(err); got != want {
		t.Fatalf("%q", got)
	}
}

// error.rs::report_problems_are_listed_and_say_what_to_do needs db.ErrReportCheck, which arrives with
// the reports (P6).

func TestSyncConflictsAndMetadataAreListed(t *testing.T) {
	err := &db.Error{Kind: db.ErrSyncConflicts, Conflicts: []db.Conflict{{Kind: db.ConflictUnmanagedTable, Table: "doc_x"}}}
	want := "schema sync refused, 1 conflict(s); nothing was changed:\n  - table doc_x already exists but is not managed by Forgebase (not adopted)"
	if got := describeError(err); got != want {
		t.Fatalf("%q", got)
	}
	meta := &db.Error{Kind: db.ErrInvalidMetadata, Problems: []core.DefinitionError{
		{Kind: core.ErrUnusedChild, DocType: "row"},
	}}
	if got := describeError(meta); got != "invalid DocType metadata:\n  - child doctype `row` is not used by any table field" {
		t.Fatalf("%q", got)
	}
}

func TestOtherErrorsKeepTheirText(t *testing.T) {
	if got := describeError(errf("plain %d", 1)); got != "plain 1" {
		t.Fatal(got)
	}
	notFound := &db.Error{Kind: db.ErrNotFound, DocType: "customer", Name: "x"}
	if got := describeError(notFound); got != "customer `x` not found" {
		t.Fatal(got)
	}
	// A wrapped database error is still described as one.
	wrapped := errSite("acme", notFound)
	if got := describeError(wrapped); got != "site `acme`: customer `x` not found" {
		t.Fatal(got)
	}
	if isUsage(wrapped) || !isUsage(errUsage("x")) {
		t.Fatal("only usage errors are usage errors")
	}
	var target *cliError
	if !errors.As(wrapped, &target) {
		t.Fatal("a site error is a cliError")
	}
}
