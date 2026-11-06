import { describe, expect, it } from 'vitest';

import {
  buildRequest,
  firstInvalid,
  hasRunQuery,
  initialValues,
  mapParamDetails,
  paramField,
  valuesQuery,
} from './reports';
import type { ReportMeta, ReportParam } from './reports';

const param = (name: string, kind: ReportParam['kind'], extra: Partial<ReportParam> = {}): ReportParam => ({
  name,
  label: name.toUpperCase(),
  kind,
  required: false,
  default: null,
  ...extra,
});

const report = (params: ReportParam[]): ReportMeta => ({
  name: 'sales.by_customer',
  app: 'sales',
  label: 'Sales by customer',
  params,
  columns: [],
});

const full = report([
  param('from', 'date', { required: true }),
  param('to', 'date', { required: true }),
  param('customer', 'link', { doctype: 'customer' }),
  param('kind', 'select', { options: ['retail', 'online'], default: 'retail' }),
  param('min', 'int'),
  param('above', 'decimal'),
  param('note', 'data'),
  param('open', 'check', { default: true }),
]);

describe('paramField', () => {
  it('is the field of the same kind, with its options and target', () => {
    expect(paramField(param('from', 'date', { required: true }))).toMatchObject({
      name: 'from',
      label: 'FROM',
      type: 'date',
      required: true,
      read_only: false,
    });
    expect(paramField(param('kind', 'select', { options: ['a'] })).options).toEqual(['a']);
    expect(paramField(param('c', 'link', { doctype: 'customer' })).target).toBe('customer');
    expect(paramField(param('n', 'data')).max_length).toBe(140);
    expect(paramField(param('d', 'date')).options).toBeUndefined();
  });
});

describe('initialValues', () => {
  it('starts from the defaults of the report', () => {
    expect(initialValues(full, new URLSearchParams())).toEqual({
      from: '',
      to: '',
      customer: '',
      kind: 'retail',
      min: '',
      above: '',
      note: '',
      open: true,
    });
  });

  it('takes what the address holds, a Check as true or false', () => {
    const query = new URLSearchParams('from=2026-10-01&customer=A%20B&min=5&open=false&kind=online');
    expect(initialValues(full, query)).toMatchObject({
      from: '2026-10-01',
      to: '',
      customer: 'A B',
      kind: 'online',
      min: '5',
      open: false,
    });
  });

  it('brings the default back for a key the address omits', () => {
    // A cleared value is not in the address either: a reload shows what the report would use.
    expect(initialValues(full, new URLSearchParams('from=2026-10-01')).kind).toBe('retail');
  });
});

describe('valuesQuery and hasRunQuery', () => {
  it('writes the parameters that have a value and every Check', () => {
    const query = valuesQuery(full, {
      from: '2026-10-01',
      to: '2026-10-31',
      customer: '',
      kind: 'retail',
      min: '',
      above: '1.50',
      note: 'a&b',
      open: false,
    });
    expect(query.toString()).toBe('from=2026-10-01&to=2026-10-31&kind=retail&above=1.50&note=a%26b&open=false');
    expect(query.get('note')).toBe('a&b');
  });

  it('round-trips through initialValues', () => {
    const values = {
      from: '2026-10-01',
      to: '2026-10-31',
      customer: 'C 1',
      kind: 'online',
      min: '3',
      above: '',
      note: '',
      open: true,
    };
    expect(initialValues(full, valuesQuery(full, values))).toEqual(values);
  });

  it('marks a report without parameters, so that a reload runs it', () => {
    const none = report([]);
    expect(valuesQuery(none, {}).toString()).toBe('run=1');
    expect(hasRunQuery(valuesQuery(none, {}))).toBe(true);
  });

  it('says there is nothing to run for an address without a query', () => {
    expect(hasRunQuery(new URLSearchParams())).toBe(false);
    expect(hasRunQuery(new URLSearchParams('from=2026-10-01'))).toBe(true);
  });
});

describe('buildRequest', () => {
  it('sends every parameter in its JSON kind, an empty control as null', () => {
    const built = buildRequest(full, {
      from: '2026-10-01',
      to: '2026-10-31',
      customer: '',
      kind: '',
      min: '5',
      above: '1.50',
      note: 'x',
      open: false,
    });
    expect(built).toEqual({
      ok: true,
      params: {
        from: '2026-10-01',
        to: '2026-10-31',
        customer: null,
        kind: null,
        min: 5,
        above: '1.50',
        note: 'x',
        open: false,
      },
    });
  });

  it('refuses what cannot be sent, on the field', () => {
    const built = buildRequest(full, { min: '1.5', above: 'abc' });
    expect(built).toEqual({
      ok: false,
      errors: { min: 'Enter a whole number.', above: 'Enter a number such as 1234.50.' },
    });
  });
});

describe('mapParamDetails and firstInvalid', () => {
  it('maps the details of the server to the parameters', () => {
    const mapped = mapParamDetails(full, ['field `from` is required', 'unknown field `x`']);
    expect(mapped.errors).toEqual({ from: 'This field is required.' });
    expect(mapped.unmapped).toEqual(['unknown field `x`']);
    expect(firstInvalid(full, { to: 'x', from: 'y' })).toBe('from');
    expect(firstInvalid(full, {})).toBeNull();
  });
});
