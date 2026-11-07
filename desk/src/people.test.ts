import { describe, expect, it } from 'vitest';

import { initials } from './people';

describe('initials', () => {
  it('takes the first letters of the first two words, in capitals', () => {
    expect(initials('Ada Lovelace')).toBe('AL');
    expect(initials('grace brewster murray hopper')).toBe('GB');
    expect(initials('Ada')).toBe('A');
  });

  it('splits a user name at the usual separators', () => {
    expect(initials('e2e_manager')).toBe('EM');
    expect(initials('ada.lovelace')).toBe('AL');
    expect(initials('mary-jane')).toBe('MJ');
  });

  it('uses the part of an e-mail address before the @', () => {
    expect(initials('ada@example.com')).toBe('A');
    expect(initials('ada.lovelace@example.com')).toBe('AL');
  });

  it('handles letters beyond ASCII and empty names', () => {
    expect(initials('élodie durand')).toBe('ÉD');
    expect(initials('')).toBe('?');
    expect(initials('___')).toBe('?');
    expect(initials('@x')).toBe('?');
  });
});
