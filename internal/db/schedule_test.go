package db_test

import (
	"fmt"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/tvstore/forgebase-go/internal/core"
	"github.com/tvstore/forgebase-go/internal/db"
	"github.com/tvstore/forgebase-go/internal/db/dbtest"
)

// The scheduler (M6.2, ADR 0013) against a real PostgreSQL: the sync of the fb_schedule rows, the tick that
// enqueues due schedules exactly once however many workers run, and the SQL of the built-in clean-up jobs.
// Ported from crates/forgebase-db/tests/schedule.rs.

var (
	every5 = db.EveryMinutes(5)
	daily3 = db.DailyAt(3, 0)
)

func scheduleRegistry() *core.Registry {
	r := core.NewRegistry()
	r.AddJob("app.a")
	r.AddJob("app.b")
	return r
}

func knownSchedules() map[string]db.Spec {
	return map[string]db.Spec{"app.a": every5, "app.b": daily3}
}

type declaredSpec struct {
	name string
	spec db.Spec
}

func syncSchedules(t *testing.T, d *dbtest.DB, declared []declaredSpec, dryRun bool) []string {
	t.Helper()
	list := []db.DeclaredSchedule{}
	for _, s := range declared {
		list = append(list, db.DeclaredSchedule{Name: s.name, Spec: s.spec})
	}
	lines, err := db.SyncSchedules(bg, d.Pool, list, dryRun)
	if err != nil {
		t.Fatal(err)
	}
	return lines
}

func tick(t *testing.T, d *dbtest.DB, known map[string]db.Spec, at *time.Time) int {
	t.Helper()
	n, err := db.Tick(bg, d.Pool, scheduleRegistry(), known, at)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// Rust: schedule.rs::sync_inserts_changes_keeps_and_deletes
func TestSyncInsertsChangesKeepsAndDeletes(t *testing.T) {
	d := dbtest.Migrated(t)
	// Insert: due at the first time after the database's now().
	lines := syncSchedules(t, d, []declaredSpec{{"app.a", every5}, {"app.b", daily3}}, false)
	want(t, lines, []string{"add app.a (every 5m)", "add app.b (daily 03:00)"}, "lines")
	want(t, d.Rows("SELECT name, spec, (next_run_at > now())::text, (last_run_at IS NULL)::text FROM fb_schedule ORDER BY name"),
		[][]string{{"app.a", "every 5m", "true", "true"}, {"app.b", "daily 03:00", "true", "true"}}, "rows")

	// Unchanged: left alone, next_run_at kept (set to a recognizable value first).
	d.Exec("UPDATE fb_schedule SET next_run_at = '2030-01-01 00:00:00+00'")
	if got := syncSchedules(t, d, []declaredSpec{{"app.a", every5}, {"app.b", daily3}}, false); len(got) != 0 {
		t.Error(got)
	}
	if got := scalar(d, "SELECT count(*)::text FROM fb_schedule WHERE next_run_at = '2030-01-01 00:00:00+00'"); got != "2" {
		t.Error(got)
	}

	// A changed spec is recomputed; an undeclared row is deleted (a dry run reports but keeps).
	declared := []declaredSpec{{"app.a", db.EveryMinutes(10)}}
	planned := syncSchedules(t, d, declared, true)
	want(t, planned, []string{"change app.a (every 5m -> every 10m)", "remove app.b (daily 03:00)"}, "planned")
	if got := scalar(d, "SELECT count(*)::text FROM fb_schedule"); got != "2" {
		t.Error(got)
	}
	if got := scalar(d, "SELECT spec FROM fb_schedule WHERE name = 'app.a'"); got != "every 5m" {
		t.Error(got)
	}
	want(t, syncSchedules(t, d, declared, false), planned, "applied")
	want(t, d.Rows("SELECT name, spec, (next_run_at > now() AND next_run_at <= now() + interval '10 minutes')::text FROM fb_schedule"),
		[][]string{{"app.a", "every 10m", "true"}}, "after")

	// Nothing declared: everything goes.
	want(t, syncSchedules(t, d, nil, false), []string{"remove app.a (every 10m)"}, "all removed")
	if got := scalar(d, "SELECT count(*)::text FROM fb_schedule"); got != "0" {
		t.Error(got)
	}
	want(t, syncSchedules(t, d, []declaredSpec{{"app.w", db.WeeklyOn(time.Monday, 2, 0)}}, false),
		[]string{"add app.w (weekly mon 02:00)"}, "weekly")
}

// Rust: schedule.rs::a_tick_enqueues_a_due_schedule_with_the_due_time_and_the_scheduler_actor
func TestATickEnqueuesADueScheduleWithTheDueTimeAndTheSchedulerActor(t *testing.T) {
	d := dbtest.Migrated(t)
	syncSchedules(t, d, []declaredSpec{{"app.a", every5}, {"app.b", daily3}}, false)
	// Nothing is due yet.
	epoch := time.Unix(0, 0)
	if n := tick(t, d, knownSchedules(), &epoch); n != 0 {
		t.Fatal(n)
	}

	d.Exec("UPDATE fb_schedule SET next_run_at = '2026-01-01 00:05:00+00' WHERE name = 'app.a'")
	if n := tick(t, d, knownSchedules(), nil); n != 1 {
		t.Fatal(n)
	}
	want(t, d.Rows("SELECT name, payload->>'scheduled_for', enqueued_by, status FROM fb_job"),
		[][]string{{"app.a", "2026-01-01T00:05:00Z", "scheduler", "queued"}}, "job")
	// The schedule moved on to the future and records the run.
	want(t, d.Rows("SELECT (next_run_at > now())::text, (last_run_at IS NOT NULL)::text, ((extract(epoch FROM next_run_at)::bigint / 60) % 5)::text FROM fb_schedule WHERE name = 'app.a'"),
		[][]string{{"true", "true", "0"}}, "schedule")
	// Ticking again enqueues nothing more.
	if n := tick(t, d, knownSchedules(), &epoch); n != 0 {
		t.Fatal(n)
	}
	if got := scalar(d, "SELECT count(*)::text FROM fb_job"); got != "1" {
		t.Error(got)
	}
}

// Rust: schedule.rs::missed_ticks_are_not_caught_up
func TestMissedTicksAreNotCaughtUp(t *testing.T) {
	d := dbtest.Migrated(t)
	syncSchedules(t, d, []declaredSpec{{"app.a", every5}}, false)
	// Ten periods in the past.
	d.Exec("UPDATE fb_schedule SET next_run_at = date_trunc('minute', now()) - interval '50 minutes'")
	due := scalar(d, `SELECT to_char(next_run_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"') FROM fb_schedule`)
	known := map[string]db.Spec{"app.a": every5}
	if n := tick(t, d, known, nil); n != 1 {
		t.Fatal(n)
	}
	epoch := time.Unix(0, 0)
	if n := tick(t, d, known, &epoch); n != 0 {
		t.Fatal(n)
	}
	want(t, d.Rows("SELECT payload->>'scheduled_for' FROM fb_job"), [][]string{{due}}, "one job")
	if got := scalar(d, "SELECT (next_run_at > now() AND next_run_at <= now() + interval '5 minutes')::text FROM fb_schedule"); got != "true" {
		t.Error(got)
	}
}

// Rust: schedule.rs::a_tick_with_a_given_time_uses_it_instead_of_the_clock
func TestATickWithAGivenTimeUsesItInsteadOfTheClock(t *testing.T) {
	d := dbtest.Migrated(t)
	syncSchedules(t, d, []declaredSpec{{"app.a", every5}}, false)
	at := time.Now().UTC().Add(24 * time.Hour)
	if n := tick(t, d, map[string]db.Spec{"app.a": every5}, &at); n != 1 {
		t.Fatal(n)
	}
	if got := scalar(d, "SELECT (next_run_at > now() + interval '23 hours')::text FROM fb_schedule"); got != "true" {
		t.Error(got)
	}
}

// Rust: schedule.rs::a_due_schedule_of_an_unknown_job_is_left_alone
func TestADueScheduleOfAnUnknownJobIsLeftAlone(t *testing.T) {
	d := dbtest.Migrated(t)
	syncSchedules(t, d, []declaredSpec{{"app.a", every5}, {"other.x", every5}}, false)
	d.Exec("UPDATE fb_schedule SET next_run_at = now() - interval '1 hour'")
	if n := tick(t, d, map[string]db.Spec{"app.a": every5}, nil); n != 1 {
		t.Fatal(n)
	}
	if got := scalar(d, "SELECT (next_run_at < now())::text FROM fb_schedule WHERE name = 'other.x'"); got != "true" {
		t.Error(got)
	}
}

// Rust: schedule.rs::eight_concurrent_ticks_enqueue_each_due_time_exactly_once
func TestEightConcurrentTicksEnqueueEachDueTimeExactlyOnce(t *testing.T) {
	const workers, rounds = 8, 50
	d := dbtest.Migrated(t)
	syncSchedules(t, d, []declaredSpec{{"app.a", every5}, {"app.b", daily3}}, false)
	pool, err := db.ConnectSized(bg, d.URL(), workers+2)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	registry, known := scheduleRegistry(), knownSchedules()
	for round := 1; round <= rounds; round++ {
		d.Exec("UPDATE fb_schedule SET next_run_at = now() - interval '1 minute'")
		// Every worker has its connection before any of them ticks, so the ticks overlap.
		var ready, done sync.WaitGroup
		start := make(chan struct{})
		counts := make([]int, workers)
		errs := make([]error, workers)
		ready.Add(workers)
		done.Add(workers)
		for i := range workers {
			go func() {
				defer done.Done()
				conn, err := pool.Acquire(bg)
				ready.Done()
				if err != nil {
					errs[i] = err
					return
				}
				defer conn.Release()
				<-start
				counts[i], errs[i] = db.Tick(bg, conn, registry, known, nil)
			}()
		}
		ready.Wait()
		close(start)
		done.Wait()
		enqueued := 0
		for i := range workers {
			if errs[i] != nil {
				t.Fatalf("round %d: %v", round, errs[i])
			}
			enqueued += counts[i]
		}
		if enqueued != 2 {
			t.Fatalf("round %d: %d jobs enqueued, want one per schedule", round, enqueued)
		}
		if got := scalar(d, "SELECT count(*)::text FROM fb_job"); got != strconv.Itoa(2*round) {
			t.Fatalf("round %d: %s jobs", round, got)
		}
	}
	want(t, d.Rows("SELECT name, count(*)::text FROM fb_job GROUP BY name ORDER BY name"),
		[][]string{{"app.a", "50"}, {"app.b", "50"}}, "per schedule")
}

// job inserts a job row in a given state, finished ago ago ("" for not finished).
func insertJobRow(d *dbtest.DB, status, finishedAgo string) {
	finished := "NULL"
	if finishedAgo != "" {
		finished = fmt.Sprintf("now() - interval '%s'", finishedAgo)
	}
	d.Exec(fmt.Sprintf("INSERT INTO fb_job (id, name, payload, status, enqueued_by, finished_at) "+
		"VALUES (gen_random_uuid(), 'app.a', '{}', '%s', 't', %s)", status, finished))
}

// Rust: schedule.rs::purge_finished_deletes_only_old_done_and_dead_jobs_in_batches
func TestPurgeFinishedDeletesOnlyOldDoneAndDeadJobsInBatches(t *testing.T) {
	d := dbtest.Migrated(t)
	insertJobRow(d, "done", "8 days")
	insertJobRow(d, "done", "8 days")
	insertJobRow(d, "dead", "8 days")
	insertJobRow(d, "dead", "30 days")
	insertJobRow(d, "done", "6 days") // recent
	insertJobRow(d, "dead", "6 days") // recent
	insertJobRow(d, "queued", "")
	insertJobRow(d, "running", "")
	// Old but not finished: never purged, whatever created_at or run_at say.
	d.Exec("UPDATE fb_job SET created_at = now() - interval '90 days', run_at = now() - interval '90 days' WHERE status IN ('queued', 'running')")
	for i, wantDeleted := range []uint64{3, 1, 0} {
		n, err := db.PurgeFinishedJobs(bg, d.Pool, 3)
		if err != nil || n != wantDeleted {
			t.Fatalf("purge %d: %d, %v (a batch is bounded)", i, n, err)
		}
	}
	want(t, d.Rows("SELECT status, count(*)::text FROM fb_job GROUP BY status ORDER BY status"),
		[][]string{{"dead", "1"}, {"done", "1"}, {"queued", "1"}, {"running", "1"}}, "left")
}

// Rust: schedule.rs::purge_finished_keeps_a_dead_job_that_was_retried
func TestPurgeFinishedKeepsADeadJobThatWasRetried(t *testing.T) {
	d := dbtest.Migrated(t)
	insertJobRow(d, "dead", "30 days")
	id := d.Rows("SELECT id::text FROM fb_job")[0][0]
	if result, _, err := db.RetryDeadJob(bg, d.Pool, id); err != nil || result != db.RetryQueued {
		t.Fatal(result, err)
	}
	if n, err := db.PurgeFinishedJobs(bg, d.Pool, 10); err != nil || n != 0 {
		t.Fatal(n, err)
	}
	want(t, d.Rows("SELECT status, (finished_at IS NULL)::text FROM fb_job"), [][]string{{"queued", "true"}}, "kept")
}

// Rust: schedule.rs::purge_login_failures_keeps_rows_that_still_count_or_lock
func TestPurgeLoginFailuresKeepsRowsThatStillCountOrLock(t *testing.T) {
	d := dbtest.Migrated(t)
	d.Exec("INSERT INTO fb_login_failure (username, failures, locked_until, last_failure_at) VALUES " +
		"('stale', 3, NULL, now() - interval '16 minutes'), " +
		"('stale_unlocked', 0, now() - interval '1 minute', now() - interval '20 minutes'), " +
		"('never_failed', 0, NULL, NULL), " +
		"('counting', 3, NULL, now() - interval '5 minutes'), " +
		"('just_inside', 3, NULL, now() - interval '14 minutes 59 seconds'), " +
		"('locked', 0, now() + interval '5 minutes', now() - interval '10 minutes'), " +
		"('locked_old_failure', 0, now() + interval '1 minute', now() - interval '20 minutes')")
	if n, err := db.PurgeLoginFailures(bg, d.Pool, 1000); err != nil || n != 3 {
		t.Fatal(n, err)
	}
	want(t, d.Rows("SELECT username FROM fb_login_failure ORDER BY username"),
		[][]string{{"counting"}, {"just_inside"}, {"locked"}, {"locked_old_failure"}}, "kept")
	// A bounded batch.
	d.Exec("UPDATE fb_login_failure SET locked_until = NULL, last_failure_at = now() - interval '1 hour'")
	for _, wantDeleted := range []uint64{3, 1} {
		if n, err := db.PurgeLoginFailures(bg, d.Pool, 3); err != nil || n != wantDeleted {
			t.Fatal(n, err)
		}
	}
}

// Rust: schedule.rs::a_purged_login_failure_row_behaves_like_no_row
func TestAPurgedLoginFailureRowBehavesLikeNoRow(t *testing.T) {
	// The five failures lock the user; after the lock and the window passed, a purge changes nothing a login
	// can see: the next failure counts as the first.
	d := dbtest.Migrated(t)
	if _, err := db.CreateUser(bg, d.Pool, "ann", "correct horse", nil); err != nil {
		t.Fatal(err)
	}
	for range 5 {
		if _, err := db.Login(bg, d.Pool, "ann", "wrong password"); err == nil {
			t.Fatal("a wrong password logged in")
		}
	}
	if got := scalar(d, "SELECT (locked_until > now())::text FROM fb_login_failure"); got != "true" {
		t.Error(got)
	}
	if n, err := db.PurgeLoginFailures(bg, d.Pool, 100); err != nil || n != 0 {
		t.Fatalf("locked: %d, %v", n, err)
	}
	d.Exec("UPDATE fb_login_failure SET locked_until = now() - interval '1 second', last_failure_at = now() - interval '16 minutes'")
	if n, err := db.PurgeLoginFailures(bg, d.Pool, 100); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	if _, err := db.Login(bg, d.Pool, "ann", "correct horse"); err != nil {
		t.Fatal(err)
	}
}

// Rust: schedule.rs::purge_sessions_deletes_expired_sessions_only
func TestPurgeSessionsDeletesExpiredSessionsOnly(t *testing.T) {
	d := dbtest.Migrated(t)
	if _, err := db.CreateUser(bg, d.Pool, "ann", "correct horse", nil); err != nil {
		t.Fatal(err)
	}
	d.Exec("INSERT INTO fb_session (token_hash, csrf_hash, user_id, expires_at, last_seen_at) " +
		"SELECT sha256(convert_to(t, 'UTF8')), sha256(convert_to(t, 'UTF8')), u.id, e, s " +
		"FROM fb_user u, (VALUES " +
		"('valid', now() + interval '1 day', now() - interval '1 hour'), " +
		"('almost_idle', now() + interval '1 day', now() - interval '23 hours'), " +
		"('absolute', now() - interval '1 second', now()), " +
		"('idle', now() + interval '1 day', now() - interval '25 hours'), " +
		"('both', now() - interval '1 day', now() - interval '3 days')) AS v(t, e, s)")
	for i, wantDeleted := range []uint64{2, 1, 0} {
		n, err := db.PurgeSessions(bg, d.Pool, 2)
		if err != nil || n != wantDeleted {
			t.Fatalf("purge %d: %d, %v (bounded)", i, n, err)
		}
	}
	want(t, d.Rows("SELECT t FROM unnest(ARRAY['valid', 'almost_idle', 'absolute', 'idle', 'both']) t "+
		"WHERE EXISTS (SELECT 1 FROM fb_session WHERE token_hash = sha256(convert_to(t, 'UTF8'))) ORDER BY t"),
		[][]string{{"almost_idle"}, {"valid"}}, "kept")
}
