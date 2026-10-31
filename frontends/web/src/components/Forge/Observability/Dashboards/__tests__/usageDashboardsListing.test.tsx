// Copyright (c) 2026 Reliant Labs

/**
 * Where the usage dashboards are found and how they open: the server's
 * org-usage and org-billing built-ins, and the client's own LLM usage, listed
 * with a project's dashboards and opened as typed views (LLM usage without
 * any DashboardService document request).
 */

import { describe, expect, it, vi } from "vitest";
import { screen, within } from "@testing-library/react";

import type { DashboardRef, DashboardSummary } from "@/services/controlPlane/observability/dashboardTypes";

import { createFixtureApi, FIXTURE_PROJECT } from "../../__fixtures__/fixtureApi";
import { renderWithApi } from "../../__tests__/obsTestUtils";
import { DashboardsList } from "../DashboardsList";
import { DashboardView, type DashboardHrefs } from "../DashboardView";

vi.mock("uplot", async () => (await import("../../__tests__/obsMocks")).obsMocks.uplot());
vi.mock("../OrganizationUsageView", () => ({ OrganizationUsageView: ({ orgId }: { orgId: string }) => <h1>Spend and usage for {orgId}</h1> }));
vi.mock("../OrganizationBillingView", () => ({ OrganizationBillingView: ({ orgId }: { orgId: string }) => <h1>Billing and infrastructure for {orgId}</h1> }));
vi.mock("../LLMUsageView", () => ({ LLMUsageView: ({ orgId }: { orgId: string }) => <h1>LLM usage for {orgId}</h1> }));

const hrefs = { dashboard: (ref: DashboardRef) => `/d/${ref.kind}/${ref.key}`, release: (version: string) => `/releases/${version}` };
const viewHrefs: DashboardHrefs = {
  logs: () => "/logs",
  traces: () => "/traces",
  workload: (name) => `/workloads/${name}`,
  list: "/dashboards",
  release: (v) => `/releases/${v}`,
  trace: (id) => `/trace/${id}`,
  dashboard: (ref) => `/d/${ref.kind}/${ref.key}`,
};

const builtin = (key: string, viewKind: DashboardSummary["viewKind"], title: string): DashboardSummary => ({
  ref: { kind: "builtin", projectId: "", key },
  source: "builtin",
  viewKind,
  group: "product",
  title,
  description: title,
  readOnly: true,
  starred: false,
  lastViewedAtMs: null,
  panelCount: 0,
  previewPanels: [],
  release: null,
  workspace: null,
  category: "billing",
});

const USAGE = builtin("org-usage", "organization-usage", "Spend and usage");
const BILLING = builtin("org-billing", "organization-billing", "Billing and infrastructure");

function viewOf(api: ReturnType<typeof createFixtureApi>, ref: DashboardRef) {
  renderWithApi(
    api,
    <DashboardView project={FIXTURE_PROJECT} dashRef={ref} env="all" environments={[]} search={{}} onSearch={vi.fn()} onEnvChange={vi.fn()} hrefs={viewHrefs} navigateTo={vi.fn()} />
  );
}

describe("usage dashboards", () => {
  it("are listed with a project's dashboards, LLM usage without the billing-permission note", async () => {
    const api = createFixtureApi();
    api.listDashboards = vi.fn().mockResolvedValue({ canWrite: false, dashboards: [USAGE, BILLING] });
    renderWithApi(api, <DashboardsList project={FIXTURE_PROJECT} env="all" environments={[]} search={{}} onSearch={vi.fn()} hrefs={hrefs} navigateTo={vi.fn()} />);
    // All three are in the Product group, the client's LLM usage after the
    // server's built-ins.
    const listed = await screen.findByTestId("dashboards-group-product");
    expect(within(listed).getAllByRole("link").map((a) => a.getAttribute("href"))).toEqual([
      "/d/builtin/org-usage",
      "/d/builtin/org-billing",
      "/d/builtin/org-llm",
    ]);
    const row = (name: string) => within(listed).getByRole("link", { name }).closest("tr")!;
    expect(row("Spend and usage")).toHaveTextContent("Requires cost-read permission");
    expect(row("Billing and infrastructure")).toHaveTextContent("Requires cost-read permission");
    expect(row("LLM usage")).not.toHaveTextContent("Requires cost-read permission");
  });

  it("open org-usage as its typed view", async () => {
    const api = createFixtureApi();
    api.getDashboard = vi.fn().mockResolvedValueOnce({ summary: USAGE, spec: { id: "org-usage", title: "", filters: [], panels: [] }, version: 0, canWrite: false });
    viewOf(api, USAGE.ref);
    expect(await screen.findByRole("heading", { name: /Spend and usage for/ })).toBeInTheDocument();
  });

  it("open org-billing as its typed view", async () => {
    const api = createFixtureApi();
    api.getDashboard = vi.fn().mockResolvedValueOnce({ summary: BILLING, spec: { id: "org-billing", title: "", filters: [], panels: [] }, version: 0, canWrite: false });
    viewOf(api, BILLING.ref);
    expect(await screen.findByRole("heading", { name: /Billing and infrastructure for/ })).toBeInTheDocument();
  });

  it("open LLM usage without asking DashboardService for a document", async () => {
    const api = createFixtureApi();
    api.getDashboard = vi.fn();
    viewOf(api, { kind: "builtin", projectId: "", key: "org-llm" });
    expect(await screen.findByRole("heading", { name: /LLM usage for/ })).toBeInTheDocument();
    expect(api.getDashboard).not.toHaveBeenCalled();
  });
});
