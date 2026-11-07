// The suggestions of a Link field: a small, sorted list query on the target DocType.

import type { ListParams } from './api';

/** How many suggestions are asked for. */
export const SUGGESTION_LIMIT = 10;

/** Escapes `\`, `%` and `_` so that the typed text matches literally in a `like` pattern. */
export function escapeLike(text: string): string {
  return text.replace(/[\\%_]/g, (c) => `\\${c}`);
}

/**
 * The list query for the names that start with `typed` (everything when nothing is typed): the
 * filter format of `GET /api/v1/resource/{doctype}` (`filters`, an array of `{field, op, value}`),
 * sorted by name, with a small limit. The user may still type any name; the server decides.
 */
export function suggestionParams(typed: string): ListParams {
  return {
    filters:
      typed === ''
        ? undefined
        : JSON.stringify([{ field: 'name', op: 'like', value: `${escapeLike(typed)}%` }]),
    sort: 'name',
    limit: SUGGESTION_LIMIT,
    offset: 0,
  };
}
