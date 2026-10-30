// Copyright (c) 2026 Reliant Labs
import { Code, ConnectError } from "@connectrpc/connect";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { OrganizationUsageRequest } from "@/services/controlPlane/observability/organizationUsageApi";

import { OrganizationBillingView } from "../OrganizationBillingView";
import { organizationCostFixture } from "./organizationCostFixtures";
import { NOW_MS, ORG, respond, respondInventoryUnavailable, respondUnavailable } from "./organizationUsageFixtures";

vi.mock("uplot", async () => (await import("../../__tests__/obsMocks")).obsMocks.uplot());

const { listUsage, getSummary } = vi.hoisted(() => ({ listUsage: vi.fn(), getSummary: vi.fn() }));
vi.mock("@/services/controlPlane/observability/organizationUsageApi", () => ({ listOrganizationUsage: listUsage }));
vi.mock("@/services/controlPlane/observability/organizationCostsApi", () => ({ getOrganizationCostSummary: getSummary }));

function view(orgId = ORG) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } });
  return render(
    <QueryClientProvider client={client}>
      <OrganizationBillingView orgId={orgId} nowMs={NOW_MS} />
    </QueryClientProvider>
  );
}

beforeEach(() => {
  listUsage.mockReset().mockImplementation(async (req: OrganizationUsageRequest) => respond(req));
  getSummary.mockReset().mockResolvedValue(organizationCostFixture());
});

describe("OrganizationBillingView", () => {
  it("renders the inventory, usage and spend rate panels from a populated response", async () => {
    view();
    const boxes = await screen.findByTestId("billing-devbox-count");
    expect(boxes).toHaveTextContent("3");
    expect(boxes).toHaveTextContent("2 running · 1 suspended");
    expect(screen.getByTestId("billing-devbox-storage")).toHaveTextContent("150 GiB");
    expect(screen.getByTestId("billing-devboxes-now")).toHaveTextContent("small");
    expect(screen.getByRole("group", { name: "Dev-box running hours by size" })).toBeInTheDocument();

    const apps = screen.getByTestId("billing-hosted-apps");
    expect(within(apps).getByTestId("billing-count-hosted_apps")).toHaveTextContent("3 in prod");
    expect(apps).toHaveTextContent("12.5 vCPU-h");
    // The range's hosted-apps point leaves memory unpriced, so its figure is a lower bound.
    expect(apps).toHaveTextContent("≥ $4.00");
    expect(apps).toHaveTextContent("Estimate at list price · lower bound, excludes memory");
    expect(within(apps).getByTestId("lower-bound")).toHaveAttribute("title", expect.stringContaining("excludes memory"));
    expect(screen.getByTestId("billing-databases")).toHaveTextContent("240 GiB-h");
    expect(screen.getByTestId("billing-databases")).toHaveTextContent("Backups");

    const rate = screen.getByTestId("usage-spend-rate");
    expect(within(rate).getByText("Managed LLM").closest("tr")).toHaveTextContent("$33.00");
    expect(screen.getByTestId("billing-devbox-heatmap")).toHaveTextContent("Not available yet");
    expect(within(screen.getByTestId("billing-invoices")).getByRole("link", { name: "Open invoices" })).toHaveAttribute("href", "/settings/billing");
  });

  it("states partial and unavailable coverage per source", async () => {
    view();
    const coverage = await screen.findByTestId("usage-coverage");
    expect(within(coverage).getByText("Dev boxes").parentElement).toHaveTextContent("Partial");
    expect(within(coverage).getByText("Observability").parentElement).toHaveTextContent("Unavailable");
    expect(coverage).toHaveTextContent("Observability metering could not be read.");
    await waitFor(() => expect(screen.getByRole("heading", { name: "Billing and infrastructure" }).parentElement).toHaveTextContent("Partial"));
  });

  it("shows unpriced usage as n/a and an unavailable source as unavailable, never $0", async () => {
    view();
    await screen.findByTestId("billing-amount-static_hosting");
    const statics = screen.getByTestId("billing-static");
    expect(statics).toHaveTextContent("2 MB");
    expect(within(statics).getByTestId("billing-amount-static_hosting")).toHaveTextContent("Cost in range: n/a · Measured, not priced");
    expect(statics).toHaveTextContent("Free-site allowance");
    const obs = screen.getByTestId("billing-observability");
    expect(obs).toHaveTextContent("Cost in range: Unavailable · Not measured; not zero");
    expect(obs).toHaveTextContent("No billed observability ingest in this range.");
    expect(screen.getByTestId("billing-hosted-other")).toHaveTextContent("Cost in range: No usage");
    expect(screen.queryByText("$0.00")).not.toBeInTheDocument();
  });

  it("says unavailable when no source can be read", async () => {
    listUsage.mockImplementation(async (req: OrganizationUsageRequest) => respondUnavailable(req));
    view();
    expect(await screen.findByTestId("billing-devbox-count")).toHaveTextContent("Unavailable");
    expect(screen.getByText("No dev box ran in this range.")).toBeInTheDocument();
    expect(screen.getByTestId("billing-hosted-apps")).toHaveTextContent("Cost in range: Unavailable");
    expect(screen.queryByText("$0.00")).not.toBeInTheDocument();
  });

  it("reports current counts as unavailable, never 0 or Complete, when only the inventory cannot be read", async () => {
    listUsage.mockImplementation(async (req: OrganizationUsageRequest) => respondInventoryUnavailable(req));
    view();
    const boxes = await screen.findByTestId("billing-devbox-count");
    expect(boxes).toHaveTextContent("Unavailable");
    expect(boxes).toHaveTextContent("unknown, not zero");
    expect(within(boxes).getByTestId("completeness-badge")).toHaveAttribute("data-completeness", "unavailable");
    expect(screen.getByTestId("billing-devbox-storage")).toHaveTextContent("Unavailable");
    expect(screen.getByTestId("billing-devboxes-now")).toHaveTextContent("Current dev boxes could not be read");
    for (const category of ["hosted_apps", "databases", "static_hosting"]) {
      const count = screen.getByTestId(`billing-count-${category}`);
      expect(count).toHaveTextContent("Unavailable");
      expect(count).not.toHaveTextContent("None deployed");
      expect(within(count).getByTestId("completeness-badge")).toHaveAttribute("data-completeness", "unavailable");
    }
    // Usage history is a different source and still renders.
    expect(screen.getByTestId("billing-hosted-apps")).toHaveTextContent("12.5 vCPU-h");
  });

  it("refuses to show anything without billing:read, including what was cached before", async () => {
    const user = userEvent.setup();
    view();
    expect(await screen.findByTestId("billing-devbox-count")).toHaveTextContent("3");
    listUsage.mockRejectedValue(new ConnectError("denied", Code.PermissionDenied));
    await user.click(screen.getByRole("button", { name: "Refresh" }));
    expect(await screen.findByTestId("billing-permission-required")).toHaveTextContent("billing:read");
    expect(screen.queryByTestId("billing-devbox-count")).not.toBeInTheDocument();
    expect(screen.queryByTestId("billing-hosted-apps")).not.toBeInTheDocument();
  });

  it("does not query without an organization", () => {
    view("");
    expect(screen.getByRole("status")).toHaveTextContent("Select an organization");
    expect(listUsage).not.toHaveBeenCalled();
  });
});
