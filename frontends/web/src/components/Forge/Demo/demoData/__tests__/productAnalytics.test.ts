// Copyright (c) 2026 Reliant Labs

/**
 * The built-in product analytics dashboards (Overview's Traffic and Users,
 * Users, Product, Acquisition: forge #2075's specs, shop/forge-dashboards.json)
 * answer from ONE audience, and agree with each other and with the Users page
 * the way the real ones do (product-analytics-design.md §4's consistency
 * rules). Every read goes through the demo's QueryPanel with the dashboards'
 * own panels, as the console makes it.
 */

import { afterAll, beforeAll, describe, expect, it, vi } from "vitest";

vi.mock("@/services/controlPlane/config", () => ({ CONTROL_PLANE_API_URL: "https://cp.test", hasControlPlane: true }));

import type { ObservabilityApi } from "@/services/controlPlane/observability/ObservabilityApiContext";
import type { Dashboard, Panel, PanelResult } from "@/services/controlPlane/observability/types";

import { demoAudience, demoAudienceTotals } from "../shop/audience";
import { demoActivity, weekStart } from "../shop/audienceActivity";
import { PRODUCT_ANALYTICS_ANSWERS } from "../shop/productAnalytics";
import { createShopApi } from "../shop/shopApi";
import { DAY, DEMO_ENV_IDS, demoNow, HOUR, resetDemoNowForTests } from "../shopStory";

const WHEN = [
  ["on a weekday afternoon", Date.UTC(2026, 10, 10, 15, 0, 0)],
  ["on a Sunday night", Date.UTC(2026, 10, 15, 3, 0, 0)],
] as const;

let NOW = 0;
let api: ObservabilityApi;
const specs = new Map<string, Dashboard>();

async function read(dashboard: string, panelId: string, window: { startMs: number; endMs: number }, envIds: string[] = [], trend = false): Promise<PanelResult> {
  const spec = specs.get(dashboard)!;
  const own = spec.panels.find((p) => p.id === panelId);
  if (!own) throw new Error(`${dashboard} has no panel ${panelId}`);
  const panel: Panel = trend ? { ...own, id: `${own.id}.trend`, sql: own.trendSql, trendSql: undefined } : own;
  return api.queryPanel({ orgId: "org-acme", projectId: "shop", envIds, filters: [], window, panel, services: [], stepSeconds: 0, dashboardId: dashboard });
}
const figure = (r: PanelResult, i = 0) => r.rows[0]?.values[i] ?? null;
const column = (r: PanelResult, name: string) => r.rows.map((row) => row.values[r.columns.indexOf(name)] ?? 0);
const sum = (xs: (number | null)[]) => xs.reduce<number>((a, b) => a + (b ?? 0), 0);
const rowOf = (r: PanelResult, key: string, value: string) => r.rows.find((row) => row.group[key] === value);

describe.each(WHEN)("the product analytics dashboards, %s", (_when, at) => {
  let week = { startMs: 0, endMs: 0 };
  beforeAll(async () => {
    resetDemoNowForTests(at);
    NOW = demoNow();
    week = { startMs: NOW - 7 * DAY, endMs: NOW };
    api = createShopApi(NOW);
    for (const id of ["overview", "users", "product", "acquisition"]) {
      specs.set(id, (await api.getDashboard({ orgId: "org-acme", ref: { kind: "release", projectId: "shop", key: id } })).spec);
    }
  });
  afterAll(() => resetDemoNowForTests());

  it("lists the built-in dashboards in the Product group, as forge generates them", async () => {
    const index = await api.listDashboards({ orgId: "org-acme", projectId: "shop" });
    const product = index.dashboards.filter((d) => d.group === "product" && d.source === "generated").map((d) => d.ref.key);
    expect(product).toEqual(["users", "product", "acquisition"]);
    expect(specs.get("overview")!.sections?.map((s) => s.id)).toEqual(["health", "traffic", "users"]);
  });

  it("answers every sql panel and sparkline of the four, by panel id, with data", async () => {
    for (const [id, spec] of specs) {
      for (const panel of spec.panels.filter((p) => p.kind === "sql")) {
        expect(PRODUCT_ANALYTICS_ANSWERS, `${id}/${panel.id}`).toContain(`${id}/${panel.id}`);
        const result = await read(id, panel.id, week);
        // A flag nobody evaluated would be empty; shop's express-checkout is evaluated every day.
        expect(result.rows.length + result.series.length, `${id}/${panel.id}`).toBeGreaterThan(0);
        if (panel.trendSql) {
          expect(PRODUCT_ANALYTICS_ANSWERS).toContain(`${id}/${panel.id}.trend`);
          expect((await read(id, panel.id, week, [], true)).series.length, `${id}/${panel.id} trend`).toBeGreaterThan(0);
        }
      }
    }
  });

  it("counts the Users page's people: accounts, and active/new/returning from sign-up and last sign-in (rule 1)", async () => {
    const people = DEMO_ENV_IDS.flatMap((env) => demoAudience(env));
    const accounts = DEMO_ENV_IDS.reduce((n, env) => n + demoAudienceTotals(env).accounts, 0);
    const active = people.filter((a) => a.lastSignInAtMs !== null && a.lastSignInAtMs >= NOW - 7 * DAY).length;
    const fresh = people.filter((a) => a.lastSignInAtMs !== null && a.createdAtMs >= NOW - 7 * DAY).length;

    const card = { active: await read("overview", "active-7d", week), fresh: await read("overview", "new-7d", week), returning: await read("overview", "returning-7d", week) };
    expect(figure(await read("overview", "accounts", week))).toBe(accounts);
    expect(figure(await read("users", "accounts", week))).toBe(accounts);
    expect(figure(card.active)).toBe(active);
    expect(figure(card.fresh)).toBe(fresh);
    expect(figure(card.returning)).toBe(active - fresh);
    // The Users dashboard over the same 7 days says the same.
    expect(figure(await read("users", "active", week))).toBe(active);
    expect(figure(await read("users", "new", week))).toBe(fresh);
    expect(figure(await read("users", "returning", week))).toBe(active - fresh);
    // Each card figure carries its previous 7 days.
    expect(card.active.columns).toEqual(["Active users (7 days)", "Previous 7 days"]);
    expect(figure(card.active, 1)).toBeGreaterThan(0);
    // prod alone is the Users page's prod.
    expect(figure(await read("overview", "accounts", week, ["env-prod"]))).toBe(demoAudienceTotals("env-prod").accounts);
  });

  it.each([180, 365])("counts users whole over a %i-day window, older than the default 100 days of activity", async (days) => {
    const window = { startMs: NOW - days * DAY, endMs: NOW };
    const people = DEMO_ENV_IDS.flatMap((env) => demoAudience(env));
    // Every account's last session is its last sign-in, so active in the window is exactly a last sign-in in it.
    const active = people.filter((a) => a.lastSignInAtMs !== null && a.lastSignInAtMs >= window.startMs).length;
    const fresh = people.filter((a) => a.lastSignInAtMs !== null && a.createdAtMs >= window.startMs).length;
    // The audience has sign-ups older than 100 days, so a cut at the default history would undercount.
    const old = people.filter((a) => a.lastSignInAtMs !== null && a.createdAtMs >= window.startMs && a.createdAtMs < NOW - 100 * DAY);
    expect(old.length).toBeGreaterThan(0);

    expect(figure(await read("users", "active", window))).toBe(active);
    expect(figure(await read("users", "new", window))).toBe(fresh);
    expect(figure(await read("users", "returning", window))).toBe(active - fresh);
    // An old sign-up's sign-up session is there: its day's active users count it.
    const { sessions } = demoActivity("env-prod", NOW, window.startMs);
    const prodOld = demoAudience("env-prod").find((a) => a.lastSignInAtMs !== null && a.createdAtMs >= window.startMs && a.createdAtMs < NOW - 100 * DAY);
    if (prodOld) expect(sessions.some((s) => s.signUp && s.start === prodOld.createdAtMs)).toBe(true);
    // A wider history only adds older sessions: the default one's are all still there, unchanged.
    const defaultFirst = Math.floor(NOW / DAY) * DAY - 99 * DAY;
    expect(sessions.filter((s) => s.start >= defaultFirst)).toEqual(demoActivity("env-prod", NOW).sessions);
  });

  it("splits sign-ups the same way everywhere: by channel, method, country and device, all equal to new (rule 2)", async () => {
    const fresh = figure(await read("users", "new", week));
    expect(fresh).toBeGreaterThan(0);
    expect(figure(await read("acquisition", "signups", week))).toBe(fresh);
    expect(sum(column(await read("acquisition", "channels", week), "Sign-ups"))).toBe(fresh);
    expect(sum(column(await read("acquisition", "countries", week), "Sign-ups"))).toBe(fresh);
    expect(sum(column(await read("acquisition", "devices", week), "Sign-ups"))).toBe(fresh);
    expect(sum((await read("users", "signups-by-method", week)).series.flatMap((s) => s.values))).toBe(fresh);
    expect(sum((await read("acquisition", "signups-by-channel-weekly", week)).series.flatMap((s) => s.values))).toBe(fresh);
    expect(sum(column(await read("users", "signups-by-provider", week), "Sign-ups"))).toBe(fresh);
    expect(sum(column(await read("users", "time-to-activation", week), "Sign-ups"))).toBe(fresh);
  });

  it("never converts more sign-ups than a channel has visitors, and Unattributed has none (rule 3)", async () => {
    const channels = await read("acquisition", "channels", week);
    for (const row of channels.rows) {
      const conv = row.values[channels.columns.indexOf("Conversion %")];
      if (row.group.Channel === "Unattributed") expect(conv).toBeNull();
      else expect(conv ?? 0).toBeLessThanOrEqual(100);
    }
    expect(rowOf(channels, "Channel", "Unattributed")).toBeDefined();
  });

  it("builds each week's users from new, retained and resurrected (rule 4) and starts every cohort at 100% (rule 5)", async () => {
    const lifecycle = await read("users", "lifecycle-weekly", week);
    const byLabel = (label: string) => lifecycle.series.find((s) => s.label === label)!;
    const { people, sessions } = demoActivity("env-prod", NOW);
    for (const [i, t] of byLabel("New").timestamps.entries()) {
      const wk = t * 1000;
      const wau = new Set(sessions.filter((s) => s.start >= wk && s.start < Math.min(wk + 7 * DAY, NOW)).map((s) => s.account)).size;
      const staging = demoActivity("env-staging", NOW);
      const dev = demoActivity("env-dev", NOW);
      const others = [staging, dev].reduce((n, a) => n + new Set(a.sessions.filter((s) => s.start >= wk && s.start < Math.min(wk + 7 * DAY, NOW)).map((s) => s.account)).size, 0);
      expect((byLabel("New").values[i] ?? 0) + (byLabel("Retained").values[i] ?? 0) + (byLabel("Resurrected").values[i] ?? 0)).toBe(wau + others);
      expect(byLabel("Churned").values[i] ?? 0).toBeLessThanOrEqual(0);
      if (wk >= weekStart(NOW - 7 * DAY)) {
        const signedUp = DEMO_ENV_IDS.reduce((n, env) => n + demoActivity(env, NOW).people.filter((a) => a.signedUp && weekStart(a.createdAtMs) === wk).length, 0);
        expect(byLabel("New").values[i]).toBe(signedUp);
      }
    }
    expect(people.length).toBe(demoAudienceTotals("env-prod").accounts);

    const cohorts = await read("users", "retention-cohorts", week, ["env-prod"]);
    expect(cohorts.columns).toEqual(Array.from({ length: 12 }, (_, k) => `W${k}`));
    expect(cohorts.rows.length).toBeGreaterThan(8);
    for (const row of cohorts.rows) {
      expect(row.values[0]).toBe(1);
      const label = row.group["Cohort (week of)"];
      const start = Date.parse(`${label.slice(0, 10)}T00:00:00Z`);
      const size = demoAudience("env-prod").filter((a) => a.lastSignInAtMs !== null && weekStart(a.createdAtMs) === start).length;
      expect(label).toContain(`· ${size} user`);
    }
    // The newest cohort has only lived W0: the rest is "not yet", never 0%.
    expect(cohorts.rows.at(-1)!.values.slice(1).every((v) => v === null)).toBe(true);
  });

  it("narrows every funnel step by step; the activation funnel's sign-ups are the new users (rule 6)", async () => {
    for (const id of ["funnel-activation", "funnel-buy"]) {
      const counts = (await read("product", id, week)).rows.map((r) => r.values[0] ?? 0);
      for (let i = 1; i < counts.length; i++) expect(counts[i], `${id} step ${i}`).toBeLessThanOrEqual(counts[i - 1]);
    }
    const activation = await read("product", "funnel-activation", week);
    expect(activation.rows[1].values[0]).toBe(figure(await read("users", "new", week)));
    expect(activation.rows[0].values[0]).toBe(figure(await read("acquisition", "visitors", week)));
  });

  it("sums countries, devices, channels, browsers and systems to the visitors figure (rule 7)", async () => {
    const visitors = figure(await read("acquisition", "visitors", week));
    expect(visitors).toBeGreaterThan(10_000);
    for (const id of ["countries", "devices", "channels", "browsers", "os"]) {
      expect(sum(column(await read("acquisition", id, week), "Visitors")), id).toBe(visitors);
    }
    // A country is its ISO code, which the console draws with a flag and a name.
    expect((await read("acquisition", "countries", week)).rows[0].group.Country).toMatch(/^[A-Z]{2}$/);
  });

  it("keeps each page's views ≥ visitors ≥ signed-in users, and its entries summing to the visits (rule 8)", async () => {
    const pages = await read("product", "top-pages", week);
    for (const row of pages.rows) {
      const [views, visitors, signed] = row.values as number[];
      expect(views, row.group.Route).toBeGreaterThanOrEqual(visitors);
      expect(visitors, row.group.Route).toBeGreaterThanOrEqual(signed);
    }
    expect(sum(column(pages, "Entries"))).toBe(figure(await read("product", "visits", week)));
    expect(sum(column(await read("product", "entry-pages", week), "Visits"))).toBe(figure(await read("product", "visits", week)));
  });

  it("agrees on purchases and orders across the Buy funnel, goals, events and the CRUD panel (rule 9)", async () => {
    const buy = await read("product", "funnel-buy", week);
    const goals = await read("product", "goals", week);
    const events = await read("product", "events-by-name", week);
    const actions = await read("product", "actions-per-entity", week);
    const checkout = rowOf(goals, "Goal", "Checkout")!;
    expect(buy.rows.at(-1)!.values[0]).toBe(checkout.values[0]);
    const purchases = rowOf(events, "Event", "purchase")!.values[0]!;
    const orders = rowOf(events, "Event", "order_created")!.values[0]!;
    expect(purchases).toBe(checkout.values[1]);
    expect(orders).toBe(rowOf(actions, "entity", "order")!.values[0]);
    // Orders exceed purchases only by the charges v1.8.3 failed in prod.
    const failed = demoActivity("env-prod", NOW).sessions.filter((s) => s.orderAt >= week.startMs && !s.purchased).length;
    expect(orders - purchases).toBe(failed);
    // The Checkout funnel dashboard (per session) ends on the same purchases.
    const shop = (await api.getDashboard({ orgId: "org-acme", ref: { kind: "release", projectId: "shop", key: "checkout" } })).spec;
    const funnel = await api.queryPanel({ orgId: "org-acme", projectId: "shop", envIds: [], filters: [], window: week, panel: shop.panels.find((p) => p.kind === "funnel")!, services: [], stepSeconds: 0 });
    expect(funnel.funnelSteps.at(-1)!.count).toBe(purchases);
  });

  it("shows the incident only in the checkout's last hour", async () => {
    const rate = (r: PanelResult) => (r.rows[3].values[0] ?? 0) / Math.max(1, r.rows[2].values[0] ?? 0);
    const before = await read("product", "funnel-buy", { startMs: NOW - 7 * DAY, endMs: NOW - HOUR }, ["env-prod"]);
    const since = await read("product", "funnel-buy", { startMs: NOW - 40 * 60_000, endMs: NOW }, ["env-prod"]);
    expect(rate(before)).toBeGreaterThan(0.69);
    if ((since.rows[2].values[0] ?? 0) > 0) expect(rate(since)).toBeLessThan(rate(before));
    // Users and Acquisition do not move: the same week, read twice, gives the same answer.
    expect(JSON.stringify(await read("acquisition", "channels", week))).toBe(JSON.stringify(await read("acquisition", "channels", week)));
  });

  it("is a pure function of now (rule 10)", async () => {
    const first = await read("users", "dau-wau-mau", week);
    api = createShopApi(NOW);
    expect(JSON.stringify(await read("users", "dau-wau-mau", week))).toBe(JSON.stringify(first));
  });
});
