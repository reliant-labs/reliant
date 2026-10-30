// Copyright (c) 2026 Reliant Labs
import { expect, test } from "@playwright/test";

// Run against a Vite server with Playwright. This test-only HTML entry renders
// the production component with typed cache data, never a live billing request.
const entry = "/src/components/Forge/Observability/Dashboards/__tests__/e2e/organization-costs.html";
for (const width of [390, 1440]) {
  test(`automatic cost selection without deployments ${width}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 1000 });
    await page.goto("/src/components/Forge/Observability/Dashboards/__tests__/e2e/dashboard-selection.html");
    await expect(page.getByTestId("dashboards-group-platform")).toContainText("Organization costs");
    await expect(page.getByTestId("dashboards-group-platform")).toContainText("Requires cost-read permission");
    await test.info().attach(`dashboard-selection-${width}.png`, { body: await page.screenshot({ animations: "disabled" }), contentType: "image/png" });
    await page.getByRole("link", { name: "Organization costs", exact: true }).click();
    await expect(page.getByRole("heading", { name: "Organization costs", exact: true })).toBeVisible();
    await expect(page.getByRole("article", { name: "BYOK costs" })).toContainText("Not measured");
    await expect(page.getByTestId("dashboard-variables")).toHaveCount(0);
    await expect(page.getByTestId("observability-no-envs")).toHaveCount(0);
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
    await test.info().attach(`dashboard-selected-costs-${width}.png`, { body: await page.screenshot({ animations: "disabled" }), contentType: "image/png" });
  });
}
for (const width of [390, 1440]) {
  for (const dark of [false, true]) {
    test(`organization costs ${width}px ${dark ? "dark" : "light"}`, async ({ page }) => {
      await page.setViewportSize({ width, height: 1000 });
      await page.goto(`${entry}${dark ? "?dark=1" : ""}`);
      await expect(page.getByRole("heading", { name: "Organization costs", exact: true })).toBeVisible();
      await expect(page.getByRole("article", { name: "BYOK costs" })).toContainText("Not measured");
      await expect(page.getByRole("article", { name: "Managed LLM costs" })).toContainText("USD -2.50");
      await expect(page.getByRole("region", { name: "Compute billing period", exact: true })).toBeVisible();
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
      await test.info().attach(`organization-costs-${width}-${dark ? "dark" : "light"}.png`, { body: await page.screenshot({ fullPage: true, animations: "disabled" }), contentType: "image/png" });
    });
  }
}
