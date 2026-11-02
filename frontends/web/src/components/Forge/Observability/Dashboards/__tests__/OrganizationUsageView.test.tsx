// Copyright (c) 2026 Reliant Labs
import { Code, ConnectError } from "@connectrpc/connect";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { OrganizationUsageRequest } from "@/services/controlPlane/observability/organizationUsageApi";

import { OrganizationUsageView } from "../OrganizationUsageView";
import { organizationCostFixture } from "./organizationCostFixtures";
import { NOW_MS, ORG, respond, respondUnavailable, usageByCategory } from "./organizationUsageFixtures";

vi.mock("uplot", async () => (await import("../../__tests__/obsMocks")).obsMocks.uplot());

const { listUsage, getSummary } = vi.hoisted(() => ({ listUsage: vi.fn(), getSummary: vi.fn() }));
vi.mock("@/services/controlPlane/observability/organizationUsageApi", () => ({ listOrganizationUsage: listUsage }));
const purposeState = vi.hoisted(() => ({ value: { status: "loading" } as unknown, refresh: vi.fn() }));
vi.mock("@/hooks/llm-usage-queries", () => ({ useOwnManagedPurposeUsage: () => ({ ...(purposeState.value as object), refresh: purposeState.refresh, fetching: false }) }));
vi.mock("@/services/controlPlane/observability/organizationCostsApi", () => ({ getOrganizationCostSummary: getSummary }));

function view(orgId = ORG) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } });
  return render(
    <QueryClientProvider client={client}>
      <OrganizationUsageView orgId={orgId} nowMs={NOW_MS} />
    </QueryClientProvider>
  );
}

const tile = (category: string) => screen.findByTestId(`usage-tile-${category}`);

const readyPurposes = {
  status: "ready",
  completeness: "partial",
  note: "Days before 2026-07-28 come from the daily rollup.",
  buckets: [
    { key: "chat", calls: 45, errorCalls: 4, inputTokens: 4000, outputTokens: 1100, cacheReadTokens: 5000, cacheWriteTokens: 600, latencyMsTotal: 0, avgLatencyMs: 0 },
    { key: "compaction", calls: 4, errorCalls: 0, inputTokens: 900, outputTokens: 120, cacheReadTokens: 0, cacheWriteTokens: 0, latencyMsTotal: 0, avgLatencyMs: 0 },
  ],
};

beforeEach(() => {
  purposeState.value = readyPurposes;
  purposeState.refresh.mockReset();
  listUsage.mockReset().mockImplementation(async (req: OrganizationUsageRequest) => respond(req));
  getSummary.mockReset().mockResolvedValue(organizationCostFixture());
});

describe("OrganizationUsageView", () => {
  it("asks for this organization's history in UTC buckets, by category, environment and model, plus month to date", async () => {
    view();
    await tile("managed_llm");
    const calls = listUsage.mock.calls.map(([req]) => req as OrganizationUsageRequest);
    expect(calls.every((req) => req.orgId === ORG)).toBe(true);
    const day = calls.filter((req) => req.granularity === "day");
    expect(new Set(day.map((req) => req.groupBy))).toEqual(new Set(["category", "env", "model"]));
    expect(day[0]).toMatchObject({ startMs: Date.parse("2026-09-26T00:00:00Z"), endMs: Date.parse("2026-10-26T00:00:00Z") });
    expect(calls).toContainEqual({ orgId: ORG, startMs: Date.parse("2026-10-01T00:00:00Z"), endMs: Date.parse("2026-11-01T00:00:00Z"), granularity: "month", groupBy: "category" });
    expect(getSummary).toHaveBeenCalledWith(ORG);
  });

  it("renders every panel from a populated response, each figure with its basis", async () => {
    view();
    expect(await tile("managed_llm")).toHaveTextContent("$20.00");
    expect(await tile("managed_llm")).toHaveTextContent("Settled charges");
    expect(await tile("hosted_apps")).toHaveTextContent("$6.00");
    expect(await tile("hosted_apps")).toHaveTextContent("Estimate at list price");
    // Panels over the range.
    const byEnv = await screen.findByTestId("usage-by-env");
    await within(byEnv).findByText("prod");
    expect(byEnv).toHaveTextContent("$4.00");
    expect(byEnv).toHaveTextContent("staging");
    const drivers = screen.getByTestId("usage-drivers-actual");
    await within(drivers).findByText("claude-sonnet-5");
    expect(drivers).toHaveTextContent("$10.00");
    expect(drivers).toHaveTextContent("$2.34");
    const allowances = screen.getByTestId("usage-allowances");
    expect(await within(allowances).findByText(/12\.5 of 100 vCPU-hours/)).toBeInTheDocument();
    expect(allowances).toHaveTextContent("Projected by period end");
    expect(screen.getByTestId("usage-credit")).toHaveTextContent("-$2.50");
    expect(within(screen.getByTestId("usage-caps")).getByRole("link", { name: "Open Usage & limits" })).toHaveAttribute("href", "/settings/billing");
    // The two spend charts never share a basis.
    expect(screen.getByRole("group", { name: /Settled charges in US dollars/ })).toBeInTheDocument();
    expect(screen.getByRole("group", { name: /Estimated spend at list price/ })).toBeInTheDocument();
    expect(screen.queryByText(/grand total/i)).not.toBeInTheDocument();
  });

  it("shows partial coverage with the server's limitations, and the spend rate with an honest projection", async () => {
    view();
    const header = await screen.findByRole("heading", { name: "Spend and usage" });
    await waitFor(() => expect(header.parentElement).toHaveTextContent("Partial"));
    expect(within(await tile("machines")).getByTestId("completeness-badge")).toHaveAttribute("data-completeness", "partial");
    const coverage = await screen.findByTestId("usage-coverage");
    expect(coverage).toHaveTextContent("The newest dev-box meter tick is behind the range end.");
    const rate = await screen.findByTestId("usage-spend-rate");
    const managed = within(rate).getByText("Managed LLM").closest("tr")!;
    expect(managed).toHaveTextContent("$1.00");
    expect(managed).toHaveTextContent("$2.00");
    // 20 + 2/day × 6.5 days left in October.
    expect(managed).toHaveTextContent("$33.00");
    const hosted = within(rate).getByText("Hosted apps").closest("tr")!;
    expect(hosted).toHaveTextContent("Estimate at list price");
    expect(within(hosted).getByTitle("Only 12 of 24 hours measured")).toHaveTextContent("n/a");
    // A priced category with a full week measured projects its month end (6 + 0.2/day × 6.5 days).
    expect(within(rate).getByText("Databases").closest("tr")).not.toHaveTextContent("≥");
  });

  it("marks a rate with unpriced dimensions as a lower bound and says what it leaves out", async () => {
    view();
    const rate = await screen.findByTestId("usage-spend-rate");
    const hosted = within(rate).getByText("Hosted apps").closest("tr")!;
    expect(hosted).toHaveTextContent("Lower bound · excludes memory");
    expect(hosted).toHaveTextContent("≥ $0.50");
    expect(within(hosted).getAllByTestId("lower-bound")[0]).toHaveTextContent("≥ $0.50");
    expect(within(hosted).getAllByTestId("lower-bound")[0]).toHaveAttribute("title", expect.stringContaining("excludes memory"));
    // A complete rate carries no bound.
    expect(within(within(rate).getByText("Databases").closest("tr")!).queryByTestId("lower-bound")).not.toBeInTheDocument();
    // Usage with no rate at all is not priced, never $0.
    expect(within(rate).getByText("Static sites").closest("tr")).toHaveTextContent("Not priced");
    expect(within(within(rate).getByText("Static sites").closest("tr")!).queryByText("$0.00")).not.toBeInTheDocument();
  });

  it("marks a period figure as a lower bound when its points leave dimensions unpriced, and lists the partial-coverage limitation", async () => {
    view();
    const tile = await screen.findByTestId("usage-tile-hosted_apps");
    // Unpriced usage in range makes the range figure a lower bound; the month-to-date points carry none.
    expect(tile).toHaveTextContent("Hosted usage is retained for 7 days.");
    expect(within(tile).getByTestId("completeness-badge")).toHaveAttribute("data-completeness", "partial");
    expect(within(tile).getByTestId("completeness-badge").getAttribute("title")).toContain("Hosted usage is retained for 7 days.");
    const estimate = await screen.findByTestId("usage-spend-estimate");
    expect(estimate).toBeInTheDocument();
  });

  it("breaks the caller's Reliant AI calls down by purpose, counts only", async () => {
    view();
    const panel = await screen.findByTestId("usage-llm-purpose");
    expect(within(panel).getByText("Chat").closest("tr")).toHaveTextContent("10.7k");
    expect(within(panel).getByText("Context compaction").closest("tr")).toHaveTextContent("1k");
    expect(panel).toHaveTextContent("Days before 2026-07-28 come from the daily rollup.");
    expect(panel).not.toHaveTextContent("$");
  });

  it("says the purpose breakdown is unavailable, not zero, when the usage service is missing or fails", () => {
    purposeState.value = { status: "not-deployed" };
    view();
    expect(screen.getByTestId("usage-llm-purpose-state")).toHaveTextContent("Not available from this server yet");
    cleanup();
    purposeState.value = { status: "error" };
    view();
    expect(screen.getByTestId("usage-llm-purpose-state")).toHaveTextContent("unknown, not zero");
  });

  it("never turns an absent figure into zero", async () => {
    view();
    // Dev boxes carry seconds and no price; static egress carries usage and no price.
    const machines = await tile("machines");
    expect(machines).toHaveTextContent("n/a");
    expect(machines).toHaveTextContent("Measured, not priced");
    expect(machines).toHaveTextContent("10 h running");
    expect(await tile("static_hosting")).toHaveTextContent("n/a");
    // An unavailable source returns no points: unknown, not zero.
    const obs = await tile("observability");
    expect(obs).toHaveTextContent("Unavailable");
    expect(obs).toHaveTextContent("Not measured; not zero");
    // A source that looked and found nothing.
    expect(await tile("hosted_other")).toHaveTextContent("No usage");
    for (const category of ["machines", "static_hosting", "observability", "hosted_other"]) expect(await tile(category)).not.toHaveTextContent("$0.00");
  });

  it("keeps a sub-cent charge visible rather than rounding it to zero", async () => {
    listUsage.mockImplementation(async (req: OrganizationUsageRequest) => (req.granularity === "month" ? { ...usageByCategory(), points: usageByCategory().points.filter((p) => p.actualChargesUsdNanos === 4_000_000n) } : respond(req)));
    view();
    expect(await tile("managed_llm")).toHaveTextContent("<$0.01");
  });

  it("says unavailable everywhere when no source can be read", async () => {
    listUsage.mockImplementation(async (req: OrganizationUsageRequest) => respondUnavailable(req));
    view();
    for (const category of ["managed_llm", "machines", "hosted_apps", "observability"]) expect(await tile(category)).toHaveTextContent("Unavailable");
    await waitFor(() => expect(screen.getByRole("heading", { name: "Spend and usage" }).parentElement).toHaveTextContent("Unavailable"));
    expect(await screen.findByText("No category carried settled charges in this range.")).toBeInTheDocument();
    expect(screen.getByTestId("usage-spend-rate")).toHaveTextContent("No category reported a rate.");
    expect(screen.queryByText("$0.00")).not.toBeInTheDocument();
  });

  it("shows a failed read as unknown, not zero, and recovers on refresh", async () => {
    const user = userEvent.setup();
    listUsage.mockRejectedValue(new ConnectError("internal detail", Code.Unavailable));
    view();
    expect(await screen.findAllByTestId("panel-unavailable")).not.toHaveLength(0);
    expect(screen.getAllByTestId("panel-unavailable")[0]).toHaveTextContent("unknown, not zero");
    expect(screen.queryByText(/internal detail/)).not.toBeInTheDocument();
    listUsage.mockImplementation(async (req: OrganizationUsageRequest) => respond(req));
    await user.click(screen.getByRole("button", { name: "Refresh" }));
    expect(await tile("managed_llm")).toHaveTextContent("$20.00");
  });

  it("refuses to show anything without billing:read, including what was cached before", async () => {
    const user = userEvent.setup();
    view();
    expect(await tile("managed_llm")).toHaveTextContent("$20.00");
    listUsage.mockRejectedValue(new ConnectError("denied", Code.PermissionDenied));
    getSummary.mockRejectedValue(new ConnectError("denied", Code.PermissionDenied));
    await user.click(screen.getByRole("button", { name: "Refresh" }));
    expect(await screen.findByTestId("billing-permission-required")).toHaveTextContent("billing:read");
    expect(screen.queryByText("$20.00")).not.toBeInTheDocument();
    expect(screen.queryByTestId("usage-period-to-date")).not.toBeInTheDocument();
  });

  it("reads twelve months by month", async () => {
    const user = userEvent.setup();
    view();
    await tile("managed_llm");
    await user.click(within(screen.getByTestId("usage-range")).getByRole("button", { name: "12 months" }));
    await waitFor(() =>
      expect(listUsage).toHaveBeenCalledWith(
        expect.objectContaining({ granularity: "month", groupBy: "env", startMs: Date.parse("2025-11-01T00:00:00Z"), endMs: Date.parse("2026-11-01T00:00:00Z") })
      )
    );
  });

  it("does not query without an organization", () => {
    view("");
    expect(screen.getByRole("status")).toHaveTextContent("Select an organization");
    expect(listUsage).not.toHaveBeenCalled();
    expect(getSummary).not.toHaveBeenCalled();
  });
});
