import { describe, expect, it } from 'vitest';

import { SUGGESTION_LIMIT, escapeLike, suggestionParams } from './links';

describe('Link suggestions', () => {
  it('asks for the names that start with the typed text, sorted, with a small limit', () => {
    const params = suggestionParams('ann');
    expect(params.limit).toBe(SUGGESTION_LIMIT);
    expect(params.limit).toBeLessThanOrEqual(10);
    expect(params.offset).toBe(0);
    expect(params.sort).toBe('name');
    expect(JSON.parse(params.filters ?? 'null')).toEqual([{ field: 'name', op: 'like', value: 'ann%' }]);
  });

  it('asks for the first names when nothing is typed', () => {
    expect(suggestionParams('').filters).toBeUndefined();
  });

  it('matches LIKE metacharacters literally', () => {
    expect(escapeLike('50%_off\\')).toBe('50\\%\\_off\\\\');
    expect(JSON.parse(suggestionParams('a_b%').filters ?? '[]')).toEqual([
      { field: 'name', op: 'like', value: 'a\\_b\\%%' },
    ]);
  });
});
