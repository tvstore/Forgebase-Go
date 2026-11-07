import { describe, expect, it } from 'vitest';

import type { AvailableAction, HistoryEntry } from './api';
import { actionLayout, actionsAreCurrent, HISTORY_CAP, HISTORY_SHOWN, historyView } from './workflow';

const action = (name: string): AvailableAction => ({ action: name, to: `${name} state` });

describe('actionLayout', () => {
  it('has no buttons for no actions', () => {
    expect(actionLayout([], false)).toEqual({ primary: null, secondary: null, more: [], blocked: false });
  });

  it('makes the first action primary', () => {
    const layout = actionLayout([action('Approve')], false);
    expect(layout.primary).toEqual(action('Approve'));
    expect(layout.secondary).toBeNull();
    expect(layout.more).toEqual([]);
  });

  it('shows two actions as buttons and puts the rest in the More menu, in order', () => {
    const layout = actionLayout([action('A'), action('B'), action('C'), action('D')], false);
    expect(layout.primary?.action).toBe('A');
    expect(layout.secondary?.action).toBe('B');
    expect(layout.more.map((a) => a.action)).toEqual(['C', 'D']);
  });

  it('blocks every action while the form has unsaved changes, and only then', () => {
    expect(actionLayout([action('A')], true).blocked).toBe(true);
    expect(actionLayout([action('A')], false).blocked).toBe(false);
  });
});

describe('actionsAreCurrent', () => {
  it('trusts actions only for the version they were fetched for', () => {
    expect(actionsAreCurrent(3, 3)).toBe(true);
    expect(actionsAreCurrent(2, 3)).toBe(false);
    expect(actionsAreCurrent(null, 3)).toBe(false);
  });
});

const entry = (n: number): HistoryEntry => ({
  action: `Action ${n}`,
  from: 'A',
  to: 'B',
  actor: 'ann',
  at: '2026-10-30T10:00:00Z',
});
const entries = (count: number): HistoryEntry[] => Array.from({ length: count }, (_, i) => entry(i + 1));

describe('historyView', () => {
  it('is empty for no entries', () => {
    expect(historyView([], false)).toEqual({ items: [], hidden: 0, capped: false });
  });

  it('puts the newest entry first (the API is oldest first) without changing its input', () => {
    const input = entries(3);
    expect(historyView(input, false).items.map((e) => e.action)).toEqual(['Action 3', 'Action 2', 'Action 1']);
    expect(input[0]?.action).toBe('Action 1');
  });

  it('shows the newest 20 and counts the rest', () => {
    const view = historyView(entries(25), false);
    expect(view.items).toHaveLength(HISTORY_SHOWN);
    expect(view.items[0]?.action).toBe('Action 25');
    expect(view.items.at(-1)?.action).toBe('Action 6');
    expect(view.hidden).toBe(5);
  });

  it('shows everything when expanded', () => {
    const view = historyView(entries(25), true);
    expect(view.items).toHaveLength(25);
    expect(view.hidden).toBe(0);
  });

  it('shows exactly 20 entries without a "Show all"', () => {
    expect(historyView(entries(20), false).hidden).toBe(0);
  });

  it('says so when the API returned its whole cap of 500', () => {
    expect(historyView(entries(HISTORY_CAP), true).capped).toBe(true);
    expect(historyView(entries(HISTORY_CAP - 1), true).capped).toBe(false);
  });
});
