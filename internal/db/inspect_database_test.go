package db_test

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/tvstore/forgebase-go/internal/db"
	"github.com/tvstore/forgebase-go/internal/db/dbtest"
)

// Rust: inspect_database.rs::a_new_database_is_empty_and_anything_in_it_makes_it_not_empty. The server
// version and the emptiness check that `site restore` relies on.
func TestANewDatabaseIsEmptyAndAnythingInItMakesItNotEmpty(t *testing.T) {
	d := dbtest.New(t)
	info, err := db.InspectDatabase(bg, d.URL())
	if err != nil || !info.IsEmpty || info.ServerMajor() < 16 {
		t.Fatalf("%+v %v", info, err)
	}
	// Each of these alone makes the database "not empty"; each is undone before the next.
	for _, c := range []struct{ what, create, undo string }{
		{"a table in public", "CREATE TABLE public.t (a int)", "DROP TABLE public.t"},
		{"a sequence", "CREATE SEQUENCE public.s", "DROP SEQUENCE public.s"},
		{"a view", "CREATE VIEW public.v AS SELECT 1 AS a", "DROP VIEW public.v"},
		{"a schema", "CREATE SCHEMA extra", "DROP SCHEMA extra"},
		{"a table in another schema", "CREATE SCHEMA extra; CREATE TABLE extra.t (a int)", "DROP SCHEMA extra CASCADE"},
		{"a function", "CREATE FUNCTION public.f() RETURNS int LANGUAGE sql AS 'SELECT 1'", "DROP FUNCTION public.f()"},
		{"an enum type", "CREATE TYPE public.e AS ENUM ('a')", "DROP TYPE public.e"},
		{"a domain", "CREATE DOMAIN public.d AS int", "DROP DOMAIN public.d"},
	} {
		d.Exec(c.create)
		if info, err := db.InspectDatabase(bg, d.URL()); err != nil || info.IsEmpty {
			t.Errorf("%s must make the database not empty: %+v %v", c.what, info, err)
		}
		d.Exec(c.undo)
		if info, err := db.InspectDatabase(bg, d.URL()); err != nil || !info.IsEmpty {
			t.Errorf("undoing %s must leave it empty: %+v %v", c.what, info, err)
		}
	}
	// Back to not empty once more, to end the test on the other answer.
	d.Exec("CREATE TABLE public.t (a int)")
	if info, err := db.InspectDatabase(bg, d.URL()); err != nil || info.IsEmpty {
		t.Errorf("%+v %v", info, err)
	}
}

// Rust: inspect_database.rs::a_missing_database_is_told_apart
func TestAMissingDatabaseIsToldApart(t *testing.T) {
	url := dbtest.URLForDatabase(t, "fb_no_such_database_xyz")
	_, err := db.InspectDatabase(bg, url)
	if err == nil || !db.IsMissingDatabase(err) {
		t.Fatalf("%v", err)
	}
	if strings.Contains(err.Error(), "forgebase:forgebase") {
		t.Errorf("%v", err)
	}
}

// Rust: tls.rs / site_apps.rs (create_database without TLS): the database is created once, a second
// call reports that it exists, and a maintenance database can be named.
func TestCreateDatabaseCreatesOnceAndReportsAnExistingOne(t *testing.T) {
	d := dbtest.New(t) // the admin URL and a cleanup that drops its own database only
	name := fmt.Sprintf("fb_test_createdb_%x_%d", os.Getpid(), scratchCounter.Add(1))
	url := dbtest.URLForDatabase(t, name)
	t.Cleanup(func() { d.Exec("DROP DATABASE IF EXISTS " + db.QuoteIdent(name) + " WITH (FORCE)") })
	for i, want := range []bool{true, false} {
		created, err := db.CreateDatabase(bg, url, "")
		if err != nil || created != want {
			t.Fatalf("call %d: %v %v", i, created, err)
		}
	}
	info, err := db.InspectDatabase(bg, url)
	if err != nil || !info.IsEmpty {
		t.Fatalf("%+v %v", info, err)
	}
	// The maintenance database is named by the caller: a missing one is the server's error.
	if _, err := db.CreateDatabase(bg, url, "fb_no_such_maintenance_db"); err == nil || !db.IsMissingDatabase(err) {
		t.Errorf("%v", err)
	}
}
