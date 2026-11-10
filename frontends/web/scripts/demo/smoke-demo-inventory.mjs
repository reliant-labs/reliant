#!/usr/bin/env node
// Built public demo proof; never use an authenticated application dev server.
// WEB_BUILD_TARGET=demo pnpm build && WEB_BUILD_TARGET=demo pnpm exec vite preview
// node scripts/demo/smoke-demo-inventory.mjs http://localhost:4173
// SCREENSHOT_DIR=/tmp/demo-proof optionally keeps screenshots outside the repo.
// Each page is opened as a fresh document, as a shared or reloaded link is: a host
// that answers it with anything but the app (a dotted route like
// /releases/v1.8.3 with no SPA fallback) fails with the status it answered.
import fs from "node:fs";
import path from "node:path";
import assert from "node:assert/strict";
import { chromium } from "playwright";

const base = process.argv[2] ?? "http://localhost:4173";
const origin = new URL(base).origin;
const browser = await chromium.launch({ executablePath: process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE ?? "/usr/bin/google-chrome", args: ["--no-sandbox"] });
const context = await browser.newContext({ viewport: { width: 1440, height: 1000 } });
const page = await context.newPage();
const errors = [];
const requests = [];
page.on("pageerror", (error) => errors.push(error.message));
page.on("request", (request) => {
  const url = new URL(request.url());
  if (!["http:", "https:"].includes(url.protocol)) return;
  requests.push(`${request.method()} ${url.pathname}`);
  if (url.origin !== origin || /(?:controlplane|reliant\.v1|supabase|oauth|\/auth\/)/i.test(url.href)) errors.push(`Forbidden request: ${request.url()}`);
});
async function screenshot(name) {
  if (!process.env.SCREENSHOT_DIR) return;
  fs.mkdirSync(process.env.SCREENSHOT_DIR, { recursive: true });
  await page.screenshot({ path: path.join(process.env.SCREENSHOT_DIR, `${name}.png`), fullPage: true });
}
/** Loads `pathname` as a fresh document; the host must answer with the app, not a file 404. */
async function open(pathname) {
  const response = await page.goto(`${base}${pathname}`);
  const type = response?.headers()["content-type"] ?? "";
  if (!response?.ok() || !type.startsWith("text/html")) {
    const body = ((await response?.text().catch(() => "")) ?? "").trim().slice(0, 80);
    throw new Error(`${pathname}: the host answered ${response?.status() ?? "nothing"} ${type} ${JSON.stringify(body)} instead of the app${/\/[^/]*\.[^/]*$/.test(pathname.split("?")[0]) ? " (a dotted route: the host has no SPA fallback for it)" : ""}`);
  }
}
const releaseHeading = () => page.getByRole("main").getByRole("heading", { level: 1, name: "v1.8.3" });
try {
  // The fixture: the demo's own release list must still have v1.8.3 (the config
  // history below compares it with v1.8.4). Opening it from the list proves the
  // page; opening its URL afresh proves the deep link a visitor shares or reloads.
  await open("/forge/p/shop/releases?env=prod");
  const listed = page.getByRole("main").getByRole("link", { name: "v1.8.3", exact: true }).first();
  try {
    await listed.waitFor({ timeout: 15000 });
  } catch {
    const hrefs = await page.getByRole("main").locator('a[href*="/releases/"]').evaluateAll((els) => els.map((a) => a.getAttribute("href")));
    const versions = [...new Set(hrefs.map((href) => href.match(/\/releases\/([^/?]+)/)?.[1]).filter(Boolean))];
    throw new Error(`the demo's releases no longer include v1.8.3 (they list ${JSON.stringify(versions)}): this fixture is stale`);
  }
  await listed.click();
  await releaseHeading().waitFor();
  console.log("PASS release detail opens from the release list");
  await open("/forge/p/shop/releases/v1.8.3?env=prod");
  await releaseHeading().waitFor();
  await screenshot("release-detail");
  console.log("PASS release detail loads from its deep link without missing demo-twin exports");

  await open("/forge/p/shop/workloads/dev/api?env=dev&tab=config");
  await page.getByRole("button", { name: "View config values", exact: true }).click();
  await page.getByText("Bundle ID:").waitFor();
  let text = await page.locator("main").innerText();
  assert.match(text, /Release\nv1\.8\.4/);
  assert.match(text, /demo-bundle-env-dev-v1\.8\.4/);
  assert.match(text, /DATABASE_URL: '\[withheld\]'/);
  assert.match(text, /sha256:(11){32}/);
  await page.getByRole("textbox", { name: "Historical release version" }).fill("v1.8.3");
  await page.getByRole("button", { name: "Compare redacted values", exact: true }).click();
  await page.getByRole("heading", { name: "v1.8.3 → v1.8.4" }).waitFor();
  text = await page.locator("main").innerText();
  assert.match(text, /ORDER_TIMEOUT_MS/);
  assert.match(text, /Historical: "3000"/);
  assert.match(text, /Selected: "5000"/);
  assert.doesNotMatch(text, /forge\.dev\/redacted:\s*sha256/);
  await screenshot("config-history");
  console.log("PASS config is current, redacted, pinned and historically comparable");
  assert.deepEqual(errors, []);
  assert(requests.every((request) => request.startsWith("GET ")), "demo made a non-GET network call");
  console.log("PASS no backend, authentication, Supabase or daemon requests", [...new Set(requests.filter((r) => !r.includes("/assets/")))]);
} catch (err) {
  // A timeout says only what it waited for: say what the page reported too.
  if (errors.length) console.error("page errors and forbidden requests so far:", errors);
  throw err;
} finally {
  await browser.close();
}
