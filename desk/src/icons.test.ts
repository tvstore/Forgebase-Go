import { describe, expect, it } from 'vitest';

import { DEFAULT_DOCTYPE_ICON, DOCTYPE_ICONS } from './icons';
// The one list of the icon names a DocType may use: Rust validates DocType files against it. It is
// read as text from the Rust crate (a path relative to desk/src), so there is no second copy.
import list from '../../internal/core/doctype_icons.txt?raw';

const allowed = list
  .split('\n')
  .map((line) => line.trim())
  .filter((line) => line !== '');

describe('the DocType icon list', () => {
  it('has a component for every allowed name', () => {
    for (const name of allowed) expect(DOCTYPE_ICONS, `no icon component for "${name}"`).toHaveProperty(name);
  });

  it('has no component that the list does not allow (a DocType could never name it)', () => {
    for (const name of Object.keys(DOCTYPE_ICONS)) expect(allowed, `"${name}" is not in doctype_icons.txt`).toContain(name);
  });

  it('has no duplicates and includes the default icon', () => {
    expect(new Set(allowed).size).toBe(allowed.length);
    expect(allowed).toContain(DEFAULT_DOCTYPE_ICON);
  });
});
