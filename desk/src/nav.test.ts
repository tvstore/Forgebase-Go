import { describe, expect, it } from 'vitest';

import { OTHER_APP, appLabel, breadcrumbs, groupByApp } from './nav';
import { parseHash } from './router';
import { rights } from './testing';
import type { DocTypeSummary } from './types';

const d = (name: string, label: string, app: string | null): DocTypeSummary => ({
  name,
  label,
  app,
  icon: null,
  submittable: false,
  rights: rights({ read: 'all' }),
});

describe('groupByApp', () => {
  it('sorts apps A to Z with Other last, and DocTypes by label', () => {
    const groups = groupByApp([
      d('z', 'Zebra', null),
      d('b', 'Order', 'sales'),
      d('a', 'customer', 'sales'),
      d('c', 'Ticket', 'crm'),
      d('e', 'Empty app', ''),
    ]);
    expect(groups.map((g) => g.app)).toEqual(['crm', 'sales', OTHER_APP]);
    expect(groups.map((g) => g.label)).toEqual(['Crm', 'Sales', OTHER_APP]);
    expect(groups[1]?.doctypes.map((x) => x.name)).toEqual(['a', 'b']);
    expect(groups[2]?.doctypes.map((x) => x.name)).toEqual(['e', 'z']);
  });

  it('is empty for no DocTypes', () => {
    expect(groupByApp([])).toEqual([]);
  });
});

describe('appLabel', () => {
  it('humanizes the app name; no app is "Other"', () => {
    expect(appLabel('local')).toBe('Local');
    expect(appLabel('sales_extension')).toBe('Sales Extension');
    expect(appLabel('my-app')).toBe('My App');
    expect(appLabel('crm')).toBe('Crm');
    expect(appLabel(null)).toBe(OTHER_APP);
    expect(appLabel('')).toBe(OTHER_APP);
  });
});

describe('breadcrumbs', () => {
  const list = [d('sales_order', 'Sales Order', 'sales'), d('thing', 'Thing', null)];

  it('is App / DocType / Document for a form, with links but the last', () => {
    expect(breadcrumbs(parseHash('#/form/sales_order/SO-1'), list)).toEqual([
      { label: 'Sales', href: '#/' },
      { label: 'Sales Order', href: '#/list/sales_order' },
      { label: 'SO-1' },
    ]);
  });

  it('ends at the DocType for a list and at New for a new document', () => {
    expect(breadcrumbs(parseHash('#/list/sales_order'), list)).toEqual([
      { label: 'Sales', href: '#/' },
      { label: 'Sales Order' },
    ]);
    expect(breadcrumbs(parseHash('#/new/thing'), list).map((c) => c.label)).toEqual([OTHER_APP, 'Thing', 'New']);
  });

  it('is App / DocType / Import for the import page', () => {
    expect(breadcrumbs(parseHash('#/import/sales_order'), list)).toEqual([
      { label: 'Sales', href: '#/' },
      { label: 'Sales Order', href: '#/list/sales_order' },
      { label: 'Import' },
    ]);
  });

  it('is Reports / the report for a report, with its name until the reports are known', () => {
    const reports = [{ name: 'sales.by_customer', label: 'Sales by customer' }];
    expect(breadcrumbs(parseHash('#/report/sales.by_customer?from=2026-10-01'), list, reports)).toEqual([
      { label: 'Reports', href: '#/' },
      { label: 'Sales by customer' },
    ]);
    expect(breadcrumbs(parseHash('#/report/sales.by_customer'), list)).toEqual([
      { label: 'Reports', href: '#/' },
      { label: 'sales.by_customer' },
    ]);
  });

  it('shows the DocType name before the DocTypes are known, and nothing on other pages', () => {
    expect(breadcrumbs(parseHash('#/list/sales_order'), [])).toEqual([{ label: 'sales_order' }]);
    expect(breadcrumbs(parseHash('#/'), list)).toEqual([]);
    expect(breadcrumbs(parseHash('#/nope/x/y/z'), list)).toEqual([]);
  });
});
