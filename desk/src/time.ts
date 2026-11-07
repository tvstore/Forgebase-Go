// Relative times for the list and the info panel ("3 min ago", "yesterday", "12 Oct") and the exact
// local time for the tooltip. Pure functions: the caller passes "now", so tests use a fixed clock.
// Calendar days ("yesterday") are the days of the machine's local time zone.

const MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];

const MINUTE = 60_000;
const HOUR = 60 * MINUTE;

const pad = (n: number): string => String(n).padStart(2, '0');

/** The local calendar day as a number of days, so that two dates can be compared by day. */
function dayNumber(date: Date): number {
  return Math.floor(Date.UTC(date.getFullYear(), date.getMonth(), date.getDate()) / 86_400_000);
}

/** `12 Oct` (this year) or `12 Oct 2025`, in local time. */
function shortDate(date: Date, now: Date): string {
  const text = `${date.getDate()} ${MONTHS[date.getMonth()] ?? ''}`;
  return date.getFullYear() === now.getFullYear() ? text : `${text} ${date.getFullYear()}`;
}

/**
 * How long ago `iso` was, as people say it: "just now", "3 min ago", "2 h ago", "yesterday",
 * "4 days ago", then the date ("12 Oct", with the year when it is not this year). A time in the
 * future (a clock that differs a little from the server's) reads "just now" up to a minute ahead
 * and is the date after that. Text that is not a date is returned as it is.
 */
export function relativeTime(iso: string, now: Date): string {
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return iso;
  const age = now.getTime() - date.getTime();
  if (age < MINUTE) return age > -MINUTE ? 'just now' : shortDate(date, now);
  if (age < HOUR) return `${Math.floor(age / MINUTE)} min ago`;
  const days = dayNumber(now) - dayNumber(date);
  if (days === 0) return `${Math.floor(age / HOUR)} h ago`;
  if (days === 1) return 'yesterday';
  if (days < 7) return `${days} days ago`;
  return shortDate(date, now);
}

/** `12 Oct 2026, 14:05` in the local time zone (for the `title` of a `<time>`); anything else as it is. */
export function exactTime(iso: string): string {
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return iso;
  return `${date.getDate()} ${MONTHS[date.getMonth()] ?? ''} ${date.getFullYear()}, ${pad(date.getHours())}:${pad(date.getMinutes())}`;
}
