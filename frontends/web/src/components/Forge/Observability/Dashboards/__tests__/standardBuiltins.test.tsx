// Copyright (c) 2026 Reliant Labs

/**
 * The standard dashboards every project gets from the platform (source
 * builtin, category "standard"), against the in-memory DashboardService with
 * a control plane that lists them: they sit in the project's groups as
 * read-only built-ins, not in the template gallery; they open over the
 * project's data with "Save as copy" and no history; they star into the nav
 * like any dashboard; and their page addresses them in the project.
 */

import { useState } from "react";
import { describe, expect, it, vi } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import type { DashboardRef } from "@/services/controlPlane/observability/dashboardTypes";

import { createFixtureApi, FIXTURE_ENVS, FIXTURE_PROJECT } from "../../__fixtures__/fixtureApi";
import { renderWithApi } from "../../__tests__/obsTestUtils";
import type { DashboardSearch } from "../dashboardModel";
import { DashboardsList, type DashboardsSearch } from "../DashboardsList";
import { DashboardView, type DashboardHrefs } from "../DashboardView";

vi.mock("uplot", async () => (await import("../../__tests__/obsMocks")).obsMocks.uplot());

const listHrefs = { dashboard: (ref: DashboardRef) => `/d/${ref.kind}/${ref.key}` };

const viewHrefs: DashboardHrefs = {
  logs: () => "/logs",
  traces: () => "/traces",
  workload: (name) => `/workloads/${name}`,
  list: "/dashboards",
  release: (v) => `/releases/${v}`,
  trace: (id) => `/trace/${id}`,
  dashboard: (ref) => `/d/${ref.kind}/${ref.key}`,
};

const calls = (onCall: ReturnType<typeof vi.fn>, rpc: string) => onCall.mock.calls.filter(([name]) => name === rpc).map(([, req]) => req);

function renderList(initial: DashboardsSearch = {}) {
  const onCall = vi.fn();
  const api = createFixtureApi({ dashboardsStandardBuiltins: true, onCall });
  function Stateful() {
    const [search, setSearch] = useState<DashboardsSearch>(initial);
    return (
      <DashboardsList
        project={FIXTURE_PROJECT}
        env="all"
        environments={FIXTURE_ENVS}
        search={search}
        onSearch={(next) => setSearch((prev) => ({ ...prev, ...next }))}
        hrefs={listHrefs}
        navigateTo={vi.fn()}
      />
    );
  }
  renderWithApi(api, <Stateful />);
  return { onCall };
}

function renderView(key: string) {
  const onCall = vi.fn();
  const navigateTo = vi.fn();
  const api = createFixtureApi({ dashboardsStandardBuiltins: true, onCall });
  const dashRef: DashboardRef = { kind: "builtin", projectId: FIXTURE_PROJECT, key };
  function Stateful() {
    const [search, setSearch] = useState<DashboardSearch>({});
    return (
      <DashboardView
        project={FIXTURE_PROJECT}
        dashRef={dashRef}
        env="prod"
        environments={FIXTURE_ENVS}
        search={search}
        onSearch={(next) => setSearch((prev) => ({ ...prev, ...next }))}
        onEnvChange={vi.fn()}
        hrefs={viewHrefs}
        navigateTo={navigateTo}
      />
    );
  }
  renderWithApi(api, <Stateful />);
  return { onCall, navigateTo };
}

describe("a project's standard built-in dashboards", () => {
  it("are listed first, in the Built-in section, read-only, the ones a release replaces left out", async () => {
    renderList();
    const builtins = await screen.findByTestId("dashboards-group-builtin");
    expect(screen.getAllByTestId(/^dashboards-group-[a-z]+$/)[0]).toBe(builtins);
    const health = within(builtins).getByRole("link", { name: /Service health/ });
    expect(health).toHaveAttribute("href", "/d/builtin/service-red");
    const row = health.closest("tr")!;
    expect(within(row).getByTestId("dashboards-row-source")).toHaveTextContent("built-in");
    // Not an organization view: no billing permission is asked of it.
    expect(row).not.toHaveTextContent("cost-read permission");
    expect(within(builtins).getByRole("link", { name: "Product usage" })).toHaveAttribute("href", "/d/builtin/usage");
    // The release carries overview and releases: its own versions replace the built-ins.
    expect(screen.queryAllByRole("link").map((a) => a.getAttribute("href"))).not.toContain("/d/builtin/overview");
    expect(screen.queryAllByRole("link").map((a) => a.getAttribute("href"))).not.toContain("/d/builtin/releases");
  });

  it("are not offered as templates", async () => {
    renderList();
    // The gallery keeps what is not built in, and the blank dashboard.
    expect(await screen.findByTestId("dashboards-template-workloads")).toBeInTheDocument();
    expect(screen.getByTestId("dashboards-template-blank")).toBeInTheDocument();
    expect(screen.queryByTestId("dashboards-template-service-red")).not.toBeInTheDocument();
    expect(screen.queryByTestId("dashboards-template-usage")).not.toBeInTheDocument();
  });

  it("star like any dashboard", async () => {
    const user = userEvent.setup();
    const { onCall } = renderList();
    const builtins = await screen.findByTestId("dashboards-group-builtin");
    const row = within(builtins).getByRole("link", { name: /Service health/ }).closest("tr")!;
    await user.click(within(row).getByRole("button", { name: /star/i }));
    await waitFor(() => expect(calls(onCall, "StarDashboard")).toHaveLength(1));
    expect(calls(onCall, "StarDashboard")[0]).toMatchObject({ ref: { kind: "builtin", projectId: FIXTURE_PROJECT, key: "service-red" } });
    expect(await screen.findByTestId("dashboards-starred")).toHaveTextContent("Service health");
  });

  it("open over the project's data, read-only, with Save as copy and no history", async () => {
    const user = userEvent.setup();
    const { onCall, navigateTo } = renderView("service-red");
    expect(await screen.findByRole("heading", { level: 1, name: /Service health/ })).toBeInTheDocument();
    expect(screen.getByTestId("dashboard-readonly-note")).toHaveTextContent("Built in for every project");
    expect(screen.queryByTestId("dashboard-history")).not.toBeInTheDocument();
    expect(screen.queryByTestId("dashboard-promote-open")).not.toBeInTheDocument();
    // Its panels run, scoped to the project: it is not an organization view.
    await waitFor(() => expect(calls(onCall, "QueryPanel").length).toBeGreaterThan(0));
    await waitFor(() => expect(calls(onCall, "RecordDashboardView")).toHaveLength(1));
    expect(calls(onCall, "RecordDashboardView")[0]).toMatchObject({ ref: { kind: "builtin", projectId: FIXTURE_PROJECT, key: "service-red" } });

    await user.click(screen.getByTestId("dashboard-save-copy"));
    expect(screen.getByRole("dialog")).toHaveTextContent("The built-in dashboard stays as it is");
    await user.click(screen.getByTestId("dashboard-copy-confirm"));
    await waitFor(() => expect(navigateTo).toHaveBeenCalledWith("/d/workspace/service-red"));
    expect(calls(onCall, "SaveAsCopy")[0]).toMatchObject({ from: { kind: "builtin", projectId: FIXTURE_PROJECT, key: "service-red" } });
  });
});
