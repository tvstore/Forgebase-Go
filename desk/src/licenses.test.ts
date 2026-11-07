import { describe, expect, it } from 'vitest';

// The notice file that Vite copies from public/ to dist/ (see docs/blueprint/maintenance-policy.md).
import notice from '../public/THIRD-PARTY-LICENSES.txt?raw';
import inter from '../node_modules/@fontsource-variable/inter/LICENSE?raw';
import lucide from './icons/LICENSE-lucide.txt?raw';

describe('THIRD-PARTY-LICENSES.txt', () => {
  it("contains the installed font package's licence verbatim (refresh it when the font is upgraded)", () => {
    expect(notice).toContain(inter.trim());
  });

  it('names the font package and the version that package.json pins', async () => {
    const { default: manifest } = await import('../package.json');
    expect(notice).toContain(`@fontsource-variable/inter ${manifest.dependencies['@fontsource-variable/inter']}`);
  });

  it('contains the Lucide notice verbatim', () => {
    expect(notice).toContain(lucide.trim());
  });
});
