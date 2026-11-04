// Copyright (c) 2026 Reliant Labs
// BASE_URL=http://localhost:<vite-port> npx playwright test --config src/components/Forge/Observability/Dashboards/__tests__/e2e/playwright.config.ts
import { defineConfig } from "@playwright/test";

export default defineConfig({
  testDir: ".",
  testMatch: "organizationCosts.spec.ts",
  outputDir: "/tmp/reliant-organization-costs-playwright",
  fullyParallel: true,
  retries: 0,
  reporter: "list",
  use: { baseURL: process.env.BASE_URL, browserName: "chromium" },
});
