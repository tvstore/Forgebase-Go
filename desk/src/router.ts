// A tiny hash router: `#/login`, `#/`, `#/list/<doctype>?...`, `#/new/<doctype>`,
// `#/form/<doctype>/<name>`, `#/import/<doctype>` and `#/report/<app>.<name>?...`. State that belongs in the address (paging, sort, filters) is in the
// query part, so the back button and copied links work.

import { useSyncExternalStore } from 'react';

export type Route =
  | { name: 'login' }
  | { name: 'home' }
  | { name: 'list'; doctype: string; params: URLSearchParams }
  | { name: 'new'; doctype: string }
  | { name: 'form'; doctype: string; docName: string }
  /** The import page of a DocType (M9.3): upload, validate, import. */
  | { name: 'import'; doctype: string }
  /** A query report (M8.4): `report` is `<app>.<name>`, the query holds the parameters. */
  | { name: 'report'; report: string; params: URLSearchParams }
  | { name: 'notfound' };

function decode(segment: string): string | null {
  try {
    const value = decodeURIComponent(segment);
    return value === '' ? null : value;
  } catch {
    return null; // malformed percent-encoding
  }
}

/** Parses `location.hash` (with or without the leading `#`). */
export function parseHash(hash: string): Route {
  const text = hash.startsWith('#') ? hash.slice(1) : hash;
  if (text === '') return { name: 'home' };
  const [path = '', ...rest] = text.split('?');
  const query = rest.join('?');
  const parts = path.split('/');
  // A path starts with "/": parts[0] is "".
  if (parts[0] !== '') return { name: 'notfound' };
  const segments = parts.slice(1);
  if (segments.length === 1 && segments[0] === '') return { name: 'home' };
  const [kind, first, second, ...more] = segments;
  if (more.length > 0) return { name: 'notfound' };
  if (kind === 'login' && first === undefined) return { name: 'login' };
  if (kind === 'list' && first !== undefined && second === undefined) {
    const doctype = decode(first);
    return doctype === null
      ? { name: 'notfound' }
      : { name: 'list', doctype, params: new URLSearchParams(query) };
  }
  if (kind === 'report' && first !== undefined && second === undefined) {
    const report = decode(first);
    return report === null
      ? { name: 'notfound' }
      : { name: 'report', report, params: new URLSearchParams(query) };
  }
  if (kind === 'new' && first !== undefined && second === undefined) {
    const doctype = decode(first);
    return doctype === null ? { name: 'notfound' } : { name: 'new', doctype };
  }
  if (kind === 'import' && first !== undefined && second === undefined) {
    const doctype = decode(first);
    return doctype === null ? { name: 'notfound' } : { name: 'import', doctype };
  }
  if (kind === 'form' && first !== undefined && second !== undefined) {
    const doctype = decode(first);
    const docName = decode(second);
    return doctype === null || docName === null
      ? { name: 'notfound' }
      : { name: 'form', doctype, docName };
  }
  return { name: 'notfound' };
}

export const loginHref = (): string => '#/login';
export const homeHref = (): string => '#/';
export const newHref = (doctype: string): string => `#/new/${encodeURIComponent(doctype)}`;
export const importHref = (doctype: string): string => `#/import/${encodeURIComponent(doctype)}`;
export const formHref = (doctype: string, name: string): string =>
  `#/form/${encodeURIComponent(doctype)}/${encodeURIComponent(name)}`;

export function listHref(doctype: string, params?: URLSearchParams): string {
  const query = params?.toString() ?? '';
  return `#/list/${encodeURIComponent(doctype)}${query === '' ? '' : `?${query}`}`;
}

export function reportHref(report: string, params?: URLSearchParams): string {
  const query = params?.toString() ?? '';
  return `#/report/${encodeURIComponent(report)}${query === '' ? '' : `?${query}`}`;
}

/** Goes to `href` (a `#/...` address); `replace` does not add a history entry (redirects). */
export function navigate(href: string, replace = false): void {
  if (replace) window.location.replace(href);
  else window.location.hash = href;
  // The browser reports the change in a `hashchange` event of a later task. Follow it now, in the
  // same task: a controlled input (the Columns menu's checkbox) that is clicked would otherwise
  // be put back to its old state when the click handler ends, and show the change a moment later
  // (or never to a script that looks right after the click). The later event finds the address
  // already shown and does nothing.
  onHashChange();
}

// The hash the app shows. It follows `hashchange`, except while a form with unsaved changes has
// installed a blocker: the blocker is told the address the user went to, the address bar is put
// back, and the form asks in the page whether to leave (then it navigates there itself).
// Known limit: a browser Back or Forward blocked this way leaves the history entry it moved to
// pointing at the form (the entries are not rearranged), so a second Back may land on the form.
let shown: string | null = null;
let attached = false;
let blocker: ((next: string) => boolean) | null = null;
const listeners = new Set<() => void>();

function onHashChange(): void {
  const next = window.location.hash;
  if (next === shown) return;
  if (blocker?.(next)) {
    window.history.replaceState(window.history.state, '', shown ?? '#/');
    return;
  }
  shown = next;
  listeners.forEach((listener) => listener());
}

/**
 * While set, a change of the address is offered to `block(nextHash)` first; when it returns true
 * the change is undone and nothing else happens. Pass null to remove it.
 */
export function setNavigationBlocker(block: ((next: string) => boolean) | null): void {
  blocker = block;
}

function subscribe(callback: () => void): () => void {
  if (!attached) {
    shown = window.location.hash;
    window.addEventListener('hashchange', onHashChange);
    attached = true;
  }
  listeners.add(callback);
  return () => {
    listeners.delete(callback);
  };
}

const snapshot = (): string => shown ?? window.location.hash;

/** The hash of the address bar as a string (a primitive, so React can compare snapshots). */
export function useHash(): string {
  return useSyncExternalStore(subscribe, snapshot);
}
