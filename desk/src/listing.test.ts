import { describe, expect, it } from 'vitest';

import {
  DEFAULT_SORT,
  MAX_COLUMNS,
  availableColumns,
  filterFields,
  formatCell,
  formatDate,
  formatDateTime,
  groupDigits,
  hasStatus,
  isNumericColumn,
  isStatusKey,
  listColumns,
  listStateParams,
  nextSort,
  parseListState,
  rowTitle,
  searchField,
  selectColumns,
  showingText,
  sortText,
  sortableKeys,
  toggleColumn,
  withSearch,
} from './listing';
import type { DocTypeMeta, FieldMeta, FieldType, WorkflowMeta } from './types';

const field = (name: string, type: FieldType, extra: Partial<FieldMeta> = {}): FieldMeta => ({
  name,
  label: name.toUpperCase(),
  type,
  required: false,
  unique: false,
  allow_on_submit: false,
  read_only: false,
  ...extra,
});

const meta = (fields: FieldMeta[], submittable = false, extra: Partial<DocTypeMeta> = {}): DocTypeMeta => ({
  name: 'thing',
  label: 'Thing',
  app: null,
  icon: null,
  naming: 'random',
  submittable,
  title_field: null,
  list_fields: null,
  fields,
  rights: {
    read: 'all', write: 'all', create: 'all', delete: 'all', submit: 'none', cancel: 'none', amend: 'none', export: 'none', import: 'none',
  },
  ...extra,
});

describe('listColumns', () => {
  it('is name, at most five fields without Table and Text, then the owner and modified_at', () => {
    const columns = listColumns(
      meta([
        field('a', 'data'),
        field('notes', 'text'),
        field('lines', 'table'),
        field('b', 'int'),
        field('c', 'link'),
        field('d', 'check'),
        field('e', 'date'),
        field('f', 'select'),
        field('g', 'decimal'),
      ]),
    );
    expect(columns.map((c) => c.key)).toEqual(['name', 'a', 'b', 'c', 'd', 'e', 'owner', 'modified_at']);
    expect(columns[1]?.label).toBe('A');
    expect(columns.at(-1)).toMatchObject({ key: 'modified_at', type: 'datetime' });
  });

  it('works for a DocType with no fields', () => {
    expect(listColumns(meta([])).map((c) => c.key)).toEqual(['name', 'owner', 'modified_at']);
  });

  it('uses list_fields in their order, skips unknown, Table and the title field, and adds the status', () => {
    const fields = [field('a', 'data'), field('b', 'int'), field('notes', 'text'), field('lines', 'table')];
    const columns = listColumns(
      meta(fields, true, { title_field: 'a', list_fields: ['notes', 'a', 'gone', 'lines', 'b'] }),
    );
    expect(columns.map((c) => c.key)).toEqual(['name', 'notes', 'b', 'docstatus', 'owner', 'modified_at']);
    expect(columns.find((c) => c.key === 'docstatus')?.label).toBe('Status');
  });

  it('names the primary column after the title field alone', () => {
    const columns = listColumns(meta([field('a', 'data')], false, { title_field: 'a' }));
    expect(columns[0]).toEqual({ key: 'name', label: 'A', type: 'data' });
    const plain = listColumns(meta([field('a', 'data')]));
    expect(plain[0]).toEqual({ key: 'name', label: 'Name', type: 'data' });
  });

  it('falls back to the default columns without the title field, and has no status when not submittable', () => {
    const columns = listColumns(meta([field('a', 'data'), field('b', 'int')], false, { title_field: 'a' }));
    expect(columns.map((c) => c.key)).toEqual(['name', 'b', 'owner', 'modified_at']);
  });

  it('right-aligns numbers only', () => {
    const columns = listColumns(meta([field('a', 'data'), field('b', 'int'), field('c', 'decimal')]));
    expect(columns.filter(isNumericColumn).map((c) => c.key)).toEqual(['b', 'c']);
  });
});

describe('rowTitle', () => {
  const m = meta([field('a', 'data')], false, { title_field: 'a' });
  it('is the title field value, else the name', () => {
    expect(rowTitle({ name: 'N1', a: 'Ada' }, m)).toEqual({ title: 'Ada', name: 'N1' });
    expect(rowTitle({ name: 'N1', a: '' }, m)).toEqual({ title: 'N1', name: 'N1' });
    expect(rowTitle({ name: 'N1', a: null }, m)).toEqual({ title: 'N1', name: 'N1' });
    expect(rowTitle({ name: 'N1', a: 'Ada' }, meta([]))).toEqual({ title: 'N1', name: 'N1' });
  });
});

describe('link titles in the list', () => {
  const fields = [field('customer', 'link', { target: 'customer' }), field('a', 'data')];
  const titles = { customer: { 'ada@x.y': 'Ada Lovelace' } };

  it('shows the title of a Link cell, else the name', () => {
    expect(formatCell('ada@x.y', 'link', 'customer', titles)).toBe('Ada Lovelace');
    expect(formatCell('bob@x.y', 'link', 'customer', titles)).toBe('bob@x.y');
    expect(formatCell('ada@x.y', 'link', 'customer')).toBe('ada@x.y');
    expect(formatCell('ada@x.y', 'link', 'other', titles)).toBe('ada@x.y');
    expect(formatCell('ada@x.y', 'data', 'customer', titles)).toBe('ada@x.y'); // only Link cells
  });

  it('gives a Link column its target', () => {
    const columns = listColumns(meta(fields));
    expect(columns.find((c) => c.key === 'customer')).toMatchObject({ type: 'link', target: 'customer' });
    expect(columns.find((c) => c.key === 'a')).not.toHaveProperty('target');
  });

  it('titles a row by the Link title when the title field is a Link', () => {
    const m = meta(fields, false, { title_field: 'customer' });
    expect(rowTitle({ name: 'SO-1', customer: 'ada@x.y' }, m, titles)).toEqual({ title: 'Ada Lovelace', name: 'SO-1' });
    expect(rowTitle({ name: 'SO-1', customer: 'bob@x.y' }, m, titles)).toEqual({ title: 'bob@x.y', name: 'SO-1' });
    expect(rowTitle({ name: 'SO-1', customer: 'ada@x.y' }, m)).toEqual({ title: 'ada@x.y', name: 'SO-1' });
    // A title field that is not a Link is never looked up.
    const plain = meta(fields, false, { title_field: 'a' });
    expect(rowTitle({ name: 'SO-1', a: 'ada@x.y' }, plain, titles).title).toBe('ada@x.y');
  });
});

describe('showingText', () => {
  it('names the range of the page', () => {
    expect(showingText(0, 20)).toBe('Showing 1\u201320');
    expect(showingText(40, 3)).toBe('Showing 41\u201343');
    expect(showingText(0, 0)).toBe('No rows');
  });
});

describe('quick search', () => {
  it('looks in the title field when like is allowed on it, else in name', () => {
    const fields = [field('a', 'data'), field('c', 'link'), field('s', 'select')];
    expect(searchField(meta(fields, false, { title_field: 'a' }))).toEqual({ name: 'a', label: 'A' });
    expect(searchField(meta(fields, false, { title_field: 's' })).name).toBe('s');
    expect(searchField(meta(fields, false, { title_field: 'c' })).name).toBe('name'); // like is refused on a Link
    expect(searchField(meta(fields, false, { title_field: 'gone' })).name).toBe('name');
    expect(searchField(meta(fields))).toEqual({ name: 'name', label: 'Name' });
  });

  it('builds a like filter on top of the user filters', () => {
    expect(withSearch(null, '', 'name')).toBeUndefined();
    expect(withSearch('[{"field":"a","op":"is null"}]', '  ', 'name')).toBe('[{"field":"a","op":"is null"}]');
    expect(JSON.parse(withSearch(null, ' ada ', 'name') ?? '')).toEqual([
      { field: 'name', op: 'like', value: '%ada%' },
    ]);
    expect(JSON.parse(withSearch('[{"field":"a","op":"is null"}]', 'x', 'a') ?? '')).toEqual([
      { field: 'a', op: 'is null' },
      { field: 'a', op: 'like', value: '%x%' },
    ]);
  });

  it('matches the typed text literally and leaves a bad filters text to the server', () => {
    expect(JSON.parse(withSearch(null, '50%_\\', 'name') ?? '')).toEqual([
      { field: 'name', op: 'like', value: '%50\\%\\_\\\\%' },
    ]);
    expect(withSearch('not json', 'x', 'name')).toBe('not json');
    expect(withSearch('{"a":1}', 'x', 'name')).toBe('{"a":1}');
  });
});

describe('filterFields', () => {
  it('has the standard columns and every field but Table fields', () => {
    const names = (m: DocTypeMeta) => filterFields(m).map((f) => f.name);
    const base = meta([field('a', 'data'), field('lines', 'table'), field('t', 'select', { options: ['x'] })]);
    expect(names(base)).toEqual(['name', 'owner', 'modified_at', 'a', 't']);
    expect(filterFields(base).at(-1)?.options).toEqual(['x']);
    expect(names({ ...base, submittable: true })).toContain('docstatus');
    expect(names(base)).not.toContain('docstatus');
  });
});

describe('parseListState and listStateParams', () => {
  const sortable = ['name', 'a', 'modified_at'];

  it('has defaults', () => {
    expect(parseListState(new URLSearchParams(), sortable)).toEqual({
      offset: 0,
      sort: DEFAULT_SORT,
      filters: null,
      q: '',
    });
    expect(listStateParams({ offset: 0, sort: DEFAULT_SORT, filters: null, q: '' }).toString()).toBe('');
  });

  it('round-trips offset, sort, filters and the search text', () => {
    const state = { offset: 40, sort: { field: 'a', desc: true }, filters: '[{"field":"a","op":"is null"}]', q: 'ada & co' };
    const params = listStateParams(state);
    expect(params.get('sort')).toBe('a:desc');
    expect(params.get('offset')).toBe('40');
    expect(parseListState(new URLSearchParams(params.toString()), sortable)).toEqual(state);
  });

  it('ignores bad values', () => {
    for (const query of ['offset=-1', 'offset=abc', 'offset=1.5', 'offset=', 'sort=nope', 'sort=a:up', 'sort=a:desc:x', 'sort=']) {
      const state = parseListState(new URLSearchParams(query), sortable);
      expect(state.offset, query).toBe(0);
      expect(state.sort, query).toEqual(DEFAULT_SORT);
    }
    expect(parseListState(new URLSearchParams('sort=a'), sortable).sort).toEqual({ field: 'a', desc: false });
    expect(parseListState(new URLSearchParams('filters='), sortable).filters).toBeNull();
  });
});

describe('sorting', () => {
  it('flips the same column and starts another ascending', () => {
    expect(nextSort({ field: 'a', desc: false }, 'a')).toEqual({ field: 'a', desc: true });
    expect(nextSort({ field: 'a', desc: true }, 'a')).toEqual({ field: 'a', desc: false });
    expect(nextSort({ field: 'a', desc: true }, 'b')).toEqual({ field: 'b', desc: false });
    expect(sortText({ field: 'a', desc: true })).toBe('a:desc');
    expect(sortText({ field: 'a', desc: false })).toBe('a');
  });
});

describe('formatCell', () => {
  it('formats by type and never produces markup', () => {
    expect(formatCell(null, 'data')).toBe('');
    expect(formatCell(undefined, 'int')).toBe('');
    expect(formatCell(true, 'check')).toBe('Yes');
    expect(formatCell(false, 'check')).toBe('No');
    expect(formatCell(12, 'int')).toBe('12');
    expect(formatCell(1234567, 'int')).toBe('1,234,567');
    expect(formatCell('12.50', 'decimal')).toBe('12.50');
    expect(formatCell('1234.50', 'decimal')).toBe('1,234.50');
    expect(formatCell('2026-10-29T15:04:05.123456Z', 'datetime')).toBe('29 Oct 2026, 15:04 UTC');
    expect(formatCell('2026-10-29T15:04:05Z', 'datetime')).toBe('29 Oct 2026, 15:04 UTC');
    expect(formatCell('2026-10-29', 'date')).toBe('29 Oct 2026');
    expect(formatCell('not a date', 'datetime')).toBe('not a date');
    expect(formatCell('<img src=x onerror=alert(1)>', 'data')).toBe('<img src=x onerror=alert(1)>');
  });
});

describe('number and date helpers', () => {
  it('groups digits without changing them', () => {
    expect(groupDigits('0')).toBe('0');
    expect(groupDigits('999')).toBe('999');
    expect(groupDigits('1000')).toBe('1,000');
    expect(groupDigits('-1234567.5000')).toBe('-1,234,567.5000');
    expect(groupDigits('1e5')).toBe('1e5');
    expect(groupDigits('abc')).toBe('abc');
  });

  it('formats dates and leaves other text alone', () => {
    expect(formatDate('2026-01-05')).toBe('5 Jan 2026');
    expect(formatDate('2026-13-05')).toBe('2026-13-05');
    expect(formatDate('yesterday')).toBe('yesterday');
    expect(formatDateTime('2026-12-31T23:59:59Z')).toBe('31 Dec 2026, 23:59 UTC');
    expect(formatDateTime('2026-12-31 23:59')).toBe('2026-12-31 23:59');
  });
});

describe('a DocType with a workflow', () => {
  const workflow: WorkflowMeta = {
    states: [
      { name: 'Draft', docstatus: 0, style: 'neutral' },
      { name: 'Approved', docstatus: 1, style: 'success' },
    ],
    transitions: [],
  };

  it('has a Status column that sorts by the state, also when it is not submittable', () => {
    const own = listColumns(meta([field('a', 'data')], false, { workflow }));
    expect(own.find((c) => c.label === 'Status')?.key).toBe('workflow_state');
    expect(listColumns(meta([field('a', 'data')], true, { workflow })).filter((c) => c.label === 'Status')).toHaveLength(1);
    // Without a workflow the column is unchanged: the document status, only when submittable.
    expect(listColumns(meta([field('a', 'data')], true)).find((c) => c.label === 'Status')?.key).toBe('docstatus');
    expect(listColumns(meta([field('a', 'data')])).find((c) => c.label === 'Status')).toBeUndefined();
  });

  it('knows which keys are the status column', () => {
    expect(isStatusKey('workflow_state')).toBe(true);
    expect(isStatusKey('docstatus')).toBe(true);
    expect(isStatusKey('name')).toBe(false);
    expect(hasStatus(meta([], false, { workflow }))).toBe(true);
    expect(hasStatus(meta([], false, { workflow: null }))).toBe(false);
  });

  it('offers the state as a Select with the names of the states', () => {
    const fields = filterFields(meta([field('a', 'data')], true, { workflow }));
    expect(fields.find((f) => f.name === 'workflow_state')).toEqual({
      name: 'workflow_state',
      label: 'Status',
      type: 'select',
      options: ['Draft', 'Approved'],
    });
    expect(filterFields(meta([field('a', 'data')], true)).some((f) => f.name === 'workflow_state')).toBe(false);
  });
});

describe('the columns menu', () => {
  const sample = meta([
    field('a', 'data'),
    field('b', 'int'),
    field('c', 'link', { target: 'x' }),
    field('d', 'date'),
    field('e', 'check'),
    field('f', 'select'),
    field('g', 'decimal'),
    field('notes', 'text'),
    field('lines', 'table'),
  ]);
  const keys = (columns: { key: string }[]) => columns.map((c) => c.key);

  it('offers the default columns first, then the other fields without Table and Text', () => {
    expect(keys(availableColumns(sample))).toEqual(['a', 'b', 'c', 'd', 'e', 'owner', 'modified_at', 'f', 'g']);
    const titled = meta([field('a', 'data'), field('b', 'int')], false, { title_field: 'a', list_fields: ['b'] });
    expect(keys(availableColumns(titled))).toEqual(['b', 'owner', 'modified_at']);
    const submittable = meta([field('a', 'data')], true);
    expect(keys(availableColumns(submittable))).toEqual(['a', 'docstatus', 'owner', 'modified_at']);
  });

  it('shows the default columns for no choice, and the chosen ones in the order named', () => {
    expect(selectColumns(sample, null)).toEqual(listColumns(sample));
    expect(keys(selectColumns(sample, ['g', 'a']))).toEqual(['name', 'g', 'a']);
    expect(selectColumns(sample, ['g'])[1]).toMatchObject({ key: 'g', label: 'G', type: 'decimal' });
  });

  it('leaves out columns that no longer exist, and falls back when none does', () => {
    expect(keys(selectColumns(sample, ['gone', 'b', 'lines', 'notes']))).toEqual(['name', 'b']);
    expect(selectColumns(sample, ['gone', 'lines'])).toEqual(listColumns(sample));
  });

  it('checks and unchecks a column, in the order of the menu', () => {
    const shown = ['a', 'b'];
    expect(toggleColumn(sample, shown, 'g')).toEqual(['a', 'b', 'g']);
    expect(toggleColumn(sample, ['b', 'g'], 'a')).toEqual(['a', 'b', 'g']);
    expect(toggleColumn(sample, shown, 'b')).toEqual(['a']);
  });

  it('gives null when the result is the default set', () => {
    const defaults = listColumns(sample).filter((c) => c.key !== 'name').map((c) => c.key);
    expect(toggleColumn(sample, [...defaults, 'g'], 'g')).toBeNull();
    expect(toggleColumn(sample, defaults.slice(1), defaults[0] ?? '')).toBeNull();
  });

  it('adds no column past the cap and removes no last column', () => {
    const wide = meta(Array.from({ length: 12 }, (_, i) => field(`f${i}`, 'data')), false, { list_fields: ['f0'] });
    const all = availableColumns(wide).map((c) => c.key);
    const full = all.slice(0, MAX_COLUMNS);
    expect(toggleColumn(wide, full, all[MAX_COLUMNS] ?? '')).toEqual(full);
    expect(toggleColumn(wide, ['f0'], 'f0')).toEqual(['f0']);
  });

  it('keeps at most the cap when the default columns are more than the cap', () => {
    const fields = Array.from({ length: 11 }, (_, i) => field(`f${i}`, 'data'));
    const wide = meta(fields, false, { list_fields: fields.map((f) => f.name) });
    const defaults = listColumns(wide).filter((c) => c.key !== 'name').map((c) => c.key);
    expect(defaults.length).toBeGreaterThan(MAX_COLUMNS);
    // Any set that is not the default one holds at most MAX_COLUMNS columns.
    const next = toggleColumn(wide, defaults, 'f10');
    expect(next).toEqual(defaults.slice(0, MAX_COLUMNS));
    expect(toggleColumn(wide, defaults, 'f3')?.length).toBe(MAX_COLUMNS);
    expect(selectColumns(wide, defaults).length).toBe(MAX_COLUMNS + 1); // name + the cap
  });

  it('can sort by every column of the menu, shown or not', () => {
    expect(sortableKeys(sample)).toEqual(['name', 'a', 'b', 'c', 'd', 'e', 'owner', 'modified_at', 'f', 'g']);
  });
});
