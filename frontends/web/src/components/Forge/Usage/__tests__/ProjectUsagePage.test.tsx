// Copyright (c) 2026 Reliant Labs

import { render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { ProjectCost } from "@/services/controlPlane/observability/organizationCostsApi";

import { ProjectUsagePage } from "../ProjectUsagePage";

const state = vi.hoisted(() => ({ cost: { isLoading: false, error: null, data: undefined } as { isLoading: boolean; error: unknown; data: unknown } }));

vi.mock("@tanstack/react-router", () => ({ useNavigate: () => vi.fn() }));
vi.mock("../../Console/consoleContext", () => ({ useForgeConsole: () => ({ forgeProject: "shopfront", roster: { envs: [] } }) }));
vi.mock("@/hooks/observability-queries", () => ({ useObservabilityOrg: () => ({ orgId: "org-1" }) }));
vi.mock("@/hooks/organization-usage-queries", () => ({ useProjectCost: () => state.cost }));
vi.mock("../../Observability/AskReliant/AskReliant", () => ({ DeployWithAgentButton: () => <button type="button">Deploy</button> }));
vi.mock("../../Observability/charts/UPlotChart", () => ({ UPlotChart: () => <div data-testid="usage-chart" /> }));

const DAY = 86_400n;
const cost = (daily: { day: bigint; category: string; usdNanos?: bigint }[]) =>
  ({
    monthToDate: { usdNanos: 10_000_000_000n, basis: "estimated", lowerBound: false, unpricedDimensions: [] },
    daily: daily.map((d) => ({
      day: { seconds: d.day * DAY, nanos: 0 },
      category: d.category,
      environmentId: "prod",
      list: d.usdNanos === undefined ? undefined : { usdNanos: d.usdNanos, basis: "list", lowerBound: false, unpricedDimensions: [] },
      quantities: [],
      partial: false,
    })),
    drivers: [],
  }) as unknown as ProjectCost;

describe("ProjectUsagePage", () => {
  beforeEach(() => {
    state.cost = { isLoading: false, error: null, data: undefined };
  });

  it("shows one day of recorded usage instead of the no-usage empty state", () => {
    state.cost.data = cost([{ day: 20_000n, category: "compute", usdNanos: 10_000_000_000n }]);
    render(<ProjectUsagePage />);
    expect(screen.queryByTestId("usage-none")).toBeNull();
    expect(screen.getByTestId("usage-one-day").textContent).toContain("$10.00");
    expect(screen.getByTestId("usage-daily-total").textContent).toContain("$10.00 so far");
  });

  it("shows the empty state only when no day was recorded, without a $0.00 total", () => {
    state.cost.data = cost([]);
    render(<ProjectUsagePage />);
    expect(screen.getByTestId("usage-none")).toBeTruthy();
    expect(screen.getByTestId("usage-daily-total").textContent).not.toContain("$0.00");
  });

  it("charts two or more days, and qualifies a total with unmeasured rows as a lower bound", () => {
    state.cost.data = cost([
      { day: 20_000n, category: "compute", usdNanos: 1_000_000_000n },
      { day: 20_001n, category: "compute", usdNanos: 1_000_000_000n },
      { day: 20_001n, category: "storage" },
    ]);
    render(<ProjectUsagePage />);
    expect(screen.getByTestId("usage-chart")).toBeTruthy();
    expect(screen.getByTestId("usage-daily-total").textContent).toContain("≥ $2.00 so far");
    const byCategory = screen.getByTestId("usage-by-category").textContent ?? "";
    expect(byCategory).toContain("Database storage");
    expect(byCategory).toContain("n/a");
    expect(byCategory).not.toContain("$0.00");
  });
});
