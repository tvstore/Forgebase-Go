package forgebase_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/tvstore/forgebase-go"
	fbtest "github.com/tvstore/forgebase-go/testing"
)

// Schedules through the public API (M6.2, ADR 0013), with forgebase/testing: the startup errors, what migrate
// stores, and RunSchedulesAt followed by RunJobs. The tick itself (exactly once under concurrency, missed
// ticks) is tested in internal/db, and the worker that ticks in internal/app/worker_test.go. Ported from
// crates/forgebase/tests/schedules.rs.

const scheduleSeen = "name = \"seen\"\nlabel = \"Seen\"\n[[fields]]\nname = \"at\"\ntype = \"data\"\n"

// record records the scheduled_for of its payload as a seen document.
func record(ctx context.Context, job *forgebase.JobContext) error {
	var p struct {
		ScheduledFor string `json:"scheduled_for"`
	}
	at := "none"
	if err := job.DecodePayload(&p); err == nil && p.ScheduledFor != "" {
		at = p.ScheduledFor
	}
	_, err := job.Documents().Insert(ctx, "seen", forgebase.Document{Values: map[string]forgebase.Value{"at": forgebase.Text(at)}})
	return err
}

func crmWithSchedule(schedule *forgebase.Schedule) *forgebase.Binary {
	app := forgebase.NewApp("crm").
		DocTypes(docTypes("seen.toml", scheduleSeen)).
		Job("digest", record)
	if schedule != nil {
		app = app.Schedule("digest", *schedule)
	}
	return forgebase.New().App(app)
}

func scheduleOf(s forgebase.Schedule) *forgebase.Schedule { return &s }

func first(db *fbtest.DB, sql string) string { return db.Rows(sql)[0][0] }

// ---- startup errors --------------------------------------------------------------------------

// Rust: schedules.rs::schedule_mistakes_are_startup_errors
func TestScheduleMistakesAreStartupErrors(t *testing.T) {
	app := forgebase.NewApp("shop").
		Job("fine", record).
		Job("other", record).
		Schedule("fine", forgebase.EveryMinutes(0)).
		Schedule("fine", forgebase.DailyAt(1, 0)).    // a second schedule for the job
		Schedule("other", forgebase.DailyAt(24, 60)). // two problems
		Schedule("ghost", forgebase.EveryMinutes(5)). // not registered
		Schedule("nope", forgebase.WeeklyOn(time.Monday, 0, 60))
	err := forgebase.New().App(app).Check()
	problems := startupProblems(t, err)
	if len(problems) != 6 {
		t.Fatalf("%d: %q", len(problems), problems)
	}
	if !strings.Contains(problems[0], "schedule for job `fine`") || !strings.Contains(problems[0], "from 1 to 1440") {
		t.Errorf("%q", problems)
	}
	for i, part := range []string{
		"job `fine` has more than one schedule",
		"the hour 24",
		"the minute 60",
		"schedule for job `ghost`, which `shop` did not register",
		"schedule for job `nope`",
	} {
		if !strings.Contains(problems[i+1], part) {
			t.Errorf("problem %d: %q does not contain %q", i+1, problems[i+1], part)
		}
	}
	if !strings.HasPrefix(err.Error(), "invalid app setup:") {
		t.Errorf("%s", err)
	}
}

// Rust: schedules.rs::an_app_cannot_schedule_the_job_of_another_app
func TestAnAppCannotScheduleTheJobOfAnotherApp(t *testing.T) {
	a := forgebase.NewApp("a").Job("work", record)
	b := forgebase.NewApp("b").Schedule("a.work", forgebase.EveryMinutes(5))
	err := forgebase.New().App(a).App(b).Check()
	if err == nil || !strings.Contains(err.Error(), "schedule for job `a.work`, which `b` did not register") {
		t.Errorf("%v", err)
	}
}

// Rust: schedules.rs::the_edges_of_every_kind_are_valid
func TestTheEdgesOfEveryKindAreValid(t *testing.T) {
	app := forgebase.NewApp("shop").
		Job("a", record).Job("b", record).Job("c", record).Job("d", record).
		Schedule("a", forgebase.EveryMinutes(1)).
		Schedule("b", forgebase.EveryMinutes(1440)).
		Schedule("c", forgebase.DailyAt(23, 59)).
		Schedule("d", forgebase.WeeklyOn(time.Sunday, 0, 0))
	if err := forgebase.New().App(app).Check(); err != nil {
		t.Error(err)
	}
}

// Rust: schedules.rs::the_app_name_forgebase_is_reserved_for_the_built_in_jobs
func TestTheAppNameForgebaseIsReservedForTheBuiltInJobs(t *testing.T) {
	err := forgebase.New().App(forgebase.NewApp("forgebase").Job("purge_jobs", record)).Check()
	if err == nil || !strings.Contains(err.Error(), "app name `forgebase` is reserved for the framework's own jobs") {
		t.Errorf("%v", err)
	}
}

// Rust: schedules.rs::schedules_compare_by_value
func TestSchedulesCompareByValue(t *testing.T) {
	five := forgebase.EveryMinutes(5)
	if forgebase.EveryMinutes(5) != five {
		t.Error("equal schedules differ")
	}
	if forgebase.EveryMinutes(5) == forgebase.EveryMinutes(6) {
		t.Error("different schedules are equal")
	}
	if forgebase.DailyAt(1, 0) == forgebase.WeeklyOn(time.Monday, 1, 0) {
		t.Error("different kinds are equal")
	}
}

// ---- migrate stores the schedules ------------------------------------------------------------

// Rust: schedules.rs::migrate_stores_the_built_in_and_app_schedules_and_keeps_them_in_step
func TestMigrateStoresTheBuiltInAndAppSchedulesAndKeepsThemInStep(t *testing.T) {
	db := fbtest.NewDB(t)
	migrate := func(b *forgebase.Binary) {
		t.Helper()
		if _, err := db.Migrate(b, false); err != nil {
			t.Fatal(err)
		}
	}
	migrate(crmWithSchedule(scheduleOf(forgebase.EveryMinutes(15))))
	expect(t, db.Rows("SELECT name, spec FROM fb_schedule ORDER BY name"), [][]string{
		{"crm.digest", "every 15m"},
		{"forgebase.purge_jobs", "daily 03:00"},
		{"forgebase.purge_login_failures", "every 60m"},
		{"forgebase.purge_sessions", "every 60m"},
	})

	// An unchanged schedule keeps its next_run_at over another migrate.
	db.Exec("UPDATE fb_schedule SET next_run_at = '2031-01-01 00:00:00+00'")
	migrate(crmWithSchedule(scheduleOf(forgebase.EveryMinutes(15))))
	if got := first(db, "SELECT count(*)::text FROM fb_schedule WHERE next_run_at = '2031-01-01 00:00:00+00'"); got != "4" {
		t.Errorf("%s", got)
	}

	// A changed one is recomputed, and the others stay.
	migrate(crmWithSchedule(scheduleOf(forgebase.DailyAt(6, 30))))
	expect(t, db.Rows("SELECT spec, (next_run_at <> '2031-01-01 00:00:00+00')::text FROM fb_schedule WHERE name = 'crm.digest'"),
		[][]string{{"daily 06:30", "true"}})
	if got := first(db, "SELECT count(*)::text FROM fb_schedule WHERE next_run_at = '2031-01-01 00:00:00+00'"); got != "3" {
		t.Errorf("%s", got)
	}

	// A binary that does not declare the schedule deletes the row; the built-ins stay.
	migrate(crmWithSchedule(nil))
	if got := first(db, "SELECT string_agg(name, ',' ORDER BY name) FROM fb_schedule"); got !=
		"forgebase.purge_jobs,forgebase.purge_login_failures,forgebase.purge_sessions" {
		t.Errorf("%s", got)
	}
}

// ---- RunSchedulesAt and RunJobs --------------------------------------------------------------

// Rust: schedules.rs::run_schedules_at_enqueues_the_due_jobs_and_run_jobs_runs_them
func TestRunSchedulesAtEnqueuesTheDueJobsAndRunJobsRunsThem(t *testing.T) {
	db := fbtest.NewDB(t)
	binary := crmWithSchedule(scheduleOf(forgebase.EveryMinutes(15)))
	if _, err := db.Migrate(binary, false); err != nil {
		t.Fatal(err)
	}

	// Nothing is due an hour ago (every schedule was set after the migrate): nothing is enqueued.
	if n := fbtest.RunSchedulesAt(binary, db, time.Now().Add(-time.Hour)); n != 0 {
		t.Fatalf("%d", n)
	}

	// Two days later every schedule is due: the app's and the three built-in ones.
	due := first(db, `SELECT to_char(next_run_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"') FROM fb_schedule WHERE name = 'crm.digest'`)
	at := time.Now().Add(2 * 24 * time.Hour)
	if n := fbtest.RunSchedulesAt(binary, db, at); n != 4 {
		t.Fatalf("%d", n)
	}
	if n := fbtest.RunSchedulesAt(binary, db, at); n != 0 {
		t.Fatalf("enqueued once: %d", n)
	}
	expect(t, db.Rows("SELECT payload->>'scheduled_for', enqueued_by FROM fb_job WHERE name = 'crm.digest'"),
		[][]string{{due, "scheduler"}})

	run := fbtest.RunJobs(binary, db)
	expect(t, counts(run), [3]int{4, 0, 0})
	// The handler saw the due time.
	expect(t, db.Rows(`SELECT "at" FROM doc_seen`), [][]string{{due}})

	// Later still, it is due again: the next tick is a new job.
	if n := fbtest.RunSchedulesAt(binary, db, at.Add(time.Hour)); n < 1 {
		t.Errorf("%d", n)
	}
}

// Rust: schedules.rs::built_in_jobs_run_on_a_binary_without_apps
func TestBuiltInJobsRunOnABinaryWithoutApps(t *testing.T) {
	db := fbtest.NewDB(t)
	binary := forgebase.New()
	if _, err := db.Migrate(binary, false); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(2 * 24 * time.Hour)
	if n := fbtest.RunSchedulesAt(binary, db, at); n != 3 {
		t.Fatalf("%d", n)
	}
	run := fbtest.RunJobs(binary, db)
	expect(t, [2]int{run.Done, run.Dead}, [2]int{3, 0})
}
