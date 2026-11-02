// Copyright (c) 2025 Reliant Labs

/**
 * Observability › Dashboards, org-wide: the index is asked for with no project
 * (the whole org), rows come from every app and each links into its app's
 * console, a dashboard with no app links nowhere, the App filter narrows the
 * list, and nothing on it can write.
 */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Code } from "@connectrpc/connect";

import type { DashboardKind, DashboardRef, DashboardSummary } from "@/services/controlPlane/observability/dashboardTypes";

import { createFixtureApi } from "../../Observability/__fixtures__/fixtureApi";
import { renderWithApi } from "../../Observability/__tests__/obsTestUtils";
import { DashboardsList, type DashboardsAppFilter } from "../../Observability/Dashboards/DashboardsList";
import { orgDashboardHref } from "../OrgDashboards";

vi.mock("uplot", async () => (await import("../../Observability/__tests__/obsMocks")).obsMocks.uplot());

const summary = (kind: DashboardKind, projectId: string, key: string, title: string, extra: Partial<DashboardSummary> = {}): DashboardSummary => ({
  ref: { kind, projectId, key },
  source: kind === "release" ? "generated" : "workspace",
  title,
  description: "",
  readOnly: kind === "release",
  starred: false,
  lastViewedAtMs: null,
  panelCount: 2,
  previewPanels: [],
  release:
    kind === "release" ? { releaseVersion: "v1.2.0", shippedAtMs: 1, filePath: "", envIds: [] } : null,
  workspace:
    kind === "workspace"
      ? { version: 3, createdBy: { id: "u-1", name: "Ada", email: "" }, updatedBy: { id: "u-1", name: "Ada", email: "" }, createdAtMs: 1, updatedAtMs: 2, forkedFrom: "", envIds: [] }
      : null,
  category: "",
  ...extra,
});

const INDEX: DashboardSummary[] = [
  summary("release", "shop", "shop-overview", "shop: overview"),
  summary("workspace", "shop", "checkout", "Checkout latency"),
  summary("release", "api", "api-overview", "api: overview"),
  summary("workspace", "", "legacy", "Legacy board"),
];

const hrefs = { dashboard: orgDashboardHref };

function renderOrgList(appFilter?: DashboardsAppFilter, dashboards = INDEX) {
  const onCall = vi.fn();
  const listed = vi.fn();
  const base = createFixtureApi({ onCall });
  const api = {
    ...base,
    listDashboards: async (req: unknown) => {
      listed(req);
      return { dashboards, canWrite: true };
    },
  };
  const onSearch = vi.fn();
  const navigateTo = vi.fn();
  renderWithApi(
    api,
    <DashboardsList env="all" environments={[]} search={{}} onSearch={onSearch} hrefs={hrefs} navigateTo={navigateTo} appFilter={appFilter} />
  );
  return { onCall, listed, onSearch, navigateTo };
}

beforeEach(() => vi.clearAllMocks());

describe("org dashboard links", () => {
  it("opens a dashboard in its own app's console", () => {
    expect(orgDashboardHref({ kind: "workspace", projectId: "shop", key: "checkout" })).toBe("/forge/p/shop/observability/dashboards/workspace/checkout");
    expect(orgDashboardHref({ kind: "release", projectId: "api", key: "api-overview" })).toBe("/forge/p/api/observability/dashboards/release/api-overview");
  });

  it("encodes the key", () => {
    expect(orgDashboardHref({ kind: "workspace", projectId: "shop", key: "a b" })).toBe("/forge/p/shop/observability/dashboards/workspace/a%20b");
  });

  it("has no link for a dashboard with no app, or for a template", () => {
    expect(orgDashboardHref({ kind: "workspace", projectId: "", key: "legacy" })).toBe("");
    expect(orgDashboardHref({ kind: "template", projectId: "shop", key: "t" } satisfies DashboardRef)).toBe("");
  });

});

describe("DashboardsList, org mode", () => {
  it("asks for the whole org: no project, no environment", async () => {
    const { listed } = renderOrgList();
    await screen.findByTestId("dashboards-group-overview");
    expect(listed).toHaveBeenCalledTimes(1);
    const req = listed.mock.calls[0]![0] as Record<string, unknown>;
    expect(req.orgId).toBeTruthy();
    expect(req.projectId).toBeUndefined();
    expect(req.envId).toBeUndefined();
  });

  it("lists every app's dashboards, grouped, with the app named, each linking into its app, and no Overview", async () => {
    renderOrgList();
    // Generated overviews are Overview; saved ones are Custom (no group from this server).
    const overview = await screen.findByTestId("dashboards-group-overview");
    const custom = screen.getByTestId("dashboards-group-custom");
    expect(screen.queryByRole("tab", { name: "Overview" })).not.toBeInTheDocument();
    expect(screen.queryByTestId("dashboards-hub")).not.toBeInTheDocument();

    const links = (root: HTMLElement) => within(root).getAllByRole("link").map((a) => [a.getAttribute("aria-label"), a.getAttribute("href")]);
    expect(links(overview)).toEqual([
      ["shop: overview", "/forge/p/shop/observability/dashboards/release/shop-overview"],
      ["api: overview", "/forge/p/api/observability/dashboards/release/api-overview"],
    ]);
    expect(links(custom)).toEqual([["Checkout latency", "/forge/p/shop/observability/dashboards/workspace/checkout"]]);

    expect(within(overview).getAllByText("shop").length).toBeGreaterThan(0);
    expect(within(overview).getByText("api")).toBeInTheDocument();
    expect(within(screen.getByTestId("dashboards")).getByText("4 dashboards across 3 apps")).toBeInTheDocument();
  });

  it("shows a dashboard with no app as plain text under Unassigned", async () => {
    renderOrgList();
    const workspace = await screen.findByTestId("dashboards-group-custom");
    const row = within(workspace).getByText("Legacy board").closest("tr")!;
    expect(within(row).queryByRole("link")).not.toBeInTheDocument();
    expect(within(row).getByText("Unassigned")).toBeInTheDocument();
  });

  it("does not navigate on a click of a row with nowhere to go", async () => {
    const { navigateTo } = renderOrgList();
    const workspace = await screen.findByTestId("dashboards-group-custom");
    await userEvent.setup().click(within(workspace).getByText("Legacy board").closest("tr")!);
    expect(navigateTo).not.toHaveBeenCalled();
  });

  it("offers nothing that writes: no New dashboard, no template gallery, no read-only note", async () => {
    renderOrgList();
    await screen.findByTestId("dashboards-group-overview");
    expect(screen.queryByTestId("dashboards-new")).not.toBeInTheDocument();
    expect(screen.queryByTestId("dashboards-templates")).not.toBeInTheDocument();
    expect(screen.queryByTestId("dashboards-read-only")).not.toBeInTheDocument();
    expect(screen.getByText(/in its app/)).toBeInTheDocument();
  });

  it("does not fetch the template gallery", async () => {
    const { onCall } = renderOrgList();
    await screen.findByTestId("dashboards-group-overview");
    expect(onCall).not.toHaveBeenCalledWith("ListDashboardTemplates", expect.anything());
  });

  it("narrows to the picked apps through the App filter", async () => {
    const onChange = vi.fn();
    renderOrgList({ selected: ["api"], onChange });
    const overview = await screen.findByTestId("dashboards-group-overview");
    expect(within(overview).getAllByRole("row").slice(1)).toHaveLength(1);
    expect(within(overview).getByText("api: overview")).toBeInTheDocument();
    expect(screen.queryByTestId("dashboards-group-custom")).not.toBeInTheDocument();
    expect(screen.getByTestId("dashboards-chip-app")).toHaveTextContent("App: api");
  });

  it("offers every app that has a dashboard, Unassigned last, and reports a pick", async () => {
    const onChange = vi.fn();
    renderOrgList({ selected: [], onChange });
    await screen.findByTestId("dashboards-group-overview");
    const user = userEvent.setup();
    await user.click(screen.getByTestId("dashboards-chip-app"));
    const options = screen.getAllByRole("menuitemradio").map((o) => o.textContent);
    expect(options).toEqual(["every app", "api", "shop", "Unassigned"]);
    await user.click(screen.getByRole("menuitemradio", { name: "shop" }));
    expect(onChange).toHaveBeenCalledWith(["shop"]);
  });

  it("clears the app filter along with the others", async () => {
    const onChange = vi.fn();
    const { onSearch } = renderOrgList({ selected: ["api"], onChange });
    await screen.findByTestId("dashboards-group-overview");
    await userEvent.setup().click(screen.getByTestId("dashboards-clear"));
    expect(onChange).toHaveBeenCalledWith([]);
    expect(onSearch).toHaveBeenCalledWith({ q: undefined, group: undefined, source: undefined, owner: undefined });
  });

  it("has a designed empty state when no app has a dashboard", async () => {
    renderOrgList(undefined, []);
    const empty = await screen.findByTestId("dashboards-empty");
    expect(empty).toHaveTextContent("No dashboards yet");
    expect(empty).toHaveTextContent("Or open an app and start one there");
  });

  it("says the list could not be read when the control plane refuses", async () => {
    const api = createFixtureApi({ dashboardsFailWith: Code.PermissionDenied });
    renderWithApi(
      { ...api },
      <DashboardsList env="all" environments={[]} search={{}} onSearch={vi.fn()} hrefs={hrefs} navigateTo={vi.fn()} appFilter={{ selected: [], onChange: vi.fn() }} />
    );
    await waitFor(() => expect(screen.getByTestId("dashboards")).toHaveAttribute("data-state", "error"));
    expect(screen.queryByTestId("dashboards-empty")).not.toBeInTheDocument();
  });
});
