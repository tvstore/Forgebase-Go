// How a read-only or locked field is shown: as plain text, not as a disabled input. A pure function
// of the field and its form value, so the choices (a dash for nothing, "Yes"/"No", a Link's title
// with its key) are tested without a DOM.

import type { FormValue } from './fields';
import { formatDate, groupDigits } from './listing';
import { linkTitle } from './linkTitles';
import type { LinkTitles } from './linkTitles';
import type { FieldMeta } from './types';

/** What to draw for a read-only value. */
export interface Shown {
  /** The text; an em dash when there is no value. */
  text: string;
  /** There is no value (drawn muted). */
  empty: boolean;
  /** Keeps the line breaks (a Text field). */
  multiline: boolean;
  /** Numbers line up on the right. */
  numeric: boolean;
  /** A Link whose title is shown: its key, drawn muted after the title. */
  key?: string;
}

const NOTHING = '—';

/** `2026-10-29T15:04:05` (the value of a `datetime-local` control) as `29 Oct 2026, 15:04`. */
function formatLocal(value: string): string {
  const match = /^(\d{4}-\d{2}-\d{2})T(\d{2}:\d{2})/.exec(value);
  return match ? `${formatDate(match[1] ?? '')}, ${match[2]}` : value;
}

export function shownValue(field: FieldMeta, value: FormValue, titles: LinkTitles = {}): Shown {
  // Numbers line up on the right, also when there is none (the dash sits under the header).
  const base = { empty: false, multiline: false, numeric: field.type === 'int' || field.type === 'decimal' };
  if (field.type === 'check') return { ...base, text: value === true ? 'Yes' : 'No' };
  const text = typeof value === 'string' ? value : '';
  if (text === '') return { ...base, text: NOTHING, empty: true };
  switch (field.type) {
    case 'int':
    case 'decimal':
      return { ...base, text: groupDigits(text) };
    case 'date':
      return { ...base, text: formatDate(text) };
    case 'datetime':
      return { ...base, text: formatLocal(text) };
    case 'text':
      return { ...base, text, multiline: true };
    case 'link': {
      const title = linkTitle(titles, field.target, text);
      return title === undefined ? { ...base, text } : { ...base, text: title, key: text };
    }
    default:
      return { ...base, text };
  }
}
