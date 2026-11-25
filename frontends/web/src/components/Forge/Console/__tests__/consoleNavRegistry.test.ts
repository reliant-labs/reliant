import { afterEach, describe, expect, it } from "vitest";

import { registerConsoleNavEntry } from "../consoleNavRegistry";
import { buildGroupedConsoleNav, type GroupedNavInput } from "../groupedNav";
import { consolePageOf, consolePath } from "../paths";

const hub = { open: { kind: "dashboard" as const, dashKind: "release" as const, key: "product-users" }, keys: ["product-users"] };
const nav = (over: Partial<GroupedNavInput> = {}) =>
  buildGroupedConsoleNav({ project: "shop", page: { kind: "overview" }, env: "prod", envs: [{ name: "prod", preview: false }], productAnalytics: hub, ...over });
const labels = (over: Partial<GroupedNavInput> = {}) => nav(over).items.map((item) => item.label);
const analytics = (over: Partial<GroupedNavInput> = {}) => nav(over).items.find((item) => item.label === "Analytics")!;
const TABS = ["Product overview", "Users", "Product", "Acquisition", "Events & metrics"];

let unregister: (() => void) | undefined;
afterEach(() => {
  unregister?.();
  unregister = undefined;
});

// "Funnels" stands for any module that registers a row (Experiments is a built-in page now).
describe("the console nav's registered rows", () => {
  it("has no Funnels row, and no Funnels page, until one is registered", () => {
    expect(labels()).not.toContain("Funnels");
    expect(analytics().children!.map((child) => child.label)).toEqual(TABS);
    expect(consolePageOf("/forge/p/shop/funnels")).toBeNull();
  });

  it("puts a row registered into the Product analytics group inside Analytics, last, linking to its page, current there", () => {
    unregister = registerConsoleNavEntry({ id: "funnels", label: "Funnels", segment: "funnels", after: "product-analytics", group: "product-analytics" });
    expect(labels()).not.toContain("Funnels");
    const children = analytics().children!;
    expect(children.map((child) => child.label)).toEqual([...TABS, "Funnels"]);
    expect(children.at(-1)!.href).toBe("/forge/p/shop/funnels?env=prod");
    expect(children.at(-1)!.active).toBe(false);

    const page = consolePageOf("/forge/p/shop/funnels");
    expect(page).toEqual({ project: "shop", page: { kind: "extension", id: "funnels", segment: "funnels" } });
    expect(consolePath("shop", page!.page)).toBe("/forge/p/shop/funnels");
    expect(analytics({ page: page!.page }).children!.at(-1)!.active).toBe(true);
    expect(nav({ page: page!.page }).items.find((item) => item.label === "Overview")!.active).toBe(false);
  });

  it("keeps the group for an app without the Insights hub", () => {
    unregister = registerConsoleNavEntry({ id: "funnels", label: "Funnels", segment: "funnels", after: "product-analytics", group: "product-analytics" });
    expect(analytics({ productAnalytics: undefined }).children!.map((child) => child.label)).toEqual([...TABS, "Funnels"]);
  });

  it("puts a row with no group after its anchor, or the nearest row the app draws, and hides it when not available", () => {
    unregister = registerConsoleNavEntry({
      id: "funnels",
      label: "Funnels",
      segment: "funnels",
      after: "product-analytics",
      available: (input) => input.project !== "ledger",
    });
    let items = labels();
    expect(items.indexOf("Funnels")).toBe(items.indexOf("Analytics") + 1);
    expect(labels({ project: "ledger" })).not.toContain("Funnels");
    unregister();
    // Dashboards is a top-level row for every app (U6c): the row follows it.
    unregister = registerConsoleNavEntry({ id: "funnels", label: "Funnels", segment: "funnels", after: "dashboards" });
    items = labels();
    expect(items.indexOf("Funnels")).toBe(items.indexOf("Dashboards") + 1);
    unregister();
    // An anchor folded into a group (Sessions, now under Observability) places the row after that group.
    unregister = registerConsoleNavEntry({ id: "funnels", label: "Funnels", segment: "funnels", after: "sessions" });
    items = labels();
    expect(items.indexOf("Funnels")).toBe(items.indexOf("Observability") + 1);
  });

  it("opens a page with routes of its own (`target`), lit on its `kinds`, and leaves that page's path alone", () => {
    unregister = registerConsoleNavEntry({
      id: "audit",
      label: "Audit",
      segment: "audit",
      after: "dashboards",
      target: { kind: "activity" },
      kinds: ["activity"],
    });
    const row = () => nav({ page: { kind: "activity" } }).items.find((item) => item.label === "Audit")!;
    expect(row().href).toBe("/forge/p/shop/activity?env=prod");
    expect(row().active).toBe(true);
    expect(nav().items.find((item) => item.label === "Audit")!.active).toBe(false);
    // Its segment is not a registered page: the built-in route owns the URL.
    expect(consolePageOf("/forge/p/shop/audit")).toBeNull();
  });
});
