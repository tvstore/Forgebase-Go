package app

import (
	"strings"
	"testing"

	"github.com/tvstore/forgebase-go/internal/db"
)

// Ported from crates/forgebase/src/report.rs (the unit tests of the module).

func syncReport(steps int, warnings []db.Warning, dryRun bool) *db.SyncReport {
	r := &db.SyncReport{DryRun: dryRun, Warnings: warnings}
	for i := 0; i < steps; i++ {
		r.Steps = append(r.Steps, db.Step{Kind: db.StepRegisterDocType, DocType: "d" + string(rune('0'+i))})
	}
	return r
}

func emptyReport() *db.MigrateReport {
	return &db.MigrateReport{Sync: syncReport(0, nil, false), FixturesPlanned: true}
}

// Rust: report.rs::up_to_date
func TestReportUpToDate(t *testing.T) {
	if got := migrateReport(emptyReport(), false, 0); got != "framework migrations: up to date\nschema sync: up to date\n" {
		t.Fatalf("%q", got)
	}
}

// Rust: report.rs::schedule_changes_are_listed_only_when_there_are_some
func TestScheduleChangesAreListedOnlyWhenThereAreSome(t *testing.T) {
	report := emptyReport()
	if strings.Contains(migrateReport(report, false, 0), "schedules") {
		t.Fatal("no schedules, no line")
	}
	report.Schedules = []string{"add crm.digest (daily 06:30)"}
	if !strings.Contains(migrateReport(report, false, 0), "schedules: changed 1\n  add crm.digest (daily 06:30)\n") {
		t.Fatal(migrateReport(report, false, 0))
	}
	if !strings.Contains(migrateReport(report, true, 0), "schedules: would change 1\n") {
		t.Fatal(migrateReport(report, true, 0))
	}
}

// Rust: report.rs::applied_steps_and_warnings
func TestAppliedStepsAndWarnings(t *testing.T) {
	report := &db.MigrateReport{
		Migrations:      db.Migrations,
		Sync:            syncReport(1, []db.Warning{{Kind: db.WarningDocTypeNotInRegistry, DocType: "old"}}, false),
		FixturesPlanned: true,
	}
	text := migrateReport(report, false, 0)
	want := "framework migrations: 13 applied\n  0001 init\n  0002 series\n  0003 auth\n  0004 apps\n  0005 jobs\n  0006 schedules\n  0007 finished_jobs_index\n  0008 workflow_log\n  0009 saved_views\n  0010 webhooks\n  0011 fixtures\n  0012 custom_fields\n  0013 user_permissions\n"
	if !strings.HasPrefix(text, want) {
		t.Fatalf("%s", text)
	}
	if !strings.Contains(text, "schema sync: applied 1 step(s)\n-- register DocType \"d0\"") {
		t.Fatalf("%s", text)
	}
	if !strings.Contains(text, "warnings: 1\n  - DocType \"old\"") {
		t.Fatalf("%s", text)
	}
	if strings.Contains(text, "dry run") {
		t.Fatal(text)
	}
}

// Rust: report.rs::dry_run_with_pending_migrations_has_no_sync_plan
func TestDryRunWithPendingMigrationsHasNoSyncPlan(t *testing.T) {
	report := &db.MigrateReport{Migrations: db.Migrations, FixturesPlanned: true}
	text := migrateReport(report, true, 0)
	if !strings.Contains(text, "13 pending") || !strings.Contains(text, "schema sync: not planned yet") {
		t.Fatal(text)
	}
}

// Rust: report.rs::dry_run_says_nothing_was_changed
func TestDryRunSaysNothingWasChanged(t *testing.T) {
	report := &db.MigrateReport{Sync: syncReport(2, nil, true), FixturesPlanned: true}
	text := migrateReport(report, true, 0)
	if !strings.Contains(text, "would apply 2 step(s)") || !strings.HasSuffix(text, "dry run: nothing was changed\n") {
		t.Fatal(text)
	}
}

// Rust: report.rs::fixtures_are_listed_one_line_per_app
func TestFixturesAreListedOneLinePerApp(t *testing.T) {
	app := func(name string, c [4]int) db.AppFixtures {
		return db.AppFixtures{App: name, Records: c[0] + c[1] + c[2] + c[3], Inserted: c[0], Updated: c[1], Kept: c[2], Unchanged: c[3]}
	}
	report := emptyReport()
	// No fixtures: nothing is said.
	if strings.Contains(migrateReport(report, false, 2), "fixtures") {
		t.Fatal("no fixtures, no line")
	}
	report.Fixtures = []db.AppFixtures{app("crm", [4]int{2, 1, 0, 5}), app("sales", [4]int{0, 0, 0, 3})}
	want := "framework migrations: up to date\nschema sync: up to date\napp migrations: up to date\n" +
		"fixtures: crm: 2 inserted, 1 updated, 0 kept, 5 unchanged\n" +
		"fixtures: sales: 0 inserted, 0 updated, 0 kept, 3 unchanged\n"
	if got := migrateReport(report, false, 2); got != want {
		t.Fatalf("%q", got)
	}
	if dry := migrateReport(report, true, 2); !strings.Contains(dry, "fixtures: crm: would insert 2, update 1, keep 0, 5 unchanged\n") {
		t.Fatal(dry)
	}
	// While framework migrations are pending the plan is not made.
	report.Sync = nil
	report.FixturesPlanned = false
	if text := migrateReport(report, true, 2); !strings.Contains(text, "fixtures: crm: 8 record(s), not planned yet") {
		t.Fatal(text)
	}
}

// Rust: report.rs::app_migrations_are_listed_in_order_when_the_binary_has_apps
func TestAppMigrationsAreListedInOrderWhenTheBinaryHasApps(t *testing.T) {
	report := emptyReport()
	// No apps: nothing is said. Apps without pending migrations: up to date.
	if strings.Contains(migrateReport(report, false, 0), "app migrations") {
		t.Fatal("no apps, no line")
	}
	if got := migrateReport(report, false, 2); got != "framework migrations: up to date\nschema sync: up to date\napp migrations: up to date\n" {
		t.Fatalf("%q", got)
	}
	report.AppMigrations = []db.AppliedAppMigration{{App: "crm", Version: 1, Name: "a"}, {App: "sales", Version: 12, Name: "b"}}
	want := "framework migrations: up to date\nschema sync: up to date\napp migrations: 2 applied\n  crm 0001 a\n  sales 0012 b\n"
	if got := migrateReport(report, false, 2); got != want {
		t.Fatalf("%q", got)
	}
	dry := migrateReport(report, true, 2)
	if !strings.Contains(dry, "app migrations: 2 pending") || !strings.HasSuffix(dry, "dry run: nothing was changed\n") {
		t.Fatal(dry)
	}
	// While framework migrations are pending, the app migrations are still listed.
	report.Sync = nil
	text := migrateReport(report, true, 2)
	if !strings.Contains(text, "schema sync: not planned yet") || !strings.HasSuffix(text, "  sales 0012 b\n") {
		t.Fatal(text)
	}
}
