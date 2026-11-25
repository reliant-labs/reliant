// @vitest-environment node
// Copyright (c) 2025 Reliant Labs

/**
 * Experiments' place in the console: its URLs read back to the same pages.
 * (Its nav row, hidden until the deployment turns CHANGES_UI on except while
 * one of its pages is open, is the shell's: Layout/sidebar/__tests__/
 * navConfig.test.tsx.) Plus the page's arithmetic.
 */

import { describe, expect, it } from "vitest";

import type { Change } from "@/services/controlPlane/changes/changesTypes";

import { consolePageOf, consolePath } from "../../Console/paths";
import { changedFields, evenSplit, formatBp, parsePercent } from "../changeModel";

describe("Experiments in the console", () => {
  it("has URLs that read back to the same pages", () => {
    expect(consolePath("shop", { kind: "experiments" })).toBe("/forge/p/shop/experiments");
    expect(consolePath("shop", { kind: "experiment", changeId: "chg 1" })).toBe("/forge/p/shop/experiments/chg%201");
    expect(consolePageOf("/forge/p/shop/experiments")).toEqual({ project: "shop", page: { kind: "experiments" } });
    expect(consolePageOf("/forge/p/shop/experiments/chg%201")).toEqual({ project: "shop", page: { kind: "experiment", changeId: "chg 1" } });
    expect(consolePageOf("/forge/p/shop/experiments/chg-1/extra")).toBeNull();
  });
});

describe("change arithmetic", () => {
  it("reads percentages as basis points", () => {
    expect(parsePercent("5")).toBe(500);
    expect(parsePercent("0.5%")).toBe(50);
    expect(parsePercent("101")).toBeNull();
    expect(parsePercent("abc")).toBeNull();
    expect(formatBp(2550)).toBe("25.5");
  });

  it("splits evenly, the remainder to the first version", () => {
    expect(evenSplit(["a", "b", "c"])).toEqual([
      { variant: "a", weightBp: 3334 },
      { variant: "b", weightBp: 3333 },
      { variant: "c", weightBp: 3333 },
    ]);
  });

  it("says what moved between two revisions", () => {
    const base = { mode: "off", percentBp: 0, unit: "user", audience: null, split: [], metric: null, preview: [], currentRun: null, description: "", revision: 1 } as unknown as Change;
    expect(changedFields(base, { ...base, revision: 2 })).toEqual(["Revision 1 → 2"]);
    expect(changedFields(base, { ...base, revision: 2, mode: "rollout", percentBp: 500, audience: { attribute: "plan", in: ["pro"] } })).toEqual([
      "Mode: Off → Rolling out",
      "Share: 0% → 5%",
      "Eligible: everyone → plan is pro",
    ]);
  });
});
