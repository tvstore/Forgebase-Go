// The group-by summary (M8.3): which fields can be grouped and summed, the request, and the rows.
// The server validates everything again and applies the read rules; this only keeps the panel
// from offering what it would refuse.

import { docStatusLabel, docStatusOf } from './access';
import { formatDate, groupDigits } from './listing';
import { stateBadge } from './status';
import type { BadgeSpec } from './status';
import type { DocTypeMeta, FieldType } from './types';

/** The most value columns of the summary. */
export const MAX_VALUES = 3;

/** The groups the server returns at most. */
export const MAX_GROUPS = 500;

export type AggregateFn = 'sum' | 'avg' | 'min' | 'max';

export const AGGREGATE_FNS: readonly AggregateFn[] = ['sum', 'avg', 'min', 'max'];

const FN_WORD: Record<AggregateFn, string> = { sum: 'Sum', avg: 'Average', min: 'Minimum', max: 'Maximum' };

export interface GroupField {
  name: string;
  label: string;
  type: FieldType;
}

export interface ValueChoice {
  field: string;
  fn: AggregateFn;
}

export interface SummaryGroup {
  key: unknown;
  count: number;
  values: Record<string, unknown>;
}

export interface SummaryResult {
  groups: SummaryGroup[];
  truncated: boolean;
}

/**
 * The fields a summary can group by: the owner, the status (the workflow state, or the document
 * status of a submittable DocType) and the Data, Select, Link, Check, Date and Int fields. Not
 * Text, Decimal, Datetime or Table fields, and not `name` (every document has its own).
 */
export function groupFields(meta: DocTypeMeta): GroupField[] {
  const status: GroupField[] = meta.workflow
    ? [{ name: 'workflow_state', label: 'Status', type: 'select' }]
    : meta.submittable
      ? [{ name: 'docstatus', label: 'Status', type: 'int' }]
      : [];
  const own = meta.fields
    .filter((f) => ['data', 'select', 'link', 'check', 'date', 'int'].includes(f.type))
    .map((f) => ({ name: f.name, label: f.label, type: f.type }));
  return [...status, { name: 'owner', label: 'Owner', type: 'data' }, ...own];
}

/** The fields a value column can summarise: Int and Decimal fields. */
export function valueFields(meta: DocTypeMeta): GroupField[] {
  return meta.fields
    .filter((f) => f.type === 'int' || f.type === 'decimal')
    .map((f) => ({ name: f.name, label: f.label, type: f.type }));
}

/** The first value column offered: the sum of the first numeric field, or none. */
export function defaultValues(meta: DocTypeMeta): ValueChoice[] {
  const first = valueFields(meta)[0];
  return first ? [{ field: first.name, fn: 'sum' }] : [];
}

/** The key of a value in the answer: `sum_amount`. */
export const valueKey = (choice: ValueChoice): string => `${choice.fn}_${choice.field}`;

/** The `aggregates` parameter: `sum:amount,avg:qty`; a choice named twice is sent once. */
export function aggregatesParam(choices: ValueChoice[]): string {
  const seen = new Set<string>();
  const parts: string[] = [];
  for (const choice of choices) {
    const key = valueKey(choice);
    if (seen.has(key)) continue;
    seen.add(key);
    parts.push(`${choice.fn}:${choice.field}`);
  }
  return parts.join(',');
}

/** The header of a value column: `Sum of Amount`. */
export function valueHeading(choice: ValueChoice, fields: GroupField[]): string {
  const label = fields.find((f) => f.name === choice.field)?.label ?? choice.field;
  return `${FN_WORD[choice.fn]} of ${label}`;
}

/** The first `MAX_VALUES` distinct choices (what the request carries). */
export function distinctValues(choices: ValueChoice[]): ValueChoice[] {
  const seen = new Set<string>();
  return choices
    .filter((c) => {
      const key = valueKey(c);
      if (seen.has(key)) return false;
      seen.add(key);
      return true;
    })
    .slice(0, MAX_VALUES);
}

/** A number of the summary as text: digits grouped, a decimal keeps its scale; null is a dash. */
export function formatNumber(value: unknown, type: FieldType): string {
  if (value === null || value === undefined) return '—';
  if (type === 'date' && typeof value === 'string') return formatDate(value);
  if (typeof value === 'number') return groupDigits(String(value));
  return typeof value === 'string' ? groupDigits(value) : String(value);
}

/** A group's key as text for the row label; a missing value is "(none)". */
export function keyText(key: unknown, field: GroupField): string {
  if (key === null || key === undefined || key === '') return '(none)';
  if (field.type === 'check') return key === true ? 'Yes' : 'No';
  if (field.type === 'date' && typeof key === 'string') return formatDate(key);
  if (field.name === 'docstatus') return docStatusLabel(docStatusOf(key));
  return String(key);
}

/** The badge of a group of a status field (workflow state or document status), or null. */
export function keyBadge(key: unknown, field: GroupField, meta: DocTypeMeta): BadgeSpec | null {
  if (field.name === 'workflow_state' && typeof key === 'string' && key !== '') {
    return stateBadge(meta, { workflow_state: key });
  }
  if (field.name === 'docstatus' && typeof key === 'number') return stateBadge(meta, { docstatus: key });
  return null;
}

/**
 * The filter a click on a group adds: `field = key`, or `field is empty` for the group of documents
 * without a value. Values have the type the API takes (an Int a number, a Check a boolean).
 */
export function groupFilter(field: GroupField, key: unknown): Record<string, unknown> {
  if (key === null || key === undefined) return { field: field.name, op: 'is null' };
  return { field: field.name, op: '=', value: key };
}

/**
 * The `filters` text with the group's filter added at the end. Text that is not a JSON list is
 * replaced by a list with the one filter (it could not have been sent either).
 */
export function withGroupFilter(filters: string | null, field: GroupField, key: unknown): string {
  let list: unknown[] = [];
  if (filters) {
    try {
      const parsed: unknown = JSON.parse(filters);
      if (Array.isArray(parsed)) list = parsed;
    } catch {
      list = [];
    }
  }
  return JSON.stringify([...list, groupFilter(field, key)]);
}

export interface SummaryRow {
  key: unknown;
  label: string;
  badge: BadgeSpec | null;
  count: string;
  values: string[];
}

/** The rows of the table: the label, the count and one text per value column. */
export function summaryRows(
  result: SummaryResult,
  field: GroupField,
  choices: ValueChoice[],
  fields: GroupField[],
  meta: DocTypeMeta,
): SummaryRow[] {
  return result.groups.map((group) => ({
    key: group.key,
    label: keyText(group.key, field),
    badge: keyBadge(group.key, field, meta),
    count: groupDigits(String(group.count)),
    values: choices.map((choice) =>
      formatNumber(
        group.values[valueKey(choice)],
        // The result of an average is a decimal whatever the field.
        choice.fn === 'avg' ? 'decimal' : (fields.find((f) => f.name === choice.field)?.type ?? 'decimal'),
      ),
    ),
  }));
}

/** The caption of the table: `Sales Order by Status`. */
export function summaryCaption(meta: DocTypeMeta, field: GroupField): string {
  return `${meta.label} by ${field.label}`;
}

/** The note under a cut-off summary, or null. */
export function truncatedNote(result: SummaryResult): string | null {
  return result.truncated ? `Showing the first ${MAX_GROUPS} groups.` : null;
}

/**
 * The field the panel groups by when it opens: the status of a DocType that has one, else its first
 * Select field, else the first field offered.
 */
export function defaultGroupBy(meta: DocTypeMeta): GroupField | undefined {
  const fields = groupFields(meta);
  return (
    fields.find((f) => f.name === 'workflow_state' || f.name === 'docstatus') ??
    fields.find((f) => f.type === 'select') ??
    fields[0]
  );
}
