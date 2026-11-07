// Pure helpers of the list view: which columns to show, the paging/sort state kept in the
// address, and how a cell is displayed.

import type { FilterField } from './filters';
import { escapeLike } from './links';
import { linkDisplay, linkTitle, titleLinkTarget } from './linkTitles';
import type { LinkTitles } from './linkTitles';
import type { DocTypeMeta, FieldType } from './types';

/** The page size of the list view. */
export const PAGE_SIZE = 20;
/** The most field columns shown besides `name` and `modified_at`. */
export const MAX_FIELD_COLUMNS = 5;
/** The sort when the address names none: the most recently changed first. */
export const DEFAULT_SORT: Sort = { field: 'modified_at', desc: true };

export interface Column {
  /** The key in the listed document and the name used in `sort`. */
  key: string;
  label: string;
  type: FieldType;
  /** A Link column: the DocType it points to (its cells show the target's title when there is one). */
  target?: string;
}

export interface Sort {
  field: string;
  desc: boolean;
}

export const isNumeric = (type: FieldType): boolean => type === 'int' || type === 'decimal';

/** Numbers are right-aligned in the list. */
export const isNumericColumn = (column: Column): boolean => isNumeric(column.type);

/** The column that holds the link to the document (its title, with the name muted beside it). */
export const NAME_KEY = 'name';
/** The status badge column of a submittable DocType (a DocType with a workflow shows its state instead). */
export const STATUS_KEY = 'docstatus';
/** The status badge column of a DocType with a workflow: the state (M8.2). It sorts and filters by state. */
export const WORKFLOW_KEY = 'workflow_state';

/** True for the key of the status badge column, whichever kind it is. */
export const isStatusKey = (key: string): boolean => key === STATUS_KEY || key === WORKFLOW_KEY;

/** Whether the list has a status column: a submittable DocType, or one with a workflow. */
export const hasStatus = (meta: DocTypeMeta): boolean => meta.submittable || (meta.workflow ?? null) !== null;
/** The owner column: initials in a circle and the user name. */
export const OWNER_KEY = 'owner';
/** The column of the last change: a relative time with the exact time on hover. */
export const MODIFIED_KEY = 'modified_at';

/**
 * `name` first (the primary cell), then `list_fields` of the metadata (or, when it names none,
 * the first five fields that are not Table or Text), then the status of a submittable DocType, the
 * owner and `modified_at`. The title field is not repeated: the primary cell already shows it, and its
 * header carries the title field's label alone (the key is shown only in the cells, muted under the title).
 */
export function listColumns(meta: DocTypeMeta): Column[] {
  const known = new Map(meta.fields.map((f) => [f.name, f]));
  // The primary cell shows the title field: its header says so (the column still sorts by name).
  const titleField = meta.title_field === null ? undefined : known.get(meta.title_field);
  const wanted =
    meta.list_fields === null
      ? meta.fields.filter((f) => f.type !== 'table' && f.type !== 'text').map((f) => f.name)
      : meta.list_fields;
  const fields: Column[] = [];
  for (const name of wanted) {
    const f = known.get(name);
    if (!f || f.type === 'table' || f.name === NAME_KEY || f.name === meta.title_field) continue;
    fields.push({ key: f.name, label: f.label, type: f.type, ...(f.type === 'link' && f.target ? { target: f.target } : {}) });
  }
  return [
    titleField === undefined
      ? { key: NAME_KEY, label: 'Name', type: 'data' as const }
      : { key: NAME_KEY, label: titleField.label, type: 'data' as const },
    ...(meta.list_fields === null ? fields.slice(0, MAX_FIELD_COLUMNS) : fields),
    ...(hasStatus(meta)
      ? [{ key: meta.workflow ? WORKFLOW_KEY : STATUS_KEY, label: 'Status', type: 'data' as const }]
      : []),
    { key: OWNER_KEY, label: 'Owner', type: 'data' },
    { key: MODIFIED_KEY, label: 'Modified', type: 'datetime' },
  ];
}

/**
 * The primary text of a listed document: the value of the title field, else the name. When the
 * title field is a Link, the target's title is shown instead of its key where the API gave one.
 */
export function rowTitle(
  row: Record<string, unknown>,
  meta: DocTypeMeta,
  titles: LinkTitles = {},
): { title: string; name: string } {
  const name = String(row[NAME_KEY] ?? '');
  const value = meta.title_field === null ? null : row[meta.title_field];
  const shown = typeof value === 'string' && value !== '' ? linkDisplay(titles, titleLinkTarget(meta), value) : '';
  return { title: shown !== '' ? shown : name, name };
}

/** The rows of the page as text for the toolbar: `Showing 1–20` (the list API has no total count). */
export function showingText(offset: number, count: number): string {
  return count === 0 ? 'No rows' : `Showing ${offset + 1}–${offset + count}`;
}

/**
 * The field the quick search looks in: the title field when `like` is allowed on it (Data, Text
 * and Select fields), else `name`.
 */
export function searchField(meta: DocTypeMeta): { name: string; label: string } {
  const f = meta.title_field === null ? undefined : meta.fields.find((x) => x.name === meta.title_field);
  return f && (f.type === 'data' || f.type === 'text' || f.type === 'select')
    ? { name: f.name, label: f.label }
    : { name: NAME_KEY, label: 'Name' };
}

/**
 * The `filters` of the API for the user's filters (the JSON text of the address) plus the quick
 * search: `field like %text%` (the typed text matches literally). Undefined when there is neither.
 * A `filters` text that is not a JSON array is passed on unchanged: the server refuses it.
 */
export function withSearch(filters: string | null, search: string, field: string): string | undefined {
  const text = search.trim();
  if (text === '') return filters ?? undefined;
  let list: unknown[] = [];
  if (filters) {
    try {
      const parsed: unknown = JSON.parse(filters);
      if (!Array.isArray(parsed)) return filters;
      list = parsed;
    } catch {
      return filters;
    }
  }
  return JSON.stringify([...list, { field, op: 'like', value: `%${escapeLike(text)}%` }]);
}

/** Every field the filter bar offers: the standard columns and every field except Table fields. */
export function filterFields(meta: DocTypeMeta): FilterField[] {
  const standard: FilterField[] = [
    { name: 'name', label: 'Name', type: 'data' },
    { name: 'owner', label: 'Owner', type: 'data' },
    { name: 'modified_at', label: 'Modified', type: 'datetime' },
    ...(meta.submittable
      ? [{ name: 'docstatus', label: 'Document status (0 draft, 1 submitted, 2 cancelled)', type: 'int' as const }]
      : []),
    // A workflow's state is filtered by its name, chosen from the states of the workflow.
    ...(meta.workflow
      ? [
          {
            name: WORKFLOW_KEY,
            label: 'Status',
            type: 'select' as const,
            options: meta.workflow.states.map((state) => state.name),
          },
        ]
      : []),
  ];
  const own: FilterField[] = meta.fields
    .filter((f) => f.type !== 'table')
    .map((f) => ({
      name: f.name,
      label: f.label,
      type: f.type,
      ...(f.type === 'select' && f.options ? { options: f.options } : {}),
    }));
  return [...standard, ...own];
}

export interface ListState {
  offset: number;
  sort: Sort;
  /** The `filters` JSON text as it is in the address, or null. */
  filters: string | null;
  /** The quick search text ("" for none). */
  q: string;
}

/** Reads the paging, sort and filters from the query of the address; bad values are ignored. */
export function parseListState(params: URLSearchParams, sortable: string[]): ListState {
  const offsetText = params.get('offset') ?? '';
  const offset = /^\d{1,9}$/.test(offsetText) ? Number(offsetText) : 0;
  let sort = DEFAULT_SORT;
  const sortText = params.get('sort');
  if (sortText !== null) {
    const [field = '', direction, ...rest] = sortText.split(':');
    if (rest.length === 0 && sortable.includes(field) && (direction === undefined || direction === 'asc' || direction === 'desc')) {
      sort = { field, desc: direction === 'desc' };
    }
  }
  const filters = params.get('filters');
  return { offset, sort, filters: filters === '' ? null : filters, q: params.get('q') ?? '' };
}

/** The query part of the address for a list state (defaults are left out). */
export function listStateParams(state: ListState): URLSearchParams {
  const params = new URLSearchParams();
  if (state.q !== '') params.set('q', state.q);
  if (state.filters) params.set('filters', state.filters);
  if (state.sort.field !== DEFAULT_SORT.field || state.sort.desc !== DEFAULT_SORT.desc) {
    params.set('sort', sortText(state.sort));
  }
  if (state.offset > 0) params.set('offset', String(state.offset));
  return params;
}

/** The `sort` parameter of the API: `field` or `field:desc`. */
export function sortText(sort: Sort): string {
  return sort.desc ? `${sort.field}:desc` : sort.field;
}

/** The sort after a click on a column header: the same column flips, another starts ascending. */
export function nextSort(current: Sort, field: string): Sort {
  return current.field === field ? { field, desc: !current.desc } : { field, desc: false };
}

const MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];

/** `1234567` and `-1234567.50` as `1,234,567` and `-1,234,567.50` (the digits are kept as they are). */
export function groupDigits(text: string): string {
  const match = /^(-?)(\d+)(\.\d+)?$/.exec(text);
  if (!match) return text;
  const [, sign = '', whole = '', fraction = ''] = match;
  return `${sign}${whole.replace(/\B(?=(\d{3})+$)/g, ',')}${fraction}`;
}

/** `2026-10-29` as `29 Oct 2026`; anything else as it is. */
export function formatDate(value: string): string {
  const match = /^(\d{4})-(\d{2})-(\d{2})$/.exec(value);
  const month = match ? MONTHS[Number(match[2]) - 1] : undefined;
  return match && month ? `${Number(match[3])} ${month} ${match[1]}` : value;
}

/** `2026-10-29T15:04:05.123Z` as `29 Oct 2026, 15:04 UTC`; anything else as it is. */
export function formatDateTime(value: string): string {
  const match = /^(\d{4}-\d{2}-\d{2})T(\d{2}:\d{2}):\d{2}(?:\.\d+)?Z$/.exec(value);
  return match ? `${formatDate(match[1] ?? '')}, ${match[2]} UTC` : value;
}

/**
 * A cell as text. React escapes it; nothing here is ever HTML. A Link cell shows the title of its
 * target when `titles` has one for `target`, else the name as it is.
 */
export function formatCell(value: unknown, type: FieldType, target?: string, titles: LinkTitles = {}): string {
  if (value === null || value === undefined) return '';
  if (type === 'link' && typeof value === 'string') return linkTitle(titles, target, value) ?? value;
  if (type === 'check') return value === true ? 'Yes' : 'No';
  if (typeof value === 'string') {
    if (type === 'datetime') return formatDateTime(value);
    if (type === 'date') return formatDate(value);
    if (type === 'decimal') return groupDigits(value);
    return value;
  }
  if (type === 'int' && typeof value === 'number') return groupDigits(String(value));
  return String(value);
}

/** The most columns a list shows besides `name`: the cap of a saved view and of a DocType's `list_fields`. */
export const MAX_COLUMNS = 8;

/**
 * Every column the Columns menu offers, besides `name` (which holds the link and is always
 * shown): the default columns first, in their order, then the other fields that are neither Table
 * nor Text fields and are not the title field. This is the order the columns are shown in.
 */
export function availableColumns(meta: DocTypeMeta): Column[] {
  const defaults = listColumns(meta).filter((c) => c.key !== NAME_KEY);
  const known = new Set(defaults.map((c) => c.key));
  const extra: Column[] = [];
  for (const f of meta.fields) {
    if (known.has(f.name) || f.type === 'table' || f.type === 'text' || f.name === meta.title_field) continue;
    extra.push({ key: f.name, label: f.label, type: f.type, ...(f.type === 'link' && f.target ? { target: f.target } : {}) });
  }
  return [...defaults, ...extra];
}

/**
 * The columns to show for the `columns` of the address (field names, standard columns): `name`,
 * then the named ones that exist, in the order named. Names that no longer exist are left out;
 * `null`, or no name that exists, gives the default columns.
 */
export function selectColumns(meta: DocTypeMeta, keys: string[] | null): Column[] {
  if (keys === null) return listColumns(meta);
  const offered = new Map(availableColumns(meta).map((c) => [c.key, c]));
  // At most the cap, as a saved view holds: what is shown is what a saved view would hold.
  const chosen = keys.flatMap((key) => offered.get(key) ?? []).slice(0, MAX_COLUMNS);
  const first = listColumns(meta)[0];
  return first && chosen.length > 0 ? [first, ...chosen] : listColumns(meta);
}

/**
 * The `columns` of the address after the user checks or unchecks `key`: the shown columns in the
 * order of the menu, or `null` when that is the default set. A column is not added past
 * [`MAX_COLUMNS`] and the last one is not removed (an empty list means the default). The default
 * set may be longer than the cap; any other set is cut to its first `MAX_COLUMNS` columns, which a
 * saved view can hold.
 */
export function toggleColumn(meta: DocTypeMeta, shown: string[], key: string): string[] | null {
  const offered = availableColumns(meta).map((c) => c.key);
  const next = shown.includes(key)
    ? shown.filter((k) => k !== key)
    : shown.length >= MAX_COLUMNS
      ? shown
      : [...shown, key];
  const ordered = [...offered.filter((k) => next.includes(k)), ...next.filter((k) => !offered.includes(k))];
  const defaults = listColumns(meta).filter((c) => c.key !== NAME_KEY).map((c) => c.key);
  if (ordered.length === 0) return shown.length === 0 ? null : shown;
  if (ordered.length === defaults.length && ordered.every((k, i) => k === defaults[i])) return null;
  return ordered.slice(0, MAX_COLUMNS);
}

/** The keys a list can be sorted by: every column the menu offers, shown or not, and `name`. */
export function sortableKeys(meta: DocTypeMeta): string[] {
  return [...new Set([...listColumns(meta).map((c) => c.key), ...availableColumns(meta).map((c) => c.key)])];
}
