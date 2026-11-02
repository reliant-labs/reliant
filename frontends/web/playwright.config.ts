import { defineConfig, devices } from '@playwright/test';
import { responsiveViewports } from './e2e/helpers/responsive';

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
  // No retries, in CI either. A retry turns a flaky test into a green check:
  // `retries: 2` hid onboarding.spec's "Local daemon" flake (a stateless
  // ListProjects mock that lost the created project on a slow runner) as
  // "1 flaky" in a passing run. A test that fails once is a defect to fix at
  // the root, and it has to be red to get fixed.
  retries: 0,
  workers: isCI ? 1 : undefined,
  reporter: isCI ? [['github'], ['html', { open: 'never' }]] : 'html',
  timeout: 30000,

  use: {
    baseURL,
    // With no retries there is no "first retry" to trace: keep the trace of
    // any failed test instead.
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
    video: 'retain-on-failure',
  },

  projects: [
    {
      name: 'chromium',
      use: { ...devices['Desktop Chrome'] },
    },
    // A bounded representative matrix, not every spec × every device.
    // The existing Chromium project also runs these journeys with mouse input.
    ...[
      { name: 'responsive-chromium-touch', browserName: 'chromium' as const, viewport: responsiveViewports.phone },
      { name: 'responsive-webkit-phone', browserName: 'webkit' as const, viewport: responsiveViewports.phone },
      { name: 'responsive-webkit-tablet', browserName: 'webkit' as const, viewport: responsiveViewports.tablet },
    ].map(({ name, browserName, viewport }) => ({
      name,
      testMatch: '**/mobile-responsive.spec.ts',
      use: { browserName, viewport, hasTouch: true, isMobile: true, deviceScaleFactor: 2 },
    })),
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