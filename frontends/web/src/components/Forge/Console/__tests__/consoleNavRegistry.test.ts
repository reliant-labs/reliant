import { afterEach, describe, expect, it } from "vitest";

import { childrenOf, flattenIds, hrefOf, hybridNav, isCurrent, type NavCtx } from "../../../Layout/sidebar/navConfig";
import { registerConsoleNavEntry } from "../consoleNavRegistry";
import { consolePageOf, consolePath, orgPageOf, type ConsolePage } from "../paths";

/** The shell's nav on `page` of app `project` (navConfig/items.tsx draws the registered rows). */
function ctxAt(page: ConsolePage, project = "shop"): NavCtx {
  const pathname = consolePath(project, page);
  return {
    pathname,
    search: {},
    mode: "one",
    projectId: "p1",
    projectName: project,
    app: project,
    console: { project, page, env: "prod", envs: [{ name: "prod", preview: false }] },
    page: consolePageOf(pathname)?.page ?? null,
    orgPage: orgPageOf(pathname),
  };
}

const analytics = hybridNav.items.find((item) => item.id === "analytics")!;
const analyticsRows = (ctx: NavCtx) => childrenOf(analytics, ctx);
const TABS = ["Product overview", "Users", "Product", "Acquisition", "Events & metrics"];
const labelsOf = (ctx: NavCtx) => analyticsRows(ctx).map((row) => (row.kind === "row" ? row.label : row.id));

let unregister: (() => void) | undefined;
afterEach(() => {
  unregister?.();
  unregister = undefined;
});

// "Funnels" stands for any module that registers a row (Experiments is a built-in page now).
describe("the console nav's registered rows", () => {
  it("has no Funnels row, and no Funnels page, until one is registered", () => {
    expect(flattenIds(hybridNav.items, ctxAt({ kind: "overview" }))).not.toContain("registered-funnels");
    expect(labelsOf(ctxAt({ kind: "overview" }))).toEqual(TABS);
    expect(consolePageOf("/forge/p/shop/funnels")).toBeNull();
  });

  it("puts a row registered into the Product analytics group inside Analytics, last, linking to its page, current there", () => {
    unregister = registerConsoleNavEntry({ id: "funnels", label: "Funnels", segment: "funnels", after: "product-analytics", group: "product-analytics" });
    const overview = ctxAt({ kind: "overview" });
    expect(labelsOf(overview)).toEqual([...TABS, "Funnels"]);
    const row = analyticsRows(overview).at(-1)!;
    expect(hrefOf(row, overview)).toBe("/forge/p/shop/funnels?env=prod");
    expect(isCurrent(row, overview)).toBe(false);

    const page = consolePageOf("/forge/p/shop/funnels");
    expect(page).toEqual({ project: "shop", page: { kind: "extension", id: "funnels", segment: "funnels" } });
    expect(consolePath("shop", page!.page)).toBe("/forge/p/shop/funnels");
    const onPage = ctxAt(page!.page);
    expect(isCurrent(analyticsRows(onPage).at(-1)!, onPage)).toBe(true);
    expect(isCurrent(hybridNav.items.find((item) => item.id === "overview")!, onPage)).toBe(false);
  });

  it("hides a row its app does not have", () => {
    unregister = registerConsoleNavEntry({
      id: "funnels",
      label: "Funnels",
      segment: "funnels",
      after: "product-analytics",
      group: "product-analytics",
      available: (input) => input.project !== "ledger",
    });
    expect(labelsOf(ctxAt({ kind: "overview" }))).toContain("Funnels");
    expect(labelsOf(ctxAt({ kind: "overview" }, "ledger"))).not.toContain("Funnels");
  });

  it("opens a page with routes of its own (`target`), lit on its `kinds`, and leaves that page's path alone", () => {
    unregister = registerConsoleNavEntry({
      id: "audit",
      label: "Audit",
      segment: "audit",
      after: "dashboards",
      group: "product-analytics",
      target: { kind: "activity" },
      kinds: ["activity"],
    });
    const onActivity = ctxAt({ kind: "activity" });
    const row = analyticsRows(onActivity).find((item) => item.id === "registered-audit")!;
    expect(hrefOf(row, onActivity)).toBe("/forge/p/shop/activity?env=prod");
    expect(isCurrent(row, onActivity)).toBe(true);
    const overview = ctxAt({ kind: "overview" });
    expect(isCurrent(analyticsRows(overview).find((item) => item.id === "registered-audit")!, overview)).toBe(false);
    // Its segment is not a registered page: the built-in route owns the URL.
    expect(consolePageOf("/forge/p/shop/audit")).toBeNull();
  });
});
