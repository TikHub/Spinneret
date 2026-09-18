import { defineConfig, devices } from '@playwright/test';

// The suite runs against a running stack (make up); nothing is started here.
const baseURL = process.env.SPINNERET_UI_URL ?? 'http://localhost:8080';

/** Session of the console administrator, created once by e2e/auth.setup.ts. */
const storageState = 'e2e/.auth/admin.json';

/** Screenshot and layout size of the whole suite. */
const VIEWPORT = { width: 1440, height: 900 };

export default defineConfig({
  testDir: './e2e',
  timeout: 90_000,
  expect: { timeout: 15_000 },
  // Specs create and delete shared resources (sites, policies, breakers) in one
  // namespace, so they run one after another.
  fullyParallel: false,
  workers: 1,
  forbidOnly: Boolean(process.env.CI),
  retries: process.env.CI ? 1 : 0,
  reporter: process.env.CI ? [['list'], ['html', { open: 'never' }]] : 'list',
  outputDir: 'test-results',
  use: {
    baseURL,
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
    video: 'off',
    locale: 'en-US',
    timezoneId: 'UTC',
    colorScheme: 'light',
    viewport: { width: 1440, height: 900 },
    actionTimeout: 20_000,
    navigationTimeout: 30_000,
  },
  projects: [
    {
      name: 'setup',
      testMatch: /auth\.setup\.ts/,
      use: { ...devices['Desktop Chrome'], viewport: VIEWPORT },
    },
    {
      name: 'chromium',
      testIgnore: /auth\.setup\.ts/,
      dependencies: ['setup'],
      // The device preset carries its own viewport; the screenshots in
      // docs/images are taken at 1440x900, so it is restated here.
      use: { ...devices['Desktop Chrome'], viewport: VIEWPORT, storageState },
    },
  ],
});
