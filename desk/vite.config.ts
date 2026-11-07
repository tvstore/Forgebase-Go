import tailwindcss from '@tailwindcss/vite';
import react from '@vitejs/plugin-react';
import { defineConfig } from 'vitest/config';

// The desk is served by `forgebase serve` under /desk/ (FORGEBASE_DESK_DIR=desk/dist).
// `npm run dev` proxies the API to a locally running `forgebase serve`.
export default defineConfig({
  base: '/desk/',
  plugins: [tailwindcss(), react()],
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    assetsDir: 'assets',
    // Every script and style is a separate file: the CSP forbids inline code.
    assetsInlineLimit: 0,
    cssCodeSplit: false,
  },
  experimental: {
    // The font in the stylesheet is referenced relative to the stylesheet (both are in assets/),
    // not by an absolute path: the CSS then has no reference to where the desk is mounted.
    renderBuiltUrl: (_filename, { hostType }) => (hostType === 'css' ? { relative: true } : undefined),
  },
  server: {
    proxy: { '/api': 'http://localhost:8000' },
  },
  test: {
    environment: 'node',
    include: ['src/**/*.test.ts'],
  },
});
