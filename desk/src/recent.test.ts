import { describe, expect, it, vi } from 'vitest';

import { MAX_RECENT, clearRecent, getRecent, pushRecent, recordRecent, subscribeRecent } from './recent';
import type { RecentDocument } from './recent';

const doc = (name: string, doctype = 'order'): RecentDocument => ({ doctype, name, title: `Title ${name}` });

describe('pushRecent', () => {
  it('puts the newest first', () => {
    expect(pushRecent([doc('A')], doc('B')).map((d) => d.name)).toEqual(['B', 'A']);
  });

  it('moves a document that is opened again to the front instead of listing it twice', () => {
    const list = [doc('C'), doc('B'), doc('A')];
    expect(pushRecent(list, doc('A')).map((d) => d.name)).toEqual(['A', 'C', 'B']);
  });

  it('keeps the new title of a document that is opened again', () => {
    const next = pushRecent([doc('A')], { doctype: 'order', name: 'A', title: 'New title' });
    expect(next).toEqual([{ doctype: 'order', name: 'A', title: 'New title' }]);
  });

  it('tells documents of two DocTypes with the same name apart', () => {
    expect(pushRecent([doc('A', 'order')], doc('A', 'invoice'))).toHaveLength(2);
  });

  it('keeps at most ten, dropping the oldest', () => {
    let list: RecentDocument[] = [];
    for (let i = 1; i <= 12; i++) list = pushRecent(list, doc(`D${i}`));
    expect(list).toHaveLength(MAX_RECENT);
    expect(list[0]?.name).toBe('D12');
    expect(list.at(-1)?.name).toBe('D3');
  });

  it('does not change the list it is given', () => {
    const list = [doc('A')];
    pushRecent(list, doc('B'));
    expect(list).toHaveLength(1);
  });
});

describe('the in-memory list', () => {
  it('records the documents opened, newest first', () => {
    recordRecent(doc('X1'));
    recordRecent(doc('X2'));
    expect(getRecent().slice(0, 2).map((d) => d.name)).toEqual(['X2', 'X1']);
  });
});

describe('clearRecent', () => {
  it('forgets every recorded document', () => {
    recordRecent(doc('A'));
    expect(getRecent().length).toBeGreaterThan(0);
    clearRecent();
    expect(getRecent()).toEqual([]);
  });
});

describe('subscribeRecent', () => {
  it('tells a subscriber about a record and a clear, and gives a new array each time', () => {
    clearRecent();
    const listener = vi.fn();
    const unsubscribe = subscribeRecent(listener);
    const before = getRecent();
    recordRecent(doc('S1'));
    expect(listener).toHaveBeenCalledTimes(1);
    expect(getRecent()).not.toBe(before);
    clearRecent();
    expect(listener).toHaveBeenCalledTimes(2);
    unsubscribe();
    recordRecent(doc('S2'));
    expect(listener).toHaveBeenCalledTimes(2);
    clearRecent();
  });
});
