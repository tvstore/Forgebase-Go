// Pure helpers of the form view: the values the native controls hold (strings and booleans),
// their conversion to and from the API's JSON (architecture, section 7; `forgebase_http::json`)
// and change detection. The server validates everything again; this only turns what the user
// typed into the right JSON kind and rejects what cannot be sent at all (a non-integer in an Int
// field), so the user gets the message on the field.

import type { DocTypeMeta, DocumentRow, FieldMeta } from './types';

/** What a native control holds: text, or a boolean for a checkbox. */
export type FormValue = string | boolean;

export type FormValues = Record<string, FormValue>;

/** A row of a Table field. `key` is a stable React key (rows are reordered by remove only). */
export interface FormRow {
  key: number;
  values: FormValues;
}

export type FormTables = Record<string, FormRow[]>;

/** The empty value of a control: unchecked, or no text. */
export function emptyValue(field: FieldMeta): FormValue {
  return field.type === 'check' ? false : '';
}

const pad = (n: number): string => String(n).padStart(2, '0');

const RFC3339_UTC = /^(\d{4}-\d{2}-\d{2})T(\d{2}:\d{2}:\d{2})(?:\.\d+)?Z$/;

/**
 * A Datetime from the API (RFC 3339 in UTC with `Z`) as the value of `<input type="datetime-local">`:
 * `YYYY-MM-DDTHH:mm:ss` in the **browser's local time zone**. Fractions of a second are dropped
 * (the control has none); an unchanged value is never sent back (see {@link changedFields}), so
 * nothing is lost by that.
 */
export function utcToLocal(utc: string): string {
  if (!RFC3339_UTC.test(utc)) return '';
  const date = new Date(utc);
  if (Number.isNaN(date.getTime())) return '';
  return (
    `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}` +
    `T${pad(date.getHours())}:${pad(date.getMinutes())}:${pad(date.getSeconds())}`
  );
}

/**
 * The value of a `datetime-local` control (local time, `YYYY-MM-DDTHH:mm` or with seconds) as an
 * RFC 3339 string in UTC with `Z` and whole seconds, or null when it is not a valid local time.
 * A local time that does not exist (the hour skipped by a daylight saving change) is refused.
 */
export function localToUtc(local: string): string | null {
  const match = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2})(?::(\d{2}))?$/.exec(local);
  if (!match) return null;
  const [year, month, day, hour, minute, second] = [
    Number(match[1]),
    Number(match[2]),
    Number(match[3]),
    Number(match[4]),
    Number(match[5]),
    Number(match[6] ?? '0'),
  ];
  const date = new Date(year, month - 1, day, hour, minute, second);
  if (
    date.getFullYear() !== year ||
    date.getMonth() !== month - 1 ||
    date.getDate() !== day ||
    date.getHours() !== hour ||
    date.getMinutes() !== minute
  ) {
    return null; // an impossible date (February 30) or a skipped local time
  }
  // toISOString is `YYYY-MM-DDTHH:mm:ss.sssZ`: drop the milliseconds.
  return date.toISOString().replace(/\.\d{3}Z$/, 'Z');
}

/** An API value as the value of its native control. */
export function toFormValue(field: FieldMeta, json: unknown): FormValue {
  if (json === null || json === undefined) return emptyValue(field);
  switch (field.type) {
    case 'check':
      return json === true;
    case 'datetime':
      return typeof json === 'string' ? utcToLocal(json) : '';
    case 'int':
    case 'decimal':
      return String(json);
    default:
      return typeof json === 'string' ? json : String(json);
  }
}

export type Converted = { ok: true; value: unknown } | { ok: false; error: string };

const INTEGER = /^[+-]?\d+$/;
const DECIMAL = /^[+-]?(\d+\.?\d*|\.\d+)$/;

/**
 * A control's value as the JSON value of the API: `null` for an empty control, an integer for Int,
 * a **string** for Decimal (the API rejects a JSON number), `true`/`false` for Check, UTC RFC 3339
 * for Datetime. Returns an error text for a value that cannot be sent (the server decides every
 * other rule).
 */
export function toJsonValue(field: FieldMeta, value: FormValue): Converted {
  if (field.type === 'check') return { ok: true, value: value === true };
  const text = typeof value === 'string' ? value : '';
  switch (field.type) {
    case 'int': {
      const trimmed = text.trim();
      if (trimmed === '') return { ok: true, value: null };
      if (!INTEGER.test(trimmed)) return { ok: false, error: 'Enter a whole number.' };
      const number = Number(trimmed);
      if (!Number.isSafeInteger(number)) return { ok: false, error: 'This number is too large.' };
      return { ok: true, value: number };
    }
    case 'decimal': {
      const trimmed = text.trim();
      if (trimmed === '') return { ok: true, value: null };
      if (!DECIMAL.test(trimmed)) return { ok: false, error: 'Enter a number such as 1234.50.' };
      return { ok: true, value: trimmed };
    }
    case 'datetime': {
      if (text === '') return { ok: true, value: null };
      const utc = localToUtc(text);
      return utc === null
        ? { ok: false, error: 'Enter a valid date and time.' }
        : { ok: true, value: utc };
    }
    default:
      // Data, Text, Select, Link and Date (YYYY-MM-DD from the native control): text, or null.
      return { ok: true, value: text === '' ? null : text };
  }
}

/** The state of the form: one value per field and the rows of each Table field. */
export interface FormState {
  values: FormValues;
  tables: FormTables;
}

let rowCounter = 0;
/** A new React key for a row (module-wide, so keys never repeat). */
export const nextRowKey = (): number => (rowCounter += 1);

/** A new row of `fields` with every control empty. */
export function blankRow(fields: FieldMeta[]): FormRow {
  const values: FormValues = {};
  for (const f of fields) values[f.name] = emptyValue(f);
  return { key: nextRowKey(), values };
}

/** The empty form of a new document. */
export function blankState(meta: DocTypeMeta): FormState {
  const values: FormValues = {};
  const tables: FormTables = {};
  for (const f of meta.fields) {
    if (f.type === 'table') tables[f.name] = [];
    else values[f.name] = emptyValue(f);
  }
  return { values, tables };
}

/** The form state of a document as the API returned it. */
export function stateOf(meta: DocTypeMeta, doc: DocumentRow): FormState {
  const values: FormValues = {};
  const tables: FormTables = {};
  for (const f of meta.fields) {
    if (f.type === 'table') {
      const rows = Array.isArray(doc[f.name]) ? (doc[f.name] as Record<string, unknown>[]) : [];
      tables[f.name] = rows.map((row) => {
        const rowValues: FormValues = {};
        for (const c of f.fields ?? []) rowValues[c.name] = toFormValue(c, row[c.name]);
        return { key: nextRowKey(), values: rowValues };
      });
    } else {
      values[f.name] = toFormValue(f, doc[f.name]);
    }
  }
  return { values, tables };
}

/** The key of an error: `field`, or `table/row/field` for a cell (`row` counts from 1). */
export const errorKey = (field: string, row?: number, cell?: string): string =>
  row === undefined || cell === undefined ? field : `${field}/${row}/${cell}`;

export type Errors = Record<string, string>;

export type Built =
  | { ok: true; data: Record<string, unknown> }
  | { ok: false; errors: Errors };

/**
 * One row as JSON; empty controls are left out (the API reads a missing key as no value) and so
 * are `read_only` columns (the server computes them and refuses a row that carries one).
 */
function rowToJson(
  tableName: string,
  rowNumber: number,
  fields: FieldMeta[],
  row: FormRow,
  errors: Errors,
): Record<string, unknown> {
  const json: Record<string, unknown> = {};
  for (const f of fields) {
    if (f.read_only) continue; // computed by the server: the server refuses a row that has it
    const result = toJsonValue(f, row.values[f.name] ?? emptyValue(f));
    if (!result.ok) errors[errorKey(tableName, rowNumber, f.name)] = result.error;
    else if (result.value !== null) json[f.name] = result.value;
  }
  return json;
}

function tableToJson(field: FieldMeta, rows: FormRow[], errors: Errors): Record<string, unknown>[] {
  return rows.map((row, i) => rowToJson(field.name, i + 1, field.fields ?? [], row, errors));
}

/**
 * The request body of a create: every non-empty field and the rows of every Table field. A
 * field the user cannot edit is still sent when it has a value (there is none on a new
 * document); a `read_only` field never is.
 */
export function buildCreate(meta: DocTypeMeta, state: FormState): Built {
  const data: Record<string, unknown> = {};
  const errors: Errors = {};
  for (const f of meta.fields) {
    if (f.type === 'table') {
      data[f.name] = tableToJson(f, state.tables[f.name] ?? [], errors);
      continue;
    }
    if (f.read_only) continue; // never sent
    const result = toJsonValue(f, state.values[f.name] ?? emptyValue(f));
    if (!result.ok) errors[f.name] = result.error;
    else if (result.value !== null) data[f.name] = result.value;
  }
  return Object.keys(errors).length > 0 ? { ok: false, errors } : { ok: true, data };
}

const rowsText = (rows: FormRow[]): string => JSON.stringify(rows.map((r) => r.values));

/** Names of the fields (values and tables) whose control state differs from `initial`. */
export function changedFields(meta: DocTypeMeta, initial: FormState, current: FormState): string[] {
  const changed: string[] = [];
  for (const f of meta.fields) {
    if (f.type === 'table') {
      if (rowsText(initial.tables[f.name] ?? []) !== rowsText(current.tables[f.name] ?? [])) {
        changed.push(f.name);
      }
    } else if (initial.values[f.name] !== current.values[f.name]) {
      changed.push(f.name);
    }
  }
  return changed;
}

/**
 * The `data` of a `PATCH`: **only** the fields in `editable` that changed, never a `read_only`
 * one. A changed Table field is sent as all its rows (the server replaces the rows of a table it
 * is given) without their `read_only` columns.
 */
export function buildChanges(
  meta: DocTypeMeta,
  initial: FormState,
  current: FormState,
  editable: (field: FieldMeta) => boolean,
): Built {
  const data: Record<string, unknown> = {};
  const errors: Errors = {};
  const changed = new Set(changedFields(meta, initial, current));
  for (const f of meta.fields) {
    if (!changed.has(f.name) || !editable(f) || f.read_only) continue;
    if (f.type === 'table') {
      data[f.name] = tableToJson(f, current.tables[f.name] ?? [], errors);
      continue;
    }
    const result = toJsonValue(f, current.values[f.name] ?? emptyValue(f));
    if (!result.ok) errors[f.name] = result.error;
    else data[f.name] = result.value; // null clears the field
  }
  return Object.keys(errors).length > 0 ? { ok: false, errors } : { ok: true, data };
}
