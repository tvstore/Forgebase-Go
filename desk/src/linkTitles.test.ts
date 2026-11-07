import { describe, expect, it } from 'vitest';

import { linkDisplay, linkTitle, parseLinkTitles, titleLinkTarget } from './linkTitles';
import { field, meta } from './testing';

describe('parseLinkTitles', () => {
  it('reads the titles by target and name', () => {
    expect(parseLinkTitles({ customer: { 'C-1': 'Ada', 'C-2': 'Bob' } })).toEqual({
      customer: { 'C-1': 'Ada', 'C-2': 'Bob' },
    });
  });

  it('drops anything that is not a non-empty string title, and empty targets', () => {
    expect(parseLinkTitles({ a: { x: 1, y: null, z: '', ok: 'T' }, b: { x: 1 }, c: 'no', d: ['x'], e: null })).toEqual({
      a: { ok: 'T' },
    });
  });

  it('gives no titles for a response without them or with the wrong shape', () => {
    expect(parseLinkTitles(undefined)).toEqual({});
    expect(parseLinkTitles(null)).toEqual({});
    expect(parseLinkTitles([])).toEqual({});
    expect(parseLinkTitles('x')).toEqual({});
  });
});

describe('linkTitle and linkDisplay', () => {
  const titles = { customer: { 'C-1': 'Ada' } };

  it('finds the title of a name of a target', () => {
    expect(linkTitle(titles, 'customer', 'C-1')).toBe('Ada');
    expect(linkTitle(titles, 'customer', 'C-9')).toBeUndefined();
    expect(linkTitle(titles, 'other', 'C-1')).toBeUndefined();
    expect(linkTitle(titles, undefined, 'C-1')).toBeUndefined();
    expect(linkTitle(titles, 'customer', null)).toBeUndefined();
  });

  it('shows the name when there is no title', () => {
    expect(linkDisplay(titles, 'customer', 'C-1')).toBe('Ada');
    expect(linkDisplay(titles, 'customer', 'C-9')).toBe('C-9');
  });

  it('is safe for a name that is an object property', () => {
    expect(linkTitle({ customer: {} }, 'customer', 'constructor')).toBeUndefined();
  });
});

describe('titleLinkTarget', () => {
  it('is the target when the title field is a Link, else undefined', () => {
    const fields = [field('customer', 'link', { target: 'customer' }), field('a', 'data')];
    expect(titleLinkTarget(meta(fields, { title_field: 'customer' }))).toBe('customer');
    expect(titleLinkTarget(meta(fields, { title_field: 'a' }))).toBeUndefined();
    expect(titleLinkTarget(meta(fields))).toBeUndefined();
  });
});
