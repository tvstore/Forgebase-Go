import { describe, expect, it } from 'vitest';

import { cx } from './cx';

describe('cx', () => {
  it('joins the class names that are set', () => {
    expect(cx('btn', 'btn-primary')).toBe('btn btn-primary');
  });

  it('skips false, null, undefined and empty strings', () => {
    expect(cx('btn', false, null, undefined, '', 'btn-small')).toBe('btn btn-small');
  });

  it('returns an empty string for nothing', () => {
    expect(cx()).toBe('');
  });
});
