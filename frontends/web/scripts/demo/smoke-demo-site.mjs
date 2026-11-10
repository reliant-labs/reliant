#!/usr/bin/env node
// Headless smoke check of the forge demo site (demo.html, `npm run build:demo`).
//
//   node scripts/demo/smoke-demo-site.mjs https://demo.reliantlabs.io
//   node scripts/demo/smoke-demo-site.mjs http://127.0.0.1:4173      # vite preview --mode demo
//
// Two passes, and it FAILS (exit 1) on any problem in either.
//
// 1. PAGES: loads `/` and every forge console page by URL. Problems:
//    - a request to any origin but the site's own (the demo reaches no backend);
//    - a 4xx/5xx response from the site itself (other than /_otel);
//    - an uncaught page error, or a console error naming CORS;
//    - `/` not landing on the visual build story (/forge/demo?section=built);
//    - a page with no `[data-shell=demo]` root.
//    Per page it also prints how often "Sample data isn't available" showed (an
//    RPC the demo has no sample data for): a content gap, not a failure.
//
// 2. NAV: starts at `/`, clicks Explore the running app, then every console nav item, at
//    desktop 1440×900 and phone 390×844 (where the nav is a drawer behind
//    "Open navigation"). Sections that fold (Observability, Environments) are
//    unfolded first, so their sub-items are clicked too. An item is dead, and
//    the run fails, when the click does not move the URL to the item's href,
//    or the page's heading and content do not change.
//
// AUDITED READS: Data and Storage read an audited environment only after the
// reader gives a reason, in a dialog naming the environment (AccessReasonDialog),
// asked once per surface and environment for the tab. The demo's prod is audited.
// Both passes answer it as a visitor would: wait for the dialog, type
// "demo smoke <run id>", confirm, then require the read to happen and the page to
// show the reason. It is a failure when the dialog does not appear on an audited
// environment the tab has not answered for, when anything was read before it was
// answered, or when it appears anywhere else. Pass 1 opens both on prod directly.
// DEMO_AUDITED_ENVS (default "prod"): the environments the demo audits.
// DEMO_SMOKE_RUN_ID (default: the start time): the run id in the reason.
//
// CHROME: PLAYWRIGHT_CHROMIUM_EXECUTABLE (default /usr/bin/google-chrome).
// SCREENSHOT_DIR=<dir> saves each page (pass 1) as <dir>/<path>.png.
// SETTLE_MS: how long a page gets to render (default 4000; nav clicks get half).

import fs from "node:fs";
import nodePath from "node:path";
import { chromium } from "playwright";

const base = (process.argv[2] ?? "http://127.0.0.1:4173").replace(/\/$/, "");
const origin = new URL(base).origin;
const project = process.env.DEMO_PROJECT ?? "shop";
const home = `/forge/p/${project}`;
const settle = Number(process.env.SETTLE_MS ?? 4000);
const auditedEnvs = (process.env.DEMO_AUDITED_ENVS ?? "prod").split(",").map((env) => env.trim()).filter(Boolean);
const reason = `demo smoke ${process.env.DEMO_SMOKE_RUN_ID ?? new Date().toISOString()}`;
// The surfaces that ask, and what each shows once it has read.
const audited = [
  { path: `${home}/data`, subject: "data", read: (page) => page.getByRole("navigation", { name: "Tables" }).getByRole("link").first() },
  { path: `${home}/storage`, subject: "files", read: (page) => page.getByRole("main").getByRole("table").getByRole("row").nth(1) },
];
const pages = [
  "/",
  home,
  `${home}/releases`,
  `${home}/environments`,
  `${home}/environments/prod`,
  `${home}/secrets`,
  `${home}/domains`,
  `${home}/activity`,
  `${home}/observability/errors`,
  `${home}/observability/traces`,
  `${home}/observability/logs`,
  `${home}/observability/dashboards`,
  `${home}/observability/alerts`,
  ...(auditedEnvs.length ? audited.map((surface) => `${surface.path}?env=${auditedEnvs[0]}`) : []),
];
const viewports = [
  { name: "desktop", width: 1440, height: 900, mobile: false },
  { name: "phone", width: 390, height: 844, mobile: true },
];

const browser = await chromium.launch({ executablePath: process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE ?? "/usr/bin/google-chrome", args: ["--no-sandbox"] });
const failures = [];

/** A page that records every problem the PAGES pass fails on. */
async function openPage(viewport) {
  const context = await browser.newContext({
    viewport: { width: viewport.width, height: viewport.height },
    isMobile: viewport.mobile,
    hasTouch: viewport.mobile,
  });
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
  return { context, page, problems };
}

function report(label, problems, detail = "") {
  console.log(`${problems.length ? "FAIL" : "ok  "} ${label}${detail}`);
  for (const p of problems) console.log(`       ${p}`);
  failures.push(...problems.map((p) => `${label}: ${p}`));
}

/** What a nav click must change: the main heading and the page's text. */
async function pageState(page) {
  return page.evaluate(() => {
    const main = document.querySelector("main") ?? document.querySelector("[data-testid='forge-shell']") ?? document.body;
    const heading = main.querySelector("h1")?.textContent?.trim() ?? "";
    const nav = document.querySelector("[data-smoke-nav]");
    const text = [...main.querySelectorAll("*")]
      .filter((el) => !nav?.contains(el) && el.children.length === 0)
      .map((el) => el.textContent.trim())
      .filter(Boolean)
      .join("\n");
    return { path: location.pathname, heading, text };
  });
}

/** Marks the console's left nav (the <nav> holding "Overview") and returns whether it was found. */
async function markNav(page) {
  return page.evaluate((home) => {
    const overview = [...document.querySelectorAll("a[href]")].find((a) => a.textContent.trim() === "Overview" && a.getAttribute("href").startsWith(home));
    const nav = overview?.closest("nav");
    if (!nav) return false;
    nav.setAttribute("data-smoke-nav", "");
    return true;
  }, home);
}

/** On the phone the nav is a drawer: open it. Returns a problem, or null. */
async function revealNav(page, viewport) {
  if (!viewport.mobile) return null;
  const open = page.getByRole("button", { name: "Open navigation" });
  if (!(await open.count())) return "no \"Open navigation\" button: the nav is unreachable on a phone";
  if ((await open.getAttribute("aria-expanded")) !== "true") await open.click();
  await page.waitForTimeout(400);
  return null;
}

/**
 * On Data or Storage: answers the audited-read dialog the way a visitor does, and
 * checks the read. `answered` holds the surface/environment pairs this tab has
 * already given a reason for (they are not asked again). Returns problems.
 */
async function answerAudit(page, answered) {
  const url = new URL(page.url());
  const surface = audited.find((s) => url.pathname === s.path);
  if (!surface) return [];
  const env = url.searchParams.get("env");
  if (!env) return [`${url.pathname}: no ?env= in the URL, so no environment to read`];
  const where = `${url.pathname}?env=${env}`;
  const dialog = page.getByRole("dialog", { name: `Reading ${env}'s ${surface.subject} is audited` });
  const read = surface.read(page);
  const problems = [];
  const asks = auditedEnvs.includes(env) && !answered.has(`${surface.subject}:${env}`);
  if (asks) {
    try {
      await dialog.waitFor({ timeout: 15000 });
    } catch {
      return [`${where}: no "Reading ${env}'s ${surface.subject} is audited" dialog: an audited read must ask for a reason`];
    }
    if (await read.count()) problems.push(`${where}: ${surface.subject} was read before the reason was given`);
    await dialog.getByLabel(`Why are you reading ${env}'s ${surface.subject}?`).fill(reason);
    await dialog.getByRole("button", { name: `Read ${env}'s ${surface.subject}`, exact: true }).click();
    try {
      await dialog.waitFor({ state: "hidden", timeout: 5000 });
    } catch {
      return [...problems, `${where}: the reason dialog stayed open after "Read ${env}'s ${surface.subject}"`];
    }
    answered.add(`${surface.subject}:${env}`);
  }
  try {
    await read.waitFor({ timeout: 15000 });
  } catch {
    problems.push(`${where}: nothing was read${asks ? " after the reason was given" : ""}`);
  }
  if (await dialog.isVisible()) problems.push(`${where}: the reason dialog is open where it should not ask`);
  if (auditedEnvs.includes(env) && !(await page.getByRole("main").getByText(reason, { exact: true }).isVisible())) {
    problems.push(`${where}: the page does not show the reason it reads under ("${reason}")`);
  }
  if (!problems.length) console.log(`       ${where}: ${asks ? `answered the audited-read dialog ("${reason}") and ` : ""}read ${surface.subject}`);
  return problems;
}

/** The left nav's items now, as hrefs (query dropped) in nav order. Folded sections show their items once open. */
async function navItems(page) {
  const folded = page.locator("[data-smoke-nav] button[aria-expanded='false']");
  for (let i = await folded.count(); i > 0; i--) await folded.nth(0).click().catch(() => {});
  return page.locator("[data-smoke-nav] a[href]").evaluateAll((els) =>
    [...new Map(els.map((a) => [a.getAttribute("href").split("?")[0], a.textContent.trim()])).entries()].map(([href, label]) => ({ href, label })),
  );
}

try {
  // ── 1. PAGES ──
  for (const path of pages) {
    const { context, page, problems } = await openPage(viewports[0]);
    await page.goto(`${base}${path}`, { waitUntil: "load" });
    await page.waitForTimeout(settle);
    const landed = new URL(page.url());
    const text = await page.locator("body").innerText().catch(() => "");
    const unavailable = (text.match(/Sample data isn't available/g) ?? []).length;
    if (path === "/") {
      if (landed.pathname !== "/forge/demo" || landed.searchParams.get("section") !== "built") problems.push(`/ did not land on /forge/demo?section=built: ${landed.pathname}${landed.search}`);
      if (!(await page.getByTestId("demo-welcome").isVisible())) problems.push("build story has no visible summary and explore/tour choice");
      if (!(await page.getByTestId("how-built").isVisible())) problems.push("build story has no recorded build");
    }
    const want = new URL(path, base);
    if (path !== "/" && landed.pathname !== want.pathname) problems.push(`${path} landed on ${landed.pathname}`);
    if (want.searchParams.has("env") && landed.searchParams.get("env") !== want.searchParams.get("env")) problems.push(`${path} landed on env ${landed.searchParams.get("env")}`);
    if (!(await page.locator("[data-shell='demo']").count())) problems.push("no [data-shell=demo] root");
    problems.push(...(await answerAudit(page, new Set())));
    if (process.env.SCREENSHOT_DIR) {
      fs.mkdirSync(process.env.SCREENSHOT_DIR, { recursive: true });
      const name = path === "/" ? "root" : path.replace(/^\//, "").replace(/\//g, "_");
      await page.screenshot({ path: nodePath.join(process.env.SCREENSHOT_DIR, `${name}.png`) });
    }
    report(`page ${path}`, problems, ` → ${landed.pathname}${landed.search}${unavailable ? `  (${unavailable}× sample data unavailable)` : ""}`);
    await context.close();
  }

  // ── 2. NAV ──
  for (const viewport of viewports) {
    const { context, page, problems } = await openPage(viewport);
    await page.goto(`${base}/`, { waitUntil: "load" });
    await page.getByRole("button", { name: "Explore the running app", exact: true }).click();
    await page.waitForURL((url) => url.pathname === home);
    await page.waitForTimeout(settle);
    const reveal = await revealNav(page, viewport);
    if (reveal || !(await markNav(page))) {
      report(`nav ${viewport.name}`, [reveal ?? "no left nav (a <nav> holding the Overview link) on the console"]);
      await context.close();
      continue;
    }
    // Breadth-first over what the nav shows: opening a section (Observability,
    // Environments) shows its sub-items, which join the queue.
    const inProject = (list) => list.filter((item) => item.href.startsWith(home));
    const queue = inProject(await navItems(page)).filter((i) => i.href !== home);
    const seen = new Set([home, ...queue.map((i) => i.href)]);
    let prev = await pageState(page);
    const dead = [];
    const answered = new Set();
    let clicked = 0;
    const clickItem = async (item) => {
      const navProblem = await revealNav(page, viewport);
      if (navProblem) return dead.push(`${item.label}: ${navProblem}`);
      if (!(await markNav(page))) return dead.push(`${item.label}: the nav is gone after the previous click`);
      await navItems(page);
      const link = page.locator(`[data-smoke-nav] a[href^="${item.href}"]`).filter({ hasText: item.label }).first();
      // A section's sub-items show while the section is open: open it the way
      // a visitor would, with a click on the section.
      if (item.via && !(await link.isVisible().catch(() => false))) {
        const section = page.locator(`[data-smoke-nav] a[href^="${item.via.href}"]`).filter({ hasText: item.via.label }).first();
        await section.click({ timeout: 5000 }).catch(() => {});
        await page.waitForTimeout(settle / 4);
        if (viewport.mobile) await revealNav(page, viewport);
        await markNav(page);
      }
      try {
        await link.click({ timeout: 5000 });
      } catch (err) {
        const lines = String(err.message).split("\n");
        const blocker = lines.find((l) => /intercepts pointer events/.test(l));
        return dead.push(`${item.label} (${item.href}): not clickable — ${(blocker ?? lines[0]).trim()}`);
      }
      clicked++;
      await page.waitForTimeout(settle / 2);
      problems.push(...(await answerAudit(page, answered)).map((p) => `${viewport.name}: ${p}`));
      const now = await pageState(page);
      const why = [];
      if (now.path !== item.href) why.push(`URL ${now.path}, want ${item.href}`);
      if (prev.path !== item.href && now.heading === prev.heading && now.text === prev.text) why.push("heading and content unchanged");
      if (!now.text) why.push("empty page");
      if (why.length) dead.push(`${item.label} (${item.href}): ${why.join("; ")}`);
      else console.log(`       ${viewport.name}: ${item.label} → ${now.path} "${now.heading}"`);
      prev = now;
      if (await revealNav(page, viewport) === null && (await markNav(page))) {
        for (const next of inProject(await navItems(page))) {
          if (!seen.has(next.href)) {
            seen.add(next.href);
            queue.push({ ...next, via: item.via ?? { href: item.href, label: item.label } });
          }
        }
        if (viewport.mobile) await page.keyboard.press("Escape").catch(() => {});
      }
    };
    while (queue.length) await clickItem(queue.shift());
    // Overview last, so that click too leaves the page it is on.
    await clickItem({ href: home, label: "Overview" });
    if (seen.size < 7) problems.push(`only ${seen.size} nav items found`);
    for (const surface of audited) {
      if (!seen.has(surface.path)) problems.push(`no ${surface.path} nav item`);
    }
    problems.push(...dead.map((d) => `dead nav item: ${d}`));
    report(`nav ${viewport.name} ${viewport.width}×${viewport.height}`, problems, ` (${clicked} of ${seen.size} items clicked)`);
    await context.close();
  }
} finally {
  await browser.close();
}
process.exit(failures.length ? 1 : 0);
