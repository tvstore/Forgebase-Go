import { defineConfig, devices } from '@playwright/test';

// The smoke test (e2e/smoke.spec.ts) drives the built desk in Chromium against a real
// `forgebase serve` (see e2e/seed.sh and the `e2e` job of .github/workflows/ci.yml).
//
//   E2E_BASE_URL                    where the server listens (default http://localhost:8000)
//   PLAYWRIGHT_CHROMIUM_EXECUTABLE  a Chromium binary to use instead of the one that
//                                   `npx playwright install chromium` downloads (for machines
//                                   that already have one; CI leaves it unset)
//   E2E_PASSWORD                    the password of the seeded users (default: seed.sh's)
const executablePath = process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE;

export default defineConfig({
  testDir: 'e2e',
  testMatch: '*.spec.ts',
  workers: 1,
  fullyParallel: false,
  retries: 0,
  reporter: 'list',
  outputDir: 'test-results',
  use: {
    baseURL: process.env.E2E_BASE_URL ?? 'http://localhost:8000',
    trace: 'retain-on-failure',
  },
  projects: [
    {
      name: 'chromium',
      use: { ...devices['Desktop Chrome'], ...(executablePath ? { launchOptions: { executablePath } } : {}) },
    },
  ],
});
