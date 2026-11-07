import { describe, expect, it } from 'vitest';

import { conflictMessage, isStaleCode, mapDetail, mapDetails, mapNamedDetails } from './errors';
import { errorKey } from './fields';
import { field, meta } from './testing';

const order = meta([
  field('customer', 'link', { target: 'customer', required: true }),
  field('credit', 'decimal'),
  field('lines', 'table', {
    target: 'line',
    required: true,
    fields: [field('item', 'data'), field('qty', 'int')],
  }),
]);

describe('error details to fields', () => {
  it('maps a field problem', () => {
    expect(mapDetail('field `customer` is required', order)).toEqual({
      key: 'customer',
      message: 'This field is required.',
    });
    expect(mapDetail('field `credit`: 3 decimal places exceeds scale 2', order)).toEqual({
      key: 'credit',
      message: '3 decimal places exceeds scale 2.',
    });
    expect(mapDetail('field `customer`: customer `X` does not exist', order)).toEqual({
      key: 'customer',
      message: 'Customer `X` does not exist.',
    });
  });

  it('maps a cell of a table, in both wordings the API uses', () => {
    expect(mapDetail('field `lines` row 2: field `qty` is required', order)).toEqual({
      key: errorKey('lines', 2, 'qty'),
      message: 'This field is required.',
    });
    expect(mapDetail('field `lines`: row 3: field `qty`: expected an integer, found a string', order)).toEqual({
      key: errorKey('lines', 3, 'qty'),
      message: 'Expected an integer, found a string.',
    });
  });

  it('maps a table problem that is not about a cell to the table', () => {
    expect(mapDetail('field `lines` is required', order)).toEqual({
      key: 'lines',
      message: 'This field is required.',
    });
    expect(mapDetail('field `lines` row 1: unknown field `zzz`', order)?.key).toBe('lines');
  });

  it('keeps a message about a read-only field, a cell of a table included', () => {
    const locked = meta([
      field('total', 'decimal', { read_only: true }),
      field('lines', 'table', { target: 'line', fields: [field('amount', 'decimal', { read_only: true })] }),
    ]);
    const mapped = mapDetails(
      ['field `total`: 3 decimal places exceeds scale 2', 'field `lines` row 1: field `amount` is required'],
      locked,
    );
    expect(mapped.unmapped).toEqual([]);
    expect(mapped.errors).toEqual({
      total: '3 decimal places exceeds scale 2.',
      [errorKey('lines', 1, 'amount')]: 'This field is required.',
    });
  });

  it('leaves what names no field of the form unmapped', () => {
    expect(mapDetail('unknown field `nope`', order)).toBeNull();
    expect(mapDetail('field `nope` is required', order)).toBeNull();
    expect(mapDetail('the given name differs from the value of naming field `x`', order)).toBeNull();
  });

  it('splits a list: the first message per field wins, the rest is unmapped', () => {
    const mapped = mapDetails(
      [
        'field `customer` is required',
        'field `customer`: customer `X` does not exist',
        'field `lines` row 1: field `item` is required',
        'unknown field `nope`',
      ],
      order,
    );
    expect(mapped.errors).toEqual({
      customer: 'This field is required.',
      [errorKey('lines', 1, 'item')]: 'This field is required.',
    });
    expect(mapped.unmapped).toEqual(['unknown field `nope`']);
  });
});

describe('conflictMessage', () => {
  it('says the edits are kept only when the form has edits', () => {
    expect(conflictMessage('saved', true)).toBe(
      'Someone else changed this document after you opened it. Your edits are kept below and nothing was saved.',
    );
    expect(conflictMessage('submitted', false)).toBe(
      'Someone else changed this document after you opened it. Nothing was submitted.',
    );
  });
});

describe('isStaleCode', () => {
  it('is true for a conflict and a moved-on status only', () => {
    expect(isStaleCode('conflict')).toBe(true);
    expect(isStaleCode('invalid_docstatus')).toBe(true);
    expect(isStaleCode('validation_failed')).toBe(false);
  });
});

describe('mapNamedDetails', () => {
  const names = new Set(['from', 'to']);

  it('maps the details that name a known field and leaves the rest', () => {
    const mapped = mapNamedDetails(
      [
        'field `from` is required',
        'field `to`: `x` is not a date in the form YYYY-MM-DD',
        'unknown field `extra`',
        'field `other`: nope',
      ],
      names,
    );
    expect(mapped.errors).toEqual({
      from: 'This field is required.',
      to: '`x` is not a date in the form YYYY-MM-DD.',
    });
    expect(mapped.unmapped).toEqual(['unknown field `extra`', 'field `other`: nope']);
  });

  it('keeps the first message of a field', () => {
    const mapped = mapNamedDetails(['field `from` is required', 'field `from`: bad'], names);
    expect(mapped.errors).toEqual({ from: 'This field is required.' });
    expect(mapped.unmapped).toEqual([]);
  });
});
