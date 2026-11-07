package app

import (
	"time"

	"github.com/tvstore/forgebase-go/internal/db"
)

// Schedule is when a scheduled job runs, for [App.Schedule]. Three kinds: every N minutes, every day at a
// time, and every week on a day at a time. All times are UTC; there are no time zones and no cron
// expressions.
//
//	forgebase.NewApp("crm").
//		Job("send_digest", sendDigest).
//		Schedule("send_digest", forgebase.DailyAt(6, 30)).
//		Job("weekly_report", weeklyReport).
//		Schedule("weekly_report", forgebase.WeeklyOn(time.Monday, 7, 0))
//
// A Schedule only holds the values it was given. Invalid values (see each constructor) are reported with the
// other mistakes of an app when the binary starts ([StartupError]), not as a panic. Schedules compare with ==.
//
// Rules of a tick (details in ADR 0013):
//
//   - A tick is enqueued once, however many workers run. The job gets the payload {"scheduled_for": "<RFC
//     3339 UTC time of the tick>"}: the time that was due, not the time the worker noticed. Use it for
//     idempotency, because delivery is at-least-once.
//   - Ticks are not caught up. If no worker ran during several ticks, one job is enqueued when a worker
//     returns, and the next one is the next future time.
//   - The schedules of a binary are written to the database by `forgebase migrate`, which deletes the
//     schedules the binary does not declare.
type Schedule struct {
	spec db.Spec
}

// EveryMinutes is a schedule for every n minutes, from 1 to 1440 (one day). The ticks are aligned to the Unix
// epoch, not to the moment the schedule was added or the worker started: they are the times at which the
// number of minutes since 1970-01-01 00:00 UTC is divisible by n. Every 15 gives :00, :15, :30 and :45 of
// every hour; every 1440 gives midnight UTC; a number that does not divide 60 (7, for example) gives times
// that do not repeat each hour.
func EveryMinutes(n int) Schedule { return Schedule{db.EveryMinutes(n)} }

// DailyAt is a schedule for every day at hour:minute UTC. hour is 0 to 23 and minute 0 to 59.
func DailyAt(hour, minute int) Schedule { return Schedule{db.DailyAt(hour, minute)} }

// WeeklyOn is a schedule for every week on weekday at hour:minute UTC. hour is 0 to 23 and minute 0 to 59.
func WeeklyOn(weekday time.Weekday, hour, minute int) Schedule {
	return Schedule{db.WeeklyOn(weekday, hour, minute)}
}

// problems is what is wrong with the values, one message per problem.
func (s Schedule) problems() []string { return s.spec.Problems() }
