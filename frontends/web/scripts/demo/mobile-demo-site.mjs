#!/usr/bin/env node
// Real demo bundle, no RPC interception. Build with WEB_BUILD_TARGET=demo pnpm build,
// serve dist-demo (vite preview --mode demo --port 0 allocates a free port), then:
// node scripts/demo/mobile-demo-site.mjs <url>
// Screenshots are optional: SCREENSHOT_DIR=/tmp/<unique-run>.
import assert from "node:assert/strict";
import fs from "node:fs/promises";
import path from "node:path";
import { chromium } from "playwright";
import { expect } from "@playwright/test";

const base = process.argv[2];
assert(base, "Pass the demo URL");
const home = "/forge/p/shop";
const routes = [home, `${home}/workloads/prod/api`, `${home}/workloads/prod/api?tab=logs`,
  `${home}/workloads/prod/api?tab=history`, `${home}/workloads/prod/api?tab=config`,
  `${home}/releases`, `${home}/releases/v1.8.3`, `${home}/releases/v1.8.3?tab=diff`,
  `${home}/observability/dashboards`, `${home}/observability/logs`, `${home}/observability/traces`,
  `${home}/environments`, `${home}/environments/prod`, `${home}/secrets`, `${home}/domains`, `${home}/activity`, "/forge/demo?section=built"];
const browser = await chromium.launch({ executablePath: process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE ?? "/usr/bin/google-chrome", args: ["--no-sandbox"] });
const failures = [];

async function reachable(locator, label) {
  await locator.scrollIntoViewIfNeeded();
  // Retry transient layout shifts from lazy content/tour anchors; persistent
  // occlusion still fails, including a page with a falsely green scrollWidth.
  await expect.poll(async () => {
    await locator.scrollIntoViewIfNeeded();
    return locator.evaluate(el => {
      const r = el.getBoundingClientRect();
      const hit = document.elementFromPoint(r.x + r.width / 2, r.y + r.height / 2);
      return !!hit && (el === hit || el.contains(hit)) ? "reachable" : `${el.textContent}: blocked by ${hit?.outerHTML.slice(0, 220)}`;
    });
  }, { message: `${label} must receive the tap`, timeout: 5000 }).toBe("reachable");
  const geometry = await locator.evaluate(el => {
    const r = el.getBoundingClientRect();
    const hit = document.elementFromPoint(r.x + r.width / 2, r.y + r.height / 2);
    return { left: r.left, right: r.right, width: innerWidth, hit: !!hit && (el === hit || el.contains(hit)) };
  });
  assert(geometry.left >= 0 && geometry.right <= geometry.width + 1 && geometry.hit,
    `${label} clipped or occluded: ${JSON.stringify(geometry)}`);
}

try {
  for (const width of (process.env.WIDTHS ?? "360,390,768,1440").split(",").map(Number)) {
    const context = await browser.newContext({ viewport: { width, height: 900 }, isMobile: width < 768, hasTouch: width < 768 });
    const page = await context.newPage();
    page.setDefaultTimeout(10000);
    const errors = [];
    page.on("pageerror", error => errors.push(error.message));
    page.on("console", message => { if (message.type() === "error") errors.push(message.text()); });
    page.on("response", response => { if (response.status() >= 400 && !response.url().includes("/_otel")) errors.push(`${response.status()} ${response.url()}`); });
    for (const route of routes) {
      try {
        await page.goto(new URL(route, base).href);
        await page.locator("main").waitFor({ timeout: 15000 });
        await page.locator("main h1").waitFor({ timeout: 15000 });
        // A tour is a real visitor-facing control, not a mocked-away overlay.
        const done = page.getByRole("button", { name: "Done", exact: true });
        if (await done.isVisible()) await done.click();
        const welcome = page.getByRole("button", { name: /explore (on my own|freely)/i });
        if (await welcome.isVisible()) await welcome.click();
        const banner = page.getByTestId("forge-demo-banner");
        if (await banner.isVisible()) {
          const style = await banner.evaluate(el => ({ position: getComputedStyle(el).position, background: getComputedStyle(el).backgroundColor }));
          assert.equal(style.position, "static", "demo notice must scroll in normal flow");
          assert(!style.background.includes("rgba"), `demo notice must be opaque: ${style.background}`);
          const heading = page.locator("main h1");
          await heading.scrollIntoViewIfNeeded();
          const bannerBelow = await banner.evaluate(el => el.getBoundingClientRect().bottom);
          const headingTop = await heading.evaluate(el => el.getBoundingClientRect().top);
          assert(bannerBelow <= headingTop, "demo notice must not cover the content heading");
        }
        const geometry = await page.evaluate(() => ({ viewport: innerWidth, document: document.documentElement.scrollWidth }));
        assert(geometry.document <= geometry.viewport + 1, `page overflow: ${JSON.stringify(geometry)}`);
        if (process.env.SCREENSHOT_DIR) {
          await fs.mkdir(process.env.SCREENSHOT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.SCREENSHOT_DIR, `${width}-${route.replace(/[^a-z0-9]/gi, "_")}.png`) });
        }
        console.log(`ok ${width} ${route}`);
      } catch (error) { console.error(`FAIL ${width} ${route}: ${error.message}`); failures.push(`${width} ${route}: ${error.message}`); }
    }
    // Hit-testing is essential: the old shell hid overflow, so a document-width
    // assertion passed even though workload actions and Config were unreachable.
    try {
      await page.goto(new URL(`${home}/workloads/prod/api?env=prod`, base).href);
      const done = page.getByRole("button", { name: "Done", exact: true });
      if (await done.isVisible()) await done.click();
      for (const label of ["Logs", "Restart in prod", "Scale…", "Roll back prod to v1.8.2"]) {
        await reachable(page.getByTestId("workload-actions").getByRole("button", { name: label, exact: true }), label);
      }
      const config = page.getByRole("tab", { name: "Config", exact: true });
      await reachable(config, "Config tab");
      if (width < 768) await config.tap(); else await config.click();
      await page.waitForURL(/tab=config/);
      await assert.doesNotReject(() => page.getByRole("tabpanel", { name: "Config", exact: true }).waitFor());
      await page.getByRole("tab", { name: "Logs", exact: true }).click();
      await page.waitForURL(/tab=logs/);
      const range = page.getByRole("combobox", { name: "Time range", exact: true });
      await reachable(range, "time filter");
      await range.selectOption("1h");
      await page.waitForURL(/range=1h/);
      if (width < 768) {
        const open = page.getByRole("button", { name: "Open navigation" });
        await reachable(open, "navigation trigger");
        await open.tap();
        const drawer = page.getByRole("dialog", { name: "shop", exact: true });
        await drawer.waitFor();
        // Visible close + modal focus isolation are tracked in the upstream
        // sidebar_layout fix; do not claim they passed before its adoption.
        await page.keyboard.press("Escape");
        await page.waitForFunction(() => document.activeElement?.getAttribute("aria-label") === "Open navigation");
        await open.tap();
        await drawer.getByRole("link", { name: "Secrets", exact: true }).tap();
        await page.waitForURL(/\/secrets/);
        assert.equal(await drawer.count(), 0, "drawer closes after route selection");
      }
      console.log(`ok ${width} workload taps, filter and navigation`);
    } catch (error) { console.error(`FAIL ${width} interactions: ${error.message}`); failures.push(`${width} interactions: ${error.message}`); }
    failures.push(...errors.map(error => `${width} browser: ${error}`));
    await context.close();
  }
} finally { await browser.close(); }
for (const failure of failures) console.error(`FAIL ${failure}`);
process.exitCode = failures.length ? 1 : 0;
