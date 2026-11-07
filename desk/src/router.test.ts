import { describe, expect, it } from 'vitest';

import { formHref, importHref, listHref, newHref, parseHash, reportHref } from './router';

describe('parseHash', () => {
  it('parses the home and login routes', () => {
    for (const hash of ['', '#', '#/', '/']) expect(parseHash(hash)).toEqual({ name: 'home' });
    expect(parseHash('#/login')).toEqual({ name: 'login' });
  });

  it('parses a list with its query', () => {
    const route = parseHash('#/list/customer?offset=20&sort=name%3Adesc');
    expect(route.name).toBe('list');
    if (route.name !== 'list') return;
    expect(route.doctype).toBe('customer');
    expect(route.params.get('offset')).toBe('20');
    expect(route.params.get('sort')).toBe('name:desc');
  });

  it('parses a list without a query and with an encoded name', () => {
    const route = parseHash('#/list/sales%20order');
    expect(route.name === 'list' && route.doctype).toBe('sales order');
    if (route.name === 'list') expect([...route.params]).toEqual([]);
  });

  it('keeps a question mark inside the query', () => {
    const route = parseHash('#/list/a?filters=%5B%5D&x=1?2');
    if (route.name !== 'list') throw new Error('not a list');
    expect(route.params.get('x')).toBe('1?2');
  });

  it('parses new and form routes', () => {
    expect(parseHash('#/new/customer')).toEqual({ name: 'new', doctype: 'customer' });
    expect(parseHash('#/form/customer/ann%40example.com')).toEqual({
      name: 'form',
      doctype: 'customer',
      docName: 'ann@example.com',
    });
    // A name with a slash survives the round trip.
    expect(parseHash(formHref('note', 'a/b'))).toEqual({ name: 'form', doctype: 'note', docName: 'a/b' });
  });

  it('parses the import page of a DocType', () => {
    expect(parseHash('#/import/customer')).toEqual({ name: 'import', doctype: 'customer' });
    expect(parseHash(importHref('sales order'))).toEqual({ name: 'import', doctype: 'sales order' });
    expect(importHref('a b')).toBe('#/import/a%20b');
  });

  it('parses a report with its parameters', () => {
    const route = parseHash('#/report/sales.by_customer?from=2026-10-01&customer=A%20B');
    expect(route.name).toBe('report');
    if (route.name !== 'report') return;
    expect(route.report).toBe('sales.by_customer');
    expect(route.params.get('from')).toBe('2026-10-01');
    expect(route.params.get('customer')).toBe('A B');
    const bare = parseHash('#/report/sales.by_customer');
    expect(bare.name === 'report' && [...bare.params]).toEqual([]);
  });

  it('gives notfound for anything else', () => {
    for (const hash of [
      '#/nothing',
      '#nothing',
      '#/list',
      '#/list/',
      '#/list/a/b',
      '#/form/a',
      '#/form/a/b/c',
      '#/new',
      '#/import',
      '#/import/',
      '#/import/a/b',
      '#/import/%E0%A4%A',
      '#/login/x',
      '#/list/%E0%A4%A',
      '#/form/a/%',
      '#/report',
      '#/report/',
      '#/report/a.b/c',
      '#/report/%E0%A4%A',
    ]) {
      expect(parseHash(hash), hash).toEqual({ name: 'notfound' });
    }
  });
});

describe('hrefs', () => {
  it('encode names and round-trip through parseHash', () => {
    expect(listHref('customer')).toBe('#/list/customer');
    expect(listHref('a b', new URLSearchParams({ sort: 'x:desc' }))).toBe('#/list/a%20b?sort=x%3Adesc');
    expect(listHref('x', new URLSearchParams())).toBe('#/list/x');
    expect(newHref('a/b')).toBe('#/new/a%2Fb');
    expect(parseHash(newHref('a/b'))).toEqual({ name: 'new', doctype: 'a/b' });
    const route = parseHash(listHref('a b', new URLSearchParams({ filters: '[{"field":"x"}]' })));
    if (route.name !== 'list') throw new Error('not a list');
    expect(route.params.get('filters')).toBe('[{"field":"x"}]');
  });

  it('builds a report address that round-trips', () => {
    expect(reportHref('sales.by_customer')).toBe('#/report/sales.by_customer');
    expect(reportHref('sales.x', new URLSearchParams())).toBe('#/report/sales.x');
    const route = parseHash(reportHref('sales.x', new URLSearchParams({ q: 'a&b=c' })));
    expect(route.name === 'report' && route.params.get('q')).toBe('a&b=c');
  });
});
