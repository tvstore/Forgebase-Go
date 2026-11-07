import { describe, expect, it } from 'vitest';

import { buildFilters, chipLabel, filterChips, focusAfterRemove, opsFor, parseFilters, withoutFilter } from './filters';
import type { FilterField, FilterRow } from './filters';
import type { FieldType } from './types';

const fields: FilterField[] = [
  { name: 'name', label: 'Name', type: 'data' },
  { name: 'qty', label: 'Qty', type: 'int' },
  { name: 'price', label: 'Price', type: 'decimal' },
  { name: 'done', label: 'Done', type: 'check' },
  { name: 'customer', label: 'Customer', type: 'link' },
  { name: 'tier', label: 'Tier', type: 'select', options: ['a', 'b'] },
  { name: 'on', label: 'On', type: 'date' },
];

const row = (field: string, op: FilterRow['op'], value = ''): FilterRow => ({ field, op, value });

describe('opsFor', () => {
  it('offers only what the API accepts for each type', () => {
    const lessThan = (t: FieldType) => opsFor(t).includes('<');
    const like = (t: FieldType) => opsFor(t).includes('like');
    for (const t of ['data', 'text', 'select'] as const) {
      expect(like(t), t).toBe(true);
      expect(lessThan(t), t).toBe(true);
    }
    for (const t of ['int', 'decimal', 'date', 'datetime'] as const) {
      expect(like(t), t).toBe(false);
      expect(lessThan(t), t).toBe(true);
    }
    for (const t of ['check', 'link'] as const) {
      expect(like(t), t).toBe(false);
      expect(lessThan(t), t).toBe(false);
      expect(opsFor(t)).toEqual(['=', '!=', 'in', 'is null', 'is not null']);
    }
    expect(opsFor('table')).toEqual([]);
    for (const t of ['data', 'int', 'check', 'link'] as const) {
      expect(opsFor(t)).toContain('=');
      expect(opsFor(t)).toContain('is null');
    }
  });
});

describe('buildFilters', () => {
  it('builds the JSON shape of the API with typed values', () => {
    const built = buildFilters(
      [
        row('name', 'like', 'A%'),
        row('qty', '>=', ' 5 '),
        row('price', '<', '12.50'),
        row('done', '=', 'true'),
        row('customer', '!=', 'acme'),
        row('on', '=', '2026-10-29'),
      ],
      fields,
    );
    expect(built).toEqual({
      ok: true,
      filters: [
        { field: 'name', op: 'like', value: 'A%' },
        { field: 'qty', op: '>=', value: 5 },
        { field: 'price', op: '<', value: '12.50' }, // a decimal is a string
        { field: 'done', op: '=', value: true },
        { field: 'customer', op: '!=', value: 'acme' },
        { field: 'on', op: '=', value: '2026-10-29' },
      ],
    });
  });

  it('builds in, is null and is not null', () => {
    expect(buildFilters([row('qty', 'in', '1, 2,3')], fields)).toEqual({
      ok: true,
      filters: [{ field: 'qty', op: 'in', value: [1, 2, 3] }],
    });
    expect(buildFilters([row('tier', 'in', 'a,b')], fields)).toEqual({
      ok: true,
      filters: [{ field: 'tier', op: 'in', value: ['a', 'b'] }],
    });
    // No value key at all, whatever text is left in the row.
    expect(buildFilters([row('qty', 'is null', 'junk'), row('done', 'is not null')], fields)).toEqual({
      ok: true,
      filters: [
        { field: 'qty', op: 'is null' },
        { field: 'done', op: 'is not null' },
      ],
    });
  });

  it('builds an empty list for no rows', () => {
    expect(buildFilters([], fields)).toEqual({ ok: true, filters: [] });
  });

  it('keeps the spaces of a like pattern and trims other values', () => {
    expect(buildFilters([row('name', 'like', ' a b%')], fields)).toEqual({
      ok: true,
      filters: [{ field: 'name', op: 'like', value: ' a b%' }],
    });
    expect(buildFilters([row('name', '=', '  x ')], fields)).toEqual({
      ok: true,
      filters: [{ field: 'name', op: '=', value: 'x' }],
    });
  });

  it('refuses what the API would refuse, naming the field', () => {
    const problem = (rows: FilterRow[]) => {
      const built = buildFilters(rows, fields);
      return built.ok ? null : built.error;
    };
    expect(problem([row('nope', '=', 'x')])).toMatch(/unknown field/);
    expect(problem([row('done', '<', 'true')])).toMatch(/cannot be used with Done/);
    expect(problem([row('customer', 'like', 'a')])).toMatch(/cannot be used with Customer/);
    expect(problem([row('qty', 'like', '1')])).toMatch(/cannot be used with Qty/);
    expect(problem([row('qty', '=', 'abc')])).toMatch(/Qty: .*whole number/);
    expect(problem([row('qty', '=', '1.5')])).toMatch(/whole number/);
    expect(problem([row('qty', '=', '99999999999999999999')])).toMatch(/too large/);
    expect(problem([row('name', '=', '')])).toMatch(/enter a value for Name/);
    expect(problem([row('name', '=', '   ')])).toMatch(/enter a value for Name/);
    expect(problem([row('qty', 'in', '1,,2')])).toMatch(/separated by commas/);
    expect(problem([row('qty', 'in', '')])).toMatch(/separated by commas/);
    expect(problem([row('qty', 'in', '1,x')])).toMatch(/Qty/);
    expect(problem([row('done', '=', '')])).toMatch(/yes or no/);
  });
});

describe('parseFilters', () => {
  it('reads the address back into rows', () => {
    const text = JSON.stringify([
      { field: 'qty', op: 'in', value: [1, 2] },
      { field: 'done', op: '=', value: true },
      { field: 'name', op: 'is null' },
    ]);
    expect(parseFilters(text)).toEqual([
      { field: 'qty', op: 'in', value: '1, 2' },
      { field: 'done', op: '=', value: 'true' },
      { field: 'name', op: 'is null', value: '' },
    ]);
  });

  it('round-trips through buildFilters', () => {
    const rows = [row('qty', 'in', '1, 2'), row('done', '=', 'false'), row('name', 'like', 'a%')];
    const built = buildFilters(rows, fields);
    if (!built.ok) throw new Error(built.error);
    expect(parseFilters(JSON.stringify(built.filters))).toEqual(rows);
  });

  it('gives no rows for anything malformed', () => {
    for (const text of [null, '', 'nope', '{}', '"x"', '[1]', '[null]', '[{"field":1,"op":"="}]', '[{"field":"a","op":"drop"}]']) {
      expect(parseFilters(text), String(text)).toEqual([]);
    }
  });
});

describe('focusAfterRemove', () => {
  it('moves to the row that took the place of the removed one', () => {
    expect(focusAfterRemove(0, 3)).toEqual({ kind: 'row', index: 0 });
    expect(focusAfterRemove(1, 3)).toEqual({ kind: 'row', index: 1 });
  });

  it('moves to the previous row when the last one was removed', () => {
    expect(focusAfterRemove(2, 3)).toEqual({ kind: 'row', index: 1 });
  });

  it('moves to "Add filter" when no row remains', () => {
    expect(focusAfterRemove(0, 1)).toEqual({ kind: 'add' });
  });
});

describe('filter chips', () => {
  const row = (field: string, op: FilterRow['op'], value = ''): FilterRow => ({ field, op, value });

  it('words a filter the way the user would', () => {
    expect(chipLabel(row('tier', '='), fields)).toBe('Tier is ');
    expect(chipLabel(row('tier', '=', 'a'), fields)).toBe('Tier is a');
    expect(chipLabel(row('tier', '!=', 'b'), fields)).toBe('Tier is not b');
    expect(chipLabel(row('qty', '>=', '10'), fields)).toBe('Qty >= 10');
    expect(chipLabel(row('name', 'like', 'ad%'), fields)).toBe('Name matches ad%');
    expect(chipLabel(row('name', 'in', 'a, b'), fields)).toBe('Name in a, b');
    expect(chipLabel(row('customer', 'is null'), fields)).toBe('Customer is empty');
    expect(chipLabel(row('customer', 'is not null'), fields)).toBe('Customer is not empty');
  });

  it('shows a yes/no field as Yes or No', () => {
    expect(chipLabel(row('done', '=', 'true'), fields)).toBe('Done is Yes');
    expect(chipLabel(row('done', '=', 'false'), fields)).toBe('Done is No');
  });

  it('falls back to the field name for a field the DocType no longer has', () => {
    expect(chipLabel(row('gone', '=', 'x'), fields)).toBe('gone is x');
  });

  const text = JSON.stringify([
    { field: 'tier', op: '=', value: 'a' },
    { field: 'qty', op: '>', value: 3 },
    { field: 'customer', op: 'is null' },
  ]);

  it('has one chip per filter of the address, in order', () => {
    expect(filterChips(text, fields)).toEqual([
      { index: 0, label: 'Tier is a' },
      { index: 1, label: 'Qty > 3' },
      { index: 2, label: 'Customer is empty' },
    ]);
    expect(filterChips(null, fields)).toEqual([]);
    expect(filterChips('not json', fields)).toEqual([]);
  });

  it('removes one filter and keeps the others', () => {
    const without = (i: number) => JSON.parse(withoutFilter(text, i, fields) ?? 'null');
    expect(without(0)).toEqual([
      { field: 'qty', op: '>', value: 3 },
      { field: 'customer', op: 'is null' },
    ]);
    expect(without(1)).toEqual([
      { field: 'tier', op: '=', value: 'a' },
      { field: 'customer', op: 'is null' },
    ]);
    expect(without(2)).toEqual([
      { field: 'tier', op: '=', value: 'a' },
      { field: 'qty', op: '>', value: 3 },
    ]);
  });

  it('returns null when the last filter is removed, and the text unchanged for a bad index or text', () => {
    const one = JSON.stringify([{ field: 'tier', op: '=', value: 'a' }]);
    expect(withoutFilter(one, 0, fields)).toBeNull();
    expect(withoutFilter(text, 5, fields)).toBe(text);
    expect(withoutFilter(text, -1, fields)).toBe(text);
    expect(withoutFilter(null, 0, fields)).toBeNull();
    expect(withoutFilter('not json', 0, fields)).toBe('not json');
  });
});
