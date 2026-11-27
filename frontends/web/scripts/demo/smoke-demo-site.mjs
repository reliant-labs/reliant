#!/usr/bin/env node
// Headless smoke check of the forge demo site (demo.html, `npm run build:demo`).
//
//   node scripts/demo/smoke-demo-site.mjs https://demo.reliantlabs.io
//   node scripts/demo/smoke-demo-site.mjs http://127.0.0.1:4173      # vite preview --mode demo
//
// Three passes, and it FAILS (exit 1) on any problem in any of them.
//
// 1. PAGES: loads `/`, the build story and every forge console page by URL. Problems:
//    - a request to any origin but the site's own (the demo reaches no backend);
//    - a 4xx/5xx response from the site itself (other than /_otel, and a 404
//      for /config.js on a local preview: the host serves config.js at runtime,
//      so `vite preview --mode demo` has none, and the demo runs on its defaults);
//    - an uncaught page error, or a console error naming CORS;
//    - `/` not landing on the Home (/forge/demo?section=home, sections/DemoHome.tsx),
//      or the Home missing any of: its one hero heading; its "Open the console",
//      "Take the tour" and "How this shop was built" actions (each the right
//      role, and the links to the right place); its four numbered stories, each
//      with a title and a link; its agent workflow list, each row with its
//      button. Then on the Home it opens the first workflow's sheet and closes
//      it with Escape, and "Take the tour" must start the tour on the console;
//    - the build story (/forge/demo?section=built) showing no recorded build;
//    - a page with no `[data-shell=demo]` root, or not in the app's shell (no
//      `[data-testid=shell-nav]`: every page of the demo is a page of the app's
//      one shell, Layout/shell/AppShell.tsx, with the demo's nav, demoNavConfig.ts);
//    - the sample image's preview (Storage, staging) not rendering, or its URL
//      not same-origin or shaped like a presigned GCS/S3 link;
//    - Experiments listing no change, the A/B test showing no results readout,
//      or either saying Experiments aren't available (the shop pack answers
//      ChangesService, fixtures/shop/handlers/changes.ts).
//    - the AGENT PAGES (agentPages below: /inbox, the project's chats, two chat
//      transcripts, /new, the workflow library and runs, Connectors, Agent
//      sandboxes and the /machines alias) each missing their content (the Inbox's
//      sections, a transcript's messages, the runs, the library's workflows, the
//      chat list, the composer) or their heading, or reading an RPC no sample
//      data answers (`window.__demoRpcs.unknown`, demoTransport.ts: an unanswered
//      read is a missing fixture). A run's detail by direct URL is warn-only: it
//      can say "This run could not be loaded" (a known bug; opening a run from the
//      list works, and the AGENTS pass does it).
//    Per page it also prints how often "Sample data isn't available" showed (an
//    RPC the demo has no sample data for): a content gap, not a failure (on the
//    other pages; on the agent pages an unknown RPC fails).
//
//    Known console noise, never a failure: "[project-queries] Failed to touch
//    project" (TouchProject is a refused write) and "[InterleavedTimeline] Thread
//    has messages but no workflow row".
//
// 2. NAV: starts at `/`, clicks the Home's "Open the console", then every nav
//    item, at desktop 1440×900 and phone 390×844 (where the nav is the shell's
//    drawer, opened by the page header's toggle, `[data-testid=shell-nav-toggle]`).
//    The nav is the shell's (`[data-testid=shell-nav]`), drawn from the
//    signed-in app's own items in its order (Layout/sidebar/navConfig/hybrid.ts,
//    Forge/Demo/demoNavConfig.ts) plus the demo's own rows: Dashboards, Analytics,
//    Deployments, Tests, Security and Configure are buttons that fold, and must
//    hold their pages (navGroups below); Observability is a drill-in (navDrill):
//    its row opens its Overview and its rows replace the nav under a "‹ Back"
//    header; Agents is a second drill-in (its row opens the project's chats; its
//    rows are New chat, Inbox, Workflows (a group: Runs, Library), Sandboxes and
//    Connectors, then the chat list, which is not a nav page: its rows are
//    buttons, `[data-chat-row]`, never walked); every other row (navRows: Home,
//    Overview, Inbox (the app's slot row, with its count), Agents, Data, Storage,
//    Auth, How it was built) is a link of its own. The org row, Find… and the
//    project switcher must be there too. The pages the demo does not serve
//    (navAbsent: Usage) must have no item. Groups are unfolded, so every
//    sub-item is clicked too; then each drill-in is entered, each of its rows
//    clicked (re-entering the drill-in after a row that leaves its area, as
//    Inbox does), and left by its "‹ Back" header (which must land on the
//    Overview). An item is dead, and the run fails, when the click does not
//    move the URL to the item's href, or the page's heading and content do not
//    change.
//
// 3. AGENTS (desktop only): a visitor's path through the agent surfaces, in one
//    tab, by clicking:
//    - Home → the nav's Inbox row → /inbox shows ≥1 section and ≥1 item; the
//      first item's link opens its chat or run;
//    - the nav's Agents row → the drill-in; the chat list has ≥1 row; its first
//      opens /c/<id>, with that row current ([data-chat-row][aria-current=true])
//      and `[data-testid=chat-messages]` rendering ≥1 message with text; a
//      thread link (`a[href*="/t/"]`) in the transcript opens, when there is one
//      (optional);
//    - Find… opens the command palette, and Escape closes it;
//    - New chat opens /new with the composer ([data-testid=chat-input]);
//      sending is refused with the toast "This is a demo. Changes are turned
//      off.", the transport records the refused StartChat, the URL stays /new;
//    - at the end no RPC was left unanswered (`window.__demoRpcs.unknown`), and
//      nothing went to another origin (every pass).
//    A run opened by direct URL that says "This run could not be loaded" is a
//    warning (a known bug), never a failure; opening one from a list must work.
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
// CHROME: PLAYWRIGHT_CHROMIUM_EXECUTABLE (default /usr/bin/google-chrome when it
// exists, else Playwright's own chromium).
// SCREENSHOT_DIR=<dir> saves each page (pass 1) as <dir>/<path>.png.
// SETTLE_MS: how long a page gets to render (default 4000; nav clicks get half).

import fs from "node:fs";
import nodePath from "node:path";
import { chromium } from "playwright";

const base = (process.argv[2] ?? "http://127.0.0.1:4173").replace(/\/$/, "");
const origin = new URL(base).origin;
/** A local preview (vite preview), where no host serves the runtime config.js. */
const localPreview = ["127.0.0.1", "localhost", "[::1]"].includes(new URL(base).hostname);
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
// The sample bucket's one image, opened in staging (not audited). Its preview
// URL must be the fixture's obviously fake one (handlers/storage.ts
// SAMPLE_IMAGE_URL): same-origin, never shaped like a presigned GCS/S3 link.
const sampleImagePage = `${home}/storage?env=staging&bucket=uploads&object=product-photo.png`;
const PRESIGNED = /storage\.googleapis\.com|amazonaws\.com|X-Goog-|X-Amz-|Signature=|GoogleAccessId/i;
// Experiments: the list, and the shop's A/B test (model/changes.ts), which has a readout.
const experimentsPage = `${home}/experiments`;
const experimentPage = `${home}/experiments/chg-express-checkout-prod`;
// The demo's own pages, `?section=` of the demo console (demoPath.ts).
const demoSection = (section) => `/forge/demo?section=${section}`;
// The agent pages, by URL (the app's own pages, on the shop's app-layer fixtures). Each: where it
// lands (`lands`, when an alias), its h1 (`heading`), and what must render (`present`: selector →
// minimum count). `soft`: a page with a known gap today (a run's detail by direct URL says "This run
// could not be loaded"; a chat with no recorded transcript; a trigger no sample data answers): its
// problems are warnings. Every other one fails on an unknown RPC.
const shopProjectId = "shop-proj-shop";
const agentChat = "shop-chat-rate-limit";
const agentPages = [
  { path: "/inbox", heading: "Inbox", present: { "[data-testid^=inbox-section-]": 1, "[data-testid^=inbox-item-]": 1 } },
  { path: `/project/${shopProjectId}`, present: { "[data-chat-row]": 1 } },
  { path: "/c/shop-chat-cart-totals", present: { "[data-testid=chat-messages] [data-testid^=message-]": 1 } },
  { path: "/c/shop-chat-load-test", present: { "[data-testid=chat-messages] [data-testid^=message-]": 1 } },
  // Soft: this chat is in the list but the pack records no transcript for it (a fixture gap), so it opens empty.
  { path: `/c/${agentChat}`, heading: "Add rate limiting to /checkout", soft: true, present: { "[data-testid=chat-messages] [data-testid^=message-]": 1 } },
  { path: `/new?project=${shopProjectId}`, present: { "[data-testid=chat-input]": 1 } },
  { path: "/workflows/library", heading: "Workflows", present: { "[data-testid^=workflow-row-]": 1 } },
  { path: "/workflows/library/nightly-ci-triage", heading: "Nightly CI triage" },
  // Where the Inbox's "automation failing" item links.
  // Soft: the trigger's detail reads TriggerService/GetTrigger, which no sample data answers (a fixture gap).
  { path: `/workflows/library/nightly-ci-triage?project=${shopProjectId}#trigger-shop-trg-nightly`, heading: "Nightly CI triage", soft: true },
  { path: "/workflows/runs", heading: "Runs", present: { "[data-testid^=run-row-]": 1 } },
  { path: `/workflows/runs/${agentChat}`, soft: true },
  { path: "/connectors", heading: "Connectors" },
  { path: "/settings/machines", heading: "Agent sandboxes" },
  { path: "/machines", lands: "/settings/machines", heading: "Agent sandboxes" },
];
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
  `${home}/site-insights`,
  `${home}/security/vulnerabilities`,
  `${home}/security/sboms`,
  `${home}/tests`,
  `${home}/tests/coverage`,
  `${home}/tests/runs`,
  `${home}/tests/flaky`,
  experimentsPage,
  experimentPage,
  ...(auditedEnvs.length ? audited.map((surface) => `${surface.path}?env=${auditedEnvs[0]}`) : []),
  sampleImagePage,
  ...agentPages.map((spec) => spec.path),
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

// The shell's nav. A group is a button with no page of its own; it must hold
// these pages. A row is a link of its own: its label and where it goes.
const navGroups = {
  Dashboards: [`${home}/observability/dashboards`],
  Analytics: [`${home}/analytics`, ...["users", "product", "acquisition", "events"].map((tab) => `${home}/analytics/${tab}`), experimentsPage],
  Deployments: [`${home}/services`, `${home}/releases`, `${home}/environments`],
  Tests: [`${home}/tests`, `${home}/tests/coverage`, `${home}/tests/runs`, `${home}/tests/flaky`],
  Security: [`${home}/security/vulnerabilities`, `${home}/security/sboms`],
  Configure: [`${home}/secrets`, `${home}/domains`, `${home}/activity`],
};
// A drill-in: its row (by test id) opens its first page (`first`, else pages[0]); on its routes its
// rows replace the nav, under a "‹ <label>" header (nav-back) back to the Overview. `pages` are the
// rows it must hold (the `first` page is the area's own, not a row, when named apart).
const navDrill = {
  Observability: {
    row: "nav-observability",
    area: "observability",
    pages: [
      `${home}/observability`,
      ...["graph", "metrics", "logs", "rpc", "profiles", "errors", "alerts", "traces", "sessions"].map((view) => `${home}/observability/${view}`),
      `${home}/site-insights`,
    ],
  },
  // Agents ›: New chat, Inbox, Workflows ▾ (Runs, Library), Sandboxes, Connectors, then the chat list.
  // Its first page is the project's chats; Inbox is not an Agents route, so clicking it leaves the drill-in.
  Agents: {
    row: "nav-agents",
    area: "agents",
    first: `/project/${shopProjectId}`,
    pages: ["/new", "/inbox", "/workflows/runs", "/workflows/library", "/settings/machines", "/connectors"],
  },
};
const navRows = [
  { label: "Home", to: (key) => key === demoSection("home") },
  { label: "Overview", to: (key) => key === home },
  { label: "Inbox", to: (key) => key === "/inbox" },
  { label: "Agents", to: (key) => key === `/project/${shopProjectId}` },
  { label: "Observability", to: (key) => key === `${home}/observability` },
  { label: "Data", to: (key) => key === `${home}/data` },
  { label: "Storage", to: (key) => key === `${home}/storage` },
  // Users became Auth (/users redirects there).
  { label: "Auth", to: (key) => key === `${home}/auth` },
  { label: "How it was built", to: (key) => key === demoSection("built") },
];
// The pages the demo does not serve (DEMO_HIDDEN_PAGES, demoPath.ts): no nav item may lead there.
const navAbsent = [`${home}/usage`];
// A page that is the same screen as the one before it: New chat from the project's chats is the same composer.
const sameScreen = { "/new": `/project/${shopProjectId}` };
/** The agent surfaces' routes (the Agents drill-in's pages, and Inbox). */
const agentRoute = /^\/(inbox|new|project\/|c\/|workflows(\/|$)|settings\/machines|connectors)/;
/** Where an href goes, as the nav pass compares it: the path, plus a demo page's `?section=`. */
const navKey = (href) => {
  const url = new URL(href, base);
  const section = url.pathname === "/forge/demo" ? url.searchParams.get("section") : null;
  return section ? demoSection(section) : url.pathname;
};
/** A nav item the nav pass clicks: a project page, or a demo page of its own. */
const inScope = (key) => key.startsWith(home) || key.startsWith("/forge/demo?section=") || agentRoute.test(key);

const systemChrome = "/usr/bin/google-chrome";
const executablePath = process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE ?? (fs.existsSync(systemChrome) ? systemChrome : undefined);
const browser = await chromium.launch({ executablePath, args: ["--no-sandbox"] });
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
  const warnings = [];
  page.on("request", (req) => {
    const url = new URL(req.url());
    if (!["http:", "https:", "ws:", "wss:"].includes(url.protocol)) return;
    if (url.origin !== origin) problems.push(`request to another origin: ${req.method()} ${req.url()}`);
  });
  page.on("response", (res) => {
    const url = new URL(res.url());
    if (url.origin !== origin || res.status() < 400 || url.pathname.startsWith("/_otel")) return;
    if (localPreview && res.status() === 404 && url.pathname === "/config.js") return;
    // Leaving a running chat beacons a CancelChat write to the site's own origin (hooks/useCancelOnUnload.ts): a known bug in the demo, a warning.
    if (url.pathname === "/reliant.v1.ChatService/CancelChat") return warnings.push(`${res.status()} ${url.pathname} (known: the unload beacon of a running chat)`);
    problems.push(`${res.status()} ${url.pathname}`);
  });
  page.on("pageerror", (err) => problems.push(`page error: ${err.message}`));
  page.on("console", (msg) => {
    if (msg.type() === "error" && /cors|access-control/i.test(msg.text())) problems.push(`console: ${msg.text()}`);
  });
  return { context, page, problems, warnings };
}

function report(label, problems, detail = "", warnings = []) {
  console.log(`${problems.length ? "FAIL" : "ok  "} ${label}${detail}`);
  for (const p of problems) console.log(`       ${p}`);
  for (const w of warnings) console.log(`       warn: ${w}`);
  failures.push(...problems.map((p) => `${label}: ${p}`));
}

/** The sample image's preview: shown, from the page's own origin, nothing like a presigned link. Returns problems. */
async function checkSampleImage(page) {
  const img = page.getByTestId("storage-preview-image");
  try {
    await img.waitFor({ timeout: 15000 });
  } catch {
    return ["the sample image's preview ([data-testid=storage-preview-image]) did not render"];
  }
  const src = (await img.getAttribute("src")) ?? "";
  if (!src.trim()) return ["the sample image's preview URL is empty"];
  const problems = [];
  if (new URL(src, base).origin !== origin) problems.push(`the sample image's preview URL is not same-origin: ${src}`);
  if (PRESIGNED.test(src)) problems.push(`the sample image's preview URL looks presigned: ${src}`);
  try {
    await page.waitForFunction(() => {
      const image = document.querySelector('[data-testid="storage-preview-image"]');
      return image?.complete && image.naturalWidth > 0;
    }, null, { timeout: 15000 });
  } catch {
    problems.push(`the sample image's preview did not load: ${src}`);
  }
  return problems;
}

/** What a run's detail page reads that the sample data does not answer: the known "This run could not be loaded" bug, a warning. */
const knownUnknownRpcs = ["reliant.v1.RunService/GetRun", "reliant.v1.TriggerService/GetLaunchEvent"];
/** The RPCs the demo's sample data did not answer so far in this tab (`window.__demoRpcs.unknown`, demoTransport.ts), less the known run-detail gap. */
const unknownRpcs = async (page) => (await page.evaluate(() => [...new Set(window.__demoRpcs?.unknown ?? [])])).filter((rpc) => !knownUnknownRpcs.includes(rpc));
/** The RPCs the demo's read-only guard refused so far in this tab. */
const refusedRpcs = (page) => page.evaluate(() => [...new Set(window.__demoRpcs?.refused ?? [])]);
/** Whether the page says a run "could not be loaded" (the known direct-URL bug). */
const runNotLoaded = (page) => page.getByText("This run could not be loaded").first().isVisible().catch(() => false);

/**
 * An agent page by URL (agentPages): its heading, what must render, and no unanswered read.
 * Returns problems; for a `soft` page they are only warnings.
 */
async function checkAgentPage(page, spec) {
  const problems = [];
  if (await runNotLoaded(page)) return [`${spec.path}: "This run could not be loaded" (known: opening a run by direct URL)`];
  const heading = await page.locator("[data-testid='shell-main'] h1").first().textContent({ timeout: 5000 }).catch(() => null);
  if (spec.heading && heading?.trim() !== spec.heading) problems.push(`${spec.path}: heading is ${JSON.stringify(heading?.trim() ?? null)}, want ${JSON.stringify(spec.heading)}`);
  for (const [selector, min] of Object.entries(spec.present ?? {})) {
    const found = await page.locator(selector).count();
    if (found < min) problems.push(`${spec.path}: ${found} of ${selector}, want at least ${min}`);
  }
  const unknown = await unknownRpcs(page);
  if (unknown.length) problems.push(`${spec.path}: RPCs no sample data answers (a missing fixture): ${unknown.join(", ")}`);
  return problems;
}

/** What a nav click must change: the main heading and the page's text. */
async function pageState(page) {
  const state = await page.evaluate(() => {
    // The shell's one main region (AppShellFrame); the nav is beside it, not in it.
    const main = document.querySelector("[data-testid='shell-main']") ?? document.querySelector("main") ?? document.body;
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

/** The shell nav's groups and rows (top level, footer), checked against navGroups, navRows and navAbsent. Unfold the groups first. Returns problems. */
async function checkShellNav(page) {
  const { groups, rows, all, top } = await page.evaluate(() => {
    // A row's label: its text, less a badge (a count) after it (Layout/sidebar/primitives.tsx: the label is `.truncate`).
    const labelOf = (el) => (el.querySelector(".truncate") ?? el).textContent.trim();
    const nav = document.querySelector("[data-smoke-nav]");
    const href = (a) => new URL(a.getAttribute("href"), location.href).href;
    // A group is its block (NavRenderer: `nav-<id>-group`): the row is a button, its pages the links in its list.
    const groups = [...nav.querySelectorAll("[data-testid^='nav-'][data-testid$='-group']")].map((block) => {
      const button = block.querySelector("button[aria-expanded]");
      return { label: button ? labelOf(button) : "", items: [...block.querySelectorAll("[role='group'] a[href]")].map(href) };
    });
    // A row is a link that is not inside a group's list.
    const rows = [...nav.querySelectorAll("a[href]")].filter((a) => !a.closest("[role='group']")).map((a) => ({ label: labelOf(a), href: href(a) }));
    const all = [...nav.querySelectorAll("a[href]")].map(href);
    return { groups, rows, all, top: !!nav.querySelector("[data-testid='nav-top']") };
  });
  const problems = [];
  if (!top) problems.push("the nav is not at its top level (no [data-testid=nav-top]) on the Overview");
  // The header: the org row, Find… and the project switcher.
  for (const [id, text] of [["org-switcher", "Acme"], ["nav-find", "Find"], ["shell-project-switcher", project]]) {
    const el = page.locator(`[data-smoke-nav] [data-testid='${id}']`);
    if (!(await el.count())) problems.push(`the nav has no [data-testid=${id}]`);
    else if (!(await el.first().innerText()).includes(text)) problems.push(`the nav's [data-testid=${id}] reads "${(await el.first().innerText()).trim()}", want it to name "${text}"`);
  }
  for (const key of navAbsent) if (all.map(navKey).includes(key)) problems.push(`the nav links to ${key}, a page the demo does not serve`);
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

/** Marks the shell's nav (`[data-testid=shell-nav]`, AppShellFrame) and returns whether it was found. */
async function markNav(page) {
  return page.evaluate(() => {
    const nav = document.querySelector("[data-testid='shell-nav']");
    if (!nav) return false;
    nav.setAttribute("data-smoke-nav", "");
    return true;
  });
}

/** On the phone the nav is the shell's drawer: open it with the page header's toggle. Returns a problem, or null. */
async function revealNav(page, viewport) {
  if (!viewport.mobile) return null;
  const nav = page.getByTestId("shell-nav");
  if ((await nav.getAttribute("data-state").catch(() => null)) === "open") return null;
  // The shell's page header has the toggle; a chat page's own header has "Toggle Chat Sidebar" (it opens the same nav).
  const open = page.locator("[data-testid='shell-nav-toggle'], [aria-label='Toggle Chat Sidebar']").first();
  if (!(await open.count())) return `no page-header toggle ([data-testid=shell-nav-toggle] or "Toggle Chat Sidebar") on ${new URL(page.url()).pathname}: the nav is unreachable on a phone`;
  await open.click();
  try {
    await page.locator("[data-testid='shell-nav'][data-state='open']").waitFor({ timeout: 5000 });
  } catch {
    return "the page header's toggle did not open the nav drawer";
  }
  await page.waitForTimeout(300);
  return null;
}

/**
 * Closes the phone's drawer, if it is open, as a visitor does: a tap on the
 * page beside it (the drawer's backdrop, AppShellFrame). Not Escape: on a
 * console page the forge layout takes Escape as "leave the console".
 */
async function hideNav(page, viewport) {
  if (!viewport.mobile) return;
  const nav = page.getByTestId("shell-nav");
  if ((await nav.getAttribute("data-state").catch(() => null)) !== "open") return;
  await page.mouse.click(viewport.width - 8, Math.round(viewport.height / 2)).catch(() => {});
  await page.locator("[data-testid='shell-nav'][data-state='closed']").waitFor({ timeout: 3000 }).catch(() => {});
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

/**
 * The nav's items now, as hrefs (query dropped) in nav order, with their
 * labels: the top level's or the drill-in's, and the footer's. Folded groups
 * are unfolded first (only the nav's groups: the header's switchers are
 * menus). The drill-in's "‹ Back" header is not an item.
 */
async function navItems(page, within = "[data-smoke-nav]") {
  const folded = page.locator("[data-smoke-nav] [data-testid^='nav-'][data-testid$='-group'] button[aria-expanded='false']");
  for (let i = await folded.count(); i > 0; i--) await folded.nth(0).click().catch(() => {});
  const links = await page
    .locator(`${within} a[href]:not([data-testid='nav-back']):not([data-chat-row])`)
    .evaluateAll((els) => els.map((a) => [a.getAttribute("href"), (a.querySelector(".truncate") ?? a).textContent.trim(), a.getAttribute("data-testid")]));
  return [...new Map(links.map(([href, label, testId]) => [navKey(href), { label, testId }])).entries()].map(([href, { label, testId }]) => ({ href, label, testId }));
}

try {
  // ── 1. PAGES ──
  for (const path of pages) {
    const { context, page, problems, warnings } = await openPage(viewports[0]);
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
    const agentSpec = agentPages.find((spec) => spec.path === path);
    const wantPath = agentSpec?.lands ?? want.pathname;
    if (path !== "/" && landed.pathname !== wantPath) problems.push(`${path} landed on ${landed.pathname}`);
    for (const param of ["env", "section"]) {
      if (want.searchParams.has(param) && landed.searchParams.get(param) !== want.searchParams.get(param)) problems.push(`${path} landed on ${param} ${landed.searchParams.get(param)}`);
    }
    if (want.searchParams.get("section") === "built" && !(await page.getByTestId("how-built").isVisible())) problems.push("build story has no recorded build");
    if (!(await page.locator("[data-shell='demo']").count())) problems.push("no [data-shell=demo] root");
    if (!(await page.getByTestId("shell-nav").count())) problems.push("not in the app's shell (no [data-testid=shell-nav])");
    problems.push(...(await answerAudit(page, new Set())));
    if (path === sampleImagePage) problems.push(...(await checkSampleImage(page)));
    if (path === experimentsPage || path === experimentPage) {
      if (/Experiments aren't available/.test(text)) problems.push(`${path} says Experiments aren't available`);
      if (path === experimentsPage && !(await page.locator("[data-testid^='change-row-']").count())) problems.push("Experiments lists no change");
      if (path === experimentPage && !(await page.getByTestId("results-verdict").count())) problems.push("the A/B test shows no results readout");
    }
    if (agentSpec) (agentSpec.soft ? warnings : problems).push(...(await checkAgentPage(page, agentSpec)));
    if (process.env.SCREENSHOT_DIR) {
      fs.mkdirSync(process.env.SCREENSHOT_DIR, { recursive: true });
      const name = path === "/" ? "root" : path.replace(/^\//, "").replace(/\//g, "_");
      await page.screenshot({ path: nodePath.join(process.env.SCREENSHOT_DIR, `${name}.png`) });
    }
    // After the screenshot: these leave the Home as it landed.
    if (path === "/" && (await page.getByTestId("demo-home").isVisible())) problems.push(...(await exerciseHome(page)));
    report(`page ${path}`, problems, ` → ${landed.pathname}${landed.search}${unavailable ? `  (${unavailable}× sample data unavailable)` : ""}`, warnings);
    await context.close();
  }

  // ── 2. NAV ──
  for (const viewport of viewports) {
    const { context, page, problems, warnings } = await openPage(viewport);
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
      report(`nav ${viewport.name}`, [reveal ?? "no shell nav ([data-testid=shell-nav]) on the console"]);
      await context.close();
      continue;
    }
    const inProject = (list) => list.filter((item) => inScope(item.href));
    const reported = new Set();
    const pageState_path = (pg) => new URL(pg.url()).pathname;
    const drillRows = new Set(Object.values(navDrill).map((d) => d.row));
    // The top level: every row and group page but a drill-in's own row (walked below).
    const queue = inProject(await navItems(page)).filter((i) => i.href !== home && !drillRows.has(i.testId));
    problems.push(...(await checkShellNav(page)).map((p) => `${viewport.name}: ${p}`));
    const seen = new Set([home, ...queue.map((i) => i.href)]);
    let prev = await pageState(page);
    const dead = [];
    const answered = new Set();
    // Which environment each audited surface read in, when the nav reached it.
    const reached = new Map();
    let clicked = 0;
    /** Clicks `link` (a nav item) and checks the page it opens. Returns whether the click happened. */
    const openItem = async (item, link) => {
      try {
        await link.click({ timeout: 5000 });
      } catch (err) {
        const lines = String(err.message).split("\n");
        const blocker = lines.find((l) => /intercepts pointer events/.test(l));
        dead.push(`${item.label} (${item.href}): not clickable — ${(blocker ?? lines[0]).trim()}`);
        return false;
      }
      clicked++;
      await page.waitForTimeout(settle / 2);
      problems.push(...(await answerAudit(page, answered, reached)).map((p) => `${viewport.name}: ${p}`));
      const now = await pageState(page);
      const why = [];
      if (now.path !== item.href) why.push(`URL ${now.path}, want ${item.href}`);
      if (prev.path !== item.href && sameScreen[item.href] !== prev.path && now.heading === prev.heading && now.text === prev.text) why.push("heading and content unchanged");
      if (!now.text) why.push("empty page");
      if (why.length) dead.push(`${item.label} (${item.href}): ${why.join("; ")}`);
      else console.log(`       ${viewport.name}: ${item.label} → ${now.path} "${now.heading}"`);
      prev = now;
      return true;
    };
    // The item's own path, with or without a query: a prefix would also match a page under it
    // (Observability's "Overview" is /observability; the Dashboards group lists a dashboard titled "Overview" below it).
    const exact = (href) => `[data-smoke-nav] a[href="${href}"], [data-smoke-nav] a[href^="${href}?"]`;
    const clickItem = async (item) => {
      const navProblem = await revealNav(page, viewport);
      if (navProblem) return dead.push(`${item.label}: ${navProblem}`);
      if (!(await markNav(page))) return dead.push(`${item.label}: the nav is gone after the previous click`);
      await navItems(page);
      const link = page.locator(exact(item.href)).filter({ hasText: item.label }).first();
      if (!(await openItem(item, link))) return;
      if ((await revealNav(page, viewport)) === null && (await markNav(page))) {
        for (const next of inProject(await navItems(page))) {
          if (!seen.has(next.href) && !drillRows.has(next.testId)) {
            seen.add(next.href);
            queue.push(next);
          }
        }
        await hideNav(page, viewport);
      }
    };
    while (queue.length) await clickItem(queue.shift());

    // The drill-ins: the row opens the area's first page and the area's rows take the nav; each row
    // is clicked there; "‹ Back" leaves for the Overview, where the top level is back.
    for (const [label, drill] of Object.entries(navDrill)) {
      const enter = async () => {
        const navProblem = await revealNav(page, viewport);
        if (navProblem) return navProblem;
        await markNav(page);
        return null;
      };
      let problem = await enter();
      if (problem) {
        dead.push(`${label}: ${problem}`);
        continue;
      }
      const row = page.locator(`[data-smoke-nav] [data-testid='${drill.row}']`);
      if (!(await row.count())) {
        dead.push(`${label}: no drill-in row ([data-testid=${drill.row}]) in the nav`);
        continue;
      }
      if (!(await openItem({ label, href: drill.first ?? drill.pages[0] }, row.first()))) continue;
      seen.add(drill.first ?? drill.pages[0]);
      problem = await enter();
      const drillIn = page.locator(`[data-smoke-nav] [data-testid='nav-drill-in'][data-area='${drill.area}']`);
      if (problem || !(await drillIn.count())) {
        dead.push(`${label}: its row did not drill in ([data-testid=nav-drill-in][data-area=${drill.area}])${problem ? `: ${problem}` : ""}`);
        continue;
      }
      // The drill-in's own rows (the footer stays under it, and was walked above).
      const items = inProject(await navItems(page, "[data-smoke-nav] [data-testid='nav-drill-in']"));
      const keys = items.map((i) => i.href);
      const missing = drill.pages.filter((p) => !keys.includes(p));
      if (missing.length) problems.push(`${viewport.name}: the ${label} drill-in lacks ${missing.join(", ")} (it holds ${keys.join(", ")})`);
      for (const key of navAbsent) if (keys.includes(key)) problems.push(`${viewport.name}: the ${label} drill-in links to ${key}, a page the demo does not serve`);
      await hideNav(page, viewport);
      /** Back in the drill-in, when the last click left its area (Inbox is not an Agents route). Returns a problem, or null. */
      const reenter = async () => {
        const navProblem = await enter();
        if (navProblem) {
          // The page has no way to the nav (reported once per page): reload into the area to go on.
          if (!reported.has(pageState_path(page))) {
            reported.add(pageState_path(page));
            // Known app bug: Agent sandboxes has no page-header toggle, so on a phone its nav is unreachable.
            (pageState_path(page) === "/settings/machines" ? warnings : problems).push(`${viewport.name}: ${navProblem} (known: Agent sandboxes has no nav toggle)`);
          }
          await page.goto(`${base}${drill.first ?? drill.pages[0]}`, { waitUntil: "load" });
          await page.waitForTimeout(settle);
          return (await revealNav(page, viewport)) ?? ((await markNav(page)) ? null : "no shell nav after reloading");
        }
        if (await drillIn.count()) return null;
        await row.first().click({ timeout: 5000 }).catch(() => {});
        await page.waitForTimeout(settle / 2);
        const again = await enter();
        if (again) return again;
        return (await drillIn.count()) ? null : `its row did not drill in again after leaving its area`;
      };
      for (const item of items.filter((i) => i.href !== (drill.first ?? drill.pages[0]))) {
        seen.add(item.href);
        problem = await reenter();
        if (problem) {
          dead.push(`${item.label}: ${problem}`);
          continue;
        }
        await openItem(item, page.locator(exact(item.href)).filter({ hasText: item.label }).first());
        await hideNav(page, viewport);
      }
      problem = await reenter();
      const back = page.locator("[data-smoke-nav] [data-testid='nav-back']");
      if (problem || !(await back.count())) {
        dead.push(`${label}: no "‹ Back" header in its drill-in${problem ? `: ${problem}` : ""}`);
        continue;
      }
      await openItem({ label: `‹ Back from ${label}`, href: home }, back.first());
      await enter();
      if (!(await page.locator("[data-smoke-nav] [data-testid='nav-top']").count())) dead.push(`‹ Back from ${label}: the nav did not return to its top level`);
      await hideNav(page, viewport);
    }

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
    const unanswered = await unknownRpcs(page);
    if (unanswered.length) problems.push(`${viewport.name}: RPCs no sample data answers (a missing fixture): ${unanswered.join(", ")}`);
    report(`nav ${viewport.name} ${viewport.width}×${viewport.height}`, problems, ` (${seen.size} items, ${clicked} clicks: each item, the drill-ins' ‹ Back and the Overview last)`, warnings);
    await context.close();
  }

  // ── 3. AGENTS ──
  {
    const { context, page, problems, warnings } = await openPage(viewports[0]);
    const label = `agents ${viewports[0].name} ${viewports[0].width}×${viewports[0].height}`;
    const steps = [];
    const shot = async (name) => {
      if (!process.env.SCREENSHOT_DIR) return;
      fs.mkdirSync(process.env.SCREENSHOT_DIR, { recursive: true });
      await page.screenshot({ path: nodePath.join(process.env.SCREENSHOT_DIR, `demo-${name}.png`) });
    };
    const pathOf = () => new URL(page.url()).pathname;
    // A full page load drops window.__demoRpcs, so what the tab could not answer is kept across loads.
    const unknown = new Set();
    const visit = async (path) => {
      for (const rpc of await unknownRpcs(page).catch(() => [])) unknown.add(rpc);
      await page.goto(`${base}${path}`, { waitUntil: "load" });
    };
    const nav = (id) => page.locator(`[data-testid='shell-nav'] [data-testid='${id}']`).first();
    const step = async (name, run) => {
      try {
        await run();
        steps.push(name);
      } catch (err) {
        problems.push(`${name}: ${String(err.message).split("\n")[0]}`);
        return false;
      }
      return true;
    };
    /** Clicks and waits for the URL, as a visitor's click does. */
    const go = async (locator, to) => {
      await locator.click({ timeout: 8000 });
      await page.waitForURL((url) => to.test(url.pathname), { timeout: 10000 });
      await page.waitForTimeout(settle / 2);
    };
    const need = (ok, why) => {
      if (!ok) throw new Error(why);
    };

    await visit("/");
    await page.getByTestId("home-primary").click({ timeout: 30000 }).catch(() => {});
    await page.waitForURL((url) => url.pathname === home, { timeout: 15000 }).catch(() => problems.push(`the Home's "Open the console" did not open ${home}`));
    await page.waitForTimeout(settle);
    await shot("nav-top");

    // Inbox: the nav's row, then the page's sections and items, then an item's link.
    await step("Inbox row → /inbox", async () => {
      await go(nav("sidebar-inbox-button"), /^\/inbox$/);
      const sections = await page.locator("[data-testid^=inbox-section-]").count();
      const items = await page.locator("[data-testid^=inbox-item-]").count();
      need(sections >= 1 && items >= 1, `the Inbox shows ${sections} sections and ${items} items, want at least 1 of each`);
      await shot("inbox");
    });
    await step("an Inbox item opens its chat or run", async () => {
      const link = page.locator("[data-testid^=inbox-item-] a[href]").first();
      need(await link.count(), "no Inbox item has a link");
      const href = await link.getAttribute("href");
      await link.click({ timeout: 8000 });
      await page.waitForURL((url) => /^\/(c\/|workflows\/(runs|library)\/)/.test(url.pathname), { timeout: 10000 });
      await page.waitForTimeout(settle / 2);
      if (await runNotLoaded(page)) warnings.push(`the Inbox item's run ${href} said "This run could not be loaded"`);
    });

    // Agents: the drill-in, the chat list, a chat.
    await step("Agents row → drill-in", async () => {
      await visit(home);
      await page.waitForTimeout(settle);
      await go(nav("nav-agents"), new RegExp(`^/project/${shopProjectId}$`));
      need(await page.locator("[data-testid='shell-nav'] [data-testid='nav-drill-in'][data-area='agents']").count(), "the Agents row did not drill in ([data-testid=nav-drill-in][data-area=agents])");
      const rows = await page.locator("[data-testid='sidebar-chats'] [data-chat-row]").count();
      need(rows >= 1, `the Agents chat list has ${rows} rows, want at least 1`);
      await shot("nav-agents");
    });
    let chatPath = "";
    await step("first chat row → /c/<id> with a transcript", async () => {
      await page.locator("[data-testid='sidebar-chats'] [data-chat-row]").first().click({ timeout: 8000 });
      await page.waitForURL((url) => /^\/c\/[^/]+$/.test(url.pathname), { timeout: 10000 });
      chatPath = pathOf();
      await page.locator("[data-testid=chat-messages]").waitFor({ timeout: 15000 });
      await page.waitForTimeout(settle / 2);
      const current = await page.locator("[data-chat-row][aria-current='true']").count();
      need(current >= 1, "no chat row is current ([data-chat-row][aria-current=true]) on the chat's page");
      const texts = (await page.locator("[data-testid=chat-messages] [data-testid^=message-]").allInnerTexts()).filter((t) => t.trim());
      need(texts.length >= 1, "the transcript ([data-testid=chat-messages]) renders no message with text");
      console.log(`       ${chatPath}: ${texts.length} messages, the first "${texts[0].trim().replace(/\s+/g, " ").slice(0, 60)}"`);
      await shot("chat");
    });
    // A spawned agent's thread, when the transcript has a link to one (optional).
    const threadLinks = await page.locator("[data-testid=chat-messages] a[href*='/t/']").count();
    if (threadLinks) {
      await step("a thread link opens its thread", async () => {
        await go(page.locator("[data-testid=chat-messages] a[href*='/t/']").first(), /^\/c\/[^/]+\/t\/[^/]+$/);
        need(await page.locator("[data-testid=chat-messages]").count(), "the thread renders no transcript");
      });
    } else console.log(`       ${chatPath}: no thread link in the transcript (optional)`);

    // Workflows: the group's Runs, then a run opened from the list.
    await step("Workflows › Runs → a run opens from the list", async () => {
      await visit(`/project/${shopProjectId}`);
      await page.waitForTimeout(settle);
      const group = nav("nav-workflows");
      if ((await group.getAttribute("aria-expanded")) !== "true") await group.click({ timeout: 8000 });
      await go(nav("nav-workflows-runs"), /^\/workflows\/runs$/);
      const runs = await page.locator("[data-testid^=run-row-]").count();
      need(runs >= 1, `Runs lists ${runs} runs, want at least 1`);
      await shot("workflows");
      await page.locator("[data-testid^=run-row-]").first().click({ timeout: 8000 });
      await page.waitForURL((url) => /^\/workflows\/runs\/[^/]+$/.test(url.pathname), { timeout: 10000 });
      await page.waitForTimeout(settle / 2);
      need(!(await runNotLoaded(page)), `the run opened from the list (${pathOf()}) says "This run could not be loaded"`);
    });

    // Find: the nav's box opens the command palette.
    await step("Find opens the command palette", async () => {
      await visit(home);
      await page.waitForTimeout(settle);
      await nav("nav-find").click({ timeout: 8000 });
      const palette = page.getByPlaceholder("Type a command...");
      await palette.waitFor({ timeout: 5000 }).catch(() => need(false, "the Find box did not open the command palette (no \"Type a command...\" input)"));
      await shot("find");
      await page.keyboard.press("Escape");
      await palette.waitFor({ state: "hidden", timeout: 5000 }).catch(() => need(false, "Escape did not close the command palette"));
    });

    // New chat: the composer renders, and sending is refused.
    await step("New chat → composer; sending is refused", async () => {
      await visit(`/project/${shopProjectId}`);
      await page.waitForTimeout(settle);
      await go(nav("create-chat-button"), /^\/new$/);
      const composer = page.getByTestId("chat-input");
      await composer.waitFor({ timeout: 10000 }).catch(() => need(false, "the composer ([data-testid=chat-input]) did not render on /new"));
      await composer.fill("Add a wishlist to the shop");
      await page.keyboard.press("Enter");
      const toast = page.getByText("This is a demo. Changes are turned off.").first();
      await toast.waitFor({ timeout: 8000 }).catch(() => need(false, 'sending a message showed no "This is a demo. Changes are turned off." toast'));
      need(pathOf() === "/new", `sending a message left /new for ${pathOf()}`);
      const refused = await refusedRpcs(page);
      need(refused.includes("reliant.v1.ChatService/StartChat"), `the transport did not record the refused StartChat (refused: ${refused.join(", ") || "none"})`);
    });

    for (const rpc of await unknownRpcs(page)) unknown.add(rpc);
    if (unknown.size) problems.push(`RPCs no sample data answers (a missing fixture): ${[...unknown].join(", ")}`);
    report(label, problems, ` (${steps.length} steps: ${steps.join("; ")})`, warnings);
    await context.close();
  }
} finally {
  await browser.close();
}
process.exit(failures.length ? 1 : 0);
