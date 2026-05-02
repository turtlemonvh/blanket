import { defineConfig, devices } from '@playwright/test';

const BASE_URL = process.env.BLANKET_URL ?? 'http://localhost:8773';

export default defineConfig({
  testDir: './specs',
  // blanket talks to a single bolt DB shared by every test, so concurrent
  // test runs step on each other's state. Run one worker; fullyParallel:
  // false alone doesn't help because it only serializes within a single
  // spec file, not across files.
  fullyParallel: false,
  workers: 1,
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
  // Build with `make linux` (or `make darwin`) first; set BLANKET_BIN to
  // override the default path — e.g. `BLANKET_BIN=./blanket-darwin-amd64
  // npm test` on a mac. Default matches what `make linux` produces, which
  // is what `make docker-test-browser` (and CI) run against.
  webServer: process.env.BLANKET_URL
    ? undefined
    : {
        command: `${process.env.BLANKET_BIN ?? './blanket-linux-amd64'} -c testdata/blanket.test.json`,
        cwd: '../../',
        url: 'http://localhost:8773',
        reuseExistingServer: true,
        timeout: 10_000,
      },
});
