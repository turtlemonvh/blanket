import { defineConfig, devices } from '@playwright/test';

const BASE_URL = process.env.BLANKET_URL ?? 'http://localhost:8773';

export default defineConfig({
  testDir: './specs',
  fullyParallel: false, // blanket is stateful; keep tests serial
  retries: process.env.CI ? 1 : 0,
  reporter: [['list'], ['html', { open: 'never' }]],

  use: {
    baseURL: BASE_URL,
    // Capture screenshot + trace on first failure for easier debugging
    screenshot: 'only-on-failure',
    trace: 'on-first-retry',
  },

  projects: [
    {
      name: 'chromium',
      use: { ...devices['Desktop Chrome'] },
    },
  ],

  // If BLANKET_URL is not set we assume the binary is in the repo root.
  // Build with `make linux` or `make darwin` first, then `npm test` will
  // start the server automatically.
  webServer: process.env.BLANKET_URL
    ? undefined
    : {
        command: `./blanket -c testdata/blanket.test.json`,
        cwd: '../../',
        url: 'http://localhost:8773',
        reuseExistingServer: true,
        timeout: 10_000,
      },
});
