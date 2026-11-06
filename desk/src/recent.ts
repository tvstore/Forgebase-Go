// The documents opened in this tab, newest first, for the command bar. In memory only: nothing is
// written to browser storage (the eslint config forbids it), so a reload starts with none.

export interface RecentDocument {
  doctype: string;
  name: string;
  /** What to show: the document's title (its name when it has no title field). */
  title: string;
}

/** The most documents kept. */
export const MAX_RECENT = 10;

/** `entry` first; an older entry of the same document is dropped; at most [`MAX_RECENT`]. */
export function pushRecent(list: readonly RecentDocument[], entry: RecentDocument): RecentDocument[] {
  const rest = list.filter((r) => !(r.doctype === entry.doctype && r.name === entry.name));
  return [entry, ...rest].slice(0, MAX_RECENT);
}

let recent: RecentDocument[] = [];
const listeners = new Set<() => void>();

function changed(): void {
  for (const listener of listeners) listener();
}

/** Calls `listener` whenever the list changes (a form finishing its load after the bar opened); returns the unsubscribe. */
export function subscribeRecent(listener: () => void): () => void {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

/** Notes that a form was opened (or its title changed). */
export function recordRecent(entry: RecentDocument): void {
  recent = pushRecent(recent, entry);
  changed();
}

/** Forgets every document: called when the session ends, so the next user does not see them. */
export function clearRecent(): void {
  recent = [];
  changed();
}

/** The documents opened in this tab, newest first. The same array until the list changes. */
export function getRecent(): readonly RecentDocument[] {
  return recent;
}
