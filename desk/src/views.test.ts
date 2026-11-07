import { describe, expect, it } from 'vitest';

import { DEFAULT_SORT } from './listing';
import {
  MAX_LABEL_LENGTH,
  canChangeView,
  canonicalJson,
  definitionOfState,
  differsFromView,
  isBareViewLink,
  labelError,
  parseSortSpec,
  parseViewState,
  splitViews,
  stateOfView,
  viewTitle,
  withViewState,
} from './views';
import type { SavedView } from './views';

const ID = '0190b3a2-5c1e-7f00-8a00-1234567890ab';

const view = (extra: Partial<SavedView> = {}, definition: Partial<SavedView['definition']> = {}): SavedView => ({
  id: ID,
  label: 'Open orders',
  shared: false,
  owner: 'ann',
  definition: { filters: [], sort: [], columns: [], ...definition },
  version: 1,
  modified_at: '2026-10-30T10:00:00Z',
  ...extra,
});

const FILTER = { field: 'status', op: '=', value: 'Draft' };

describe('the address of a list with a view', () => {
  it('reads columns and the view id, and ignores bad values', () => {
    expect(parseViewState(new URLSearchParams(`columns=a,b,a, c ,&view=${ID}`))).toEqual({
      columns: ['a', 'b', 'c'],
      view: ID,
    });
    expect(parseViewState(new URLSearchParams())).toEqual({ columns: null, view: null });
    expect(parseViewState(new URLSearchParams('columns=&view=nope'))).toEqual({ columns: null, view: null });
    expect(parseViewState(new URLSearchParams('columns=,,&view='))).toEqual({ columns: null, view: null });
    expect(parseViewState(new URLSearchParams(`view=${ID.toUpperCase()}`)).view).toBe(ID);
  });

  it('writes them back next to the rest of the query', () => {
    const base = new URLSearchParams('q=x&columns=old&view=old');
    const next = withViewState(base, { columns: ['a', 'b'], view: ID });
    expect(next.get('columns')).toBe('a,b');
    expect(next.get('view')).toBe(ID);
    expect(next.get('q')).toBe('x');
    expect(base.get('columns')).toBe('old');
    const none = withViewState(base, { columns: null, view: null });
    expect([...none.keys()]).toEqual(['q']);
    expect(withViewState(new URLSearchParams(), { columns: [], view: null }).toString()).toBe('');
  });

  it('writes the default sort next to a view with an otherwise empty state, so it is not a link to the view', () => {
    const next = withViewState(new URLSearchParams(), { columns: null, view: ID });
    expect(next.get('sort')).toBe('modified_at:desc');
    expect(isBareViewLink(next)).toBe(false);
    expect(withViewState(new URLSearchParams('q=x'), { columns: null, view: ID }).has('sort')).toBe(false);
    expect(withViewState(new URLSearchParams(), { columns: ['a'], view: ID }).has('sort')).toBe(false);
    expect(withViewState(new URLSearchParams('sort=a'), { columns: null, view: ID }).get('sort')).toBe('a');
  });

  it('knows a link to a view: a view and no state', () => {
    expect(isBareViewLink(new URLSearchParams(`view=${ID}`))).toBe(true);
    expect(isBareViewLink(new URLSearchParams(`view=${ID}&q=x`))).toBe(false);
    expect(isBareViewLink(new URLSearchParams(`view=${ID}&offset=20`))).toBe(false);
    expect(isBareViewLink(new URLSearchParams(''))).toBe(false);
    expect(isBareViewLink(new URLSearchParams('filters=[]'))).toBe(false);
  });

  it('round-trips', () => {
    const state = { columns: ['a', 'b'], view: ID };
    expect(parseViewState(withViewState(new URLSearchParams(), state))).toEqual(state);
  });
});

describe('view and list state', () => {
  it('applies the filters, the first sort key and the columns of a view', () => {
    const state = stateOfView(view({}, { filters: [FILTER], sort: ['title:desc', 'name'], columns: ['a', 'b'] }));
    expect(JSON.parse(state.filters ?? 'null')).toEqual([FILTER]);
    expect(state.sort).toEqual({ field: 'title', desc: true });
    expect(state.columns).toEqual(['a', 'b']);
  });

  it('an empty view is the default list', () => {
    expect(stateOfView(view())).toEqual({ filters: null, sort: DEFAULT_SORT, columns: null });
    // A sort that makes no sense is the default sort.
    expect(stateOfView(view({}, { sort: ['a:sideways'] })).sort).toEqual(DEFAULT_SORT);
  });

  it('writes the filters with their keys in alphabetical order', () => {
    const state = stateOfView(view({}, { filters: [{ value: 'Draft', op: '=', field: 'status' }] }));
    expect(state.filters).toBe('[{"field":"status","op":"=","value":"Draft"}]');
  });

  it('never holds more columns than the server accepts', () => {
    const many = Array.from({ length: 12 }, (_, i) => `f${i}`);
    expect(definitionOfState({ filters: null, sort: DEFAULT_SORT }, many).columns).toEqual(many.slice(0, 8));
  });

  it('saves the list state as a definition', () => {
    expect(definitionOfState({ filters: JSON.stringify([FILTER]), sort: { field: 'a', desc: true } }, ['a', 'b'])).toEqual({
      filters: [FILTER],
      sort: ['a:desc'],
      columns: ['a', 'b'],
    });
    expect(definitionOfState({ filters: null, sort: DEFAULT_SORT }, null)).toEqual({
      filters: [],
      sort: [],
      columns: [],
    });
    expect(definitionOfState({ filters: 'not json', sort: { field: 'a', desc: false } }, null).filters).toEqual([]);
    expect(definitionOfState({ filters: '{"a":1}', sort: DEFAULT_SORT }, null).filters).toEqual([]);
  });

  it('a state taken from a view and saved again is the same view', () => {
    const original = view({}, { filters: [FILTER], sort: ['a:desc'], columns: ['a'] });
    const own = stateOfView(original);
    expect(definitionOfState(own, own.columns)).toEqual(original.definition);
  });
});

describe('differsFromView', () => {
  const base = view({}, { filters: [FILTER], sort: ['a'], columns: ['a', 'b'] });
  const same = { filters: JSON.stringify([FILTER]), sort: { field: 'a', desc: false } };

  it('is false for the state the view stands for', () => {
    expect(differsFromView(same, ['a', 'b'], base)).toBe(false);
    const own = stateOfView(base);
    expect(differsFromView(own, own.columns, base)).toBe(false);
  });

  it('ignores the order of the keys inside a filter (the database reorders them)', () => {
    const state = { ...same, filters: '[{"value":"Draft","op":"=","field":"status"}]' };
    expect(differsFromView(state, ['a', 'b'], base)).toBe(false);
  });

  it('sees another filter, sort or column set', () => {
    expect(differsFromView({ ...same, filters: null }, ['a', 'b'], base)).toBe(true);
    expect(differsFromView({ ...same, filters: JSON.stringify([{ ...FILTER, value: 'Open' }]) }, ['a', 'b'], base)).toBe(true);
    expect(differsFromView({ ...same, sort: { field: 'a', desc: true } }, ['a', 'b'], base)).toBe(true);
    expect(differsFromView({ ...same, sort: { field: 'b', desc: false } }, ['a', 'b'], base)).toBe(true);
    expect(differsFromView(same, ['a'], base)).toBe(true);
    expect(differsFromView(same, ['b', 'a'], base)).toBe(true);
    expect(differsFromView(same, null, base)).toBe(true);
  });

  it('treats no columns and an empty column list alike, and no sort as the default sort', () => {
    const plain = view();
    expect(differsFromView({ filters: null, sort: DEFAULT_SORT }, null, plain)).toBe(false);
    expect(differsFromView({ filters: '[]', sort: DEFAULT_SORT }, null, plain)).toBe(false);
    expect(differsFromView({ filters: null, sort: { field: 'a', desc: false } }, null, plain)).toBe(true);
  });

  it('looks only at the first key of the sort of the view', () => {
    const two = view({}, { sort: ['a:desc', 'b'] });
    expect(differsFromView({ filters: null, sort: { field: 'a', desc: true } }, null, two)).toBe(false);
  });
});

describe('who may change a view', () => {
  it('the owner changes a private view, an Administrator a shared one', () => {
    const mine = view();
    expect(canChangeView(mine, 'ann', false)).toBe(true);
    expect(canChangeView(mine, 'bob', false)).toBe(false);
    expect(canChangeView(mine, 'root', true)).toBe(false);
    const shared = view({ shared: true, owner: null });
    expect(canChangeView(shared, 'ann', false)).toBe(false);
    expect(canChangeView(shared, 'root', true)).toBe(true);
  });

  it('splits the menu into shared and own views, keeping the order', () => {
    const a = view({ id: 'a', shared: true, owner: null, label: 'A' });
    const b = view({ id: 'b', label: 'B' });
    const c = view({ id: 'c', shared: true, owner: null, label: 'C' });
    expect(splitViews([a, c, b])).toEqual({ shared: [a, c], mine: [b] });
    expect(splitViews([])).toEqual({ shared: [], mine: [] });
  });
});

describe('labels and titles', () => {
  it('accepts a name and says why it cannot', () => {
    expect(labelError('Open orders')).toBeNull();
    expect(labelError('  padded  ')).toBeNull(); // the dialog trims it
    expect(labelError('')).toMatch(/enter a name/i);
    expect(labelError('   ')).toMatch(/enter a name/i);
    expect(labelError('x'.repeat(MAX_LABEL_LENGTH))).toBeNull();
    expect(labelError('x'.repeat(MAX_LABEL_LENGTH + 1))).toMatch(/at most 64/);
    expect(labelError('é'.repeat(MAX_LABEL_LENGTH))).toBeNull();
    expect(labelError('a\nb')).toMatch(/punctuation/);
  });

  it('shows the view next to the title', () => {
    expect(viewTitle(view())).toBe('· Open orders');
    expect(viewTitle(undefined)).toBe('');
  });

  it('reads a sort spec', () => {
    expect(parseSortSpec('a')).toEqual({ field: 'a', desc: false });
    expect(parseSortSpec('a:asc')).toEqual({ field: 'a', desc: false });
    expect(parseSortSpec('a:desc')).toEqual({ field: 'a', desc: true });
    for (const bad of ['', ':desc', 'a:up', 'a:desc:x']) expect(parseSortSpec(bad), bad).toBeNull();
  });

  it('writes equal values as equal text whatever the order of the keys', () => {
    expect(canonicalJson({ b: 1, a: [{ d: null, c: 'x' }] })).toBe('{"a":[{"c":"x","d":null}],"b":1}');
    expect(canonicalJson(undefined)).toBe('null');
  });
});
