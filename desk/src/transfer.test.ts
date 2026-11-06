import { describe, expect, it } from 'vitest';

import {
  canExport,
  canImport,
  canUpsert,
  detectFormat,
  errorHeadline,
  exportFields,
  exportHref,
  filenameFromDisposition,
  importContentType,
  mayImport,
  moreErrorsText,
  parseSummary,
  rowErrorText,
  rowLabel,
  summaryText,
  validationKey,
} from './transfer';
import { rights } from './testing';

describe('exportHref', () => {
  it('carries the filters, the sort and the visible columns', () => {
    const href = exportHref('sales order', 'csv', {
      filters: '[{"field":"status","op":"=","value":"Open"}]',
      sort: 'modified_at:desc',
      fields: ['name', 'customer', 'modified_at'],
    });
    const [path, query] = href.split('?');
    expect(path).toBe('/api/v1/export/sales%20order');
    const params = new URLSearchParams(query);
    expect(params.get('format')).toBe('csv');
    expect(params.get('filters')).toBe('[{"field":"status","op":"=","value":"Open"}]');
    expect(params.get('sort')).toBe('modified_at:desc');
    expect(params.get('fields')).toBe('name,customer,modified_at');
  });

  it('leaves out what is not given', () => {
    expect(exportHref('customer', 'json')).toBe('/api/v1/export/customer?format=json');
    expect(exportHref('customer', 'json', { fields: [] })).toBe('/api/v1/export/customer?format=json');
  });
});

describe('exportFields', () => {
  it('adds the title field after the name, which the Name column shows', () => {
    expect(exportFields(['name', 'tier', 'owner'], 'full_name')).toEqual(['name', 'full_name', 'tier', 'owner']);
  });

  it('leaves the columns alone when the title is already one, or there is none', () => {
    expect(exportFields(['name', 'full_name', 'tier'], 'full_name')).toEqual(['name', 'full_name', 'tier']);
    expect(exportFields(['name', 'tier'], null)).toEqual(['name', 'tier']);
    expect(exportFields(['name', 'tier'], 'name')).toEqual(['name', 'tier']);
    expect(exportFields(['tier'], 'full_name')).toEqual(['tier']);
  });
});

describe('rights', () => {
  it('needs the export and the read right for an export, import and create for an import', () => {
    expect(canExport(rights({ read: 'all', export: 'all' }))).toBe(true);
    expect(canExport(rights({ read: 'own', export: 'own' }))).toBe(true);
    expect(canExport(rights({ read: 'all' }))).toBe(false);
    expect(canExport(rights({ export: 'all' }))).toBe(false);
    expect(canImport(rights({ import: 'all', create: 'all' }))).toBe(true);
    expect(canImport(rights({ import: 'all' }))).toBe(false);
    expect(canImport(rights({ create: 'all' }))).toBe(false);
    expect(canUpsert(rights({ import: 'all', create: 'all' }))).toBe(false);
    expect(canUpsert(rights({ import: 'all', create: 'all', write: 'own' }))).toBe(true);
  });
});

describe('files', () => {
  it('reads the file name of the download', () => {
    expect(filenameFromDisposition('attachment; filename="customer-20261031T101500Z.csv"', 'x.csv')).toBe(
      'customer-20261031T101500Z.csv',
    );
    expect(filenameFromDisposition(null, 'fallback.csv')).toBe('fallback.csv');
    expect(filenameFromDisposition('attachment', 'fallback.csv')).toBe('fallback.csv');
    expect(filenameFromDisposition('attachment; filename="../etc/passwd"', 'safe.csv')).toBe('safe.csv');
  });

  it('detects the format from the extension and knows its content type', () => {
    expect(detectFormat('people.CSV')).toBe('csv');
    expect(detectFormat('people.json')).toBe('json');
    expect(detectFormat('people.xlsx')).toBeNull();
    expect(detectFormat('csv')).toBeNull();
    expect(importContentType('csv')).toBe('text/csv');
    expect(importContentType('json')).toBe('application/json');
  });
});

const failed = {
  dry_run: true,
  mode: 'insert',
  rows: 5,
  inserted: 3,
  updated: 0,
  error_count: 2,
  errors: [
    { row: 2, line: 3, name: 'b@x.y', code: 'validation_failed', message: 'validation failed', details: ['field `full_name` is required'] },
    { row: 4, name: null, code: 'hook_rejected', message: 'rejected by the app' },
  ],
};

describe('parseSummary', () => {
  it('reads the counts and the row errors', () => {
    const summary = parseSummary(failed);
    expect(summary).toEqual({
      dryRun: true,
      mode: 'insert',
      rows: 5,
      inserted: 3,
      updated: 0,
      errorCount: 2,
      errors: [
        { row: 2, line: 3, name: 'b@x.y', code: 'validation_failed', message: 'validation failed', details: ['field `full_name` is required'] },
        { row: 4, line: null, name: null, code: 'hook_rejected', message: 'rejected by the app', details: [] },
      ],
    });
  });

  it('refuses what is not a summary', () => {
    expect(parseSummary(null)).toBeNull();
    expect(parseSummary(['a'])).toBeNull();
    expect(parseSummary({ rows: 'x', errors: [] })).toBeNull();
    expect(parseSummary({ rows: 1 })).toBeNull();
    // A row error without a row number is dropped, the count stays the server's.
    const odd = parseSummary({ rows: 2, errors: [{ message: 'x' }], error_count: 1 });
    expect(odd?.errors).toEqual([]);
    expect(odd?.errorCount).toBe(1);
  });
});

describe('texts', () => {
  const summary = parseSummary(failed);
  if (summary === null) throw new Error('fixture');

  it('says what an import did', () => {
    expect(summaryText({ ...summary, rows: 1, inserted: 1, errorCount: 0, errors: [] })).toBe('1 row: 1 new.');
    expect(summaryText({ ...summary, mode: 'upsert', rows: 5, inserted: 2, updated: 3 })).toBe('5 rows: 2 new, 3 updated.');
  });

  it('says how many rows have errors and when the list is cut', () => {
    expect(errorHeadline(summary)).toBe('2 rows have errors out of 5 rows. Nothing is imported until every row is fine.');
    expect(errorHeadline({ ...summary, errorCount: 1, rows: 1 })).toContain('1 row has errors out of 1 row.');
    expect(moreErrorsText(summary)).toBeNull();
    expect(moreErrorsText({ ...summary, errorCount: 150 })).toBe('Showing the first 2 errors; 148 more not shown.');
  });

  it('shows the details of a row error, else its message, and its place in the file', () => {
    const [first, second] = summary.errors;
    if (first === undefined || second === undefined) throw new Error('fixture');
    expect(rowErrorText(first)).toBe('field `full_name` is required');
    expect(rowErrorText(second)).toBe('rejected by the app');
    expect(rowLabel(first)).toBe('2 (line 3)');
    expect(rowLabel(second)).toBe('4');
  });
});

describe('mayImport', () => {
  const file = { name: 'a.csv', size: 10, lastModified: 1 };
  const key = validationKey(file, 'csv', 'insert');
  const clean = parseSummary({ rows: 1, inserted: 1, updated: 0, errors: [], error_count: 0, dry_run: true, mode: 'insert' });
  if (clean === null) throw new Error('fixture');

  it('needs a clean validation of the same file, format and mode', () => {
    expect(mayImport(null, key)).toBe(false);
    expect(mayImport({ key, summary: clean }, key)).toBe(true);
    expect(mayImport({ key, summary: { ...clean, errorCount: 1 } }, key)).toBe(false);
    expect(mayImport({ key, summary: clean }, validationKey(file, 'csv', 'upsert'))).toBe(false);
    expect(mayImport({ key, summary: clean }, validationKey(file, 'json', 'insert'))).toBe(false);
    expect(mayImport({ key, summary: clean }, validationKey({ ...file, size: 11 }, 'csv', 'insert'))).toBe(false);
  });
});
