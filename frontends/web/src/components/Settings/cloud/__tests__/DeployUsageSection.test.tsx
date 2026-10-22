// The deploy usage surface — the states a customer actually lands in.
//
// Ported from control-plane internal-console's `src/app/usage/page.test.tsx`.
// The console version drove generated hooks through a mock Connect transport
// because it WAS the route and fetched its own data. This surface is
// prop-driven (like every other band in cloud settings), so these tests drive
// the REAL domain layer — buildUsageSummary over wire-shaped fixtures — into
// the real section. The path under test is therefore the same one production
// takes from the RPC response onward; only the transport hop is absent, and
// that hop is the adapter's concern rather than this surface's.

import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { DeployUsageSection } from "@/components/Settings/cloud/usage/DeployUsageSection";
import {
  DeployResourceKind,
  buildUsageSummary,
} from "@/components/Settings/cloud/usage/usageModel";

import type {
  DeployUsageRow,
  InfraDimensionUsage,
  InfraOverageResponse,
} from "@/components/Settings/cloud/usage/usageModel";

afterEach(cleanup);

function dimension(partial: Partial<InfraDimensionUsage> = {}): InfraDimensionUsage {
  return {
    dimensionId: "infra_cpu",
    unit: "vcpu_hours",
    consumed: 50,
    includedAllowance: 100,
    action: "none",
    suspendable: true,
    withinRestoreFloor: false,
    ...partial,
  };
}

function infraOverageResponse(
  partial: Partial<InfraOverageResponse> = {},
): InfraOverageResponse {
  return {
    dimensions: [
      dimension(),
      dimension({ dimensionId: "infra_memory", unit: "gib_hours" }),
      dimension({
        dimensionId: "infra_storage",
        unit: "gib_hours",
        suspendable: false,
      }),
    ],
    budgetCents: 2500n,
    accruedOverageCents: 0,
    budgetCapReached: false,
    usageMeasured: true,
    periodStart: { seconds: 1_700_000_000n, nanos: 0 },
    periodEnd: { seconds: 1_702_000_000n, nanos: 0 },
    ...partial,
  };
}

function usageRow(overrides: Partial<DeployUsageRow> = {}): DeployUsageRow {
  return {
    resourceKind: DeployResourceKind.CPU_MILLI,
    environmentId: "env-1",
    deploymentId: "dep-1",
    quantity: 50_000n,
    costUsdNanos: 450_000_000n,
    ...overrides,
  };
}

function renderSection({
  overage = infraOverageResponse(),
  rows = [usageRow()],
  ...rest
}: {
  overage?: InfraOverageResponse;
  rows?: DeployUsageRow[];
} & Partial<Parameters<typeof DeployUsageSection>[0]> = {}) {
  const onSaveCap = vi.fn();
  render(
    <DeployUsageSection
      summary={buildUsageSummary({ overage, rows })}
      onSaveCap={onSaveCap}
      deploymentNames={new Map([["dep-1", "api"]])}
      {...rest}
    />,
  );
  return { onSaveCap };
}

describe("DeployUsageSection — consumption against allowance", () => {
  it("renders each billable dimension with its consumption", () => {
    renderSection();

    expect(screen.getByText("CPU")).toBeDefined();
    expect(screen.getByText("Memory")).toBeDefined();
    expect(screen.getByText("Storage")).toBeDefined();
  });

  it("reads 50 of 100 vCPU-hours as 50% of the allowance", () => {
    renderSection();

    const cpuBar = screen.getByRole("progressbar", {
      name: /CPU consumption against allowance/i,
    });
    expect(cpuBar.getAttribute("aria-valuenow")).toBe("50");
  });

  it("renders 0%, 100% and over-100% distinguishably", () => {
    // 0% and 100% must not look alike, and an overage must state the figure
    // that costs money rather than clamping it away.
    const { unmount } = render(
      <DeployUsageSection
        summary={buildUsageSummary({
          overage: infraOverageResponse({
            dimensions: [dimension({ consumed: 0 })],
          }),
          rows: [],
        })}
        onSaveCap={vi.fn()}
      />,
    );
    expect(
      screen
        .getByRole("progressbar", { name: /CPU consumption/i })
        .getAttribute("aria-valuenow"),
    ).toBe("0");
    unmount();

    render(
      <DeployUsageSection
        summary={buildUsageSummary({
          overage: infraOverageResponse({
            dimensions: [dimension({ consumed: 100 })],
          }),
          rows: [],
        })}
        onSaveCap={vi.fn()}
      />,
    );
    expect(
      screen
        .getByRole("progressbar", { name: /CPU consumption/i })
        .getAttribute("aria-valuenow"),
    ).toBe("100");
    cleanup();

    render(
      <DeployUsageSection
        summary={buildUsageSummary({
          overage: infraOverageResponse({
            dimensions: [dimension({ consumed: 150, action: "block_scale_up" })],
          }),
          rows: [],
        })}
        onSaveCap={vi.fn()}
      />,
    );
    // The track is still full, but the overage is stated in words — clamping
    // would lose the only number that costs money.
    expect(
      screen
        .getByRole("progressbar", { name: /CPU consumption/i })
        .getAttribute("aria-valuenow"),
    ).toBe("100");
    expect(screen.getByText(/50 vCPU-hours over/i)).toBeDefined();
  });

  it("never surfaces egress, CDN or build dimensions", () => {
    renderSection();

    expect(screen.queryByText(/egress/i)).toBeNull();
    expect(screen.queryByText(/CDN/i)).toBeNull();
    expect(screen.queryByText(/build minutes/i)).toBeNull();
  });

  it("says storage is never suspended rather than threatening one", () => {
    renderSection({
      overage: infraOverageResponse({
        dimensions: [
          dimension({
            dimensionId: "infra_storage",
            unit: "gib_hours",
            suspendable: false,
            action: "block_scale_up",
          }),
        ],
      }),
    });

    expect(
      screen.getByText(/releasing storage would destroy your data/i),
    ).toBeDefined();
  });
});

describe("DeployUsageSection — honesty about estimates", () => {
  it("states that the figures are estimates rather than an invoice", () => {
    renderSection();

    expect(screen.getByText(/these are estimates, not an invoice/i)).toBeDefined();
  });

  it("withholds unmeasured usage instead of rendering it as zero", () => {
    // usage_measured=false means nothing measured it. Rendering that as "0.0
    // used" tells the user they consumed nothing while real usage accrues —
    // the unknown-as-known defect, failing in the reassuring direction.
    renderSection({ overage: infraOverageResponse({ usageMeasured: false }) });

    expect(screen.getAllByText(/unavailable/i).length).toBeGreaterThan(0);
    expect(
      screen.getAllByText(/this is not a reading of zero/i).length,
    ).toBeGreaterThan(0);
    // No meter reports a value when nothing measured one.
    expect(screen.queryAllByRole("progressbar")).toHaveLength(0);
  });

  it("does not render a failed usage load as zero usage", () => {
    renderSection({ error: { message: "usage backend unavailable" } });

    expect(screen.getByText(/could not load your usage/i)).toBeDefined();
    expect(
      screen.getByText(/not a report that you have used nothing/i),
    ).toBeDefined();
  });
});

describe("DeployUsageSection — no compute subscription", () => {
  it("explains the state instead of rendering an empty dashboard", () => {
    renderSection({
      overage: infraOverageResponse({ dimensions: [], budgetCents: undefined }),
      rows: [],
    });

    expect(screen.getByText(/no compute plan on this account/i)).toBeDefined();
    // The two consequences the user actually needs: no free tier, and what
    // they can still run.
    expect(screen.getByText(/there is no free tier/i)).toBeDefined();
    expect(screen.getByText(/bills from the first unit/i)).toBeDefined();
  });

  it("explains why the spending cap is unavailable rather than showing a dead control", () => {
    renderSection({
      overage: infraOverageResponse({ dimensions: [], budgetCents: undefined }),
      rows: [],
    });

    expect(screen.getByText(/no allowance to exceed/i)).toBeDefined();
    expect(screen.queryByRole("button", { name: /save spending cap/i })).toBeNull();
  });
});

describe("DeployUsageSection — enforcement", () => {
  it("tells a blocked user that scale-ups are why their change did not apply", () => {
    renderSection({
      overage: infraOverageResponse({
        // The SERVER says scale-ups are blocked; the surface must report that
        // verdict rather than re-deriving one from a percentage.
        dimensions: [dimension({ consumed: 150, action: "block_scale_up" })],
      }),
    });

    expect(screen.getAllByText(/scale-ups are blocked/i).length).toBeGreaterThan(0);
    expect(
      screen.getByText(/if a change you made did not take effect, this is why/i),
    ).toBeDefined();
  });

  it("treats an unrecognised ladder rung as the MOST severe, not as benign", () => {
    // A rung this build has never heard of is more likely to be a new, more
    // severe one. Ranking it below a rung we do recognise is the direction
    // that costs a user their service with no warning.
    renderSection({
      overage: infraOverageResponse({
        dimensions: [
          dimension({ action: "none" }),
          dimension({ dimensionId: "infra_memory", action: "quarantine" }),
        ],
      }),
    });

    // Twice, and both are load-bearing: the headline verdict for the whole
    // account, and the unrecognised dimension's own row. The headline is the
    // assertion that matters — a build that ranked "quarantine" below the
    // "none" beside it would render "Inside your allowance".
    expect(
      screen.getAllByText(/enforcement state unavailable/i).length,
    ).toBeGreaterThan(0);
    // Anchored on the "none" rung's own BODY copy, not its title: the overage
    // card separately says "inside your included allowance", and matching that
    // would pass even if the verdict had been downgraded.
    expect(screen.queryByText(/no restrictions are in effect/i)).toBeNull();
    expect(
      screen.getByText(/treat your services as potentially restricted/i),
    ).toBeDefined();
  });

  it("states the per-tier behaviour honestly — only backends are scaled to zero", () => {
    renderSection();

    expect(screen.getByText(/scaled to 0 replicas/i)).toBeDefined();
    expect(screen.getByText(/a managed database is never stopped/i)).toBeDefined();
    expect(screen.getByText(/static sites continue to serve/i)).toBeDefined();
  });
});

describe("DeployUsageSection — attribution", () => {
  it("names the deployment the usage came from", () => {
    renderSection();

    expect(screen.getByText("api")).toBeDefined();
  });

  it("keeps a row that names no deployment rather than dropping the money", () => {
    renderSection({
      rows: [usageRow({ deploymentId: "", environmentId: "" })],
    });

    expect(screen.getByText(/cluster floor \(no deployment\)/i)).toBeDefined();
  });
});

describe("DeployUsageSection — the cap it writes", () => {
  it("renders the stored INFRA cap, and submits the full request", () => {
    const { onSaveCap } = renderSection();

    expect(screen.getByText(/ceiling of \$25\.00 per month/i)).toBeDefined();

    screen.getByRole("button", { name: /save spending cap/i }).click();

    // Always carries budgetCents — the partial-send hazard cannot be expressed.
    expect(onSaveCap).toHaveBeenCalledWith({ budgetCents: 2500n });
  });
});
