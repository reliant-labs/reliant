// Copyright (c) 2026 Reliant Labs

/**
 * demoNav == hybridNav − DEMO_EXCLUDED_IDS + DEMO_OWN_ITEMS: the public demo
 * draws the app's own nav (Forge/Demo/demoNavConfig.ts), the same item objects
 * in the same order, with exactly the named account-only items taken out and
 * the demo's own (Home, How it was built, its phone actions) put in. An item
 * the app adds, moves, renames or regroups arrives in the demo too, or this
 * fails: the demo cannot fork the nav, nor drop an item without naming it.
 */

import { describe, expect, it, vi } from "vitest";

import { consolePageOf, consolePath, orgPageOf } from "../../Console/paths";
import { flattenIds, hybridNav, visibleItems, childrenOf, type NavConfig, type NavCtx, type NavItem } from "@/components/Layout/sidebar/navConfig";
import { withoutPages } from "@/components/Layout/sidebar/navConfig/prune";
import { DEMO_HIDDEN_PAGES, FORGE_DEMO_PATH } from "../demoPath";
import { DEMO_EXCLUDED_IDS, DEMO_OWN_ITEMS, demoActionsSlot, demoBuiltRow, demoHomeRow, demoNav, demoNavItems } from "../demoNavConfig";
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
  "/project/p1",
  "/inbox",
  `${FORGE_DEMO_PATH}?section=home`,
];

const own = new Set(DEMO_OWN_ITEMS.map((item) => item.id));
const excluded = new Set(DEMO_EXCLUDED_IDS);

/** Every item a config draws for `ctx`, depth first, as id → label. */
function drawn(items: readonly NavItem[], ctx: NavCtx): { id: string; label: string | undefined }[] {
  return visibleItems(items, ctx).flatMap((item) => [
    { id: item.id, label: "label" in item ? item.label : undefined },
    ...drawn(childrenOf(item, ctx), ctx),
  ]);
}

describe("demoNav", () => {
  for (const part of ["items", "footer"] as const) {
    it(`${part}: is hybridNav's, the same objects in the same order, less the named exclusions`, () => {
      const app = hybridNav[part].filter((item) => !excluded.has(item.id));
      const demo = demoNavItems[part].filter((item) => !own.has(item.id));
      expect(demo.map((item) => item.id)).toEqual(app.map((item) => item.id));
      demo.forEach((item, i) => expect(item, item.id).toBe(app[i]));
    });
  }

  it("puts the demo's own items where it says: Home first, the phone's actions then How it was built in the footer", () => {
    expect(demoNavItems.items[0]).toBe(demoHomeRow);
    expect(demoNavItems.footer[0]).toBe(demoActionsSlot);
    expect(demoNavItems.footer.at(-1)).toBe(demoBuiltRow);
    expect(DEMO_OWN_ITEMS.every((item) => [...demoNavItems.items, ...demoNavItems.footer].includes(item))).toBe(true);
  });

  it("names only items the app's nav has, and leaves out every one it names", () => {
    const appTop = new Set([...hybridNav.items, ...hybridNav.footer].map((item) => item.id));
    for (const id of DEMO_EXCLUDED_IDS) expect(appTop.has(id), id).toBe(true);
    for (const place of PLACES) {
      const ctx = ctxAt(place);
      const ids = [...drawn(demoNav.items, ctx), ...drawn(demoNav.footer, ctx)].map((item) => item.id);
      for (const id of DEMO_EXCLUDED_IDS) expect(ids, `${id} at ${place}`).not.toContain(id);
    }
  });

  it("is that nav with the pages the demo does not serve pruned, and no Settings sections", () => {
    const expected = withoutPages(demoNavItems, DEMO_HIDDEN_PAGES);
    for (const place of PLACES) {
      const ctx = ctxAt(place);
      for (const part of ["items", "footer"] as const) expect(drawn(demoNav[part], ctx)).toEqual(drawn(expected[part], ctx));
    }
    expect(demoNav.sectionPages).toBeUndefined();
  });

  it("draws the app's Inbox and Agents, and its console rows, with the app's labels", () => {
    const ctx = ctxAt(consolePath(APP, { kind: "overview" }));
    const ids = [...flattenIds(demoNav.items, ctx), ...flattenIds(demoNav.footer, ctx)];
    for (const id of ["overview", "slot-inbox", "agents", "dashboards", "observability", "analytics", "deployments", "tests", "data", "storage", "auth", "configure"]) {
      expect(ids).toContain(id);
    }
    const labels = new Map(drawn(hybridNav.items, ctx).map((item) => [item.id, item.label]));
    for (const item of drawn(demoNav.items, ctx).filter((entry) => !own.has(entry.id))) expect(item.label, item.id).toBe(labels.get(item.id));
  });

  it("drills into Agents with the app's own rows: New chat, Inbox, Workflows, Sandboxes, Connectors, the chat list", () => {
    const ctx = ctxAt("/project/p1");
    const agents = demoNav.items.find((item) => item.id === "agents");
    const app = hybridNav.items.find((item) => item.id === "agents");
    expect(agents).toBeDefined();
    expect(childrenOf(agents!, ctx).map((item) => item.id)).toEqual(childrenOf(app!, ctx).map((item) => item.id));
  });

  it("lists Experiments under Analytics in the demo bundle, as the app does with CHANGES_UI on", () => {
    // The demo's twin says the demo serves Forge Changes; the app's flag is the deployment's.
    expect(demoChangesUIEnabled()).toBe(true);
    changesUI.on = true;
    try {
      const ctx = ctxAt(consolePath(APP, { kind: "overview" }));
      expect(flattenIds(demoNav.items, ctx)).toContain("experiments");
    } finally {
      changesUI.on = false;
    }
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
