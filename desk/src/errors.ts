// Maps the `details` of a `422 validation_failed` answer to the fields of the form. The details
// are the texts of `forgebase_core::document::ValidationError` and of the JSON mapping, for
// example:
//
//   field `email` is required
//   field `credit_limit`: 3 decimal places exceeds scale 2
//   field `customer`: customer `X` does not exist
//   field `items` row 2: field `qty` is required
//   field `items`: row 2: field `qty`: expected an integer, found a string
//
// A detail that is not about a field of the form (an unknown field, a naming problem) stays in
// the summary at the top of the form.

import { errorKey } from './fields';
import type { Errors } from './fields';
import type { DocTypeMeta } from './types';

export interface MappedErrors {
  /** Messages by {@link errorKey}. */
  errors: Errors;
  /** The details that name no field of the form, as the server wrote them. */
  unmapped: string[];
}

const capitalize = (text: string): string => text.charAt(0).toUpperCase() + text.slice(1);

/** `is required` reads badly on its own; everything else is already a sentence fragment. */
function clean(rest: string): string {
  const text = rest.replace(/^:\s*/, '').trim();
  if (text === 'is required') return 'This field is required.';
  return `${capitalize(text)}${/[.!?]$/.test(text) ? '' : '.'}`;
}

const FIELD = /^field `([^`]+)`(.*)$/s;
const ROW = /^:?\s*row (\d+):?\s*(.*)$/s;

/** The key and message of one detail, or null when it is about no field of the form. */
export function mapDetail(detail: string, meta: DocTypeMeta): { key: string; message: string } | null {
  const outer = FIELD.exec(detail);
  if (!outer) return null;
  const field = meta.fields.find((f) => f.name === outer[1]);
  if (!field) return null;
  const rest = outer[2] ?? '';
  const row = ROW.exec(rest);
  if (field.type !== 'table' || !row) {
    // A field-level problem; for a Table field one that is not about a row (it is required).
    return { key: errorKey(field.name), message: clean(rest) };
  }
  const number = Number(row[1]);
  const inner = FIELD.exec(row[2] ?? '');
  const cell = inner ? field.fields?.find((c) => c.name === inner[1]) : undefined;
  if (!inner || !cell) {
    // A problem of the row as a whole: shown under the table.
    return { key: errorKey(field.name), message: `Row ${number}: ${row[2] ?? ''}` };
  }
  return { key: errorKey(field.name, number, cell.name), message: clean(inner[2] ?? '') };
}

/**
 * Splits the details of a `422` into messages for named fields (the parameters of a report: no
 * rows, no Table fields) and the rest. The first message per field wins.
 */
export function mapNamedDetails(details: string[], names: ReadonlySet<string>): MappedErrors {
  const errors: Errors = {};
  const unmapped: string[] = [];
  for (const detail of details) {
    const match = FIELD.exec(detail);
    const name = match?.[1];
    if (match === null || name === undefined || !names.has(name)) unmapped.push(detail);
    else if (!(errorKey(name) in errors)) errors[errorKey(name)] = clean(match[2] ?? '');
  }
  return { errors, unmapped };
}

/** Splits the details into messages for fields (first message per field wins) and the rest. */
export function mapDetails(details: string[], meta: DocTypeMeta): MappedErrors {
  const errors: Errors = {};
  const unmapped: string[] = [];
  for (const detail of details) {
    const mapped = mapDetail(detail, meta);
    if (mapped === null) unmapped.push(detail);
    else if (!(mapped.key in errors)) errors[mapped.key] = mapped.message;
  }
  return { errors, unmapped };
}

/** The error codes that say the version held by the form is out of date. */
export function isStaleCode(code: string): boolean {
  return code === 'conflict' || code === 'invalid_docstatus';
}

/**
 * The text of a `409 conflict`. `verb` completes "nothing was ..." (saved, submitted, ...); the
 * form only says its edits are kept when it has some.
 */
export function conflictMessage(verb: string, dirty: boolean): string {
  const base = 'Someone else changed this document after you opened it.';
  return dirty ? `${base} Your edits are kept below and nothing was ${verb}.` : `${base} Nothing was ${verb}.`;
}
