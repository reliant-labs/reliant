// Copyright (c) 2026 Reliant Labs

/**
 * demoNav ⊂ hybridNav, in order: the public demo draws the app's own nav
 * (Forge/Demo/demoNavConfig.ts), so with the demo's own rows (Home, How it was
 * built, its phone actions) taken out, every row it draws is one the app
 * draws, under the same label, in the same order. A row the app moves,
 * renames or regroups moves in the demo too; this fails if the demo forks.
 */

import { describe, expect, it, vi } from "vitest";

import { consolePageOf, consolePath, orgPageOf } from "../../Console/paths";
import { flattenIds, hybridNav, visibleItems, childrenOf, type NavConfig, type NavCtx, type NavItem } from "@/components/Layout/sidebar/navConfig";
import { DEMO_HIDDEN_PAGES, FORGE_DEMO_PATH } from "../demoPath";
import { DEMO_OWN_ITEMS, demoNav } from "../demoNavConfig";
import { changesUIEnabled as demoChangesUIEnabled } from "@/lib/changesFeature.demo";

// The app's CHANGES_UI, off unless a test turns it on: the demo bundle's twin (lib/changesFeature.demo.ts) is on.
const changesUI = vi.hoisted(() => ({ on: false }));
vi.mock("@/lib/changesFeature", () => ({ changesUIEnabled: () => changesUI.on }));

const APP = "shop";

function ctxAt(href: string): NavCtx {
  const [pathname, query = ""] = href.split("?");
  return {
    pathname,
    search: Object.fromEntries(new URLSearchParams(query)),
    mode: "one",
    projectId: "p1",
    projectName: APP,
    app: APP,
    console: null,
    page: consolePageOf(pathname)?.page ?? null,
    orgPage: orgPageOf(pathname),
  };
}

const PLACES = [
  consolePath(APP, { kind: "overview" }),
  consolePath(APP, { kind: "obs-view", view: "traces" }),
  consolePath(APP, { kind: "secrets" }),
  `${FORGE_DEMO_PATH}?section=home`,
];

const own = new Set(DEMO_OWN_ITEMS.map((item) => item.id));

/** Every item a config draws for `ctx`, depth first, as id → label. */
function drawn(items: readonly NavItem[], ctx: NavCtx): { id: string; label: string | undefined }[] {
  return visibleItems(items, ctx).flatMap((item) => [
    { id: item.id, label: "label" in item ? item.label : undefined },
    ...drawn(childrenOf(item, ctx), ctx),
  ]);
}

function isSubsequence(sub: readonly string[], of: readonly string[]): boolean {
  let at = 0;
  for (const id of sub) {
    at = of.indexOf(id, at);
    if (at < 0) return false;
    at++;
  }
  return true;
}

function expectSubsetAt(place: string) {
  const ctx = ctxAt(place);
  for (const part of ["items", "footer"] as const) {
    const app = drawn(hybridNav[part], ctx);
    const demo = drawn(demoNav[part], ctx).filter((item) => !own.has(item.id));
    expect(isSubsequence(demo.map((item) => item.id), app.map((item) => item.id))).toBe(true);
    const labels = new Map(app.map((item) => [item.id, item.label]));
    for (const item of demo) expect(item.label, item.id).toBe(labels.get(item.id));
  }
}

describe("demoNav", () => {
  for (const place of PLACES) {
    it(`is hybridNav with rows removed, in order, at ${place}`, () => expectSubsetAt(place));
  }

  it("lists Experiments under Analytics in the demo bundle, as the app does with CHANGES_UI on", () => {
    // The demo's twin says the demo serves Forge Changes; the app's flag is the deployment's.
    expect(demoChangesUIEnabled()).toBe(true);
    changesUI.on = true;
    try {
      const ctx = ctxAt(consolePath(APP, { kind: "overview" }));
      expect(flattenIds(demoNav.items, ctx)).toContain("experiments");
      for (const place of PLACES) expectSubsetAt(place);
    } finally {
      changesUI.on = false;
    }
  });

  it("draws the app's console rows and none of the app's own", () => {
    const ctx = ctxAt(consolePath(APP, { kind: "overview" }));
    const ids = [...flattenIds(demoNav.items, ctx), ...flattenIds(demoNav.footer, ctx)];
    for (const id of ["overview", "dashboards", "observability", "analytics", "deployments", "tests", "data", "storage", "auth", "configure"]) {
      expect(ids).toContain(id);
    }
    for (const id of ["slot-inbox", "agents", "usage-billing", "slot-help", "settings"]) expect(ids).not.toContain(id);
    expect(demoNav.sectionPages).toBeUndefined();
  });

  it("has no row for a page the demo does not serve", () => {
    const rows: NavItem[] = [];
    const walk = (items: readonly NavItem[], ctx: NavCtx) =>
      visibleItems(items, ctx).forEach((item) => {
        rows.push(item);
        walk(childrenOf(item, ctx), ctx);
      });
    for (const place of PLACES) walk([...demoNav.items, ...demoNav.footer], ctxAt(place));
    const pages = rows.flatMap((item) => (item.kind === "row" && item.page ? [item.page] : []));
    expect(pages.length).toBeGreaterThan(0);
    for (const hidden of DEMO_HIDDEN_PAGES) expect(pages).not.toContain(hidden);
    // The app's own nav does have them: the pruning is the demo's.
    const appIds = flattenIds(hybridNav.items, ctxAt(consolePath(APP, { kind: "overview" })));
    expect(appIds).toContain("project-usage");
  });

  it("lights Home and How it was built on their own pages, and no console row there", () => {
    const at = (section: string) => ctxAt(`${FORGE_DEMO_PATH}?section=${section}`);
    const config: NavConfig = demoNav;
    const home = config.items.find((item) => item.id === "demo-home");
    const built = config.footer.find((item) => item.id === "demo-built");
    expect(home?.kind === "row" && home.match?.(at("home"))).toBe(true);
    expect(home?.kind === "row" && home.match?.(at("built"))).toBe(false);
    expect(built?.kind === "row" && built.match?.(at("built"))).toBe(true);
    const overview = config.items.find((item) => item.id === "overview");
    expect(overview?.kind === "row" && overview.match?.(at("home"))).toBe(false);
  });
});
