import { describe, expect, it } from 'vitest';

import { MAX_RESULTS, findCommands, flatten, moveActive } from './command';
import type { RecentDocument } from './recent';
import { rights } from './testing';
import type { DocTypeSummary } from './types';

const doctype = (name: string, label: string, create = false): DocTypeSummary => ({
  name,
  label,
  app: null,
  icon: null,
  submittable: false,
  rights: rights({ read: 'all', create: create ? 'all' : 'none' }),
});

const doctypes = [
  doctype('sales_order', 'Sales Order', true),
  doctype('customer', 'Customer', true),
  doctype('report', 'Report'), // read only: no "New"
];
const recent: RecentDocument[] = [
  { doctype: 'sales_order', name: 'SO-2026-00002', title: 'Ada Lovelace' },
  { doctype: 'customer', name: 'grace@x.y', title: 'Grace Hopper' },
];

const labels = (input: string, list = doctypes, recents = recent) =>
  findCommands(input, list, recents).map((g) => [g.title, g.commands.map((c) => c.label)]);

describe('findCommands', () => {
  it('lists everything for an empty input: recent, then DocTypes, then Create', () => {
    expect(labels('')).toEqual([
      ['Recent', ['Ada Lovelace', 'Grace Hopper']],
      ['DocTypes', ['Go to Sales Order', 'Go to Customer', 'Go to Report']],
      ['Create', ['New Sales Order', 'New Customer']],
    ]);
  });

  it('matches a case-insensitive substring of the DocType label', () => {
    expect(labels('SALES')).toEqual([
      ['DocTypes', ['Go to Sales Order']],
      ['Create', ['New Sales Order']],
    ]);
    expect(labels('  der ')).toEqual([
      ['DocTypes', ['Go to Sales Order']],
      ['Create', ['New Sales Order']],
    ]);
  });

  it('matches the DocType name too', () => {
    expect(labels('sales_o')[0]).toEqual(['DocTypes', ['Go to Sales Order']]);
  });

  it('matches a recent document by its title or its name', () => {
    expect(labels('lovel')).toEqual([['Recent', ['Ada Lovelace']]]);
    expect(labels('so-2026')).toEqual([['Recent', ['Ada Lovelace']]]);
    expect(labels('GRACE@')).toEqual([['Recent', ['Grace Hopper']]]);
  });

  it('offers New only where the user may create', () => {
    const create = findCommands('report', doctypes, []).find((g) => g.title === 'Create');
    expect(create).toBeUndefined();
    expect(labels('report')).toEqual([['DocTypes', ['Go to Report']]]);
  });

  it('gives each result its address and the DocType of a recent document as a hint', () => {
    const [recentGroup, goGroup, newGroup] = findCommands('', doctypes, recent);
    expect(recentGroup?.commands[0]).toMatchObject({
      kind: 'recent',
      hint: 'Sales Order',
      href: '#/form/sales_order/SO-2026-00002',
    });
    expect(goGroup?.commands[0]).toMatchObject({ kind: 'doctype', href: '#/list/sales_order' });
    expect(newGroup?.commands[0]).toMatchObject({ kind: 'create', href: '#/new/sales_order' });
  });

  it('leaves out a recent document of a DocType the user can no longer read', () => {
    const only = [doctype('customer', 'Customer')];
    expect(findCommands('', only, recent).flatMap((g) => g.commands.map((c) => c.label))).not.toContain('Ada Lovelace');
  });

  it('has unique ids', () => {
    const ids = flatten(findCommands('', doctypes, recent)).map((c) => c.id);
    expect(new Set(ids).size).toBe(ids.length);
  });

  it('says nothing found as no groups', () => {
    expect(findCommands('zzz', doctypes, recent)).toEqual([]);
    expect(findCommands('', [], [])).toEqual([]);
  });

  it('caps the list at 20 results, keeping the groups in order', () => {
    const many = Array.from({ length: 30 }, (_, i) => doctype(`dt_${i}`, `Type ${i}`, true));
    const manyRecent = Array.from({ length: 10 }, (_, i) => ({ doctype: 'dt_0', name: `N${i}`, title: `T${i}` }));
    const groups = findCommands('', many, manyRecent);
    expect(flatten(groups)).toHaveLength(MAX_RESULTS);
    // 10 recent, then 10 of the DocTypes; the Create group no longer fits.
    expect(groups.map((g) => [g.title, g.commands.length])).toEqual([
      ['Recent', 10],
      ['DocTypes', 10],
    ]);
  });
});

describe('findCommands with reports', () => {
  const reports = [
    { name: 'sales.by_customer', label: 'Sales by customer' },
    { name: 'sales.aging', label: 'Receivables aging' },
  ];
  const run = (input: string, list = reports) =>
    findCommands(input, doctypes, [], list).find((g) => g.title === 'Reports')?.commands.map((c) => c.label);

  it('adds a Reports group with "Run <report>", last', () => {
    const groups = findCommands('', doctypes, recent, reports);
    expect(groups.map((g) => g.title)).toEqual(['Recent', 'DocTypes', 'Create', 'Reports']);
    expect(run('')).toEqual(['Run Sales by customer', 'Run Receivables aging']);
  });

  it('matches the label and the name, ignoring case', () => {
    expect(run('AGING')).toEqual(['Run Receivables aging']);
    expect(run('sales.by')).toEqual(['Run Sales by customer']);
    expect(run('customer')).toEqual(['Run Sales by customer']);
    expect(run('zzz')).toBeUndefined();
  });

  it('gives the report address and a unique id', () => {
    const command = findCommands('aging', doctypes, [], reports).flatMap((g) => g.commands)[0];
    expect(command).toMatchObject({ kind: 'report', href: '#/report/sales.aging', id: 'report/sales.aging' });
    const ids = flatten(findCommands('', doctypes, recent, reports)).map((c) => c.id);
    expect(new Set(ids).size).toBe(ids.length);
  });

  it('has no group when the user may run no report', () => {
    expect(run('', [])).toBeUndefined();
    expect(findCommands('', doctypes, recent).map((g) => g.title)).not.toContain('Reports');
  });

  it('is cut at the cap like the other groups', () => {
    const many = Array.from({ length: 30 }, (_, i) => ({ name: `a.r${i}`, label: `Report ${i}` }));
    const groups = findCommands('report ', [], [], many);
    expect(flatten(groups)).toHaveLength(MAX_RESULTS);
  });
});

describe('moveActive', () => {
  it('moves down and up and wraps around', () => {
    expect(moveActive(0, 1, 3)).toBe(1);
    expect(moveActive(2, 1, 3)).toBe(0);
    expect(moveActive(0, -1, 3)).toBe(2);
    expect(moveActive(2, -1, 3)).toBe(1);
  });

  it('starts at the first (down) or the last (up) when nothing is active', () => {
    expect(moveActive(-1, 1, 3)).toBe(0);
    expect(moveActive(-1, -1, 3)).toBe(2);
  });

  it('has no active item when there are no results', () => {
    expect(moveActive(0, 1, 0)).toBe(-1);
  });
});
