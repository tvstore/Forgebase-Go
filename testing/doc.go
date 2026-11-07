// Package testing gives the tests of a Forgebase app a throwaway PostgreSQL database, an in-process
// HTTP client and a guard for the embedded DocType set (ADR 0028). It is part of the public API,
// like the root package, and is imported by apps; it is meant for tests only.
//
// The package is called testing like the standard library's, so a test file imports one of them under
// another name; the generated app (`forgebase new-app`) uses fbtest:
//
//	import (
//		"testing"
//
//		fbtest "github.com/tvstore/forgebase-go/testing"
//	)
//
//	func TestAnOrderNeedsACustomer(t *testing.T) {
//		binary := forgebase.New().App(crm.App()).App(sales.App())
//		db := fbtest.NewDB(t)
//		if _, err := db.Migrate(binary, false); err != nil {
//			t.Fatal(err)
//		}
//		db.CreateUser("ann", "Sales User")
//		ann := db.Client(binary).Login("ann")
//		reply := ann.Post("/api/v1/resource/sales_order", map[string]any{"total": 5})
//		if reply.Status != 422 {
//			t.Fatalf("%d %v", reply.Status, reply.Body)
//		}
//	}
//
// The database comes from the environment variable FORGEBASE_TEST_DATABASE_URL: the URL of a
// PostgreSQL 16 server with a superuser or CREATEDB role, for example
// postgres://forgebase:forgebase@localhost:5432/postgres. Every [DB] creates its own database
// (fb_test_app_*) and drops it when the test ends, also when it fails; a test that needs the server
// fails when the variable is not set, it never skips. The helpers fail the test (t.Fatalf) instead of
// returning errors, except where the test wants to look at the error ([DB.Migrate]).
//
// [RunJobs] runs the background jobs that are due, with the worker's own code, and [RunSchedulesAt]
// enqueues the scheduled jobs that are due at a given time.
//
// Not in this version (later phases): checking report SQL (P6), custom fields and metadata reload, and
// webhook settings (P7).
package testing
