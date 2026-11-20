import { afterEach, describe, expect, it } from "vitest";

import { registerConsoleNavEntry } from "../consoleNavRegistry";
import { buildGroupedConsoleNav, type GroupedNavInput } from "../groupedNav";
import { consolePageOf, consolePath } from "../paths";

const hub = { open: { kind: "dashboard" as const, dashKind: "release" as const, key: "product-users" }, keys: ["product-users"] };
const nav = (over: Partial<GroupedNavInput> = {}) =>
  buildGroupedConsoleNav({ project: "shop", page: { kind: "overview" }, env: "prod", envs: [{ name: "prod", preview: false }], productAnalytics: hub, ...over });
const labels = (over: Partial<GroupedNavInput> = {}) => nav(over).items.map((item) => item.label);
const analytics = (over: Partial<GroupedNavInput> = {}) => nav(over).items.find((item) => item.label === "Product analytics")!;

let unregister: (() => void) | undefined;
afterEach(() => {
  unregister?.();
  unregister = undefined;
});

describe("the console nav's registered rows", () => {
  it("has no Experiments row, and no Experiments page, until one is registered", () => {
    expect(labels()).not.toContain("Experiments");
    expect(analytics().children!.map((child) => child.label)).toEqual(["Insights", "Events & metrics"]);
    expect(consolePageOf("/forge/p/shop/experiments")).toBeNull();
  });

  it("puts a row registered into the Product analytics group inside it, last, linking to its page, current there", () => {
    unregister = registerConsoleNavEntry({ id: "experiments", label: "Experiments", segment: "experiments", after: "product-analytics", group: "product-analytics" });
    expect(labels()).not.toContain("Experiments");
    const children = analytics().children!;
    expect(children.map((child) => child.label)).toEqual(["Insights", "Events & metrics", "Experiments"]);
    expect(children[2]!.href).toBe("/forge/p/shop/experiments?env=prod");
    expect(children[2]!.active).toBe(false);

    const page = consolePageOf("/forge/p/shop/experiments");
    expect(page).toEqual({ project: "shop", page: { kind: "extension", id: "experiments", segment: "experiments" } });
    expect(consolePath("shop", page!.page)).toBe("/forge/p/shop/experiments");
    expect(analytics({ page: page!.page }).children![2]!.active).toBe(true);
    expect(nav({ page: page!.page }).items.find((item) => item.label === "Overview")!.active).toBe(false);
  });

  it("keeps the group for an app without the Insights hub", () => {
    unregister = registerConsoleNavEntry({ id: "experiments", label: "Experiments", segment: "experiments", after: "product-analytics", group: "product-analytics" });
    expect(analytics({ productAnalytics: undefined }).children!.map((child) => child.label)).toEqual(["Events & metrics", "Experiments"]);
  });

  it("puts a row with no group after its anchor, or the nearest row the app draws, and hides it when not available", () => {
    unregister = registerConsoleNavEntry({
      id: "experiments",
      label: "Experiments",
      segment: "experiments",
      after: "product-analytics",
      available: (input) => input.project !== "ledger",
    });
    let items = labels();
    expect(items.indexOf("Experiments")).toBe(items.indexOf("Product analytics") + 1);
    expect(labels({ project: "ledger" })).not.toContain("Experiments");
    unregister();
    // Dashboards is a top-level row for every app (U6c): the row follows it.
    unregister = registerConsoleNavEntry({ id: "experiments", label: "Experiments", segment: "experiments", after: "dashboards" });
    items = labels();
    expect(items.indexOf("Experiments")).toBe(items.indexOf("Dashboards") + 1);
  });
});
