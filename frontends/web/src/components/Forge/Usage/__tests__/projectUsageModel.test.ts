// Copyright (c) 2026 Reliant Labs

import { describe, expect, it } from "vitest";

import type { ProjectCostDay } from "@/gen/controlplane/services/billing/v1/billing_pb";

import { aggregateAmountText, projectDaily } from "../projectUsageModel";

const DAY = 86_400;
const row = (day: number, category: string, environmentId: string, usdNanos?: bigint, extra: { lowerBound?: boolean; unpricedDimensions?: string[] } = {}) =>
  ({
    day: { seconds: BigInt(day * DAY), nanos: 0 },
    category,
    environmentId,
    list: usdNanos === undefined && !extra.lowerBound ? undefined : { usdNanos, basis: "list", lowerBound: false, unpricedDimensions: [], ...extra },
  }) as unknown as ProjectCostDay;

describe("projectDaily", () => {
  it("reports an unmeasured category as n/a, never $0, and the partial total as a lower bound", () => {
    const view = projectDaily([row(1, "compute", "prod", 2_000_000_000n), row(1, "storage", "prod")]);
    expect(view.total).toEqual({ nanos: 2_000_000_000n, lowerBound: true });
    expect(aggregateAmountText(view.total)).toBe("≥ $2.00");
    expect(view.byCategory).toEqual([
      ["compute", { nanos: 2_000_000_000n, lowerBound: false }],
      ["storage", { nanos: undefined, lowerBound: false }],
    ]);
    expect(aggregateAmountText(view.byCategory[1][1])).toBe("n/a");
    expect(view.byEnv).toEqual([["prod", { nanos: 2_000_000_000n, lowerBound: true }]]);
  });

  it("keeps an explicit zero a zero and an unmeasured cell a gap on the chart", () => {
    const view = projectDaily([row(1, "compute", "prod", 0n), row(1, "storage", "prod"), row(2, "compute", "prod", 1_000_000_000n)]);
    expect(view.days).toEqual([DAY, 2 * DAY]);
    expect(view.categories).toEqual([
      { category: "compute", values: [0, 1] },
      { category: "storage", values: [null, 0] },
    ]);
    expect(aggregateAmountText(projectDaily([row(1, "compute", "prod", 0n)]).total)).toBe("$0.00");
  });

  it("carries a row's own lower bound and unpriced dimensions into its sums", () => {
    const view = projectDaily([
      row(1, "compute", "prod", 1_000_000_000n, { lowerBound: true }),
      row(1, "bandwidth", "staging", 500_000_000n, { unpricedDimensions: ["cdn_gib"] }),
      row(1, "databases", "dev", 250_000_000n),
    ]);
    expect(view.byCategory.map(([c, a]) => [c, aggregateAmountText(a)])).toEqual([
      ["compute", "≥ $1.00"],
      ["bandwidth", "≥ $0.50"],
      ["databases", "$0.25"],
    ]);
    expect(view.total).toEqual({ nanos: 1_750_000_000n, lowerBound: true });
  });

  it("has no total when there are no rows: unknown, not $0", () => {
    const view = projectDaily([]);
    expect(view.days).toEqual([]);
    expect(view.total).toEqual({ nanos: undefined, lowerBound: false });
    expect(aggregateAmountText(view.total)).toBe("n/a");
  });
});
