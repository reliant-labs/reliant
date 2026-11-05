import { describe, expect, it } from "vitest";

import type { OrganizationCostSummary } from "@/services/controlPlane/observability/organizationCostsApi";
import type { OrganizationUsage } from "@/services/controlPlane/observability/organizationUsageApi";
import { categoryAmount, spendSeries } from "@/components/Forge/Observability/Dashboards/organizationUsageModel";

import {
  FORGE_USAGE_CATEGORIES,
  forgeScopedCostSummary,
  forgeScopedUsage,
  isForgeUsageCategory,
  provisionedRows,
} from "../forgeUsage";

/**
 * Usage & spend in the forge shell is the org's billing history narrowed to
 * what forge provisions. Managed LLM must not reach a single panel.
 */

const ALL = ["managed_llm", "machines", "hosted_apps", "databases", "static_hosting", "hosted_other", "observability"];

function usage(): OrganizationUsage {
  return {
    orgId: "org_1",
    currency: "USD",
    granularity: 1,
    groupBy: 1,
    completeness: "complete",
    points: ALL.map((category) => ({
      category,
      groupKey: "",
      groupLabel: category,
      detail: "",
      bucketStart: { seconds: 1_767_225_600n, nanos: 0 },
      listEquivalentUsdNanos: 1_000_000_000n,
      unpricedDimensions: [],
      quantities: [],
    })),
    coverage: ALL.map((category) => ({ category, completeness: "complete", limitations: [] })),
    spendRates: ALL.map((category) => ({ category, basis: "list_equivalent", measuredHours: 24, unpricedDimensions: [], lowerBound: false })),
    inventory: [
      { category: "machines", size: "large", state: "running", environmentId: "", environmentName: "", count: 2n, storageGib: 100n },
      { category: "databases", size: "small", state: "running", environmentId: "env_1", environmentName: "prod", count: 1n, storageGib: 10n },
      { category: "managed_llm", size: "", state: "", environmentId: "", environmentName: "", count: 9n, storageGib: 0n },
    ],
  } as unknown as OrganizationUsage;
}

describe("FORGE_USAGE_CATEGORIES", () => {
  it("is hosting, static, databases, machines, builds/registry and observability — never managed LLM", () => {
    expect([...FORGE_USAGE_CATEGORIES].sort()).toEqual(ALL.filter((c) => c !== "managed_llm").sort());
    expect(isForgeUsageCategory("managed_llm")).toBe(false);
    expect(isForgeUsageCategory("static_hosting")).toBe(true);
  });
});

describe("forgeScopedUsage", () => {
  it("drops managed LLM from every list a panel reads", () => {
    const scoped = forgeScopedUsage(usage());
    for (const list of [scoped.points, scoped.coverage, scoped.spendRates, scoped.inventory]) {
      expect(list.some((entry: { category: string }) => entry.category === "managed_llm")).toBe(false);
    }
    expect(scoped.points).toHaveLength(6);
    expect(scoped.completeness).toBe("complete");
  });

  it("leaves no managed-LLM spend in a total or a chart series", () => {
    const scoped = forgeScopedUsage(usage());
    expect(categoryAmount(scoped, "managed_llm").kind).not.toBe("amount");
    expect(spendSeries(scoped, "list_equivalent").map((s) => s.key)).not.toContain("managed_llm");
    expect(categoryAmount(scoped, "hosted_apps").kind).toBe("amount");
  });

  it("does not mutate the cached answer it was given", () => {
    const original = usage();
    forgeScopedUsage(original);
    expect(original.points).toHaveLength(7);
  });
});

describe("forgeScopedCostSummary", () => {
  it("keeps hosting, machines and observability sources and drops managed LLM", () => {
    const summary = {
      orgId: "org_1",
      currency: "USD",
      periods: [],
      sources: ["managed_llm", "machines", "hosted_infrastructure", "observability"].map((category) => ({ category, allowances: [] })),
    } as unknown as OrganizationCostSummary;
    expect(forgeScopedCostSummary(summary).sources.map((s) => s.category)).toEqual(["machines", "hosted_infrastructure", "observability"]);
  });
});

describe("provisionedRows", () => {
  it("lists what is provisioned now, largest first, without managed LLM", () => {
    expect(provisionedRows(usage())).toEqual([
      { category: "machines", size: "large", state: "running", environment: "", count: 2, storageGib: 100 },
      { category: "databases", size: "small", state: "running", environment: "prod", count: 1, storageGib: 10 },
    ]);
  });
});
