package db

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Unit tests of the schedule times (no database). Ported from the #[cfg(test)] module of
// crates/forgebase-db/src/schedule.rs.

// utc reads `YYYY-MM-DD H:MM[:SS[.fraction]]` as a UTC time.
func utc(t *testing.T, text string) time.Time {
	t.Helper()
	date, clock, _ := strings.Cut(text, " ")
	hms, fraction, _ := strings.Cut(clock, ".")
	var y, mo, d int
	if _, err := fmt.Sscanf(date, "%d-%d-%d", &y, &mo, &d); err != nil {
		t.Fatal(err)
	}
	clockParts := [3]int{}
	for i, p := range strings.Split(hms, ":") {
		n, err := strconv.Atoi(p)
		if err != nil {
			t.Fatal(err)
		}
		clockParts[i] = n
	}
	nanos := 0
	if fraction != "" {
		n, err := strconv.Atoi((fraction + "000000000")[:9])
		if err != nil {
			t.Fatal(err)
		}
		nanos = n
	}
	return time.Date(y, time.Month(mo), d, clockParts[0], clockParts[1], clockParts[2], nanos, time.UTC)
}

func every(t *testing.T, n int, at string) time.Time { return EveryMinutes(n).NextAfter(utc(t, at)) }

func daily(t *testing.T, hour, minute int, at string) time.Time {
	return DailyAt(hour, minute).NextAfter(utc(t, at))
}

func weekly(t *testing.T, day time.Weekday, hour, minute int, at time.Time) time.Time {
	return WeeklyOn(day, hour, minute).NextAfter(at)
}

func sameTime(t *testing.T, got, want time.Time, label string) {
	t.Helper()
	if !got.Equal(want) {
		t.Errorf("%s: got %s, want %s", label, got.Format(time.RFC3339Nano), want.Format(time.RFC3339Nano))
	}
}

// Rust: schedule.rs::next_after_stays_strictly_after_at_the_end_of_the_time_range
func TestNextAfterStaysStrictlyAfterAtTheEndOfTheTimeRange(t *testing.T) {
	end := time.Date(9999, 12, 31, 12, 0, 0, 0, time.UTC)
	for _, spec := range []Spec{EveryMinutes(5), DailyAt(3, 0), WeeklyOn(time.Monday, 3, 0)} {
		if !spec.NextAfter(end).After(end) {
			t.Errorf("%v", spec)
		}
	}
}

// Rust: schedule.rs::every_n_minutes_is_aligned_to_the_epoch_and_strictly_after
func TestEveryNMinutesIsAlignedToTheEpochAndStrictlyAfter(t *testing.T) {
	sameTime(t, every(t, 15, "2026-10-30 10:07:30"), utc(t, "2026-10-30 10:15"), "inside")
	// On a tick: the next one, not the same.
	sameTime(t, every(t, 15, "2026-10-30 10:15"), utc(t, "2026-10-30 10:30"), "on a tick")
	// One nanosecond before a tick, and one second after.
	sameTime(t, every(t, 15, "2026-10-30 10:14:59.999999999"), utc(t, "2026-10-30 10:15"), "before")
	sameTime(t, every(t, 15, "2026-10-30 10:15:01"), utc(t, "2026-10-30 10:30"), "after")
	// The epoch itself.
	sameTime(t, every(t, 1, "1970-01-01 0:00"), utc(t, "1970-01-01 0:01"), "epoch")
	// Before the epoch.
	sameTime(t, every(t, 60, "1969-12-31 23:30"), utc(t, "1970-01-01 0:00"), "before the epoch")
	// 7 does not divide the hour: the alignment is to the epoch, not to the hour.
	at := every(t, 7, "2026-10-30 10:00")
	if (at.Unix()/60)%7 != 0 || !at.After(utc(t, "2026-10-30 10:00")) || at.After(utc(t, "2026-10-30 10:07")) {
		t.Error(at)
	}
}

// Rust: schedule.rs::every_minute_crosses_the_minute_hour_day_month_and_year_boundaries
func TestEveryMinuteCrossesTheMinuteHourDayMonthAndYearBoundaries(t *testing.T) {
	sameTime(t, every(t, 1, "2026-10-30 10:07:30"), utc(t, "2026-10-30 10:08"), "minute")
	sameTime(t, every(t, 1, "2026-10-30 23:59:59"), utc(t, "2026-10-31 0:00"), "day")
	sameTime(t, every(t, 1, "2026-12-31 23:59"), utc(t, "2027-01-01 0:00"), "year")
	sameTime(t, every(t, 1, "2028-02-28 23:59"), utc(t, "2028-02-29 0:00"), "leap day")
}

// Rust: schedule.rs::every_1440_minutes_is_midnight_utc
func TestEvery1440MinutesIsMidnightUTC(t *testing.T) {
	sameTime(t, every(t, 1440, "2026-10-30 0:00"), utc(t, "2026-10-31 0:00"), "a")
	sameTime(t, every(t, 1440, "2026-10-30 23:59"), utc(t, "2026-10-31 0:00"), "b")
	sameTime(t, every(t, 1440, "2026-12-31 12:00"), utc(t, "2027-01-01 0:00"), "c")
}

// Rust: schedule.rs::a_time_with_an_offset_is_read_in_utc
func TestATimeWithAnOffsetIsReadInUTC(t *testing.T) {
	at := utc(t, "2026-10-30 0:00").In(time.FixedZone("", 2*3600))
	sameTime(t, DailyAt(2, 0).NextAfter(at), utc(t, "2026-10-30 2:00"), "offset")
}

// Rust: schedule.rs::daily_is_today_before_the_time_and_tomorrow_at_or_after_it
func TestDailyIsTodayBeforeTheTimeAndTomorrowAtOrAfterIt(t *testing.T) {
	sameTime(t, daily(t, 2, 30, "2026-10-30 2:29:59"), utc(t, "2026-10-30 2:30"), "before")
	sameTime(t, daily(t, 2, 30, "2026-10-30 2:30"), utc(t, "2026-10-31 2:30"), "at")
	sameTime(t, daily(t, 2, 30, "2026-10-30 2:30:00.000000001"), utc(t, "2026-10-31 2:30"), "after")
	sameTime(t, daily(t, 0, 0, "2026-10-30 0:00"), utc(t, "2026-10-31 0:00"), "midnight")
	sameTime(t, daily(t, 23, 59, "2026-10-30 23:59"), utc(t, "2026-10-31 23:59"), "last minute")
}

// Rust: schedule.rs::daily_crosses_the_end_of_the_month_the_year_and_a_leap_day
func TestDailyCrossesTheEndOfTheMonthTheYearAndALeapDay(t *testing.T) {
	sameTime(t, daily(t, 3, 0, "2026-04-30 4:00"), utc(t, "2026-05-01 3:00"), "month")
	sameTime(t, daily(t, 3, 0, "2026-12-31 4:00"), utc(t, "2027-01-01 3:00"), "year")
	// 2028 is a leap year, 2027 and 2100 are not.
	sameTime(t, daily(t, 3, 0, "2028-02-28 4:00"), utc(t, "2028-02-29 3:00"), "leap")
	sameTime(t, daily(t, 3, 0, "2028-02-29 4:00"), utc(t, "2028-03-01 3:00"), "after leap")
	sameTime(t, daily(t, 3, 0, "2027-02-28 4:00"), utc(t, "2027-03-01 3:00"), "2027")
	sameTime(t, daily(t, 3, 0, "2100-02-28 4:00"), utc(t, "2100-03-01 3:00"), "2100")
}

// Rust: schedule.rs::weekly_finds_every_weekday_from_a_known_day
func TestWeeklyFindsEveryWeekdayFromAKnownDay(t *testing.T) {
	// 2026-10-30 is a Friday.
	friday := utc(t, "2026-10-30 12:00")
	if friday.Weekday() != time.Friday {
		t.Fatal(friday.Weekday())
	}
	for _, c := range []struct {
		day  time.Weekday
		next string
	}{
		{time.Saturday, "2026-10-31 2:00"},
		{time.Sunday, "2026-11-01 2:00"},
		{time.Monday, "2026-11-02 2:00"},
		{time.Tuesday, "2026-11-03 2:00"},
		{time.Wednesday, "2026-11-04 2:00"},
		{time.Thursday, "2026-11-05 2:00"},
		// The same day, but the time has passed: a week later.
		{time.Friday, "2026-11-06 2:00"},
	} {
		sameTime(t, weekly(t, c.day, 2, 0, friday), utc(t, c.next), c.day.String())
	}
}

// Rust: schedule.rs::weekly_on_the_same_day_before_at_and_after_the_time
func TestWeeklyOnTheSameDayBeforeAtAndAfterTheTime(t *testing.T) {
	sameTime(t, weekly(t, time.Friday, 2, 0, utc(t, "2026-10-30 1:59:59")), utc(t, "2026-10-30 2:00"), "before")
	sameTime(t, weekly(t, time.Friday, 2, 0, utc(t, "2026-10-30 2:00")), utc(t, "2026-11-06 2:00"), "at")
	sameTime(t, weekly(t, time.Friday, 2, 0, utc(t, "2026-10-30 2:00:01")), utc(t, "2026-11-06 2:00"), "after")
}

// Rust: schedule.rs::weekly_crosses_the_end_of_the_month_the_year_and_a_leap_day
func TestWeeklyCrossesTheEndOfTheMonthTheYearAndALeapDay(t *testing.T) {
	// Thursday 2026-12-31 -> Monday 2027-01-04.
	sameTime(t, weekly(t, time.Monday, 0, 0, utc(t, "2026-12-31 5:00")), utc(t, "2027-01-04 0:00"), "year")
	// Sunday 2028-02-27 -> Thursday 2028-03-02 over the leap day.
	sameTime(t, weekly(t, time.Thursday, 6, 15, utc(t, "2028-02-27 7:00")), utc(t, "2028-03-02 6:15"), "leap")
	// Tuesday 2028-02-29 itself.
	sameTime(t, weekly(t, time.Tuesday, 6, 15, utc(t, "2028-02-28 7:00")), utc(t, "2028-02-29 6:15"), "leap day")
}

// Rust: schedule.rs::the_canonical_text_names_the_schedule
func TestTheCanonicalTextNamesTheSchedule(t *testing.T) {
	for spec, text := range map[Spec]string{
		EveryMinutes(15):             "every 15m",
		DailyAt(2, 0):                "daily 02:00",
		WeeklyOn(time.Monday, 2, 5):  "weekly mon 02:05",
		WeeklyOn(time.Sunday, 23, 9): "weekly sun 23:09",
	} {
		if got := spec.Canonical(); got != text {
			t.Errorf("%q, want %q", got, text)
		}
	}
}

// Rust: schedule.rs::invalid_values_are_listed_and_the_edges_are_valid
func TestInvalidValuesAreListedAndTheEdgesAreValid(t *testing.T) {
	for _, c := range []struct {
		spec Spec
		n    int
	}{
		{EveryMinutes(1), 0}, {EveryMinutes(1440), 0}, {EveryMinutes(0), 1}, {EveryMinutes(1441), 1},
		{DailyAt(23, 59), 0}, {DailyAt(24, 0), 1}, {DailyAt(24, 60), 2},
		{WeeklyOn(time.Friday, 0, 60), 1}, {DailyAt(-1, 0), 1}, {Spec{}, 1},
	} {
		if got := c.spec.Problems(); len(got) != c.n {
			t.Errorf("%+v: %v, want %d problem(s)", c.spec, got, c.n)
		}
	}
}

// Rust: schedule.rs::invalid_values_do_not_panic_in_next_after
func TestInvalidValuesDoNotPanicInNextAfter(t *testing.T) {
	at := utc(t, "2026-10-30 12:00")
	for _, spec := range []Spec{EveryMinutes(0), EveryMinutes(1 << 40), DailyAt(99, 99), DailyAt(-5, -5), WeeklyOn(time.Weekday(99), 0, 0), {}} {
		if !spec.NextAfter(at).After(at) {
			t.Errorf("%+v", spec)
		}
	}
}
