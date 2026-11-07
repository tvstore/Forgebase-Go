import js from '@eslint/js';
import reactHooks from 'eslint-plugin-react-hooks';
import { defineConfig } from 'eslint/config';
import tseslint from 'typescript-eslint';

// Browser storage is forbidden everywhere in the desk: the only secret that reaches the browser
// is the HttpOnly session cookie, and the CSRF token lives in memory (src/api.ts). Inline styles
// and raw HTML are forbidden too (Content-Security-Policy, XSS).
const storage = ['localStorage', 'sessionStorage', 'indexedDB'];

export default defineConfig(
  { ignores: ['dist', 'node_modules'] },
  js.configs.recommended,
  tseslint.configs.recommended,
  reactHooks.configs.flat.recommended,
  {
    // Plain Node scripts: the few globals they use.
    files: ['scripts/**/*.mjs'],
    languageOptions: { globals: { console: 'readonly', process: 'readonly', URL: 'readonly' } },
  },
  {
    rules: {
      'no-restricted-globals': [
        'error',
        ...storage.map((name) => ({
          name,
          message: 'No browser storage: tokens stay in memory (see src/api.ts).',
        })),
      ],
      'no-restricted-properties': [
        'error',
        ...['window', 'globalThis', 'self'].flatMap((object) =>
          storage.map((property) => ({
            object,
            property,
            message: 'No browser storage: tokens stay in memory (see src/api.ts).',
          })),
        ),
        {
          object: 'document',
          property: 'cookie',
          message: 'The desk never reads or writes cookies; the session cookie is HttpOnly.',
        },
      ],
      'no-restricted-syntax': [
        'error',
        {
          selector: "JSXAttribute[name.name='dangerouslySetInnerHTML']",
          message: 'No raw HTML.',
        },
        {
          selector: "JSXAttribute[name.name='style']",
          message: 'Use CSS classes (src/styles.css); the CSP has no inline styles.',
        },
      ],
    },
  },
  {
    // The smoke test looks INTO the browser storage to prove that the desk left nothing there.
    files: ['e2e/**/*.ts', 'playwright.config.ts'],
    rules: { 'no-restricted-properties': 'off', 'no-restricted-globals': 'off' },
  },
);
