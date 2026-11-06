import { describe, expect, it } from 'vitest';

import { shownValue } from './display';
import { field } from './testing';

describe('shownValue', () => {
  it('shows a dash, muted, when there is no value', () => {
    for (const type of ['data', 'int', 'decimal', 'date', 'datetime', 'select', 'link', 'text'] as const) {
      expect(shownValue(field('f', type), '')).toMatchObject({ text: '—', empty: true });
    }
  });

  it('right-aligns the dash of an empty number too, so that it sits under its header', () => {
    expect(shownValue(field('f', 'int'), '').numeric).toBe(true);
    expect(shownValue(field('f', 'data'), '').numeric).toBe(false);
  });

  it('shows a yes/no field as Yes or No, never empty', () => {
    expect(shownValue(field('f', 'check'), true).text).toBe('Yes');
    expect(shownValue(field('f', 'check'), false)).toMatchObject({ text: 'No', empty: false });
  });

  it('groups the digits of numbers and right-aligns them', () => {
    expect(shownValue(field('f', 'int'), '1234567')).toMatchObject({ text: '1,234,567', numeric: true });
    expect(shownValue(field('f', 'decimal'), '1250.00')).toMatchObject({ text: '1,250.00', numeric: true });
    expect(shownValue(field('f', 'decimal'), '-9.50').text).toBe('-9.50');
  });

  it('writes dates and times out', () => {
    expect(shownValue(field('f', 'date'), '2026-10-29').text).toBe('29 Oct 2026');
    expect(shownValue(field('f', 'datetime'), '2026-10-29T15:04:05').text).toBe('29 Oct 2026, 15:04');
  });

  it('keeps the line breaks of a Text field', () => {
    expect(shownValue(field('f', 'text'), 'a\nb')).toMatchObject({ text: 'a\nb', multiline: true });
  });

  it('shows text and select values as they are', () => {
    expect(shownValue(field('f', 'data'), 'Ada')).toMatchObject({ text: 'Ada', numeric: false, multiline: false });
    expect(shownValue(field('f', 'select'), 'gold').text).toBe('gold');
  });

  it('shows a Link by its title with the key beside it, or by the name when there is no title', () => {
    const link = field('customer', 'link', { target: 'customer' });
    const titles = { customer: { 'C-1': 'Ada Lovelace' } };
    expect(shownValue(link, 'C-1', titles)).toMatchObject({ text: 'Ada Lovelace', key: 'C-1' });
    const plain = shownValue(link, 'C-2', titles);
    expect(plain.text).toBe('C-2');
    expect(plain.key).toBeUndefined();
    expect(shownValue(link, 'C-1').text).toBe('C-1');
  });
});
