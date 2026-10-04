import { defineConfig, devices } from '@playwright/test';

// End-to-end tests run in CI only (the e2e job). Each test starts its own
// quil web and daemon through e2e/harness.ts, so there is no webServer here.
export default defineConfig({
  testDir: 'e2e',
  workers: 1,
  timeout: 120_000,
  forbidOnly: true,
  reporter: [['list']],
  use: {
    trace: 'retain-on-failure',
  },
  projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }],
});
