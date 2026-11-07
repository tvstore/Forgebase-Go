import { describe, expect, it } from 'vitest';

import {
  MAX_GROUPS,
  MAX_VALUES,
  aggregatesParam,
  defaultGroupBy,
  defaultValues,
  distinctValues,
  formatNumber,
  groupFields,
  groupFilter,
  keyBadge,
  keyText,
  summaryCaption,
  summaryRows,
  truncatedNote,
  valueFields,
  valueHeading,
  valueKey,
  withGroupFilter,
} from './summary';
import type { GroupField, SummaryResult } from './summary';
import { field, meta } from './testing';
import type { DocTypeMeta } from './types';

const order: DocTypeMeta = meta(
  [
    field('title', 'data'),
    field('stage', 'select', { options: ['new', 'won'] }),
    field('customer', 'link', { target: 'customer' }),
    field('notes', 'text'),
    field('amount', 'decimal'),
    field('qty', 'int'),
    field('paid', 'check'),
    field('due', 'date'),
    field('seen', 'datetime'),
    field('lines', 'table'),
  ],
  { label: 'Order' },
);

const names = (fields: { name: string }[]) => fields.map((f) => f.name);

describe('the fields of the summary', () => {
  it('groups by owner and the data, select, link, check, date and int fields', () => {
    expect(names(groupFields(order))).toEqual(['owner', 'title', 'stage', 'customer', 'qty', 'paid', 'due']);
  });

  it('puts the status first: the document status of a submittable DocType, the state of a workflow', () => {
    const submittable = meta([field('a', 'data')], { submittable: true });
    expect(groupFields(submittable)[0]).toEqual({ name: 'docstatus', label: 'Status', type: 'int' });
    const workflow = meta([field('a', 'data')], {
      submittable: true,
      workflow: { states: [{ name: 'Draft', docstatus: 0, style: 'neutral' }], transitions: [] },
    });
    expect(names(groupFields(workflow)).slice(0, 2)).toEqual(['workflow_state', 'owner']);
    expect(names(groupFields(workflow))).not.toContain('docstatus');
  });

  it('sums the int and decimal fields only', () => {
    expect(names(valueFields(order))).toEqual(['amount', 'qty']);
    expect(defaultValues(order)).toEqual([{ field: 'amount', fn: 'sum' }]);
    expect(defaultValues(meta([field('a', 'data')]))).toEqual([]);
  });
});

describe('the field the panel starts with', () => {
  it('is the status, else the first select field, else the first field', () => {
    expect(defaultGroupBy(order)?.name).toBe('stage');
    expect(defaultGroupBy(meta([field('a', 'data')], { submittable: true }))?.name).toBe('docstatus');
    expect(defaultGroupBy(meta([field('a', 'data')]))?.name).toBe('owner');
  });
});

describe('the request', () => {
  it('lists each value as function:field, once', () => {
    expect(
      aggregatesParam([
        { field: 'amount', fn: 'sum' },
        { field: 'qty', fn: 'avg' },
        { field: 'amount', fn: 'sum' },
      ]),
    ).toBe('sum:amount,avg:qty');
    expect(aggregatesParam([])).toBe('');
  });

  it('keeps at most three distinct values', () => {
    const many = [
      { field: 'a', fn: 'sum' as const },
      { field: 'a', fn: 'sum' as const },
      { field: 'a', fn: 'avg' as const },
      { field: 'b', fn: 'min' as const },
      { field: 'b', fn: 'max' as const },
    ];
    expect(distinctValues(many).map(valueKey)).toEqual(['sum_a', 'avg_a', 'min_b']);
    expect(distinctValues(many)).toHaveLength(MAX_VALUES);
  });

  it('names the columns', () => {
    const fields = valueFields(order);
    expect(valueHeading({ field: 'amount', fn: 'avg' }, fields)).toBe('Average of AMOUNT');
    expect(valueHeading({ field: 'gone', fn: 'max' }, fields)).toBe('Maximum of gone');
    expect(summaryCaption(order, groupFields(order)[2] as GroupField)).toBe('Order by STAGE');
  });
});

describe('the rows', () => {
  const stage = groupFields(order).find((f) => f.name === 'stage') as GroupField;
  const status = { name: 'docstatus', label: 'Status', type: 'int' } as const;

  it('formats numbers, dates and the missing value', () => {
    expect(formatNumber(1234567, 'int')).toBe('1,234,567');
    expect(formatNumber('1234.50', 'decimal')).toBe('1,234.50');
    expect(formatNumber('2026-10-29', 'date')).toBe('29 Oct 2026');
    expect(formatNumber(null, 'int')).toBe('—');
    expect(formatNumber(undefined, 'decimal')).toBe('—');
  });

  it('words a group key', () => {
    expect(keyText('won', stage)).toBe('won');
    expect(keyText(null, stage)).toBe('(none)');
    expect(keyText('', stage)).toBe('(none)');
    expect(keyText(true, { name: 'paid', label: 'Paid', type: 'check' })).toBe('Yes');
    expect(keyText(false, { name: 'paid', label: 'Paid', type: 'check' })).toBe('No');
    expect(keyText('2026-10-29', { name: 'due', label: 'Due', type: 'date' })).toBe('29 Oct 2026');
    expect(keyText(1, status)).toBe('Submitted');
    expect(keyText(7, { name: 'qty', label: 'Qty', type: 'int' })).toBe('7');
  });

  it('gives the status groups a badge', () => {
    expect(keyBadge(1, status, order)).toEqual({ label: 'Submitted', tone: 'success' });
    expect(keyBadge(0, status, order)).toEqual({ label: 'Draft', tone: 'neutral' });
    expect(keyBadge('won', stage, order)).toBeNull();
    const workflow = meta([], {
      workflow: { states: [{ name: 'Pending', docstatus: 0, style: 'warning' }], transitions: [] },
    });
    const state = { name: 'workflow_state', label: 'Status', type: 'select' } as const;
    expect(keyBadge('Pending', state, workflow)).toEqual({ label: 'Pending', tone: 'warning' });
    expect(keyBadge('Gone', state, workflow)).toEqual({ label: 'Gone', tone: 'neutral' });
    expect(keyBadge(null, state, workflow)).toBeNull();
  });

  it('builds the table rows', () => {
    const result: SummaryResult = {
      groups: [
        { key: 'new', count: 1200, values: { sum_amount: '35.50', avg_qty: '2.333333', sum_qty: 7 } },
        { key: null, count: 1, values: { sum_amount: null } },
      ],
      truncated: false,
    };
    const choices = [
      { field: 'amount', fn: 'sum' as const },
      { field: 'qty', fn: 'avg' as const },
    ];
    expect(summaryRows(result, stage, choices, valueFields(order), order)).toEqual([
      { key: 'new', label: 'new', badge: null, count: '1,200', values: ['35.50', '2.333333'] },
      { key: null, label: '(none)', badge: null, count: '1', values: ['—', '—'] },
    ]);
  });

  it('says when the groups were cut', () => {
    expect(truncatedNote({ groups: [], truncated: true })).toBe(`Showing the first ${MAX_GROUPS} groups.`);
    expect(truncatedNote({ groups: [], truncated: false })).toBeNull();
  });
});

describe('the filter a group adds', () => {
  const stage: GroupField = { name: 'stage', label: 'Stage', type: 'select' };

  it('compares a value, or looks for an empty one', () => {
    expect(groupFilter(stage, 'won')).toEqual({ field: 'stage', op: '=', value: 'won' });
    expect(groupFilter({ name: 'qty', label: 'Qty', type: 'int' }, 3)).toEqual({ field: 'qty', op: '=', value: 3 });
    expect(groupFilter({ name: 'paid', label: 'Paid', type: 'check' }, false)).toEqual({
      field: 'paid',
      op: '=',
      value: false,
    });
    expect(groupFilter(stage, null)).toEqual({ field: 'stage', op: 'is null' });
  });

  it('is added to the filters of the address', () => {
    expect(withGroupFilter(null, stage, 'won')).toBe('[{"field":"stage","op":"=","value":"won"}]');
    expect(withGroupFilter('[{"field":"a","op":"is null"}]', stage, 'won')).toBe(
      '[{"field":"a","op":"is null"},{"field":"stage","op":"=","value":"won"}]',
    );
    expect(withGroupFilter('not json', stage, null)).toBe('[{"field":"stage","op":"is null"}]');
    expect(withGroupFilter('{"a":1}', stage, 'x')).toBe('[{"field":"stage","op":"=","value":"x"}]');
  });
});
