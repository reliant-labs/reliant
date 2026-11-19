import { defineConfig, devices } from '@playwright/test';

// The visual-regression baseline (e2e/visual/README.md): the key surfaces of
// the production build, against fixture data, in dark, light and mobile.
//
// Its own config, not a project of playwright.config.ts: the CI Playwright
// lane runs that config, and this suite is opt-in (`task test:web:visual`).
// Its specs are `*.visual.ts`, so the default config's testMatch never
// collects them either.
//
// It runs inside the pinned Playwright Linux image (scripts/visual-docker.sh,
// which `task test:web:visual` calls), so the baselines are the same pixels
// on every host.
//
// The server is `vite preview` of an existing build (the task builds it);
// VISUAL_PORT moves it off the default, BASE_URL points at a server you run.
const port = Number(process.env.VISUAL_PORT || 4391);
const baseURL = process.env.BASE_URL || `http://127.0.0.1:${port}`;

const shared = {
  locale: 'en-US',
  timezoneId: 'UTC',
  reducedMotion: 'reduce',
  deviceScaleFactor: 1,
} as const;

export default defineConfig({
  testDir: './e2e/visual',
  testMatch: '**/*.visual.ts',
  // One baseline per project and surface; the platform is not in the name
  // because the suite skips off linux (font rasterization differs per OS).
  snapshotPathTemplate: '{testDir}/__screenshots__/{projectName}/{arg}{ext}',
  fullyParallel: true,
  forbidOnly: !!process.env.CI,
  // A visual diff that passes on retry is a flake to fix, not a pass.
  retries: 0,
  workers: process.env.VISUAL_WORKERS ? Number(process.env.VISUAL_WORKERS) : 3,
  reporter: [['list'], ['html', { open: 'never', outputFolder: 'playwright-report-visual' }]],
  outputDir: 'test-results-visual',
  timeout: 90_000,
  expect: { timeout: 20_000 },
  use: {
    baseURL,
    trace: 'retain-on-failure',
  },
  projects: [
    // A surface's tag (@desktop / @mobile) picks the projects that capture
    // it. The harness's own tests (@harness) run once, here.
    { name: 'desktop-dark', grep: /@desktop|@harness/, use: { ...devices['Desktop Chrome'], ...shared, viewport: { width: 1440, height: 900 }, colorScheme: 'dark' } },
    { name: 'desktop-light', grep: /@desktop/, use: { ...devices['Desktop Chrome'], ...shared, viewport: { width: 1440, height: 900 }, colorScheme: 'light' } },
    {
      name: 'mobile-dark',
      grep: /@mobile/,
      use: { ...devices['Pixel 7'], ...shared, viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true, colorScheme: 'dark' },
    },
  ],
  ...(process.env.BASE_URL
    ? {}
    : {
        webServer: {
          command: `npx vite preview --host 127.0.0.1 --port ${port} --strictPort`,
          url: baseURL,
          reuseExistingServer: false,
          timeout: 120_000,
        },
      }),
});
