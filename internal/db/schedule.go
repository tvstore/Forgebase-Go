package db

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/tvstore/forgebase-go/internal/core"
)

// The scheduler: schedule times, the fb_schedule table and the tick (M6.2, ADR 0013; the Rust module is
// forgebase-db schedule.rs).
//
// A schedule row says when a job is next due. There is no leader: a tick is claimed by moving next_run_at
// forward in the transaction that enqueues the job, guarded on the value that was read, so exactly one of
// any number of concurrent workers wins a due time (Tick). Missed ticks are not caught up: the new
// next_run_at is the first time after the database's now(). Every time is the database's now(), never a
// clock of the worker.
//
// The definitions live in the binary. SyncSchedules makes the rows equal to what the binary declares; it
// runs inside migrate, under the migration lock.

// MaxEveryMinutes is the most minutes of `every N minutes`: one day.
const MaxEveryMinutes = 1440

// SchedulerActor is the actor recorded on a job that a tick enqueued.
const SchedulerActor = "scheduler"

// SpecKind is the kind of a Spec.
type SpecKind int

const (
	// SpecEveryMinutes: every N minutes, aligned to the Unix epoch.
	SpecEveryMinutes SpecKind = iota + 1
	// SpecDailyAt: every day at a UTC time.
	SpecDailyAt
	// SpecWeeklyOn: every week on a day at a UTC time.
	SpecWeeklyOn
)

// Spec is when a job is due. It holds plain values: nothing is checked until Problems (the apps are
// validated together at startup). The zero value is invalid.
type Spec struct {
	Kind SpecKind
	// Minutes is the N of SpecEveryMinutes, 1 to 1440.
	Minutes int
	// Weekday is the day of SpecWeeklyOn.
	Weekday time.Weekday
	// Hour (0 to 23) and Minute (0 to 59) are the UTC time of SpecDailyAt and SpecWeeklyOn.
	Hour, Minute int
}

// EveryMinutes is a Spec for every n minutes, aligned to the Unix epoch: the times at which the minutes
// since the epoch are divisible by n. n is 1 to 1440.
func EveryMinutes(n int) Spec { return Spec{Kind: SpecEveryMinutes, Minutes: n} }

// DailyAt is a Spec for every day at this UTC time.
func DailyAt(hour, minute int) Spec { return Spec{Kind: SpecDailyAt, Hour: hour, Minute: minute} }

// WeeklyOn is a Spec for every week on this day at this UTC time.
func WeeklyOn(weekday time.Weekday, hour, minute int) Spec {
	return Spec{Kind: SpecWeeklyOn, Weekday: weekday, Hour: hour, Minute: minute}
}

// Problems is what is wrong with the values, one message per problem.
func (s Spec) Problems() []string {
	var problems []string
	switch s.Kind {
	case SpecEveryMinutes:
		if s.Minutes < 1 || s.Minutes > MaxEveryMinutes {
			problems = append(problems, fmt.Sprintf(
				"EveryMinutes(%d): the number of minutes must be from 1 to %d", s.Minutes, MaxEveryMinutes))
		}
		return problems
	case SpecDailyAt:
	case SpecWeeklyOn:
		if s.Weekday < time.Sunday || s.Weekday > time.Saturday {
			problems = append(problems, fmt.Sprintf("the weekday %d is not a time.Weekday", int(s.Weekday)))
		}
	default:
		return []string{"the schedule is empty (build one with EveryMinutes, DailyAt or WeeklyOn)"}
	}
	if s.Hour < 0 || s.Hour > 23 {
		problems = append(problems, fmt.Sprintf("the hour %d must be from 0 to 23", s.Hour))
	}
	if s.Minute < 0 || s.Minute > 59 {
		problems = append(problems, fmt.Sprintf("the minute %d must be from 0 to 59", s.Minute))
	}
	return problems
}

// Canonical is the text stored in fb_schedule.spec: `every 15m`, `daily 02:00` or `weekly mon 02:00`.
func (s Spec) Canonical() string {
	switch s.Kind {
	case SpecEveryMinutes:
		return fmt.Sprintf("every %dm", s.Minutes)
	case SpecDailyAt:
		return fmt.Sprintf("daily %02d:%02d", s.Hour, s.Minute)
	case SpecWeeklyOn:
		return fmt.Sprintf("weekly %s %02d:%02d", dayAbbreviation(s.Weekday), s.Hour, s.Minute)
	}
	return "invalid"
}

// NextAfter is the first time of the schedule strictly after t, in UTC. It is only meaningful for values
// that have no Problems; out of range values are clamped so the function never panics.
func (s Spec) NextAfter(t time.Time) time.Time {
	t = t.UTC()
	switch s.Kind {
	case SpecEveryMinutes:
		n := int64(min(max(s.Minutes, 1), MaxEveryMinutes))
		minutes := floorDiv(t.Unix(), 60)
		next := (floorDiv(minutes, n) + 1) * n
		return time.Unix(next*60, 0).UTC()
	case SpecWeeklyOn:
		today := (int(t.Weekday()) + 6) % 7 // days from Monday
		wanted := (int(s.Weekday)%7 + 6) % 7
		ahead := ((wanted-today)%7 + 7) % 7
		return atOrAfter(t, t.AddDate(0, 0, ahead), s.Hour, s.Minute, 7)
	}
	// SpecDailyAt, and the clamped result for the empty Spec.
	return atOrAfter(t, t, s.Hour, s.Minute, 1)
}

func floorDiv(a, b int64) int64 {
	q := a / b
	if a%b != 0 && (a < 0) != (b < 0) {
		q--
	}
	return q
}

// atOrAfter is day at hour:minute UTC if that is after t, otherwise step days later.
func atOrAfter(t, day time.Time, hour, minute, step int) time.Time {
	hour, minute = min(max(hour, 0), 23), min(max(minute, 0), 59)
	candidate := time.Date(day.Year(), day.Month(), day.Day(), hour, minute, 0, 0, time.UTC)
	if candidate.After(t) {
		return candidate
	}
	return candidate.AddDate(0, 0, step)
}

func dayAbbreviation(d time.Weekday) string {
	switch d {
	case time.Monday:
		return "mon"
	case time.Tuesday:
		return "tue"
	case time.Wednesday:
		return "wed"
	case time.Thursday:
		return "thu"
	case time.Friday:
		return "fri"
	case time.Saturday:
		return "sat"
	}
	return "sun"
}

// DeclaredSchedule is a schedule a binary declares: the full job name it enqueues, and when.
type DeclaredSchedule struct {
	// Name is the full job name, <app>.<job>.
	Name string
	Spec Spec
}

// SyncSchedules makes fb_schedule equal to declared, in one transaction, and returns one line per change:
//
//   - a new schedule is inserted, due at its first time after the database's now();
//   - a schedule whose spec text changed gets the new text and a recomputed next_run_at;
//   - an unchanged one is left alone (it keeps its next_run_at);
//   - a row that is not declared is deleted: the binary that migrates is the source of truth.
//
// With dryRun the transaction is rolled back and the lines say what would change.
func SyncSchedules(ctx context.Context, client TxStarter, declared []DeclaredSchedule, dryRun bool) ([]string, error) {
	tx, end, err := begin(ctx, client)
	if err != nil {
		return nil, err
	}
	defer end()
	var now time.Time
	if err := tx.QueryRow(ctx, "SELECT now()").Scan(&now); err != nil {
		return nil, wrap(err)
	}
	stored := map[string]string{}
	rows, err := tx.Query(ctx, "SELECT name, spec FROM fb_schedule")
	if err != nil {
		return nil, wrap(err)
	}
	for rows.Next() {
		var name, spec string
		if err := rows.Scan(&name, &spec); err != nil {
			rows.Close()
			return nil, wrap(err)
		}
		stored[name] = spec
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, wrap(err)
	}
	lines := []string{}
	names := map[string]bool{}
	for _, schedule := range declared {
		names[schedule.Name] = true
		spec := schedule.Spec.Canonical()
		next := schedule.Spec.NextAfter(now)
		old, exists := stored[schedule.Name]
		switch {
		case !exists:
			if _, err := tx.Exec(ctx, "INSERT INTO fb_schedule (name, spec, next_run_at) VALUES ($1, $2, $3)",
				schedule.Name, spec, next); err != nil {
				return nil, wrap(err)
			}
			lines = append(lines, fmt.Sprintf("add %s (%s)", schedule.Name, spec))
		case old != spec:
			if _, err := tx.Exec(ctx, "UPDATE fb_schedule SET spec = $2, next_run_at = $3, updated_at = now() WHERE name = $1",
				schedule.Name, spec, next); err != nil {
				return nil, wrap(err)
			}
			lines = append(lines, fmt.Sprintf("change %s (%s -> %s)", schedule.Name, old, spec))
		}
	}
	var gone []string
	for name := range stored {
		if !names[name] {
			gone = append(gone, name)
		}
	}
	// Rust keeps the stored rows in a BTreeMap: removals are in byte order of the name.
	sort.Strings(gone)
	for _, name := range gone {
		if _, err := tx.Exec(ctx, "DELETE FROM fb_schedule WHERE name = $1", name); err != nil {
			return nil, wrap(err)
		}
		lines = append(lines, fmt.Sprintf("remove %s (%s)", name, stored[name]))
	}
	if dryRun {
		if err := tx.Rollback(ctx); err != nil {
			return nil, wrap(err)
		}
	} else if err := tx.Commit(ctx); err != nil {
		return nil, wrap(err)
	}
	return lines, nil
}

// Tick enqueues the job of every schedule that is due, and returns how many it enqueued.
//
// known are the schedules of this binary by full job name; a due row of another name is left for a binary
// that has it. The algorithm:
//
//  1. One statement reads the due rows (next_run_at <= at) of the known names together with at. at is the
//     database's now() of that statement, so it never comes from the worker's clock; atOverride, when not
//     nil, replaces it (tests only).
//  2. For each due row, in a transaction of its own: UPDATE fb_schedule SET next_run_at = <next>,
//     last_run_at = at WHERE name = $1 AND next_run_at = <the value read>. Only one of several concurrent
//     workers changes the row; the others find the guard false and get no row. The winner enqueues the job
//     in the same transaction (enqueued_by = 'scheduler', payload {"scheduled_for": "<the due time, RFC
//     3339 UTC>"}) and commits.
//  3. <next> is the first time of the schedule after at, so ticks that were missed (no worker was
//     running) are not caught up: one run, then the next future time.
func Tick(ctx context.Context, client Client, registry *core.Registry, known map[string]Spec, atOverride *time.Time) (int, error) {
	if len(known) == 0 {
		return 0, nil
	}
	names := make([]string, 0, len(known))
	for name := range known {
		names = append(names, name)
	}
	sort.Strings(names)
	type dueRow struct {
		name     string
		seen, at time.Time
	}
	rows, err := client.Query(ctx,
		"SELECT name, next_run_at, coalesce($1::timestamptz, now()) FROM fb_schedule "+
			"WHERE next_run_at <= coalesce($1::timestamptz, now()) AND name = ANY($2::text[]) "+
			"ORDER BY next_run_at, name", atOverride, names)
	if err != nil {
		return 0, wrap(err)
	}
	var due []dueRow
	for rows.Next() {
		var d dueRow
		if err := rows.Scan(&d.name, &d.seen, &d.at); err != nil {
			rows.Close()
			return 0, wrap(err)
		}
		due = append(due, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, wrap(err)
	}
	enqueued := 0
	for _, d := range due {
		spec, ok := known[d.name]
		if !ok {
			continue
		}
		ok, err := claimTick(ctx, client, registry, d.name, d.seen, d.at, spec.NextAfter(d.at))
		if err != nil {
			return enqueued, err
		}
		if ok {
			enqueued++
		}
	}
	return enqueued, nil
}

// claimTick moves one due schedule forward and enqueues its job, in one transaction. It is false when
// another worker won the due time.
func claimTick(ctx context.Context, client Client, registry *core.Registry, name string, seen, at, next time.Time) (bool, error) {
	tx, end, err := begin(ctx, client)
	if err != nil {
		return false, err
	}
	defer end()
	var scheduledFor string
	err = tx.QueryRow(ctx,
		"UPDATE fb_schedule SET next_run_at = $2, last_run_at = $3, updated_at = now() "+
			"WHERE name = $1 AND next_run_at = $4 "+
			"RETURNING to_char($4::timestamptz AT TIME ZONE 'UTC', 'YYYY-MM-DD\"T\"HH24:MI:SS\"Z\"')",
		name, next, at, seen).Scan(&scheduledFor)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, wrap(err)
	}
	if _, err := Enqueue(ctx, tx, registry, name, map[string]any{"scheduled_for": scheduledFor}, SchedulerActor); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, wrap(err)
	}
	return true, nil
}
