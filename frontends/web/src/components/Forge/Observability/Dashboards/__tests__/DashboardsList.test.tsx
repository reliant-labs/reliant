// Copyright (c) 2025 Reliant Labs

/**
 * Observability › Dashboards against the in-memory DashboardService: the
 * shelves (starred with previews, recently viewed, from your releases,
 * workspace), the filters, starring, the template gallery and New dashboard —
 * each write naming its environment (D1) — and the read-only, empty and
 * failed states.
 */

import { useState } from "react";
import { describe, expect, it, vi } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Code } from "@connectrpc/connect";

import type { DashboardRef } from "@/services/controlPlane/observability/dashboardTypes";

import { createFixtureApi, FIXTURE_ENVS, FIXTURE_PROJECT, type FixtureOptions } from "../../__fixtures__/fixtureApi";
import { renderWithApi } from "../../__tests__/obsTestUtils";
import { DashboardsList, type DashboardsSearch } from "../DashboardsList";

vi.mock("uplot", async () => (await import("../../__tests__/obsMocks")).obsMocks.uplot());

const hrefs = {
  dashboard: (ref: DashboardRef) => `/d/${ref.kind}/${ref.key}`,
  release: (version: string) => `/releases/${version}`,
};

function renderList(options: FixtureOptions = {}, env = "prod") {
  const onCall = vi.fn();
  const navigateTo = vi.fn();
  const api = createFixtureApi({ ...options, onCall });
  function Stateful() {
    const [search, setSearch] = useState<DashboardsSearch>({});
    return (
      <DashboardsList
        project={FIXTURE_PROJECT}
        env={env}
        environments={FIXTURE_ENVS}
        search={search}
        onSearch={(next) => setSearch((prev) => ({ ...prev, ...next }))}
        hrefs={hrefs}
        navigateTo={navigateTo}
      />
    );
  }
  renderWithApi(api, <Stateful />);
  return { onCall, navigateTo };
}

describe("Dashboards", () => {
  it("lists the project's dashboards for the scoped environment, on four shelves", async () => {
    const { onCall } = renderList();
    const starred = await screen.findByTestId("dashboards-starred");
    expect(onCall).toHaveBeenCalledWith("ListDashboards", expect.objectContaining({ projectId: FIXTURE_PROJECT, envId: "env-prod" }));
    expect(within(starred).getAllByRole("link").map((a) => a.textContent)).toEqual(["shop: overview", "Frontend · web", "Checkout latency"]);
    // A starred card draws its headline panels through QueryPanel.
    await waitFor(() => expect(onCall).toHaveBeenCalledWith("QueryPanel", expect.objectContaining({ panel: expect.objectContaining({ id: "p95" }) })));
    expect(within(screen.getByTestId("dashboards-recent")).getAllByRole("link")[0]).toHaveTextContent("Checkout latency");
    const release = screen.getByTestId("dashboards-release");
    expect(within(release).getByText("deploy/observability/dashboards/checkout.json")).toBeInTheDocument();
    expect(within(release).getAllByRole("link", { name: "v1.8.3" })[0]).toHaveAttribute("href", "/releases/v1.8.3");
    const workspace = screen.getByTestId("dashboards-workspace");
    expect(within(workspace).getAllByRole("row").slice(1).map((r) => r.querySelector("a")?.textContent)).toEqual(["Checkout latency", "Daemon fleet", "Billing funnel"]);
  });

  it("filters by text, source and owner, and clears", async () => {
    const user = userEvent.setup();
    renderList();
    await screen.findByTestId("dashboards-starred");
    await user.type(screen.getByTestId("dashboards-search"), "fleet");
    expect(screen.queryByTestId("dashboards-release")).not.toBeInTheDocument();
    expect(within(screen.getByTestId("dashboards-workspace")).getAllByRole("row")).toHaveLength(2);
    await user.click(screen.getByTestId("dashboards-chip-release"));
    expect(screen.getByTestId("dashboards-no-match")).toHaveTextContent("No dashboards match");
    await user.click(screen.getByTestId("dashboards-clear"));
    await user.click(screen.getByTestId("dashboards-chip-owner"));
    await user.click(screen.getByRole("menuitemradio", { name: "repo" }));
    expect(within(screen.getByTestId("dashboards-release")).getAllByRole("row").slice(1)).toHaveLength(2);
    expect(screen.queryByTestId("dashboards-workspace")).not.toBeInTheDocument();
  });

  it("stars and unstars at once, for the caller", async () => {
    const user = userEvent.setup();
    const { onCall } = renderList();
    const release = await screen.findByTestId("dashboards-release");
    await user.click(within(release).getByRole("button", { name: "Star shop: workloads" }));
    expect(within(release).getByRole("button", { name: "Unstar shop: workloads" })).toHaveAttribute("aria-pressed", "true");
    expect(onCall).toHaveBeenCalledWith("StarDashboard", expect.objectContaining({ ref: expect.objectContaining({ kind: "release", key: "workloads" }) }));
    await waitFor(() => expect(within(screen.getByTestId("dashboards-starred")).getByText("shop: workloads")).toBeInTheDocument());
  });

  it("starts from a template: the copy names its environment, and opens when saved", async () => {
    const user = userEvent.setup();
    const { onCall, navigateTo } = renderList();
    await user.click(await screen.findByTestId("dashboards-template-service-red"));
    const confirm = screen.getByTestId("dashboard-copy-confirm");
    expect(confirm).toHaveTextContent("Save copy for prod");
    await user.click(screen.getByRole("radio", { name: /All environments/ }));
    expect(confirm).toHaveTextContent("Save copy for all environments");
    await user.click(confirm);
    await waitFor(() => expect(navigateTo).toHaveBeenCalledWith("/d/workspace/service-red"));
    expect(onCall).toHaveBeenCalledWith("SaveAsCopy", expect.objectContaining({ from: expect.objectContaining({ kind: "template", key: "service-red" }), envScope: { envIds: [] } }));
  });

  it("creates a blank dashboard scoped to the environment, and says so when the name is taken", async () => {
    const user = userEvent.setup();
    const { onCall, navigateTo } = renderList();
    await user.click(await screen.findByTestId("dashboards-new"));
    await user.click(screen.getByTestId("dashboards-new-blank"));
    await user.type(screen.getByTestId("dashboard-new-title"), "Checkout latency");
    expect(screen.getByTestId("dashboard-new-slug")).toHaveValue("checkout-latency");
    await user.click(screen.getByTestId("dashboard-create-confirm"));
    expect(await screen.findByTestId("dashboard-write-error")).toHaveTextContent("already exists");
    await user.clear(screen.getByTestId("dashboard-new-slug"));
    await user.type(screen.getByTestId("dashboard-new-slug"), "checkout-latency-2");
    await user.click(screen.getByTestId("dashboard-create-confirm"));
    await waitFor(() => expect(navigateTo).toHaveBeenCalledWith("/d/workspace/checkout-latency-2"));
    expect(onCall).toHaveBeenCalledWith("SaveDashboard", expect.objectContaining({ slug: "checkout-latency-2", baseVersion: 0, envScope: { envIds: ["env-prod"] } }));
  });

  it("keeps a member who is not an admin to reading and starring", async () => {
    renderList({ dashboardsReadOnly: true });
    expect(await screen.findByTestId("dashboards-read-only")).toHaveTextContent("needs an organization admin");
    expect(screen.getByTestId("dashboards-template-blank")).toBeDisabled();
  });

  it("explains an empty project, and a failure", async () => {
    renderList({ dashboardsEmpty: true });
    expect(await screen.findByTestId("dashboards-empty")).toHaveTextContent("Dashboards ship with your releases");
    expect(await screen.findByTestId("dashboards-templates")).toBeInTheDocument();
  });

  it("says when the dashboards cannot be read", async () => {
    renderList({ dashboardsFailWith: Code.Unavailable });
    // Unavailable is retried once before it is shown.
    expect(await screen.findByTestId("obs-error-unavailable", {}, { timeout: 5000 })).toBeInTheDocument();
  });
});
