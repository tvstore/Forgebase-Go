// Link titles (UX.4): `?link_titles=1` makes the API add `link_titles` next to `data`:
// `{"customer": {"CUST-0001": "Ada Lovelace"}}`, by target DocType and then by name. A target the
// user cannot read, or one without a title, has no entry: the desk then shows the plain name.

import type { DocTypeMeta } from './types';

export type LinkTitles = Record<string, Record<string, string>>;

export const NO_TITLES: LinkTitles = {};

/** Reads the `link_titles` of a response defensively: anything that is not a string title is dropped. */
export function parseLinkTitles(raw: unknown): LinkTitles {
  if (typeof raw !== 'object' || raw === null || Array.isArray(raw)) return NO_TITLES;
  const titles: LinkTitles = {};
  for (const [target, names] of Object.entries(raw)) {
    if (typeof names !== 'object' || names === null || Array.isArray(names)) continue;
    const entries = Object.entries(names).filter(
      (entry): entry is [string, string] => typeof entry[1] === 'string' && entry[1] !== '',
    );
    if (entries.length > 0) titles[target] = Object.fromEntries(entries);
  }
  return titles;
}

/** The title of document `name` of DocType `target`, or undefined. */
export function linkTitle(titles: LinkTitles, target: string | undefined, name: unknown): string | undefined {
  if (target === undefined || typeof name !== 'string') return undefined;
  const names = titles[target];
  // Own properties only: a name such as "constructor" must not find something on the prototype.
  const title = names !== undefined && Object.hasOwn(names, name) ? names[name] : undefined;
  return typeof title === 'string' && title !== '' ? title : undefined;
}

/**
 * The text to show for a Link value: its title when there is one, else the name itself (the same
 * as before link titles existed).
 */
export function linkDisplay(titles: LinkTitles, target: string | undefined, name: string): string {
  return linkTitle(titles, target, name) ?? name;
}

/** The target DocType when the title field of `meta` is a Link field, else undefined. */
export function titleLinkTarget(meta: DocTypeMeta): string | undefined {
  const field = meta.title_field === null ? undefined : meta.fields.find((f) => f.name === meta.title_field);
  return field?.type === 'link' ? field.target : undefined;
}
