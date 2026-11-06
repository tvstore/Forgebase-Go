import { describe, expect, it } from 'vitest';

import { ariaSort, cellText, isNumericKind, nextSort, rowCountText, sortRows, truncatedText } from './reportRows';
import type { ReportColumn } from './reports';

const col = (name: string, kind: ReportColumn['kind'], doctype?: string): ReportColumn => ({ name, label: name, kind, doctype });

describe('cellText', () => {
  it('writes each kind like the list does', () => {
    expect(cellText(1234567, col('n', 'int'))).toBe('1,234,567');
    expect(cellText('1234.50', col('t', 'decimal'))).toBe('1,234.50');
    expect(cellText('2026-10-29', col('d', 'date'))).toBe('29 Oct 2026');
    expect(cellText('2026-10-29T15:04:05Z', col('d', 'datetime'))).toBe('29 Oct 2026, 15:04 UTC');
    expect(cellText(true, col('c', 'check'))).toBe('Yes');
    expect(cellText(false, col('c', 'check'))).toBe('No');
    expect(cellText(null, col('x', 'data'))).toBe('');
    expect(cellText('<b>x</b>', col('x', 'data'))).toBe('<b>x</b>');
  });

  it('shows the title of a link when there is one', () => {
    const titles = { customer: { 'C-1': 'Acme' } };
    expect(cellText('C-1', col('c', 'link', 'customer'), titles)).toBe('Acme');
    expect(cellText('C-2', col('c', 'link', 'customer'), titles)).toBe('C-2');
  });
});

describe('isNumericKind', () => {
  it('is true for int and decimal only', () => {
    expect(['int', 'decimal'].every((k) => isNumericKind(k as 'int' | 'decimal'))).toBe(true);
    expect(isNumericKind('date')).toBe(false);
    expect(isNumericKind('data')).toBe(false);
  });
});

describe('nextSort and ariaSort', () => {
  it('goes ascending, descending, and back to the order of the SQL', () => {
    const first = nextSort(null, 2);
    expect(first).toEqual({ column: 2, desc: false });
    const second = nextSort(first, 2);
    expect(second).toEqual({ column: 2, desc: true });
    expect(nextSort(second, 2)).toBeNull();
  });

  it('starts ascending on another column', () => {
    expect(nextSort({ column: 1, desc: true }, 3)).toEqual({ column: 3, desc: false });
  });

  it('names the sorted column only', () => {
    expect(ariaSort({ column: 1, desc: false }, 1)).toBe('ascending');
    expect(ariaSort({ column: 1, desc: true }, 1)).toBe('descending');
    expect(ariaSort({ column: 1, desc: true }, 0)).toBeUndefined();
    expect(ariaSort(null, 0)).toBeUndefined();
  });
});

describe('sortRows', () => {
  const columns = [
    col('name', 'data'),
    col('orders', 'int'),
    col('total', 'decimal'),
    col('day', 'date'),
    col('at', 'datetime'),
    col('open', 'check'),
    col('customer', 'link', 'customer'),
  ];
  const rows: unknown[][] = [
    ['b', 10, '9.50', '2026-10-02', '2026-10-02T10:00:00Z', true, 'C-2'],
    ['a', 9, '100.25', '2026-09-30', '2026-10-01T23:00:00Z', false, 'C-1'],
    ['B', null, null, null, null, null, null],
    ['c', 100, '20', '2026-12-01', '2026-10-02T09:00:00Z', true, 'C-3'],
  ];
  const names = (sorted: unknown[][]) => sorted.map((r) => r[0]);

  it('keeps the order of the SQL without a sort', () => {
    expect(sortRows(rows, columns, null)).toBe(rows);
  });

  it('sorts numbers as numbers, not as text', () => {
    expect(names(sortRows(rows, columns, { column: 1, desc: false }))).toEqual(['a', 'b', 'c', 'B']);
    expect(names(sortRows(rows, columns, { column: 1, desc: true }))).toEqual(['c', 'b', 'a', 'B']);
  });

  it('sorts decimals, which arrive as strings, as numbers', () => {
    expect(names(sortRows(rows, columns, { column: 2, desc: false }))).toEqual(['b', 'c', 'a', 'B']);
    expect(names(sortRows(rows, columns, { column: 2, desc: true }))).toEqual(['a', 'c', 'b', 'B']);
  });

  it('sorts dates and datetimes in time order', () => {
    expect(names(sortRows(rows, columns, { column: 3, desc: false }))).toEqual(['a', 'b', 'c', 'B']);
    expect(names(sortRows(rows, columns, { column: 4, desc: false }))).toEqual(['a', 'c', 'b', 'B']);
  });

  it('sorts text with localeCompare rules, ignoring case, and is stable', () => {
    // "b" and "B" are equal ignoring case: they keep the order of the SQL.
    expect(names(sortRows(rows, columns, { column: 0, desc: false }))).toEqual(['a', 'b', 'B', 'c']);
    expect(names(sortRows(rows, columns, { column: 0, desc: true }))).toEqual(['c', 'b', 'B', 'a']);
  });

  it('sorts checks false before true, and links by their title', () => {
    expect(names(sortRows(rows, columns, { column: 5, desc: false }))).toEqual(['a', 'b', 'c', 'B']);
    const titles = { customer: { 'C-1': 'Zed', 'C-2': 'Acme', 'C-3': 'Mid' } };
    expect(names(sortRows(rows, columns, { column: 6, desc: false }, titles))).toEqual(['b', 'c', 'a', 'B']);
  });

  it('puts empty values last in both directions', () => {
    for (const column of [1, 2, 3, 4, 5, 6]) {
      for (const desc of [false, true]) {
        const sorted = sortRows(rows, columns, { column, desc });
        expect(sorted[3]?.[0], `column ${column} desc ${desc}`).toBe('B');
      }
    }
  });

  it('does not change the rows it was given and ignores a column that is not there', () => {
    const copy = JSON.stringify(rows);
    sortRows(rows, columns, { column: 1, desc: true });
    expect(JSON.stringify(rows)).toBe(copy);
    expect(sortRows(rows, columns, { column: 9, desc: false })).toBe(rows);
  });
});

describe('row counts', () => {
  it('says rows with digit grouping', () => {
    expect(rowCountText(0)).toBe('0 rows');
    expect(rowCountText(1)).toBe('1 row');
    expect(rowCountText(1234)).toBe('1,234 rows');
  });

  it('says what a cut result shows', () => {
    expect(truncatedText(10000)).toBe('Showing the first 10,000 rows. Narrow the parameters to see the rest.');
  });
});
