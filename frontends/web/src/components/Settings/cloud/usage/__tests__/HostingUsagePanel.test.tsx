import { fireEvent, render, screen, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { Code, ConnectError } from "@connectrpc/connect";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { afterEach, describe, expect, it, vi } from "vitest";

import { setTransport } from "@/lib/connect";
import { mockTransport } from "@/lib/test-utils";

import { HostingUsagePanel, HostingUsageView } from "../HostingUsagePanel";
import type { OrgHostingUsageSummary } from "../hostingBillingQueries";

afterEach(() => setTransport(null));

const ts = (iso: string) => timestampFromDate(new Date(iso));

function summary(overrides: Partial<OrgHostingUsageSummary> = {}): OrgHostingUsageSummary {
  return {
    spentCents: 1234n,
    projectedMonthEndCents: 4000n,
    projectionIsEstimate: true,
    budgetCents: 5000n,
    periodStart: ts("2026-11-01T00:00:00Z"),
    periodEnd: ts("2026-11-30T23:59:59Z"),
    measuredThrough: ts("2026-11-20T05:00:00Z"),
    runningRateCentsPerHour: 12,
    budgetReached: false,
    billingActive: true,
    apps: [
      { deploymentId: "d2", deploymentName: "web", environmentId: "e2", environmentName: "staging", spentCents: 100n, projectedCents: 300n, staticStorageBytes: 0n, staticTransferBytes: 0n },
      { deploymentId: "d1", deploymentName: "api", environmentId: "e1", environmentName: "prod", spentCents: 1000n, projectedCents: 3000n, staticStorageBytes: 0n, staticTransferBytes: 0n },
      { deploymentId: "d2", deploymentName: "web", environmentId: "e1", environmentName: "prod", spentCents: 134n, projectedCents: 700n, staticStorageBytes: 2n ** 29n, staticTransferBytes: 2n ** 30n },
    ],
    ...overrides,
  };
}

describe("HostingUsageView", () => {
  it("shows spend, budget and the projection labelled as an estimate", () => {
    render(<HostingUsageView summary={summary()} />);
    expect(screen.getByTestId("hosting-usage-spent")).toHaveTextContent("$12.34");
    expect(screen.getByText("$50.00")).toBeInTheDocument();
    const projection = screen.getByTestId("hosting-usage-projection");
    expect(projection).toHaveTextContent("Projected by 2026-11-30");
    expect(projection).toHaveTextContent("Estimate");
    expect(projection).toHaveTextContent("$40.00");
    expect(projection).toHaveTextContent("Spend so far plus what your running apps cost per hour (≈$0.12/hour), until month end.");
    expect(screen.getByText("Measured through 2026-11-20 05:00:00 UTC")).toBeInTheDocument();
    expect(screen.getByRole("progressbar", { name: /monthly budget/ })).toBeInTheDocument();
  });

  it("does not call a measured projection an estimate", () => {
    render(<HostingUsageView summary={summary({ projectionIsEstimate: false })} />);
    expect(screen.getByTestId("hosting-usage-projection")).not.toHaveTextContent("Estimate");
    expect(screen.getByRole("columnheader", { name: "Projected" })).toBeInTheDocument();
  });

  it("states a sub-cent hourly rate without rounding it to zero", () => {
    render(<HostingUsageView summary={summary({ runningRateCentsPerHour: 0.4 })} />);
    expect(screen.getByTestId("hosting-usage-projection")).toHaveTextContent("≈<$0.01/hour");
  });

  it("says the budget covers the month when nothing would block", () => {
    render(<HostingUsageView summary={summary()} />);
    expect(screen.getByTestId("hosting-usage-budget-status")).toHaveTextContent("Your budget covers the projected month.");
  });

  it("says when new deploys would pause at the current rate", () => {
    render(<HostingUsageView summary={summary({ blockedAt: ts("2026-11-26T10:00:00Z") })} />);
    expect(screen.getByTestId("hosting-usage-budget-status")).toHaveTextContent("At this rate new deploys would pause around 2026-11-26.");
  });

  it("says deploys are paused once the budget is reached", () => {
    render(<HostingUsageView summary={summary({ budgetReached: true, blockedAt: ts("2026-11-19T10:00:00Z") })} />);
    expect(screen.getByTestId("hosting-usage-budget-status")).toHaveTextContent(
      "Budget reached — new deploys are paused until 2026-11-30 or you raise the budget.",
    );
  });

  it("notes that usage beyond the free tier needs billing when billing is inactive", () => {
    render(<HostingUsageView summary={summary({ billingActive: false, budgetCents: 0n, apps: [] })} />);
    expect(screen.getByTestId("hosting-usage-billing-inactive")).toHaveTextContent(/beyond the free tier needs billing/);
    expect(screen.queryByTestId("hosting-usage-budget-status")).not.toBeInTheDocument();
    expect(screen.queryByRole("progressbar")).not.toBeInTheDocument();
  });

  it("lists usage by app then environment", () => {
    render(<HostingUsageView summary={summary()} />);
    const table = screen.getByRole("table", { name: "Hosting usage by app and environment" });
    const headers = within(table).getAllByRole("columnheader").map((h) => h.textContent);
    expect(headers).toEqual(["App", "Environment", "Spent", "Projected (estimate)", "Static storage", "Static transfer"]);
    const rows = within(table).getAllByRole("row").slice(1);
    expect(rows.map((r) => within(r).getAllByRole("cell").slice(0, 2).map((c) => c.textContent).join("/"))).toEqual([
      "api/prod",
      "web/prod",
      "web/staging",
    ]);
    expect(rows[1]).toHaveTextContent("$1.34");
    expect(rows[1]).toHaveTextContent("0.5 GB");
    expect(rows[1]).toHaveTextContent("1 GB");
  });

  it("shows an empty period plainly", () => {
    render(<HostingUsageView summary={summary({ apps: [] })} />);
    expect(screen.getByText("No hosted apps this period.")).toBeInTheDocument();
    expect(screen.queryByRole("table")).not.toBeInTheDocument();
  });

  it("shows a loading line", () => {
    render(<HostingUsageView loading />);
    expect(screen.getByRole("status")).toHaveTextContent("Loading hosting usage…");
  });

  it("treats an unserved RPC as unavailable, not as zero", () => {
    render(<HostingUsageView error={new ConnectError("no", Code.Unimplemented)} />);
    expect(screen.getByText("Usage by app isn't available on this server yet.")).toBeInTheDocument();
    expect(screen.queryByText("$0.00")).not.toBeInTheDocument();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("keeps a failed read retryable and never shows it as zero usage", () => {
    const onRetry = vi.fn();
    render(<HostingUsageView error={new ConnectError("down", Code.Unavailable)} onRetry={onRetry} />);
    expect(screen.getByRole("alert")).toHaveTextContent("Could not load hosting usage — this is a problem reading it, not zero usage.");
    expect(screen.queryByText("$0.00")).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(onRetry).toHaveBeenCalledOnce();
  });
});

describe("HostingUsagePanel", () => {
  it("says usage by app is not available when the server lacks the RPC", async () => {
    setTransport(mockTransport());
    const cache = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={cache}>
        <HostingUsagePanel orgId="org-a" />
      </QueryClientProvider>,
    );
    expect(await screen.findByText("Usage by app isn't available on this server yet.")).toBeInTheDocument();
  });
});
