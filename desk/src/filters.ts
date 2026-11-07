// The filter bar's model: which operators fit which field type, and the `filters` JSON of
// `GET /api/v1/resource/{doctype}` built from the user's rows. The server validates everything
// again (and the permission rules apply there); this only keeps the UI from offering nonsense.

import type { FieldType } from './types';

export type Op = '=' | '!=' | '<' | '<=' | '>' | '>=' | 'in' | 'like' | 'is null' | 'is not null';

/** What the filter bar knows about a filterable field. */
export interface FilterField {
  name: string;
  label: string;
  type: FieldType;
  /** Select fields: the allowed values. */
  options?: string[];
}

/** One row of the filter bar; `value` is the text the user typed or chose. */
export interface FilterRow {
  field: string;
  op: Op;
  value: string;
}

export const NO_VALUE_OPS: readonly Op[] = ['is null', 'is not null'];

const EQUALITY: Op[] = ['=', '!=', 'in', 'is null', 'is not null'];
const COMPARABLE: Op[] = ['=', '!=', '<', '<=', '>', '>=', 'in', 'is null', 'is not null'];
const TEXT: Op[] = ['=', '!=', '<', '<=', '>', '>=', 'in', 'like', 'is null', 'is not null'];

/** The operators the API accepts for a field type (`forgebase_db::list`). */
export function opsFor(type: FieldType): Op[] {
  switch (type) {
    case 'data':
    case 'text':
    case 'select':
      return TEXT; // the only types that take `like`
    case 'int':
    case 'decimal':
    case 'date':
    case 'datetime':
      return COMPARABLE;
    case 'check':
    case 'link':
      return EQUALITY; // no ordering, no `like`
    case 'table':
      return []; // the rows of a Table field cannot be filtered
  }
}

export type Built = { ok: true; filters: unknown[] } | { ok: false; error: string };

function typed(type: FieldType, text: string): { ok: true; value: unknown } | { ok: false; error: string } {
  switch (type) {
    case 'int': {
      if (!/^-?\d+$/.test(text)) return { ok: false, error: `"${text}" is not a whole number` };
      const number = Number(text);
      if (!Number.isSafeInteger(number)) return { ok: false, error: `"${text}" is too large` };
      return { ok: true, value: number };
    }
    case 'check':
      if (text === 'true') return { ok: true, value: true };
      if (text === 'false') return { ok: true, value: false };
      return { ok: false, error: 'choose yes or no' };
    default:
      // Text, Select, Link, Date, Datetime and Decimal (a string in the API) are strings.
      return { ok: true, value: text };
  }
}

/** Builds the filter array for the API, or the first problem found, worded for the user. */
export function buildFilters(rows: FilterRow[], fields: FilterField[]): Built {
  const filters: unknown[] = [];
  for (const row of rows) {
    const field = fields.find((f) => f.name === row.field);
    if (!field) return { ok: false, error: `unknown field "${row.field}"` };
    if (!opsFor(field.type).includes(row.op)) {
      return { ok: false, error: `"${row.op}" cannot be used with ${field.label}` };
    }
    if (NO_VALUE_OPS.includes(row.op)) {
      filters.push({ field: row.field, op: row.op });
      continue;
    }
    const text = row.value.trim();
    if (row.op === 'in') {
      const parts = text === '' ? [] : text.split(',').map((p) => p.trim());
      if (parts.length === 0 || parts.some((p) => p === '')) {
        return { ok: false, error: `enter values separated by commas for ${field.label}` };
      }
      const values: unknown[] = [];
      for (const part of parts) {
        const result = typed(field.type, part);
        if (!result.ok) return { ok: false, error: `${field.label}: ${result.error}` };
        values.push(result.value);
      }
      filters.push({ field: row.field, op: 'in', value: values });
      continue;
    }
    // A pattern keeps its spaces; every other single value is trimmed.
    const single = row.op === 'like' ? row.value : text;
    if (single === '' && field.type !== 'check') {
      return { ok: false, error: `enter a value for ${field.label}` };
    }
    const result = typed(field.type, single);
    if (!result.ok) return { ok: false, error: `${field.label}: ${result.error}` };
    filters.push({ field: row.field, op: row.op, value: result.value });
  }
  return { ok: true, filters };
}

const ALL_OPS: readonly string[] = [
  '=', '!=', '<', '<=', '>', '>=', 'in', 'like', 'is null', 'is not null',
];

/** Reads the `filters` JSON of the address back into rows; anything malformed gives no rows. */
export function parseFilters(text: string | null): FilterRow[] {
  if (!text) return [];
  let json: unknown;
  try {
    json = JSON.parse(text);
  } catch {
    return [];
  }
  if (!Array.isArray(json)) return [];
  const rows: FilterRow[] = [];
  for (const item of json) {
    if (typeof item !== 'object' || item === null) return [];
    const { field, op, value } = item as Record<string, unknown>;
    if (typeof field !== 'string' || typeof op !== 'string' || !ALL_OPS.includes(op)) return [];
    const text = Array.isArray(value)
      ? value.map(String).join(', ')
      : value === undefined || value === null
        ? ''
        : String(value);
    rows.push({ field, op: op as Op, value: text });
  }
  return rows;
}

/** Where keyboard focus goes after a filter row is removed or added. */
export type FocusTarget = { kind: 'row'; index: number } | { kind: 'add' };

/**
 * After removing row `removed` of `count` rows: the row that took its place (the next one), or
 * the one before it when the last row was removed, or the "Add filter" button when none is left.
 */
export function focusAfterRemove(removed: number, count: number): FocusTarget {
  const remaining = count - 1;
  if (remaining <= 0) return { kind: 'add' };
  return { kind: 'row', index: Math.min(removed, remaining - 1) };
}

const OP_WORDS: Record<Op, string> = {
  '=': 'is',
  '!=': 'is not',
  '<': '<',
  '<=': '<=',
  '>': '>',
  '>=': '>=',
  in: 'in',
  like: 'matches',
  'is null': 'is empty',
  'is not null': 'is not empty',
};

/** One active filter as a chip: its text ("Status is Draft") and its place in the filter list. */
export interface Chip {
  index: number;
  label: string;
}

/** The chip text of a filter row, in the user's words: `Tier is gold`, `Points >= 10`, `Notes is empty`. */
export function chipLabel(row: FilterRow, fields: FilterField[]): string {
  const field = fields.find((f) => f.name === row.field);
  const name = field?.label ?? row.field;
  const word = OP_WORDS[row.op];
  if (NO_VALUE_OPS.includes(row.op)) return `${name} ${word}`;
  const value = field?.type === 'check' ? (row.value === 'true' ? 'Yes' : 'No') : row.value;
  return `${name} ${word} ${value}`;
}

/** The chips for the `filters` text of the address, one per filter, in order. */
export function filterChips(filters: string | null, fields: FilterField[]): Chip[] {
  return parseFilters(filters).map((row, index) => ({ index, label: chipLabel(row, fields) }));
}

/**
 * The `filters` text without filter number `index` (null when none is left): what a click on the
 * chip's x applies. Text that is not a list of filters is returned unchanged.
 */
export function withoutFilter(filters: string | null, index: number, fields: FilterField[]): string | null {
  const rows = parseFilters(filters);
  if (index < 0 || index >= rows.length) return filters;
  const rest = rows.filter((_, i) => i !== index);
  if (rest.length === 0) return null;
  const built = buildFilters(rest, fields);
  return built.ok ? JSON.stringify(built.filters) : filters;
}
