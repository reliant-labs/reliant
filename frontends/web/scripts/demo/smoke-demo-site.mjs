#!/usr/bin/env node
// Headless smoke check of the forge demo site (demo.html, `npm run build:demo`).
//
//   node scripts/demo/smoke-demo-site.mjs https://demo.reliantlabs.io
//   node scripts/demo/smoke-demo-site.mjs http://127.0.0.1:4173      # vite preview --mode demo
//
// Loads `/` and every forge console tab, and FAILS (exit 1) on:
//   - a request to any origin but the site's own (the demo reaches no backend);
//   - a 4xx/5xx response from the site itself (other than /_otel, reported);
//   - an uncaught page error, or a console error naming CORS;
//   - `/` not landing on /forge/demo, or no tour/console on the page.
// Prints, per page, whether it showed "Sample data isn't available" (an RPC
// the demo has no sample data for), which is a content gap, not a failure.
//
// CHROME: PLAYWRIGHT_CHROMIUM_EXECUTABLE (default /usr/bin/google-chrome).

import { chromium } from "playwright";

const base = (process.argv[2] ?? "http://127.0.0.1:4173").replace(/\/$/, "");
const origin = new URL(base).origin;
const project = process.env.DEMO_PROJECT ?? "shop";
const pages = [
  "/",
  `/forge/p/${project}`,
  `/forge/p/${project}/releases`,
  `/forge/p/${project}/environments`,
  `/forge/p/${project}/environments/prod`,
  `/forge/p/${project}/secrets`,
  `/forge/p/${project}/domains`,
  `/forge/p/${project}/activity`,
  `/forge/p/${project}/observability/errors`,
  `/forge/p/${project}/observability/traces`,
  `/forge/p/${project}/observability/logs`,
  `/forge/p/${project}/observability/dashboards`,
  `/forge/p/${project}/observability/alerts`,
];

const browser = await chromium.launch({ executablePath: process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE ?? "/usr/bin/google-chrome", args: ["--no-sandbox"] });
const failures = [];
try {
  for (const path of pages) {
    const context = await browser.newContext({ viewport: { width: 1440, height: 900 } });
    const page = await context.newPage();
    const problems = [];
    page.on("request", (req) => {
      const url = new URL(req.url());
      if (!["http:", "https:", "ws:", "wss:"].includes(url.protocol)) return;
      if (url.origin !== origin) problems.push(`request to another origin: ${req.method()} ${req.url()}`);
    });
    page.on("response", (res) => {
      const url = new URL(res.url());
      if (url.origin === origin && res.status() >= 400 && !url.pathname.startsWith("/_otel")) problems.push(`${res.status()} ${url.pathname}`);
    });
    page.on("pageerror", (err) => problems.push(`page error: ${err.message}`));
    page.on("console", (msg) => {
      if (msg.type() === "error" && /cors|access-control/i.test(msg.text())) problems.push(`console: ${msg.text()}`);
    });
    await page.goto(`${base}${path}`, { waitUntil: "load" });
    await page.waitForTimeout(Number(process.env.SETTLE_MS ?? 4000));
    // A tour started on load may have moved the page to its first step: go to
    // the console page in the app, as a click on its nav would.
    if (path !== "/" && new URL(page.url()).pathname !== path) {
      await page.evaluate((href) => globalThis.__RELIANT_ROUTER?.navigate({ href }), path);
      await page.waitForTimeout(Number(process.env.SETTLE_MS ?? 4000));
    }
    const landed = new URL(page.url());
    const text = await page.locator("body").innerText().catch(() => "");
    const unavailable = (text.match(/Sample data isn't available/g) ?? []).length;
    if (path === "/" && landed.pathname !== "/forge/demo") problems.push(`/ landed on ${landed.pathname}, not /forge/demo`);
    if (!(await page.locator("[data-shell='demo']").count())) problems.push("no [data-shell=demo] root");
    console.log(`${problems.length ? "FAIL" : "ok  "} ${path} → ${landed.pathname}${landed.search}${unavailable ? `  (${unavailable}× sample data unavailable)` : ""}`);
    for (const p of problems) console.log(`       ${p}`);
    failures.push(...problems.map((p) => `${path}: ${p}`));
    await context.close();
  }
} finally {
  await browser.close();
}
process.exit(failures.length ? 1 : 0);
