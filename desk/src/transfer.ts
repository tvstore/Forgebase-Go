// Import and export (M9.3, ADR 0021): the address of an export, the file name the server gives
// it, and the answer of an import. Pure functions, tested without a DOM.

import type { Rights } from './types';

export type FileFormat = 'csv' | 'json';
export type ImportMode = 'insert' | 'upsert';

export const FORMAT_LABELS: Record<FileFormat, string> = { csv: 'CSV', json: 'JSON' };

/** The export link of the list: its filters, its sort and the keys of the visible columns. */
export interface ExportOptions {
  /** The `filters` JSON text of the list request (the quick search included). */
  filters?: string;
  /** `field` or `field:desc`. */
  sort?: string;
  /** The keys to write, in order; absent: the server's default. */
  fields?: string[];
}

/**
 * `GET /api/v1/export/<doctype>` for the current list: the same filters and sort, and the visible
 * columns as `fields`. A plain link: a session cookie needs no CSRF token for a GET.
 */
export function exportHref(doctype: string, format: FileFormat, options: ExportOptions = {}): string {
  const query = new URLSearchParams({ format });
  if (options.filters) query.set('filters', options.filters);
  if (options.sort) query.set('sort', options.sort);
  if (options.fields && options.fields.length > 0) query.set('fields', options.fields.join(','));
  return `/api/v1/export/${encodeURIComponent(doctype)}?${query.toString()}`;
}

/**
 * The keys an export of the visible columns writes: the keys of the columns in order, with the
 * DocType's title field right after `name` when no column shows it (the Name column shows the
 * title, and an export that left it out would write only the key).
 */
export function exportFields(columnKeys: string[], titleField: string | null): string[] {
  if (titleField === null || titleField === 'name' || columnKeys.includes(titleField)) return columnKeys;
  const at = columnKeys.indexOf('name');
  return at < 0 ? columnKeys : [...columnKeys.slice(0, at + 1), titleField, ...columnKeys.slice(at + 1)];
}

/** Whether the user may export a DocType: the export and the read right (either may be `own`). */
export const canExport = (rights: Rights): boolean => rights.export !== 'none' && rights.read !== 'none';

/** Whether the user may import into a DocType: the import and the create right. */
export const canImport = (rights: Rights): boolean => rights.import !== 'none' && rights.create !== 'none';

/** An upsert also needs the write right. */
export const canUpsert = (rights: Rights): boolean => canImport(rights) && rights.write !== 'none';

/** The file name of a `Content-Disposition: attachment; filename="..."` header, else `fallback`. */
export function filenameFromDisposition(header: string | null, fallback: string): string {
  const match = header === null ? null : /filename="([^"\\/]+)"/.exec(header);
  return match?.[1] ?? fallback;
}

/** The format a file name says (`.csv`, `.json`), or null. */
export function detectFormat(filename: string): FileFormat | null {
  const lower = filename.toLowerCase();
  if (lower.endsWith('.csv')) return 'csv';
  if (lower.endsWith('.json')) return 'json';
  return null;
}

/** The `Content-Type` an import of this format must carry. */
export const importContentType = (format: FileFormat): string =>
  format === 'csv' ? 'text/csv' : 'application/json';

/** One row the import refused. */
export interface RowError {
  row: number;
  /** The line of a CSV file. */
  line: number | null;
  /** The document name, when the row gave one. */
  name: string | null;
  code: string;
  message: string;
  details: string[];
}

/** The answer of an import (`data` of the 200, `details` of the 422 `import_failed`). */
export interface ImportSummary {
  dryRun: boolean;
  mode: ImportMode;
  rows: number;
  inserted: number;
  updated: number;
  errors: RowError[];
  errorCount: number;
}

const isRecord = (value: unknown): value is Record<string, unknown> =>
  typeof value === 'object' && value !== null && !Array.isArray(value);

const count = (value: unknown): number => (typeof value === 'number' && Number.isFinite(value) ? value : 0);

/** Reads the summary the server sent; null when it is not one. */
export function parseSummary(value: unknown): ImportSummary | null {
  if (!isRecord(value) || typeof value.rows !== 'number' || !Array.isArray(value.errors)) return null;
  const errors: RowError[] = [];
  for (const entry of value.errors) {
    if (!isRecord(entry) || typeof entry.row !== 'number') continue;
    errors.push({
      row: entry.row,
      line: typeof entry.line === 'number' ? entry.line : null,
      name: typeof entry.name === 'string' ? entry.name : null,
      code: typeof entry.code === 'string' ? entry.code : '',
      message: typeof entry.message === 'string' ? entry.message : '',
      details: Array.isArray(entry.details) ? entry.details.filter((d): d is string => typeof d === 'string') : [],
    });
  }
  return {
    dryRun: value.dry_run === true,
    mode: value.mode === 'upsert' ? 'upsert' : 'insert',
    rows: value.rows,
    inserted: count(value.inserted),
    updated: count(value.updated),
    errors,
    errorCount: count(value.error_count) || errors.length,
  };
}

const plural = (n: number, one: string, many: string): string => `${n} ${n === 1 ? one : many}`;

/** What an import did (or, for a validation, would do): `5 rows: 3 new, 2 updated.` */
export function summaryText(summary: ImportSummary): string {
  const parts = [`${summary.inserted} new`];
  if (summary.mode === 'upsert') parts.push(`${summary.updated} updated`);
  return `${plural(summary.rows, 'row', 'rows')}: ${parts.join(', ')}.`;
}

/** The headline of a summary with errors. */
export function errorHeadline(summary: ImportSummary): string {
  return `${plural(summary.errorCount, 'row has', 'rows have')} errors out of ${plural(summary.rows, 'row', 'rows')}. Nothing is imported until every row is fine.`;
}

/** The note under the error table when the server listed only the first rows. */
export function moreErrorsText(summary: ImportSummary): string | null {
  const hidden = summary.errorCount - summary.errors.length;
  return hidden > 0 ? `Showing the first ${summary.errors.length} errors; ${hidden} more not shown.` : null;
}

/** The message of a row error with its details: `validation failed: field `x` is required`. */
export function rowErrorText(error: RowError): string {
  return error.details.length > 0 ? error.details.join('; ') : error.message;
}

/** The position of a row in its file: `3` for JSON, `3 (line 4)` for CSV. */
export function rowLabel(error: RowError): string {
  return error.line === null ? String(error.row) : `${error.row} (line ${error.line})`;
}

/** What identifies a validation: the file and the options it was checked with. */
export function validationKey(
  file: { name: string; size: number; lastModified: number },
  format: FileFormat,
  mode: ImportMode,
): string {
  return `${file.name}|${file.size}|${file.lastModified}|${format}|${mode}`;
}

/** What a validation says; the Import button needs a clean one for the very same file and options. */
export interface Validation {
  key: string;
  summary: ImportSummary;
}

/** True when `validation` is of the current choice and found no error. */
export function mayImport(validation: Validation | null, key: string): boolean {
  return validation !== null && validation.key === key && validation.summary.errorCount === 0;
}
