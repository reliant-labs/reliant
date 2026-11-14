#!/usr/bin/env node
// Headless smoke check of the forge demo site (demo.html, `npm run build:demo`).
//
//   node scripts/demo/smoke-demo-site.mjs https://demo.reliantlabs.io
//   node scripts/demo/smoke-demo-site.mjs http://127.0.0.1:4173      # vite preview --mode demo
//
// Two passes, and it FAILS (exit 1) on any problem in either.
//
// 1. PAGES: loads `/`, the build story and every forge console page by URL. Problems:
//    - a request to any origin but the site's own (the demo reaches no backend);
//    - a 4xx/5xx response from the site itself (other than /_otel);
//    - an uncaught page error, or a console error naming CORS;
//    - `/` not landing on the Home (/forge/demo?section=home, sections/DemoHome.tsx),
//      or the Home missing any of: its one hero heading; its "Open the console",
//      "Take the tour" and "How this shop was built" actions (each the right
//      role, and the links to the right place); its four numbered stories, each
//      with a title and a link; its agent workflow list, each row with its
//      button. Then on the Home it opens the first workflow's sheet and closes
//      it with Escape, and "Take the tour" must start the tour on the console;
//    - the build story (/forge/demo?section=built) showing no recorded build;
//    - a page with no `[data-shell=demo]` root.
//    Per page it also prints how often "Sample data isn't available" showed (an
//    RPC the demo has no sample data for): a content gap, not a failure.
//
// 2. NAV: starts at `/`, clicks the Home's "Open the console", then every nav
//    item, at desktop 1440×900 and phone 390×844 (where the nav is a drawer
//    behind "Open navigation"). The nav is grouped (Console/groupedNav.tsx and
//    the demo's DemoNavExtras.tsx): Issues, Performance and More tools are
//    buttons that fold, and must hold their pages (navGroups below); every
//    other row (navRows: Home, Overview, Sessions, … How it was built) is a
//    link of its own. Groups are unfolded, and a row with sub-pages
//    (Environments) is opened, so every sub-item is clicked too. An item is
//    dead, and the run fails, when the click does not move the URL to the
//    item's href, or the page's heading and content do not change.
//
// AUDITED READS: Data and Storage read an audited environment only after the
// reader gives a reason, in a dialog naming the environment (AccessReasonDialog),
// asked once per surface and environment for the tab. The demo's prod is audited.
// Both passes answer it as a visitor would: wait for the dialog, type
// "demo smoke <run id>", confirm, then require the read to happen and the page to
// show the reason. It is a failure when the dialog does not appear on an audited
// environment the tab has not answered for, when anything was read before it was
// answered, or when it appears anywhere else. The environment is the one the
// browser rendered with (its root's data-env), not the URL: a nav link need not
// carry ?env=. Pass 1 opens both on prod directly; the nav pass must reach each
// in an audited environment and answer there, so the visitor's path is proved too.
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
// The surfaces that ask: their browser root (which names the environment it
// reads, data-env), and what each shows once it has read.
const audited = [
  { path: `${home}/data`, subject: "data", root: "data-browser", read: (page) => page.getByRole("navigation", { name: "Tables" }).getByRole("link").first() },
  { path: `${home}/storage`, subject: "files", root: "storage-browser", read: (page) => page.getByRole("main").getByRole("table").getByRole("row").nth(1) },
];
// The demo's own pages, `?section=` of the demo console (demoPath.ts).
const demoSection = (section) => `/forge/demo?section=${section}`;
const pages = [
  "/",
  demoSection("built"),
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

// The Home `/` lands on (sections/DemoHome.tsx), found by the test ids it sets.
const homeHeading = "Ship your app safely, see what breaks, and let an agent fix it.";
const homeActions = [
  { id: "home-primary", role: "link", name: "Open the console", to: (url) => url.pathname === home },
  { id: "home-take-tour", role: "button", name: "Take the tour" },
  { id: "home-how-built", role: "link", name: "How this shop was built", to: (url) => url.pathname === "/forge/demo" && url.searchParams.get("section") === "built" },
];
const homeStories = ["slow", "checkout", "release", "people"];
// AGENT_WORKFLOW_ORDER (sections/agentWorkflows.ts).
const agentWorkflows = ["fix-errors", "watch-alerts", "slow-pages", "weekly-digest", "release-guard"];
// The tour "Take the tour" starts (TOUR_START, DemoScreenActions.tsx).
const tourStart = "forge:fg-intro";

// The grouped nav. A group is a button with no page of its own; it must hold
// these pages. A row is a link of its own: its label and where it goes.
const navGroups = {
  Issues: [`${home}/observability/errors`, `${home}/observability/alerts`],
  Performance: [`${home}/observability/traces`, `${home}/observability/rpc`],
  "More tools": [`${home}/observability/dashboards`, `${home}/observability/logs`, `${home}/observability/metrics`, `${home}/observability/graph`, `${home}/storage`],
};
const navRows = [
  { label: "Home", to: (key) => key === demoSection("home") },
  { label: "Overview", to: (key) => key === home },
  { label: "Sessions", to: (key) => key === `${home}/observability/sessions` },
  { label: "Product analytics", to: (key) => key.startsWith(`${home}/observability/dashboards/`) },
  { label: "Releases", to: (key) => key === `${home}/releases` },
  { label: "Data", to: (key) => key === `${home}/data` },
  { label: "Users", to: (key) => key === `${home}/users` },
  { label: "Environments", to: (key) => key === `${home}/environments` },
  { label: "Secrets", to: (key) => key === `${home}/secrets` },
  { label: "Domains", to: (key) => key === `${home}/domains` },
  { label: "Activity", to: (key) => key === `${home}/activity` },
  { label: "How it was built", to: (key) => key === demoSection("built") },
];
/** Where an href goes, as the nav pass compares it: the path, plus a demo page's `?section=`. */
const navKey = (href) => {
  const url = new URL(href, base);
  const section = url.pathname === "/forge/demo" ? url.searchParams.get("section") : null;
  return section ? demoSection(section) : url.pathname;
};
/** A nav item the nav pass clicks: a project page, or a demo page of its own. */
const inScope = (key) => key.startsWith(home) || key.startsWith("/forge/demo?section=");

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
  const state = await page.evaluate(() => {
    const main = document.querySelector("main") ?? document.querySelector("[data-testid='forge-shell']") ?? document.body;
    const heading = main.querySelector("h1")?.textContent?.trim() ?? "";
    const nav = document.querySelector("[data-smoke-nav]");
    const text = [...main.querySelectorAll("*")]
      .filter((el) => !nav?.contains(el) && el.children.length === 0)
      .map((el) => el.textContent.trim())
      .filter(Boolean)
      .join("\n");
    return { href: location.href, heading, text };
  });
  return { path: navKey(state.href), heading: state.heading, text: state.text };
}

/**
 * The Home's hero, actions, stories and agent workflows, as it renders. Returns
 * problems.
 */
async function checkHome(page) {
  const landing = page.getByTestId("demo-home");
  try {
    await landing.waitFor({ timeout: 15000 });
  } catch {
    return ["no visible Home ([data-testid=demo-home])"];
  }
  const problems = [];
  const headings = (await landing.getByRole("heading", { level: 1 }).allTextContents()).map((h) => h.trim());
  if (headings.length !== 1 || headings[0] !== homeHeading) problems.push(`the Home's hero heading is ${JSON.stringify(headings)}, want one h1 ${JSON.stringify(homeHeading)}`);
  for (const action of homeActions) {
    const el = landing.getByTestId(action.id);
    if (!(await el.isVisible())) {
      problems.push(`the Home has no visible "${action.name}" ([data-testid=${action.id}])`);
      continue;
    }
    if ((await landing.getByRole(action.role, { name: action.name, exact: true }).and(el).count()) !== 1) {
      problems.push(`the Home's [data-testid=${action.id}] is not a ${action.role} named "${action.name}" (it reads "${(await el.innerText()).trim()}")`);
    }
    if (action.to) {
      const href = await el.getAttribute("href");
      if (!href || !action.to(new URL(href, base))) problems.push(`the Home's "${action.name}" goes to ${href}`);
    }
  }
  const stories = await landing.locator("[data-testid^='home-story-']").evaluateAll((els) =>
    els.map((el) => ({
      id: el.getAttribute("data-testid").replace(/^home-story-/, ""),
      tag: el.tagName,
      // All four in one ordered list.
      list: el.parentElement?.tagName === "OL" && el.parentElement === els[0].parentElement,
      number: el.querySelector("p")?.textContent.trim() ?? "",
      title: el.querySelector("h3")?.textContent.trim() ?? "",
      link: el.querySelector("a[href]")?.getAttribute("href") ?? "",
      shown: !!el.offsetParent,
    }))
  );
  if (stories.map((s) => s.id).join() !== homeStories.join()) problems.push(`the Home's stories are [${stories.map((s) => s.id)}], want [${homeStories}]`);
  stories.forEach((story, i) => {
    const want = String(i + 1).padStart(2, "0");
    const why = [];
    if (story.tag !== "LI" || !story.list) why.push("not an item of the one numbered list");
    if (!story.number.startsWith(want)) why.push(`numbered "${story.number}", want ${want}`);
    if (!story.title) why.push("no title");
    if (!story.link || new URL(story.link, base).origin !== origin) why.push(`link ${JSON.stringify(story.link)}`);
    if (!story.shown) why.push("not shown");
    if (why.length) problems.push(`the Home's story ${story.id}: ${why.join("; ")}`);
  });
  const list = landing.getByTestId("agent-workflow-list");
  if (!(await list.isVisible())) return [...problems, "the Home has no visible agent workflow list ([data-testid=agent-workflow-list])"];
  const rows = await list.locator(":scope > li").evaluateAll((els) => els.map((el) => el.getAttribute("data-testid")));
  const want = agentWorkflows.map((id) => `agent-card-${id}`);
  if (rows.join() !== want.join()) problems.push(`the Home's agent workflows are [${rows}], want [${want}]`);
  for (const id of agentWorkflows) {
    const open = list.getByTestId(`agent-card-open-${id}`);
    if (!(await open.isVisible()) || (await list.getByRole("button").and(open).count()) !== 1) problems.push(`the Home's agent workflow ${id} has no visible button`);
  }
  return problems;
}

/** On the Home: a workflow's sheet opens and closes, and "Take the tour" starts the tour. Returns problems. */
async function exerciseHome(page) {
  const problems = [];
  const id = agentWorkflows[0];
  await page.getByTestId(`agent-card-open-${id}`).click();
  const sheet = page.getByRole("dialog").filter({ has: page.getByTestId(`agent-sheet-${id}`) });
  try {
    await sheet.waitFor({ timeout: 5000 });
    await page.keyboard.press("Escape");
    await sheet.waitFor({ state: "hidden", timeout: 5000 });
  } catch {
    problems.push(`the Home's ${id} workflow sheet did not open as a dialog, or did not close on Escape`);
  }
  await page.getByTestId("home-take-tour").click();
  try {
    await page.waitForURL((url) => url.pathname === home && url.searchParams.get("tour") === tourStart, { timeout: 10000 });
  } catch {
    problems.push(`"Take the tour" went to ${page.url()}, want ${home}?tour=${tourStart}`);
  }
  return problems;
}

/** The grouped nav's groups and rows, checked against navGroups and navRows. Unfold the groups first. Returns problems. */
async function checkGroupedNav(page) {
  const { groups, rows } = await page.evaluate(() => {
    const nav = document.querySelector("[data-smoke-nav]");
    const href = (a) => new URL(a.getAttribute("href"), location.href).href;
    // A group is a row that is a button; its pages are the links in its list.
    const groups = [...nav.querySelectorAll("li > button[aria-expanded]")].map((button) => ({
      label: button.textContent.trim(),
      items: [...(button.parentElement.querySelector(":scope > ul")?.querySelectorAll("a[href]") ?? [])].map(href),
    }));
    // A row is a link that is not inside another row's list.
    const rows = [...nav.querySelectorAll("a[href]")]
      .filter((a) => !a.closest("li")?.parentElement?.closest("li"))
      .map((a) => ({ label: a.textContent.trim(), href: href(a) }));
    return { groups, rows };
  });
  const problems = [];
  for (const [label, want] of Object.entries(navGroups)) {
    const group = groups.find((g) => g.label === label);
    if (!group) {
      problems.push(`no "${label}" group in the nav (groups: ${groups.map((g) => g.label).join(", ") || "none"})`);
      continue;
    }
    const keys = group.items.map(navKey);
    const missing = want.filter((key) => !keys.includes(key));
    if (missing.length) problems.push(`the "${label}" group lacks ${missing.join(", ")} (it holds ${keys.join(", ")})`);
  }
  for (const row of navRows) {
    const found = rows.filter((r) => r.label === row.label);
    if (!found.length) problems.push(`no "${row.label}" row in the nav`);
    else if (!found.some((r) => row.to(navKey(r.href)))) problems.push(`the nav's "${row.label}" row goes to ${found.map((r) => navKey(r.href)).join(", ")}`);
  }
  return problems;
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
async function answerAudit(page, answered, reached = new Map()) {
  const url = new URL(page.url());
  const surface = audited.find((s) => url.pathname === s.path);
  if (!surface) return [];
  // The environment the browser reads from is the one it rendered with, whatever
  // the URL says: a nav link need not carry ?env=, and the console's scope can
  // fill it in. The browser root names it (data-env, from its env prop).
  const root = page.getByTestId(surface.root);
  try {
    await root.waitFor({ timeout: 15000 });
  } catch {
    return [`${url.pathname}: the ${surface.subject} browser never rendered, so no environment to read`];
  }
  const env = await root.getAttribute("data-env");
  if (!env) return [`${url.pathname}: the ${surface.subject} browser names no environment`];
  const where = `${url.pathname} (${env})`;
  const asked = new URL(page.url()).searchParams.get("env");
  if (asked && asked !== env) return [`${where}: the URL asks for ${asked}, but the ${surface.subject} browser reads ${env}`];
  reached.set(surface.subject, env);
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
  const links = await page.locator("[data-smoke-nav] a[href]").evaluateAll((els) => els.map((a) => [a.getAttribute("href"), a.textContent.trim()]));
  return [...new Map(links.map(([href, label]) => [navKey(href), label])).entries()].map(([href, label]) => ({ href, label }));
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
      if (navKey(landed.href) !== demoSection("home")) problems.push(`/ did not land on the Home, ${demoSection("home")}: ${landed.pathname}${landed.search}`);
      problems.push(...(await checkHome(page)));
    }
    const want = new URL(path, base);
    if (path !== "/" && landed.pathname !== want.pathname) problems.push(`${path} landed on ${landed.pathname}`);
    for (const param of ["env", "section"]) {
      if (want.searchParams.has(param) && landed.searchParams.get(param) !== want.searchParams.get(param)) problems.push(`${path} landed on ${param} ${landed.searchParams.get(param)}`);
    }
    if (want.searchParams.get("section") === "built" && !(await page.getByTestId("how-built").isVisible())) problems.push("build story has no recorded build");
    if (!(await page.locator("[data-shell='demo']").count())) problems.push("no [data-shell=demo] root");
    problems.push(...(await answerAudit(page, new Set())));
    if (process.env.SCREENSHOT_DIR) {
      fs.mkdirSync(process.env.SCREENSHOT_DIR, { recursive: true });
      const name = path === "/" ? "root" : path.replace(/^\//, "").replace(/\//g, "_");
      await page.screenshot({ path: nodePath.join(process.env.SCREENSHOT_DIR, `${name}.png`) });
    }
    // After the screenshot: these leave the Home as it landed.
    if (path === "/" && (await page.getByTestId("demo-home").isVisible())) problems.push(...(await exerciseHome(page)));
    report(`page ${path}`, problems, ` → ${landed.pathname}${landed.search}${unavailable ? `  (${unavailable}× sample data unavailable)` : ""}`);
    await context.close();
  }

  // ── 2. NAV ──
  for (const viewport of viewports) {
    const { context, page, problems } = await openPage(viewport);
    await page.goto(`${base}/`, { waitUntil: "load" });
    // A visitor's way in: the Home's primary action.
    try {
      await page.getByRole("link", { name: "Open the console", exact: true }).and(page.getByTestId("home-primary")).click({ timeout: 30000 });
      await page.waitForURL((url) => url.pathname === home, { timeout: 15000 });
    } catch (err) {
      report(`nav ${viewport.name}`, [`the Home's "Open the console" did not open ${home}: ${String(err.message).split("\n")[0]}`]);
      await context.close();
      continue;
    }
    await page.waitForTimeout(settle);
    const reveal = await revealNav(page, viewport);
    if (reveal || !(await markNav(page))) {
      report(`nav ${viewport.name}`, [reveal ?? "no left nav (a <nav> holding the Overview link) on the console"]);
      await context.close();
      continue;
    }
    // Breadth-first over what the nav shows: unfolding a group (Issues, More
    // tools) or opening a row with sub-pages (Environments) shows its
    // sub-items, which join the queue.
    const inProject = (list) => list.filter((item) => inScope(item.href));
    const queue = inProject(await navItems(page)).filter((i) => i.href !== home);
    problems.push(...(await checkGroupedNav(page)).map((p) => `${viewport.name}: ${p}`));
    const seen = new Set([home, ...queue.map((i) => i.href)]);
    let prev = await pageState(page);
    const dead = [];
    const answered = new Set();
    // Which environment each audited surface read in, when the nav reached it.
    const reached = new Map();
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
      problems.push(...(await answerAudit(page, answered, reached)).map((p) => `${viewport.name}: ${p}`));
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
      // The visitor's path must prove the dialog too, not only the direct links.
      else if (auditedEnvs.length && !answered.has(`${surface.subject}:${reached.get(surface.subject)}`)) {
        const env = reached.get(surface.subject);
        const how = !env ? `never rendered ${surface.path}` : auditedEnvs.includes(env) ? `reached ${surface.path} in ${env} but never answered its dialog` : `reached ${surface.path} in ${env}, which is not audited`;
        problems.push(`the nav ${how}: the audited-read dialog went unproved on the nav path`);
      }
    }
    problems.push(...dead.map((d) => `dead nav item: ${d}`));
    report(`nav ${viewport.name} ${viewport.width}×${viewport.height}`, problems, ` (${clicked} of ${seen.size} items clicked)`);
    await context.close();
  }
} finally {
  await browser.close();
}
process.exit(failures.length ? 1 : 0);
