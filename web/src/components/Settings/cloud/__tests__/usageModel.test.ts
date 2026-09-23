// Tests for the deploy usage/budget domain model.
//
// Ported from control-plane internal-console's `src/lib/usage-model.test.ts`.
// The assertions are unchanged; only the fixtures moved, because this module
// declares its own wire interfaces rather than importing generated protobuf
// types (see usageModel.ts's header for why).
//
// The thing asserted hardest here is the one that costs real money when it is
// wrong: buildOverageRequest must ALWAYS emit budget_cents, because
// SetCurrentUserInfraOverage replaces the stored cap outright and a request
// that omits the field silently uncaps the user.
//
// Note what is NOT tested here, deliberately: unit conversion between catalog
// allowances and meter quantities. That reconciliation lives on the server
// (internal/billing/inframeter), and GetCurrentUserInfraOverage reports
// consumed and included_allowance already in one declared unit. A client-side
// conversion test would be pinning a second implementation we do not want to
// have.

import { describe, expect, it } from "vitest";

import {
  DeployResourceKind,
  buildAttribution,
  buildOverageRequest,
  buildUsageSummary,
  overageChoiceFromCap,
  parseRung,
  unitLabel,
} from "@/components/Settings/cloud/usage/usageModel";

import type {
  DeployUsageRow,
  InfraDimensionUsage,
  InfraOverageResponse,
} from "@/components/Settings/cloud/usage/usageModel";

function usageRow(partial: Partial<DeployUsageRow> = {}): DeployUsageRow {
  return {
    resourceKind: DeployResourceKind.CPU_MILLI,
    environmentId: "env-1",
    deploymentId: "dep-1",
    quantity: 0n,
    costUsdNanos: 0n,
    ...partial,
  };
}

function dimension(partial: Partial<InfraDimensionUsage> = {}): InfraDimensionUsage {
  return {
    dimensionId: "infra_cpu",
    unit: "vcpu_hours",
    consumed: 0,
    includedAllowance: 100,
    action: "none",
    suspendable: true,
    withinRestoreFloor: false,
    ...partial,
  };
}

function overageResponse(
  partial: Partial<InfraOverageResponse> = {},
): InfraOverageResponse {
  return {
    dimensions: [dimension()],
    accruedOverageCents: 0,
    budgetCapReached: false,
    usageMeasured: true,
    ...partial,
  };
}

// ── The partial-send hazard ───────────────────────────────────────────

describe("buildOverageRequest — the partial-send hazard", () => {
  it("always sends budget_cents, for every possible choice", () => {
    // THE regression test. The RPC replaces the cap outright, so a request
    // missing this field silently uncaps the user and nothing surfaces it
    // until the bill arrives.
    const choices = [
      { kind: "uncapped" } as const,
      { kind: "capped", budgetCents: 5000 } as const,
    ];

    for (const choice of choices) {
      const fields = buildOverageRequest(choice);
      expect(Object.hasOwn(fields, "budgetCents")).toBe(true);
      expect(fields.budgetCents).not.toBeUndefined();
      expect(typeof fields.budgetCents).toBe("bigint");
    }
  });

  it("sends the user's cap unchanged when they are capped", () => {
    expect(buildOverageRequest({ kind: "capped", budgetCents: 2500 })).toEqual({
      budgetCents: 2500n,
    });
  });

  it("spells 'no cap' as an explicit zero rather than an omission", () => {
    // 0 and unset are both uncapped to the enforcement gate (it tests `> 0`),
    // but an explicit 0 is distinguishable from a dropped field in a log or a
    // network trace — which is what makes the bug findable.
    expect(buildOverageRequest({ kind: "uncapped" })).toEqual({ budgetCents: 0n });
  });

  it("carries no enabled flag — the deploy cap has no off switch", () => {
    // A bool here would imply an off position the platform does not have: a
    // running deployment accrues usage whether or not anyone opted in.
    expect(Object.hasOwn(buildOverageRequest({ kind: "uncapped" }), "enabled")).toBe(
      false,
    );
  });
});

describe("overageChoiceFromCap — 'no cap' vs 'cap of 0'", () => {
  it("reads an unset cap as uncapped, never as a zero-dollar cap", () => {
    expect(overageChoiceFromCap(undefined)).toEqual({ kind: "uncapped" });
  });

  it("reads a stored 0 as uncapped too — 0 means no cap on the wire", () => {
    // A UI that rendered this as "$0.00" would tell the user nothing can bill
    // them, which is the exact inverse of what a 0 means.
    expect(overageChoiceFromCap(0n)).toEqual({ kind: "uncapped" });
  });

  it("reads a positive cap as capped", () => {
    expect(overageChoiceFromCap(1500n)).toEqual({ kind: "capped", budgetCents: 1500 });
  });
});

// ── Consumption against allowance ─────────────────────────────────────

describe("buildUsageSummary — consumption against allowance", () => {
  function cpuAt(consumed: number, action = "none") {
    const summary = buildUsageSummary({
      overage: overageResponse({
        dimensions: [dimension({ consumed, includedAllowance: 100, action })],
      }),
      rows: [],
    });
    return summary.dimensions.find((d) => d.id === "cpu");
  }

  it("renders 0% when nothing has been consumed", () => {
    const cpu = cpuAt(0);
    expect(cpu?.percentUsed).toBe(0);
    expect(cpu?.overage).toBe(0);
  });

  it("renders a mid-range reading", () => {
    const cpu = cpuAt(50);
    expect(cpu?.percentUsed).toBe(50);
    expect(cpu?.overage).toBe(0);
  });

  it("renders exactly 100% at the allowance, with no overage yet", () => {
    const cpu = cpuAt(100);
    expect(cpu?.percentUsed).toBe(100);
    expect(cpu?.overage).toBe(0);
  });

  it("reports the overage amount when over", () => {
    const cpu = cpuAt(150);
    expect(cpu?.percentUsed).toBe(150);
    expect(cpu?.overage).toBe(50);
  });

  it("compares consumed and allowance without converting either", () => {
    // Both arrive in the unit the server declared, so the reading is a plain
    // division. A client-side 1000x or 1024x factor here would be the bug.
    const summary = buildUsageSummary({
      overage: overageResponse({
        dimensions: [
          dimension({
            dimensionId: "infra_memory",
            consumed: 512,
            includedAllowance: 1024,
          }),
        ],
      }),
      rows: [],
    });
    const memory = summary.dimensions.find((d) => d.id === "memory");
    expect(memory?.consumed).toBe(512);
    expect(memory?.allowance).toBe(1024);
    expect(memory?.percentUsed).toBe(50);
  });

  it("treats any consumption with no allowance as fully consumed", () => {
    const cpu = cpuAt(0);
    expect(cpu?.hasAllowance).toBe(true);

    const summary = buildUsageSummary({
      overage: overageResponse({
        dimensions: [dimension({ consumed: 5, includedAllowance: 0 })],
      }),
      rows: [],
    });
    const noAllowance = summary.dimensions.find((d) => d.id === "cpu");
    expect(noAllowance?.hasAllowance).toBe(false);
    expect(noAllowance?.percentUsed).toBe(100);
  });

  it("surfaces only cpu, memory and storage — never vcluster, egress or build", () => {
    // vcluster_floor is a real dimension the RPC can report, but it is not one
    // of the three tiers this product sells. Egress/CDN/build cannot appear at
    // all — nothing measures them, so they were removed from the catalog.
    const summary = buildUsageSummary({
      overage: overageResponse({
        dimensions: [
          dimension({ dimensionId: "infra_cpu" }),
          dimension({ dimensionId: "infra_memory" }),
          dimension({ dimensionId: "infra_storage", suspendable: false }),
          dimension({ dimensionId: "vcluster_floor" }),
        ],
      }),
      rows: [],
    });
    expect(summary.dimensions.map((d) => d.id)).toEqual(["cpu", "memory", "storage"]);
  });

  it("marks every figure as an estimate", () => {
    expect(buildUsageSummary({ rows: [] }).isEstimate).toBe(true);
  });

  it("carries the server's accrued overage rather than re-deriving one", () => {
    const summary = buildUsageSummary({
      overage: overageResponse({ accruedOverageCents: 412, budgetCapReached: true }),
      rows: [],
    });
    expect(summary.accruedOverageCents).toBe(412);
    expect(summary.budgetCapReached).toBe(true);
  });

  it("marks storage as non-suspendable when the server says so", () => {
    const summary = buildUsageSummary({
      overage: overageResponse({
        dimensions: [dimension({ dimensionId: "infra_storage", suspendable: false })],
      }),
      rows: [],
    });
    expect(summary.dimensions[0]?.suspendable).toBe(false);
  });
});

// ── Unmeasured usage ──────────────────────────────────────────────────

describe("buildUsageSummary — usage_measured", () => {
  it("defaults to NOT measured when the server did not say", () => {
    // Withholding is the safe reading: a missing flag must not default to
    // "measured" and reinstate the unknown-as-known bug.
    expect(buildUsageSummary({ rows: [] }).usageMeasured).toBe(false);
  });

  it("reports unmeasured usage as unmeasured rather than as zero", () => {
    const summary = buildUsageSummary({
      overage: overageResponse({ usageMeasured: false }),
      rows: [],
    });
    expect(summary.usageMeasured).toBe(false);
  });

  it("reports measured usage as measured", () => {
    expect(
      buildUsageSummary({ overage: overageResponse({ usageMeasured: true }), rows: [] })
        .usageMeasured,
    ).toBe(true);
  });
});

// ── The no-subscription state ─────────────────────────────────────────

describe("buildUsageSummary — no compute subscription", () => {
  it("reports no dimensions rather than inventing a free allowance", () => {
    const summary = buildUsageSummary({
      overage: overageResponse({ dimensions: [] }),
      rows: [],
    });
    expect(summary.dimensions).toHaveLength(0);
  });

  it("does not claim overage it cannot price without a plan", () => {
    const summary = buildUsageSummary({
      overage: overageResponse({ dimensions: [], accruedOverageCents: 0 }),
      rows: [],
    });
    expect(summary.accruedOverageCents).toBe(0);
  });

  it("clamps a negative declared allowance to zero, never to unlimited", () => {
    const summary = buildUsageSummary({
      overage: overageResponse({
        dimensions: [dimension({ includedAllowance: -1 })],
      }),
      rows: [],
    });
    expect(summary.dimensions[0]?.hasAllowance).toBe(false);
  });
});

// ── The ladder ────────────────────────────────────────────────────────

describe("parseRung", () => {
  it("accepts every rung the server's vocabulary defines", () => {
    for (const action of ["none", "notify", "block_scale_up", "throttle", "suspend"]) {
      expect(parseRung(action)).toBe(action);
    }
  });

  it("maps an unrecognised rung to 'unknown', not to 'none'", () => {
    // A rung this build has never heard of is more likely to be a NEW, more
    // severe one. Reporting it as "nothing is happening" is the failure
    // direction that costs the user their service with no warning.
    expect(parseRung("quarantine")).toBe("unknown");
    expect(parseRung("")).toBe("unknown");
  });

  it("takes the server's rung verbatim rather than deriving one", () => {
    const summary = buildUsageSummary({
      overage: overageResponse({
        // Consumption well inside the allowance, but the server says
        // throttled — because it can see a multi-day streak this client
        // cannot. The server wins.
        dimensions: [dimension({ consumed: 10, includedAllowance: 100, action: "throttle" })],
      }),
      rows: [],
    });
    expect(summary.dimensions[0]?.rung).toBe("throttle");
  });
});

describe("unitLabel", () => {
  it("renders the server's unit tokens readably", () => {
    expect(unitLabel("vcpu_hours")).toBe("vCPU-hours");
    expect(unitLabel("gib_hours")).toBe("GiB-hours");
  });

  it("shows an unrecognised unit verbatim rather than guessing", () => {
    expect(unitLabel("furlongs")).toBe("furlongs");
  });
});

// ── Attribution ───────────────────────────────────────────────────────

describe("buildAttribution", () => {
  it("folds rows per deployment and sorts by accrued cost", () => {
    const rows = [
      usageRow({ deploymentId: "cheap", quantity: 1000n, costUsdNanos: 10_000_000n }),
      usageRow({ deploymentId: "pricey", quantity: 5000n, costUsdNanos: 90_000_000n }),
    ];
    const attribution = buildAttribution(rows);
    expect(attribution.map((r) => r.deploymentId)).toEqual(["pricey", "cheap"]);
    expect(attribution[0]?.accruedCents).toBe(9);
    // 5000 milli-vCPU-hours displayed as 5 vCPU-hours. Display only — this
    // figure is never compared against an allowance.
    expect(attribution[0]?.cpuDisplay).toBe(5);
  });

  it("keeps rows that name no deployment instead of dropping them", () => {
    // A vCluster floor is real money with nothing to blame. Dropping it makes
    // this table disagree with the total above it.
    const attribution = buildAttribution([
      usageRow({ deploymentId: "", environmentId: "", costUsdNanos: 50_000_000n }),
    ]);
    expect(attribution).toHaveLength(1);
    expect(attribution[0]?.accruedCents).toBe(5);
  });

  it("separates the same deployment id across different environments", () => {
    const attribution = buildAttribution([
      usageRow({ deploymentId: "dep", environmentId: "prod", costUsdNanos: 10_000_000n }),
      usageRow({ deploymentId: "dep", environmentId: "staging", costUsdNanos: 10_000_000n }),
    ]);
    expect(attribution).toHaveLength(2);
  });
});
