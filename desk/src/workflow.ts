// Pure rules of the workflow in the form (M8.2): how the available actions are laid out in the
// header and how the history is cut for the Activity card. The server decides every action
// (`GET .../transitions`); this only decides how they are shown.

import type { AvailableAction, HistoryEntry } from './api';

/** How many actions are buttons in the header; the rest go to the More menu. */
export const VISIBLE_ACTIONS = 2;

export interface ActionLayout {
  /** The first action: the navy button, unless the form has unsaved changes. */
  primary: AvailableAction | null;
  /** The second action: a secondary button. */
  secondary: AvailableAction | null;
  /** The third and later actions, for the More menu. */
  more: AvailableAction[];
  /**
   * Unsaved changes: the buttons are shown but disabled ("Save first"), and Save is the primary
   * action. A transition saves nothing, so it must never run over edits that would be lost.
   */
  blocked: boolean;
}

export function actionLayout(actions: AvailableAction[], dirty: boolean): ActionLayout {
  return {
    primary: actions[0] ?? null,
    secondary: actions[1] ?? null,
    more: actions.slice(VISIBLE_ACTIONS),
    blocked: dirty,
  };
}

/**
 * The actions were fetched for one version of the document. After a transition, save or reload the
 * document has a new version and the old actions may no longer apply (an action name can repeat in
 * a multi-level approval), so they must not be used until the new ones arrive.
 */
export function actionsAreCurrent(fetchedForVersion: number | null, documentVersion: number): boolean {
  return fetchedForVersion !== null && fetchedForVersion === documentVersion;
}

/** The history entries shown first, before "Show all". */
export const HISTORY_SHOWN = 20;

/** The newest entries the API keeps and returns. */
export const HISTORY_CAP = 500;

export interface HistoryView {
  /** Newest first. */
  items: HistoryEntry[];
  /** How many entries "Show all" would add. */
  hidden: number;
  /** The API returned its whole cap, so older transitions exist that are not listed. */
  capped: boolean;
}

/** The API returns oldest first; the card shows the newest at the top, the first 20 unless expanded. */
export function historyView(entries: HistoryEntry[], expanded: boolean): HistoryView {
  const newestFirst = [...entries].reverse();
  const items = expanded ? newestFirst : newestFirst.slice(0, HISTORY_SHOWN);
  return { items, hidden: newestFirst.length - items.length, capped: entries.length >= HISTORY_CAP };
}
