// The rows of a report on screen (M8.4): how a cell is written, how a column is sorted on the
// client, and the row count. The server returns the rows in the order of the SQL; sorting here
// only reorders what was returned. Pure functions, tested without a DOM.

import { formatCell, groupDigits } from './listing';
import type { LinkTitles } from './linkTitles';
import { linkTitle } from './linkTitles';
import type { ColumnKind, ReportColumn } from './reports';
import type { FieldType } from './types';

/** A column of numbers lines up on the right. */
export const isNumericKind = (kind: ColumnKind): boolean => kind === 'int' || kind === 'decimal';

/** The cell as text (a Link shows its title when the API gave one). Never HTML. */
export function cellText(value: unknown, column: ReportColumn, titles: LinkTitles = {}): string {
  const type: FieldType = column.kind;
  return formatCell(value, type, column.doctype, titles);
}

/** The sort of a report: a column (by position) and a direction; `null` is the order of the SQL. */
export type SortState = { column: number; desc: boolean } | null;

/** A click on a header: a new column is ascending, then descending, then back to the order of the SQL. */
export function nextSort(current: SortState, column: number): SortState {
  if (current === null || current.column !== column) return { column, desc: false };
  return current.desc ? null : { column, desc: true };
}

/** The `aria-sort` of the header of `column`. */
export function ariaSort(sort: SortState, column: number): 'ascending' | 'descending' | undefined {
  if (sort === null || sort.column !== column) return undefined;
  return sort.desc ? 'descending' : 'ascending';
}

const collator = new Intl.Collator('en', { sensitivity: 'base', numeric: true });

const isEmpty = (value: unknown): boolean => value === null || value === undefined;

/** Compares two non-empty values of a column of `kind`: numbers as numbers (decimals arrive as strings), dates in time order, text by `localeCompare`. */
function compare(a: unknown, b: unknown, column: ReportColumn, titles: LinkTitles): number {
  switch (column.kind) {
    case 'int':
    case 'decimal':
      return Number(a) - Number(b);
    case 'date':
    case 'datetime':
      return Date.parse(String(a)) - Date.parse(String(b));
    case 'check':
      return Number(a === true) - Number(b === true);
    case 'link':
      return collator.compare(linkTitle(titles, column.doctype, a) ?? String(a), linkTitle(titles, column.doctype, b) ?? String(b));
    default:
      return collator.compare(String(a), String(b));
  }
}

/**
 * The rows in the order of `sort` (a new array; the rows are the same objects). The sort is stable,
 * and an empty value (`null`) is **last in both directions**, so that a descending sort does not
 * bring the gaps to the top.
 */
export function sortRows(rows: unknown[][], columns: ReportColumn[], sort: SortState, titles: LinkTitles = {}): unknown[][] {
  if (sort === null) return rows;
  const column = columns[sort.column];
  if (column === undefined) return rows;
  const sign = sort.desc ? -1 : 1;
  return rows
    .map((row, index) => ({ row, index }))
    .sort((x, y) => {
      const a = x.row[sort.column];
      const b = y.row[sort.column];
      if (isEmpty(a) || isEmpty(b)) {
        if (isEmpty(a) && isEmpty(b)) return x.index - y.index;
        return isEmpty(a) ? 1 : -1;
      }
      const order = compare(a, b, column, titles);
      // NaN (a value that is not a number or a date): keep the order of the SQL.
      return Number.isNaN(order) || order === 0 ? x.index - y.index : sign * order;
    })
    .map(({ row }) => row);
}

/** "1 row", "1,234 rows". */
export function rowCountText(count: number): string {
  return count === 1 ? '1 row' : `${groupDigits(String(count))} rows`;
}

/** The note under a result that the report cut at its row limit. */
export function truncatedText(count: number): string {
  return `Showing the first ${groupDigits(String(count))} rows. Narrow the parameters to see the rest.`;
}
