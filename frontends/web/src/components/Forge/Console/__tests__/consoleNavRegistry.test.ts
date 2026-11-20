import { afterEach, describe, expect, it } from "vitest";

import { registerConsoleNavEntry } from "../consoleNavRegistry";
import { buildGroupedConsoleNav, type GroupedNavInput } from "../groupedNav";
import { consolePageOf, consolePath } from "../paths";

const hub = { open: { kind: "dashboard" as const, dashKind: "release" as const, key: "product-users" }, keys: ["product-users"] };
const nav = (over: Partial<GroupedNavInput> = {}) =>
  buildGroupedConsoleNav({ project: "shop", page: { kind: "overview" }, env: "prod", envs: [{ name: "prod", preview: false }], productAnalytics: hub, ...over });
const labels = (over: Partial<GroupedNavInput> = {}) => nav(over).items.map((item) => item.label);

let unregister: (() => void) | undefined;
afterEach(() => {
  unregister?.();
  unregister = undefined;
});

describe("the console nav's registered rows", () => {
  it("has no Experiments row, and no Experiments page, until one is registered", () => {
    expect(labels()).not.toContain("Experiments");
    expect(consolePageOf("/forge/p/shop/experiments")).toBeNull();
  });

  it("puts a registered row after Product analytics, linking to its page, current there", () => {
    unregister = registerConsoleNavEntry({ id: "experiments", label: "Experiments", segment: "experiments", after: "product-analytics" });
    const items = labels();
    expect(items.indexOf("Experiments")).toBe(items.indexOf("Product analytics") + 1);
    const row = nav().items.find((item) => item.label === "Experiments")!;
    expect(row.href).toBe("/forge/p/shop/experiments?env=prod");
    expect(row.active).toBe(false);

    const page = consolePageOf("/forge/p/shop/experiments");
    expect(page).toEqual({ project: "shop", page: { kind: "extension", id: "experiments", segment: "experiments" } });
    expect(consolePath("shop", page!.page)).toBe("/forge/p/shop/experiments");
    expect(nav({ page: page!.page }).items.find((item) => item.label === "Experiments")!.active).toBe(true);
    expect(nav({ page: page!.page }).items.find((item) => item.label === "Overview")!.active).toBe(false);
  });

  it("follows the nearest row the app draws when its anchor is not drawn, and hides when not available", () => {
    unregister = registerConsoleNavEntry({
      id: "experiments",
      label: "Experiments",
      segment: "experiments",
      after: "product-analytics",
      available: (input) => input.project !== "ledger",
    });
    const items = labels({ productAnalytics: undefined });
    expect(items.indexOf("Experiments")).toBe(items.indexOf("Sessions") + 1);
    expect(labels({ project: "ledger" })).not.toContain("Experiments");
  });
});
