// Checks the licenses of every package in package-lock.json against an allow-list (plain Node,
// no dependency). Run with `npm run licenses`; CI runs it too.
//
// The allow-list is deliberately minimal: only what is in the tree today. Adding a license is a
// decision to record in docs/blueprint/dependency-evaluation.md and maintenance-policy.md.
//
// - Packages that end up in the shipped desk (not marked "dev" in the lockfile) must be under a
//   permissive license from SHIPPED.
// - Development tooling (never shipped) may also use DEV_ONLY: MPL-2.0 (lightningcss, a tool of
//   Vite; weak copyleft, file level, and nothing of it is in the built desk), BlueOak-1.0.0
//   (minimatch) and CC-BY-4.0 (caniuse-lite, browser data used by build tools).
// - OFL-1.1 (the SIL Open Font License) is allowed for FONT packages only (UX.4): a package
//   from the `@fontsource` or `@fontsource-variable` scope, which ships CSS and woff2 files and no
//   JavaScript. It is never allowed for code. See docs/blueprint/maintenance-policy.md.
// - A package with no license, a license expression or any other license fails the check.
//
// Limit: the lockfile "dev" flag is only as good as the imports. A dev-only package that is
// imported from src/ would be bundled into the shipped desk although it is flagged dev here.
// Reviewers check the imports in src/; there is deliberately no scanner of dist/.
//
// It also enforces an owner decision: the only runtime dependencies are react, react-dom and the
// self-hosted font (UX.4, U2).

import { readFileSync } from 'node:fs';

const SHIPPED = new Set(['MIT', 'ISC', 'Apache-2.0', 'BSD-2-Clause', 'BSD-3-Clause']);
const DEV_ONLY = new Set([...SHIPPED, 'MPL-2.0', 'BlueOak-1.0.0', 'CC-BY-4.0']);
const RUNTIME_DEPENDENCIES = ['@fontsource-variable/inter', 'react', 'react-dom'];
// The one license that is allowed for fonts only, and the package scopes that count as fonts.
const FONT_LICENSE = 'OFL-1.1';
const FONT_REGISTRY = /^https:\/\/registry\.npmjs\.org\/@fontsource(-variable)?\//;
const FONT_PACKAGE = /^@fontsource(-variable)?\//;

const read = (name) => JSON.parse(readFileSync(new URL(`../${name}`, import.meta.url), 'utf8'));
const lock = read('package-lock.json');
const manifest = read('package.json');

const problems = [];

if (!lock.lockfileVersion || lock.lockfileVersion < 2 || !lock.packages) {
  problems.push('package-lock.json is not a lockfile of version 2 or 3 with a "packages" map');
}

const runtime = Object.keys(manifest.dependencies ?? {}).sort();
if (JSON.stringify(runtime) !== JSON.stringify(RUNTIME_DEPENDENCIES)) {
  problems.push(
    `runtime dependencies must be exactly ${RUNTIME_DEPENDENCIES.join(', ')}, found: ${runtime.join(', ') || 'none'}`,
  );
}

/**
 * A font package for the OFL exception: the install path names it, the lockfile entry's own name
 * (when it has one) agrees, and it was resolved from the npm registry's @fontsource scopes, so an
 * npm alias cannot put another package under a font's path.
 */
function isFontPackage(name, info) {
  return (
    FONT_PACKAGE.test(name) &&
    (info.name === undefined || info.name === name) &&
    typeof info.resolved === 'string' &&
    FONT_REGISTRY.test(info.resolved)
  );
}

const counts = new Map();
let total = 0;
for (const [path, info] of Object.entries(lock.packages ?? {})) {
  if (path === '') continue; // the project itself
  total += 1;
  const name = path.replace(/^.*node_modules\//, '');
  const dev = info.dev === true;
  const license = info.license;
  const key = `${license ?? 'MISSING'}${dev ? ' (dev only)' : ' (shipped)'}`;
  counts.set(key, (counts.get(key) ?? 0) + 1);
  if (typeof license !== 'string') {
    problems.push(`${name}@${info.version}: no license in the lockfile`);
  } else if (license === FONT_LICENSE && isFontPackage(name, info)) {
    // A font package: allowed, shipped or not.
  } else if (!(dev ? DEV_ONLY : SHIPPED).has(license)) {
    problems.push(
      `${name}@${info.version}: license ${license} is not allowed ${dev ? 'for a development package' : 'for a shipped package'}`,
    );
  }
}

console.log(`${total} packages in package-lock.json`);
for (const [key, count] of [...counts].sort()) console.log(`  ${String(count).padStart(4)}  ${key}`);

if (problems.length > 0) {
  console.error('\nLicense check failed:');
  for (const problem of problems) console.error(`  - ${problem}`);
  process.exit(1);
}
console.log('License check passed.');
