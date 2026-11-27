// Copyright (c) 2026 Reliant Labs

/**
 * withoutPages (navConfig/prune.ts): a nav without the console pages its host
 * does not serve, as a pure transform of the config. The public demo prunes
 * Usage from the app's own items this way.
 */

import { describe, expect, it, vi } from "vitest";

import { inboxPath } from "@/lib/paths";

import { consolePageOf, consolePath, orgPageOf } from "../../../Forge/Console/paths";
import { flattenIds, hrefOf, hybridNav, visibleItems, type NavConfig, type NavCtx, type NavItem, type NavRow } from "../navConfig";
import { withoutPages } from "../navConfig/prune";

vi.mock("@/lib/changesFeature", () => ({ changesUIEnabled: () => false }));

function ctxAt(href: string, over: Partial<NavCtx> = {}): NavCtx {
  const [pathname, query = ""] = href.split("?");
  return {
    pathname,
    search: Object.fromEntries(new URLSearchParams(query)),
    mode: "one",
    projectId: "p1",
    projectName: "shopfront",
    app: "shopfront",
    console: null,
    page: consolePageOf(pathname)?.page ?? null,
    orgPage: orgPageOf(pathname),
    ...over,
  };
}

const row = (id: string, page?: NavRow["page"]): NavRow => ({ kind: "row", id, label: id, href: () => `/${id}`, page });
const ids = (config: NavConfig, ctx = ctxAt(inboxPath())) => [...flattenIds(config.items, ctx), ...flattenIds(config.footer, ctx)];

describe("withoutPages", () => {
  it("drops the rows that open a pruned page, at any depth, and keeps the rest", () => {
    const config: NavConfig = {
      items: [row("services", "services"), { kind: "group", id: "configure", label: "Configure", children: [row("secrets", "secrets"), row("usage", "usage")] }],
      footer: [row("billing"), row("speed", "site-insights")],
    };
    expect(ids(withoutPages(config, ["usage", "site-insights"]))).toEqual(["services", "configure", "secrets", "billing"]);
  });

  it("drops a group, or an area, left with nothing to open", () => {
    const config: NavConfig = {
      items: [
        { kind: "group", id: "money", label: "Money", children: [row("usage", "usage")] },
        { kind: "area", id: "perf", label: "Perf", match: () => false, children: [row("speed", "site-insights")] },
        { kind: "group", id: "empty", label: "Empty", children: [] },
      ],
      footer: [],
      sectionPages: [{ kind: "area", id: "settings", label: "Settings", match: () => false, children: [row("usage-settings", "usage")] }],
    };
    const pruned = withoutPages(config, ["usage", "site-insights"]);
    // A group that was empty to begin with is the config's own business.
    expect(pruned.items.map((item) => item.id)).toEqual(["empty"]);
    expect(pruned.sectionPages).toEqual([]);
  });

  it("prunes rows built per context when the nav draws them, and hides a group they all left", () => {
    const rows = (ctx: NavCtx): readonly NavItem[] => (ctx.app ? [row("usage", "usage"), row("tab", "analytics")] : [row("usage", "usage")]);
    const config: NavConfig = { items: [{ kind: "group", id: "dyn", label: "Dyn", children: rows }], footer: [] };
    const pruned = withoutPages(config, ["usage"]);
    expect(ids(pruned)).toEqual(["dyn", "tab"]);
    expect(ids(pruned, ctxAt(inboxPath(), { app: null }))).toEqual([]);
  });

  it("returns the config itself with nothing to prune, and never changes the one passed in", () => {
    expect(withoutPages(hybridNav, [])).toBe(hybridNav);
    const ctx = ctxAt(consolePath("shopfront", { kind: "usage" }));
    const before = ids(hybridNav, ctx);
    const pruned = withoutPages(hybridNav, ["usage", "site-insights"]);
    expect(ids(hybridNav, ctx)).toEqual(before);
    expect(before).toEqual(expect.arrayContaining(["project-usage"]));
    expect(ids(pruned, ctx)).not.toContain("project-usage");
  });

  it("takes the demo's pages out of the app's nav and nothing else", () => {
    const ctx = ctxAt(consolePath("shopfront", { kind: "overview" }));
    const area = (config: NavConfig) => visibleItems(config.items, ctx).find((item) => item.id === "observability")!;
    const obs = (config: NavConfig) => flattenIds([area(config)], ctx);
    const pruned = withoutPages(hybridNav, ["usage", "site-insights"]);
    expect(ids(pruned, ctx)).toEqual(ids(hybridNav, ctx).filter((id) => id !== "project-usage" && id !== "site-insights"));
    expect(obs(pruned)).toEqual(obs(hybridNav).filter((id) => id !== "site-insights"));
  });
});

describe("a row's page", () => {
  /** Every row of a config, in every group and area, at `ctx`. */
  const rowsOf = (items: readonly NavItem[], ctx: NavCtx): NavRow[] =>
    items.flatMap((item) =>
      item.kind === "row" ? [item] : item.kind === "group" || item.kind === "area" ? rowsOf(typeof item.children === "function" ? item.children(ctx) : item.children, ctx) : [],
    );

  it("is the kind of console page the row opens", () => {
    const ctx = ctxAt(consolePath("shopfront", { kind: "overview" }));
    const tagged = rowsOf([...hybridNav.items, ...hybridNav.footer], ctx).filter((item) => item.page);
    expect(tagged.map((item) => item.id)).toEqual(expect.arrayContaining(["services", "project-usage", "site-insights", "previews", "auth"]));
    for (const item of tagged) {
      const href = hrefOf(item, ctx)!;
      expect(consolePageOf(href.split("?")[0])?.page.kind, item.id).toBe(item.page);
    }
  });
});
