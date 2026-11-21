// Copyright (c) 2026 Reliant Labs

import { describe, expect, it } from "vitest";

import type { LineCoverage } from "@/services/controlPlane/tests/types";

import { childName, compactCount, coverageTarget, lineState, parentPath, pathCrumbs, percentOf } from "../coverageModel";

const line = (hits: Record<string, number>, partial = false, statements = 1): LineCoverage => ({ line: 1, statements, hits, partial, changed: false });

describe("coverage paths", () => {
  it("walks up and names children under their parent", () => {
    expect(parentPath("internal/billing/refund.go")).toBe("internal/billing");
    expect(parentPath("main.go")).toBe("");
    expect(pathCrumbs("internal/billing").map((crumb) => crumb.path)).toEqual(["", "internal", "internal/billing"]);
    expect(childName("internal/billing/refund.go", "internal/billing")).toBe("refund.go");
    expect(childName("internal/billing", "")).toBe("internal/billing");
  });
});

describe("whose report", () => {
  it("a commit wins, then an explicitly chosen environment, then the newest report", () => {
    expect(coverageTarget("abc", "env-1", "def")).toEqual({ gitCommit: "abc" });
    expect(coverageTarget(undefined, "env-1", "def")).toEqual({ environmentId: "env-1" });
    expect(coverageTarget(undefined, undefined, "def")).toEqual({ gitCommit: "def" });
    expect(coverageTarget(undefined, undefined, null)).toBeNull();
  });
});

describe("a line's state", () => {
  it("combined: any lane's hit covers it; partial needs a hit", () => {
    expect(lineState(line({ unit: 3 }))).toBe("covered");
    expect(lineState(line({}))).toBe("uncovered");
    expect(lineState(line({}, true))).toBe("uncovered");
    expect(lineState(line({ e2e: 1 }, true))).toBe("partial");
    expect(lineState(line({}, false, 0))).toBeNull();
  });

  it("one lane: only that lane's hits count", () => {
    expect(lineState(line({ unit: 3 }), "e2e")).toBe("uncovered");
    expect(lineState(line({ unit: 3, e2e: 1 }), "e2e")).toBe("covered");
  });
});

describe("figures", () => {
  it("never says 0% for nothing, and keeps counts narrow", () => {
    expect(percentOf(null)).toBe("—");
    expect(percentOf({ statements: 0, covered: 0, percent: 0 })).toBe("—");
    expect(percentOf({ statements: 10, covered: 0, percent: 0 })).toBe("0.0%");
    expect(compactCount(999)).toBe("999");
    expect(compactCount(2_310)).toBe("2.3k");
    expect(compactCount(48_210)).toBe("48k");
  });
});
