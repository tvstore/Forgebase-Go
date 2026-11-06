import { afterAll, describe, expect, it, vi } from 'vitest';

import {
  blankState,
  buildChanges,
  buildCreate,
  changedFields,
  emptyValue,
  errorKey,
  localToUtc,
  stateOf,
  toFormValue,
  toJsonValue,
  utcToLocal,
} from './fields';
import { field, meta } from './testing';

const order = meta([
  field('title', 'data', { required: true, max_length: 10 }),
  field('notes', 'text'),
  field('qty', 'int'),
  field('price', 'decimal', { precision: 12, scale: 2 }),
  field('paid', 'check'),
  field('due', 'date'),
  field('at', 'datetime'),
  field('kind', 'select', { options: ['a', 'b'] }),
  field('customer', 'link', { target: 'customer' }),
  field('lines', 'table', {
    target: 'line',
    fields: [field('item', 'data', { required: true }), field('amount', 'int')],
  }),
]);

describe('time zone conversion of Datetime', () => {
  // The conversion uses the time zone of the process (the browser's in the desk).
  const setZone = (name: string) => vi.stubEnv('TZ', name);
  afterAll(() => {
    vi.unstubAllEnvs();
  });

  it('shows UTC as local time and sends local time back as UTC with Z', () => {
    setZone('Asia/Jakarta'); // UTC+7, no daylight saving
    expect(utcToLocal('2026-10-29T15:04:05Z')).toBe('2026-10-29T22:04:05');
    expect(utcToLocal('2026-10-29T15:04:05.123456Z')).toBe('2026-10-29T22:04:05');
    expect(localToUtc('2026-10-29T22:04:05')).toBe('2026-10-29T15:04:05Z');
    // The control may give no seconds.
    expect(localToUtc('2026-10-29T22:04')).toBe('2026-10-29T15:04:00Z');
    // The local date changes the UTC date.
    expect(localToUtc('2026-01-01T03:00')).toBe('2025-12-31T20:00:00Z');
  });

  it('round-trips in zones east and west of UTC and across a daylight saving change', () => {
    for (const zone of ['UTC', 'America/New_York', 'Asia/Kolkata', 'Pacific/Auckland']) {
      setZone(zone);
      for (const utc of ['2026-03-08T07:30:00Z', '2026-07-01T00:00:00Z', '2026-11-01T05:59:59Z']) {
        expect(localToUtc(utcToLocal(utc)), `${zone} ${utc}`).toBe(utc);
      }
    }
    setZone('America/New_York');
    expect(utcToLocal('2026-07-01T12:00:00Z')).toBe('2026-07-01T08:00:00'); // EDT, UTC-4
    expect(utcToLocal('2026-01-01T12:00:00Z')).toBe('2026-01-01T07:00:00'); // EST, UTC-5
  });

  it('refuses impossible and non-existent local times and bad text', () => {
    setZone('America/New_York');
    expect(localToUtc('2026-02-30T10:00')).toBeNull();
    expect(localToUtc('2026-03-08T02:30')).toBeNull(); // skipped by the spring-forward change
    expect(localToUtc('')).toBeNull();
    expect(localToUtc('2026-10-29')).toBeNull();
    expect(localToUtc('2026-10-29T25:00')).toBeNull();
    expect(utcToLocal('not a date')).toBe('');
    expect(utcToLocal('2026-10-29T15:04:05+07:00')).toBe(''); // the API always sends Z
  });
});

describe('value conversion', () => {
  it('Int is a JSON integer; a non-integer is an inline error', () => {
    const qty = field('qty', 'int');
    expect(toJsonValue(qty, '42')).toEqual({ ok: true, value: 42 });
    expect(toJsonValue(qty, ' -7 ')).toEqual({ ok: true, value: -7 });
    expect(toJsonValue(qty, '')).toEqual({ ok: true, value: null });
    for (const bad of ['1.5', '1e3', 'abc', 'not a number', '0x10', '1,000']) {
      expect(toJsonValue(qty, bad), bad).toEqual({ ok: false, error: 'Enter a whole number.' });
    }
    expect(toJsonValue(qty, '9007199254740993').ok).toBe(false); // beyond 2^53: not exact in JSON
  });

  it('Decimal is sent as a string, never a number', () => {
    const price = field('price', 'decimal');
    expect(toJsonValue(price, '1234.50')).toEqual({ ok: true, value: '1234.50' });
    expect(toJsonValue(price, ' .5 ')).toEqual({ ok: true, value: '.5' });
    expect(toJsonValue(price, '-0.10')).toEqual({ ok: true, value: '-0.10' });
    expect(toJsonValue(price, '')).toEqual({ ok: true, value: null });
    for (const bad of ['1,5', 'abc', '1e3', '1.2.3', '--1']) {
      expect(toJsonValue(price, bad).ok, bad).toBe(false);
    }
  });

  it('Check is always a boolean; the other types are text or null when empty', () => {
    expect(toJsonValue(field('paid', 'check'), true)).toEqual({ ok: true, value: true });
    expect(toJsonValue(field('paid', 'check'), false)).toEqual({ ok: true, value: false });
    expect(toJsonValue(field('kind', 'select'), '')).toEqual({ ok: true, value: null }); // empty option
    expect(toJsonValue(field('kind', 'select'), 'a')).toEqual({ ok: true, value: 'a' });
    expect(toJsonValue(field('due', 'date'), '2026-10-29')).toEqual({ ok: true, value: '2026-10-29' });
    expect(toJsonValue(field('due', 'date'), '')).toEqual({ ok: true, value: null });
    expect(toJsonValue(field('title', 'data'), '')).toEqual({ ok: true, value: null });
    // Text is kept exactly as typed, spaces included.
    expect(toJsonValue(field('title', 'data'), ' a b ')).toEqual({ ok: true, value: ' a b ' });
  });

  it('reads API values into control values', () => {
    expect(toFormValue(field('qty', 'int'), 5)).toBe('5');
    expect(toFormValue(field('qty', 'int'), null)).toBe('');
    expect(toFormValue(field('price', 'decimal'), '12.50')).toBe('12.50');
    expect(toFormValue(field('paid', 'check'), true)).toBe(true);
    expect(toFormValue(field('paid', 'check'), null)).toBe(false);
    expect(toFormValue(field('kind', 'select'), null)).toBe('');
    expect(emptyValue(field('paid', 'check'))).toBe(false);
    expect(emptyValue(field('title', 'data'))).toBe('');
  });
});

describe('building requests', () => {
  it('a create sends the non-empty fields and every table', () => {
    const state = blankState(order);
    state.values.title = 'Hello';
    state.values.paid = false;
    const built = buildCreate(order, state);
    // Empty controls are left out; a Check is always sent; a table is sent (here with no rows).
    expect(built).toEqual({ ok: true, data: { title: 'Hello', paid: false, lines: [] } });
  });

  it('a create collects the inline errors of fields and cells', () => {
    const state = blankState(order);
    state.values.qty = '1.5';
    state.tables.lines = [
      { key: 1, values: { item: 'x', amount: '' } },
      { key: 2, values: { item: 'y', amount: 'z' } },
    ];
    const built = buildCreate(order, state);
    expect(built).toEqual({
      ok: false,
      errors: { qty: 'Enter a whole number.', [errorKey('lines', 2, 'amount')]: 'Enter a whole number.' },
    });
  });

  it('a create sends the rows with their non-empty cells', () => {
    const state = blankState(order);
    state.tables.lines = [{ key: 1, values: { item: 'x', amount: '3' } }, { key: 2, values: { item: 'y', amount: '' } }];
    const built = buildCreate(order, state);
    expect(built).toEqual({ ok: true, data: { paid: false, lines: [{ item: 'x', amount: 3 }, { item: 'y' }] } });
  });

  const stored = {
    title: 'Old',
    notes: null,
    qty: 3,
    price: '1.00',
    paid: false,
    due: null,
    at: null,
    kind: 'a',
    customer: null,
    lines: [{ item: 'x', amount: 1 }],
  };
  const all = () => true;

  it('an edit sends only the changed fields', () => {
    const initial = stateOf(order, stored);
    const current = structuredClone(initial);
    expect(changedFields(order, initial, current)).toEqual([]);
    expect(buildChanges(order, initial, current, all)).toEqual({ ok: true, data: {} });

    current.values.title = 'New';
    current.values.kind = ''; // cleared: sent as null
    current.values.paid = true;
    expect(changedFields(order, initial, current)).toEqual(['title', 'paid', 'kind']);
    expect(buildChanges(order, initial, current, all)).toEqual({
      ok: true,
      data: { title: 'New', paid: true, kind: null },
    });
  });

  it('an edit sends a changed table whole and an unchanged one not at all', () => {
    const initial = stateOf(order, stored);
    const current = structuredClone(initial);
    current.tables.lines = [
      ...(current.tables.lines ?? []),
      { key: 99, values: { item: 'new', amount: '' } },
    ];
    expect(buildChanges(order, initial, current, all)).toEqual({
      ok: true,
      data: { lines: [{ item: 'x', amount: 1 }, { item: 'new' }] },
    });
    // A cell edit is a change of the table too.
    const edited = structuredClone(initial);
    if (edited.tables.lines?.[0]) edited.tables.lines[0].values.amount = '2';
    expect(changedFields(order, initial, edited)).toEqual(['lines']);
    // Removing every row is a change: an empty array.
    const emptied = structuredClone(initial);
    emptied.tables.lines = [];
    expect(buildChanges(order, initial, emptied, all)).toEqual({ ok: true, data: { lines: [] } });
  });

  it('an edit never sends a field that is not editable', () => {
    const initial = stateOf(order, stored);
    const current = structuredClone(initial);
    current.values.title = 'New';
    current.values.notes = 'n';
    const built = buildChanges(order, initial, current, (f) => f.name === 'notes');
    expect(built).toEqual({ ok: true, data: { notes: 'n' } });
  });

  it('an edit reports an unsendable value on its field', () => {
    const initial = stateOf(order, stored);
    const current = structuredClone(initial);
    current.values.qty = 'abc';
    expect(buildChanges(order, initial, current, all)).toEqual({
      ok: false,
      errors: { qty: 'Enter a whole number.' },
    });
  });

  it('an unchanged stored Datetime is not sent back (its fractions are not lost)', () => {
    const withTime = { ...stored, at: '2026-10-29T15:04:05.123456Z' };
    const initial = stateOf(order, withTime);
    const current = structuredClone(initial);
    current.values.title = 'New';
    expect(buildChanges(order, initial, current, all)).toEqual({ ok: true, data: { title: 'New' } });
  });
});

describe('read_only fields are shown but never sent', () => {
  const priced = meta([
    field('title', 'data'),
    field('total', 'decimal', { precision: 12, scale: 2, read_only: true }),
    field('lines', 'table', {
      target: 'line',
      fields: [field('item', 'data'), field('qty', 'int'), field('amount', 'decimal', { read_only: true })],
    }),
  ]);
  const stored = { title: 'T', total: '30.00', lines: [{ item: 'x', qty: 3, amount: '30.00' }] };

  it('they are shown: the stored value is in the control', () => {
    const state = stateOf(priced, stored);
    expect(state.values.total).toBe('30.00');
    expect(state.tables.lines?.[0]?.values.amount).toBe('30.00');
  });

  it('a create leaves them out, top level and in rows', () => {
    const state = stateOf(priced, stored);
    expect(buildCreate(priced, state)).toEqual({
      ok: true,
      data: { title: 'T', lines: [{ item: 'x', qty: 3 }] },
    });
  });

  it('an edit leaves them out even when the predicate allows them, and a changed table drops its computed cells', () => {
    const initial = stateOf(priced, stored);
    const current = structuredClone(initial);
    current.values.total = '99.00';
    if (current.tables.lines?.[0]) current.tables.lines[0].values.qty = '4';
    expect(buildChanges(priced, initial, current, () => true)).toEqual({
      ok: true,
      data: { lines: [{ item: 'x', qty: 4 }] },
    });
    // Only the read-only field changed: nothing to send.
    const only = structuredClone(initial);
    only.values.total = '99.00';
    expect(buildChanges(priced, initial, only, () => true)).toEqual({ ok: true, data: {} });
  });
});
