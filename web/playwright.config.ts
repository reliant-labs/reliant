import { defineConfig, devices } from '@playwright/test';

// Default baseURL - can be overridden via BASE_URL env var
// Note: vite dev serves on 5173, vite preview serves on 4173
const isCI = !!process.env.CI;
const baseURL = process.env.BASE_URL || (isCI ? 'http://localhost:4173' : 'http://localhost:5173');

export default defineConfig({
  testDir: './e2e',
  // Manual QA scripts, not automated tests — excluded from the default run:
  //  - debug-canvas.spec.ts: zero `expect()` calls, pure console.log
  //    diagnostics against onboarding-gated app state.
  //  - workflow-builder-validation.spec.ts: hardcodes `localhost:3046`,
  //    unrelated to this config's managed webServer (localhost:4173), so it
  //    can never pass here. Run either manually with `playwright test
  //    e2e/<file>` against a dev server when needed.
  testIgnore: ['**/debug-canvas.spec.ts', '**/workflow-builder-validation.spec.ts'],
  fullyParallel: true,
  forbidOnly: isCI,
  retries: isCI ? 2 : 0,
  workers: isCI ? 1 : undefined,
  reporter: isCI ? [['github'], ['html', { open: 'never' }]] : 'html',
  timeout: 30000,

  use: {
    baseURL,
    trace: 'on-first-retry',
    screenshot: 'only-on-failure',
    video: 'retain-on-failure',
  },

  projects: [
    {
      name: 'chromium',
      use: { ...devices['Desktop Chrome'] },
    },
    {
      name: 'memory-tests',
      testMatch: '**/memory*.spec.ts',
      use: {
        ...devices['Desktop Chrome'],
        launchOptions: {
          args: [
            '--enable-precise-memory-info',
            '--js-flags=--expose-gc',
            '--disable-web-security',
            '--disable-features=IsolateOrigins,site-per-process',
          ],
        },
      },
    },
  ],

  // Web server configuration:
  // - In CI: Use 'vite preview' to serve built files (faster, no compilation)
  // - Locally with BASE_URL: Skip server, use existing one
  // - Locally without BASE_URL: Start dev server
  ...(process.env.BASE_URL
    ? {}
    : {
        webServer: {
          command: isCI ? 'npm run preview' : 'npm run dev',
          url: baseURL,
          reuseExistingServer: !isCI,
          timeout: 120 * 1000,
        },
      }),
});