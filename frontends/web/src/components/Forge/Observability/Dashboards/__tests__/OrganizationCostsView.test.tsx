// Copyright (c) 2026 Reliant Labs
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { Code, ConnectError } from "@connectrpc/connect";
import { getOrganizationCostSummary } from "@/services/controlPlane/observability/organizationCostsApi";
import { OrganizationCostsView, OrganizationCostSummaryContent } from "../OrganizationCostsView";
import { organizationCostFixture } from "./organizationCostFixtures";

vi.mock("@/services/controlPlane/observability/organizationCostsApi", () => ({ getOrganizationCostSummary: vi.fn() }));
const getSummary = vi.mocked(getOrganizationCostSummary);
afterEach(() => { cleanup(); vi.resetAllMocks(); });

function view(orgId = "org-example", client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })) {
  const result = render(<QueryClientProvider client={client}><OrganizationCostsView orgId={orgId} /></QueryClientProvider>);
  return { ...result, client };
}

describe("OrganizationCostsView", () => {
  it("requests only the organization and shows a loading state", () => {
    getSummary.mockImplementation(() => new Promise(() => undefined));
    view();
    expect(screen.getByRole("status")).toHaveTextContent("Loading organization costs");
    expect(getSummary).toHaveBeenCalledWith("org-example");
    expect(screen.getByText(/All projects and environments/)).toBeInTheDocument();
  });

  it("renders independent periods, amounts, coverage and native allowances without totals", async () => {
    getSummary.mockResolvedValue(organizationCostFixture());
    view();
    const managed = await screen.findByRole("article", { name: "Managed LLM costs" });
    expect(managed).toHaveTextContent("USD 12.34");
    expect(managed).toHaveTextContent("USD -2.50");
    expect(managed).toHaveTextContent("Pending items: 2");
    expect(managed).not.toHaveTextContent("USD 9.84");
    const byok = screen.getByRole("article", { name: "BYOK costs" });
    expect(byok).toHaveTextContent("Not measured");
    expect(within(byok).getAllByText("Not reported").length).toBeGreaterThanOrEqual(4);
    expect(byok).not.toHaveTextContent("USD 0.00");
    const infra = screen.getByRole("article", { name: "Hosted infrastructure costs" });
    expect(infra).toHaveTextContent("USD 4.000000001");
    expect(infra).toHaveTextContent("100 vCPU-hours included · 12.5 vCPU-hours consumed");
    expect(infra).toHaveTextContent("2026-10-18 12:00:00 UTC");
    expect(infra).toHaveTextContent("2026-10-25 11:55:00 UTC");
    expect(infra).toHaveTextContent("Only seven days");
    expect(screen.getByRole("article", { name: "Observability costs" })).toHaveTextContent("USD 0.00");
    expect(screen.getByRole("article", { name: "Observability costs" })).toHaveTextContent("Consumption not reported");
    expect(screen.getByRole("article", { name: "Static hosting costs" })).toHaveTextContent("Unavailable");
    expect(within(screen.getByRole("region", { name: "Compute billing period" })).getByRole("article", { name: "Machines costs" })).toHaveTextContent("USD 20.00");
    expect(within(screen.getByRole("region", { name: "Month to date" })).queryByRole("article", { name: "Machines costs" })).not.toBeInTheDocument();
    expect(screen.getByText(/End \(exclusive\): 2026-11-12/)).toBeInTheDocument();
    expect(screen.getByText(/Wallet top-ups are funding/)).toBeInTheDocument();
    expect(screen.queryByText(/grand total/i)).not.toBeInTheDocument();
  });

  it("distinguishes empty source data from permission refusal", async () => {
    getSummary.mockResolvedValue({ ...organizationCostFixture(), sources: [] });
    view();
    expect(await screen.findByText(/No cost sources were returned/)).toHaveTextContent("does not mean there were no charges");
  });

  it("hides cached billing amounts after a live permission refusal", async () => {
    const user = userEvent.setup();
    getSummary.mockResolvedValueOnce(organizationCostFixture()).mockRejectedValue(new ConnectError("denied", Code.PermissionDenied));
    view();
    expect(await screen.findByText("USD 12.34")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Refresh costs" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("billing:read");
    expect(screen.queryByText("USD 12.34")).not.toBeInTheDocument();
    expect(screen.queryByText(/No cost sources were returned/)).not.toBeInTheDocument();
  });

  it("can recover from a service failure without calling it empty", async () => {
    const user = userEvent.setup();
    getSummary.mockRejectedValueOnce(new ConnectError("internal detail", Code.Unavailable)).mockResolvedValue(organizationCostFixture());
    view();
    expect(await screen.findByRole("alert")).toHaveTextContent("unavailable, not zero");
    expect(screen.queryByText(/internal detail/)).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Refresh costs" }));
    expect(await screen.findByText("USD 12.34")).toBeInTheDocument();
  });

  it("never shows an old organization's result after switching scope", async () => {
    getSummary.mockResolvedValueOnce(organizationCostFixture()).mockImplementation(() => new Promise(() => undefined));
    const { rerender, client } = view();
    await screen.findByText("USD 12.34");
    rerender(<QueryClientProvider client={client}><OrganizationCostsView orgId="org-next" /></QueryClientProvider>);
    await waitFor(() => expect(getSummary).toHaveBeenCalledWith("org-next"));
    expect(screen.queryByText("USD 12.34")).not.toBeInTheDocument();
  });

  it("retains sources without period metadata and rejects mismatched organization responses", async () => {
    const summary = organizationCostFixture();
    render(<OrganizationCostSummaryContent summary={{ ...summary, periods: [] }} />);
    expect(screen.getByRole("article", { name: "Machines costs" })).toBeInTheDocument();
    cleanup();
    getSummary.mockResolvedValue({ ...summary, orgId: "someone-else" });
    view();
    expect(await screen.findByRole("alert")).toHaveTextContent("does not match this organization");
    expect(screen.queryByText("USD 12.34")).not.toBeInTheDocument();
  });

  it("does not query without an organization", () => {
    view("");
    expect(screen.getByRole("status")).toHaveTextContent("Select an organization");
    expect(getSummary).not.toHaveBeenCalled();
  });
});
