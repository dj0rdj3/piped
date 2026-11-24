import assert from 'node:assert/strict';
import test from 'node:test';
import { formatScheduleTime, formatTimestamp } from './date-time.js';

test('local timestamps use DD MMM [YYYY], HH:MM with the client year omitted', () => {
  for (const [date, currentYear, expected] of [
    [new Date(2026, 0, 2, 0, 4), 2026, '02 Jan, 00:04'],
    [new Date(2025, 10, 23, 17, 9), 2026, '23 Nov 2025, 17:09'],
    [new Date(2027, 0, 1, 0, 1), 2026, '01 Jan 2027, 00:01'],
    [new Date(2026, 8, 3, 13, 5), 2026, '03 Sep, 13:05'],
    [new Date(2026, 11, 31, 23, 59), 2027, '31 Dec 2026, 23:59'],
  ]) {
    assert.equal(formatTimestamp(date.getTime() / 1000, currentYear), expected);
  }
  const currentYear = new Date().getFullYear();
  assert.equal(
    formatTimestamp(new Date(currentYear, 0, 2, 0, 4).getTime() / 1000),
    '02 Jan, 00:04',
  );
});

test('schedule dates retain server wall time but omit years using the client year', () => {
  for (const [timestamp, currentYear, expected] of [
    ['2026-11-24T09:05:00+01:00', 2026, '24 Nov, 09:05'],
    ['2026-01-02T00:04:00Z', 2026, '02 Jan, 00:04'],
    ['2026-12-31T23:59:00-12:00', 2027, '31 Dec 2026, 23:59'],
    ['2027-01-01T00:01:00+14:00', 2026, '01 Jan 2027, 00:01'],
    ['2026-10-25T02:30:00+02:00', 2026, '25 Oct, 02:30'],
    ['2026-10-25T02:30:00+01:00', 2026, '25 Oct, 02:30'],
  ]) {
    assert.equal(formatScheduleTime(timestamp, currentYear), expected);
  }
  const currentYear = new Date().getFullYear();
  assert.equal(formatScheduleTime(`${currentYear}-01-02T00:04:00+14:00`), '02 Jan, 00:04');
});
