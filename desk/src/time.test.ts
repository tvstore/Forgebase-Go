import { describe, expect, it } from 'vitest';

import { exactTime, relativeTime } from './time';

// A fixed clock in local time, so that the tests give the same answer in every time zone.
const now = new Date(2026, 9, 30, 15, 30, 0); // 30 Oct 2026, 15:30 local
const ago = (ms: number): string => new Date(now.getTime() - ms).toISOString();
const local = (y: number, m: number, d: number, h = 12, min = 0): string => new Date(y, m, d, h, min).toISOString();

describe('relativeTime', () => {
  it('says just now under a minute', () => {
    expect(relativeTime(ago(0), now)).toBe('just now');
    expect(relativeTime(ago(59_000), now)).toBe('just now');
  });

  it('counts minutes and hours of today', () => {
    expect(relativeTime(ago(60_000), now)).toBe('1 min ago');
    expect(relativeTime(ago(3 * 60_000), now)).toBe('3 min ago');
    expect(relativeTime(ago(59 * 60_000), now)).toBe('59 min ago');
    expect(relativeTime(local(2026, 9, 30, 13, 30), now)).toBe('2 h ago');
    expect(relativeTime(local(2026, 9, 30, 0, 5), now)).toBe('15 h ago');
  });

  it('says yesterday for the day before, by the calendar and not by 24 hours', () => {
    expect(relativeTime(local(2026, 9, 29, 23, 59), now)).toBe('yesterday');
    expect(relativeTime(local(2026, 9, 29, 0, 1), now)).toBe('yesterday');
    // 17 hours ago, but on the day before: still yesterday.
    expect(relativeTime(local(2026, 9, 29, 22, 30), new Date(2026, 9, 30, 15, 30))).toBe('yesterday');
  });

  it('counts days up to a week, then shows the date', () => {
    expect(relativeTime(local(2026, 9, 28), now)).toBe('2 days ago');
    expect(relativeTime(local(2026, 9, 24), now)).toBe('6 days ago');
    expect(relativeTime(local(2026, 9, 23), now)).toBe('23 Oct');
    expect(relativeTime(local(2026, 9, 12), now)).toBe('12 Oct');
    expect(relativeTime(local(2026, 0, 3), now)).toBe('3 Jan');
  });

  it('adds the year for another year', () => {
    expect(relativeTime(local(2025, 11, 31), now)).toBe('31 Dec 2025');
  });

  it('treats a little clock difference as now and a far future as a date', () => {
    expect(relativeTime(new Date(now.getTime() + 30_000).toISOString(), now)).toBe('just now');
    expect(relativeTime(local(2026, 10, 12), now)).toBe('12 Nov');
  });

  it('returns what is not a date as it is', () => {
    expect(relativeTime('soon', now)).toBe('soon');
    expect(relativeTime('', now)).toBe('');
  });
});

describe('exactTime', () => {
  it('is the local date and time to the minute', () => {
    expect(exactTime(local(2026, 9, 12, 14, 5))).toBe('12 Oct 2026, 14:05');
    expect(exactTime(local(2026, 0, 3, 9, 7))).toBe('3 Jan 2026, 09:07');
  });

  it('returns what is not a date as it is', () => {
    expect(exactTime('x')).toBe('x');
  });
});
