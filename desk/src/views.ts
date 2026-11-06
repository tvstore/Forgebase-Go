// Saved list views (M8.3): the conversion between a view and the state of the list (the address),
// and the checks the Views menu needs. Pure functions; `pages/List.tsx` does the rest.
//
// A view holds `filters`, `sort` and `columns` in the forms of the list API. The address of a
// list holds the same three (`filters`, `sort`, `columns`) plus `view=<id>`, which says which
// view the state started from. The address, not the view, is the state: a reload keeps it.

import { DEFAULT_SORT, MAX_COLUMNS, sortText } from './listing';
import type { ListState, Sort } from './listing';

/** The longest label of a view, in characters (the server's limit). */
export const MAX_LABEL_LENGTH = 64;

export interface ViewDefinition {
  filters: unknown[];
  sort: string[];
  columns: string[];
}

export interface SavedView {
  id: string;
  label: string;
  shared: boolean;
  /** The user name of the owner; null for a shared view. */
  owner: string | null;
  definition: ViewDefinition;
  version: number;
  modified_at: string;
}

/** The parts of the address that belong to views and columns (the rest is `ListState`). */
export interface ViewState {
  /** The shown columns besides `name`, or null for the default ones. */
  columns: string[] | null;
  /** The id of the view the state started from, or null. */
  view: string | null;
}

const VIEW_ID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

/** Reads `columns` and `view` from the query of the address; bad values are ignored. */
export function parseViewState(params: URLSearchParams): ViewState {
  const names = (params.get('columns') ?? '')
    .split(',')
    .map((name) => name.trim())
    .filter((name) => name !== '');
  const columns = [...new Set(names)];
  const view = params.get('view') ?? '';
  return { columns: columns.length > 0 ? columns : null, view: VIEW_ID.test(view) ? view.toLowerCase() : null };
}

/** The parts of the query that are the state of the list (the rest is `view`). */
const STATE_KEYS = ['filters', 'sort', 'columns', 'q', 'offset'];

/** True for an address with a view and nothing else: a link to the view, to be resolved. */
export const isBareViewLink = (params: URLSearchParams): boolean =>
  params.has('view') && !STATE_KEYS.some((key) => params.has(key));

/**
 * `params` with `columns` and `view` set from `state` (left out when null). A view next to an
 * empty state (the user cleared what the view set) would read as a link to the view, so the default
 * sort is written out: the address then says "the default list, started from this view".
 */
export function withViewState(params: URLSearchParams, state: ViewState): URLSearchParams {
  const next = new URLSearchParams(params);
  next.delete('columns');
  next.delete('view');
  if (state.columns !== null && state.columns.length > 0) next.set('columns', state.columns.join(','));
  if (state.view !== null) {
    next.set('view', state.view);
    if (!STATE_KEYS.some((key) => next.has(key))) next.set('sort', sortText(DEFAULT_SORT));
  }
  return next;
}

/** `field`, `field:asc` or `field:desc` as a sort; anything else is null. */
export function parseSortSpec(spec: string): Sort | null {
  const [field = '', direction, ...rest] = spec.split(':');
  if (field === '' || rest.length > 0 || !(direction === undefined || direction === 'asc' || direction === 'desc')) {
    return null;
  }
  return { field, desc: direction === 'desc' };
}

/** JSON with the keys of every object in alphabetical order: equal values give equal text. */
export function canonicalJson(value: unknown): string {
  if (Array.isArray(value)) return `[${value.map(canonicalJson).join(',')}]`;
  if (value !== null && typeof value === 'object') {
    const entries = Object.entries(value as Record<string, unknown>)
      .sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0))
      .map(([key, item]) => `${JSON.stringify(key)}:${canonicalJson(item)}`);
    return `{${entries.join(',')}}`;
  }
  return JSON.stringify(value) ?? 'null';
}

/** The `filters` text of the address as a list; text that is not a JSON list gives an empty one. */
function filterList(filters: string | null): unknown[] {
  if (!filters) return [];
  try {
    const parsed: unknown = JSON.parse(filters);
    return Array.isArray(parsed) ? parsed : [];
  } catch {
    return [];
  }
}

/**
 * The list state a view stands for: its filters, the first key of its sort (the list sorts by
 * one column; no sort is the default sort) and its columns (none is the default columns).
 */
export function stateOfView(view: SavedView): { filters: string | null; sort: Sort; columns: string[] | null } {
  const { filters, sort, columns } = view.definition;
  const first = sort.length > 0 ? parseSortSpec(sort[0] ?? '') : null;
  return {
    filters: filters.length > 0 ? canonicalJson(filters) : null,
    sort: first ?? DEFAULT_SORT,
    columns: columns.length > 0 ? columns : null,
  };
}

/** The definition to save for the list state: the filters as a list, the sort and the columns. */
export function definitionOfState(state: Pick<ListState, 'filters' | 'sort'>, columns: string[] | null): ViewDefinition {
  const isDefault = state.sort.field === DEFAULT_SORT.field && state.sort.desc === DEFAULT_SORT.desc;
  return {
    filters: filterList(state.filters),
    sort: isDefault ? [] : [sortText(state.sort)],
    columns: (columns ?? []).slice(0, MAX_COLUMNS), // the server refuses more
  };
}

/**
 * Whether the list state differs from the view it started from: another filter, sort or column
 * set. The search text and the page are not part of a view. The order of the keys inside a filter
 * does not matter (the database may reorder them).
 */
export function differsFromView(
  state: Pick<ListState, 'filters' | 'sort'>,
  columns: string[] | null,
  view: SavedView,
): boolean {
  const own = stateOfView(view);
  // No filters, `null` and `[]` are the same.
  const text = (filters: string | null): string => {
    const list = filterList(filters);
    return list.length === 0 ? '' : canonicalJson(list);
  };
  return (
    text(state.filters) !== text(own.filters) ||
    state.sort.field !== own.sort.field ||
    state.sort.desc !== own.sort.desc ||
    (columns ?? []).join(',') !== (own.columns ?? []).join(',')
  );
}

/** Who may change or delete a view: its owner, and for a shared view an Administrator. */
export function canChangeView(view: SavedView, username: string, isAdmin: boolean): boolean {
  return view.shared ? isAdmin : view.owner === username;
}

/** The views of the menu in its two groups (the server already sorts each by label). */
export function splitViews(views: SavedView[]): { shared: SavedView[]; mine: SavedView[] } {
  return { shared: views.filter((v) => v.shared), mine: views.filter((v) => !v.shared) };
}

/** Why a label cannot be saved, worded for the user, or null. The server checks it again. */
export function labelError(label: string): string | null {
  const text = label.trim();
  if (text === '') return 'Enter a name for the view.';
  if ([...text].length > MAX_LABEL_LENGTH) return `Use at most ${MAX_LABEL_LENGTH} characters.`;
  // eslint-disable-next-line no-control-regex
  if (/[\u0000-\u001f\u007f-\u009f]/.test(text)) return 'Use letters, digits and punctuation only.';
  return null;
}

/** The text next to the page title: the DocType's name stays the heading, this is the view. */
export function viewTitle(view: SavedView | undefined): string {
  return view ? `· ${view.label}` : '';
}
