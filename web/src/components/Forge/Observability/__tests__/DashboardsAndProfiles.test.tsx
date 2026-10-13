// Copyright (c) 2025 Reliant Labs

/**
 * Dashboards run forge's spec panel by panel through QueryPanel (per-rpc RED
 * by default), and Profiles embeds speedscope with a text alternative beside
 * it.
 */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { createFixtureApi, FIXTURE_TRACE_ID } from "../__fixtures__/fixtureApi";
import { renderPanel } from "./obsTestUtils";

vi.mock("uplot", async () => (await import("./obsMocks")).obsMocks.uplot());
vi.mock("@tanstack/react-virtual", async () => (await import("./obsMocks")).obsMocks.virtual());

describe("DashboardsView", () => {
  it("runs each panel through QueryPanel: numbers, per-service lines, a RED table, a metric and a way into Logs", async () => {
    const onCall = vi.fn();
    renderPanel(createFixtureApi({ onCall }), { view: "dashboards" });
    const requests = await screen.findByTestId("obs-panel-requests");
    await waitFor(() => expect(within(requests).getByTestId("obs-panel-number")).toHaveTextContent(/\d/));
    // A grouped line: one uPlot series per service, the canvas described in words.
    const rate = screen.getByTestId("obs-panel-request-rate");
    await waitFor(() => expect(rate.querySelector("figure")).toHaveAccessibleName("Requests per service"));
    expect(rate.querySelector("figcaption")).toHaveTextContent(/api: latest .*worker: latest/);
    // A ratio line: one series per group, labelled by the group alone.
    await waitFor(() =>
      expect(screen.getByTestId("obs-panel-error-rate").querySelector("figcaption")).toHaveTextContent(/^api: latest [\d.<]+%/)
    );
    const table = await within(screen.getByTestId("obs-panel-red-by-service")).findByTestId("obs-panel-table");
    expect(table).toHaveTextContent("worker");
    expect(table).toHaveTextContent(/\d+(\.\d+)?ms/);
    await waitFor(() => expect(screen.getByTestId("obs-panel-go-goroutines").querySelector("figure")).toBeInTheDocument());
    expect(within(screen.getByTestId("obs-panel-error-logs")).getByTestId("obs-panel-link")).toHaveTextContent("errors only");
    // Search panels are never sent to QueryPanel.
    expect(onCall.mock.calls.filter(([rpc]) => rpc === "QueryPanel").map(([, req]) => req.panel.id)).not.toContain("error-logs");
    expect(screen.getByTestId("obs-dashboards-synthesized")).toBeInTheDocument();
  });

  it("says in place when the server can't compute a panel, with its reason", async () => {
    renderPanel(createFixtureApi(), { view: "dashboards" });
    const sessions = await screen.findByTestId("obs-panel-sessions");
    const note = await within(sessions).findByTestId("obs-panel-unsupported");
    expect(note).toHaveTextContent('aggregation "count_distinct" is not supported');
    expect(within(note).getByRole("button", { name: /Open Traces/ })).toBeInTheDocument();
  });

  it("draws an rpc's rate, error ratio and latency as lines over time", async () => {
    const onCall = vi.fn();
    renderPanel(createFixtureApi({ onCall }), { view: "dashboards", service: "api" });
    const latency = await screen.findByTestId("obs-panel-rpc-CreateOrder-latency");
    await waitFor(() => expect(latency.querySelector("figure")).toHaveAccessibleName("CreateOrder: latency"));
    expect(latency.querySelector("figcaption")).toHaveTextContent(/p50: latest .*p95: latest .*p99: latest/);
    const table = await within(screen.getByTestId("obs-panel-red-by-rpc")).findByTestId("obs-panel-table");
    expect(table).toHaveTextContent("orders.v1.OrdersService/CreateOrder");
    // The service's own dashboard is not narrowed by the service filter a second time.
    const call = onCall.mock.calls.find(([rpc, req]) => rpc === "QueryPanel" && req.panel.id === "rpc-CreateOrder-latency");
    expect(call?.[1]).toMatchObject({ services: [], orgId: expect.any(String), envIds: ["env-prod"] });
  });

  it("applies the dashboard's service filter to every panel", async () => {
    const user = userEvent.setup();
    const onCall = vi.fn();
    renderPanel(createFixtureApi({ onCall }), { view: "dashboards" });
    await user.selectOptions(await screen.findByTestId("obs-dashboard-service"), "worker");
    await waitFor(() =>
      expect(onCall).toHaveBeenCalledWith("QueryPanel", expect.objectContaining({ services: ["worker"], panel: expect.objectContaining({ id: "requests" }) }))
    );
  });

  it("switches dashboards from the picker", async () => {
    const user = userEvent.setup();
    const harness = renderPanel(createFixtureApi(), { view: "dashboards" });
    await user.selectOptions(await screen.findByTestId("obs-dashboard-picker"), "service-api");
    expect(harness.current().dashboard).toBe("service-api");
    expect(await screen.findByTestId("obs-panel-red-by-rpc")).toBeInTheDocument();
  });

  it("says so when there are no dashboards yet", async () => {
    const api = createFixtureApi();
    const real = api.getDashboards;
    api.getDashboards = async (req) => ({ ...(await real(req)), dashboards: [] });
    renderPanel(api, { view: "dashboards" });
    expect(await screen.findByTestId("obs-dashboards-empty")).toHaveTextContent("No dashboards yet");
  });
});

describe("DashboardsView: dashboards from the release (K4)", () => {
  it("says who wrote each dashboard: forge's generated set, or the project's own file", async () => {
    const user = userEvent.setup();
    renderPanel(createFixtureApi({ dashboards: "release" }), { view: "dashboards" });
    expect(await screen.findByTestId("obs-dashboard-origin")).toHaveAttribute("data-origin", "gen");
    expect(screen.queryByTestId("obs-dashboards-synthesized")).not.toBeInTheDocument();
    await user.selectOptions(screen.getByTestId("obs-dashboard-picker"), "checkout");
    await waitFor(() => expect(screen.getByTestId("obs-dashboard-origin")).toHaveAttribute("data-origin", "user"));
    expect(screen.getByTestId("obs-dashboard-origin")).toHaveTextContent("Yours");
  });

  it("calls a synthesized set what it is", async () => {
    renderPanel(createFixtureApi(), { view: "dashboards" });
    expect(await screen.findByTestId("obs-dashboard-origin")).toHaveAttribute("data-origin", "synthesized");
  });

  it("draws the release markers a dashboard declares, and says so when the ledger is not there", async () => {
    renderPanel(createFixtureApi({ dashboards: "release" }), { view: "dashboards", dashboard: "releases" });
    await waitFor(() => expect(screen.getByTestId("obs-release-markers")).toHaveAttribute("data-status", "ready"));
    expect(screen.getByTestId("obs-release-markers")).toHaveTextContent(/promotions? in this window/);
  });

  it("without a release ledger, keeps the dashboard and says no markers are drawn", async () => {
    renderPanel(createFixtureApi({ dashboards: "release", releaseMarkers: false }), { view: "dashboards", dashboard: "releases" });
    const note = await screen.findByTestId("obs-release-markers");
    expect(note).toHaveAttribute("data-status", "unavailable");
    expect(note).toHaveTextContent("Release history isn't available here yet");
    expect(await screen.findByTestId("obs-panel-request-rate-by-version")).toBeInTheDocument();
  });

  it("reads a dashboard filter's values from the signal it names", async () => {
    const user = userEvent.setup();
    const onCall = vi.fn();
    const harness = renderPanel(createFixtureApi({ dashboards: "release", onCall }), { view: "dashboards", dashboard: "workloads" });
    const workload = await screen.findByTestId("obs-dashboard-filter-workload");
    expect(workload).toHaveAttribute("data-signal", "metrics");
    await waitFor(() =>
      expect(onCall).toHaveBeenCalledWith("ListAttributeValues", expect.objectContaining({ key: "k8s.workload.name", signal: "metrics" }))
    );
    await waitFor(() => expect(within(workload).getAllByRole("option").length).toBeGreaterThan(1));
    const value = within(workload).getAllByRole("option")[1].getAttribute("value")!;
    await user.selectOptions(workload, value);
    expect(harness.current().f).toEqual([`k8s.workload.name:${value}`]);
  });
});

describe("ProfilesView", () => {
  const created: string[] = [];
  beforeEach(() => {
    // jsdom has no object URLs.
    URL.createObjectURL = vi.fn(() => {
      const url = `blob:http://localhost/${created.length}`;
      created.push(url);
      return url;
    });
    URL.revokeObjectURL = vi.fn();
  });
  afterEach(() => {
    created.length = 0;
  });

  it("asks for a service before drawing anything", async () => {
    renderPanel(createFixtureApi(), { view: "profiles" });
    expect(await screen.findByTestId("obs-profiles-pick")).toHaveTextContent("Choose a service");
  });

  it("embeds speedscope with the profile as a blob URL, and lists the heaviest functions", async () => {
    renderPanel(createFixtureApi(), { view: "profiles", service: "api" });
    const frame = await screen.findByTestId("obs-speedscope");
    expect(frame).toHaveAttribute("title", "Flamegraph: api CPU");
    expect(frame.getAttribute("src")).toMatch(/^\/speedscope\/index\.html#profileURL=blob%3A/);
    const top = screen.getByTestId("obs-top-functions");
    expect(within(top).getAllByRole("row")[1]).toHaveTextContent("syscall.Syscall");
    expect(top).toHaveTextContent("150ms");
  });

  it("offers the profile types the service actually has", async () => {
    renderPanel(createFixtureApi(), { view: "profiles", service: "api" });
    await screen.findByTestId("obs-speedscope");
    const options = within(screen.getByTestId("obs-profile-type")).getAllByRole("option").map((o) => o.getAttribute("value"));
    expect(options).toEqual(["cpu", "alloc_space", "inuse_space", "goroutine"]);
  });

  it("narrows to a trace's samples, and explains an empty answer for short spans", async () => {
    const onCall = vi.fn();
    const api = createFixtureApi({ onCall });
    const real = api.getFlamegraph;
    api.getFlamegraph = async (req) => ({ ...(await real(req)), stackCount: 0, speedscopeJson: "" });
    renderPanel(api, { view: "profiles", service: "api", trace: FIXTURE_TRACE_ID });
    expect(await screen.findByTestId("obs-profiles-empty")).toHaveTextContent("shorter than about 10ms");
    expect(onCall).toHaveBeenCalledWith("GetFlamegraph", expect.objectContaining({ traceId: FIXTURE_TRACE_ID, profileType: "cpu" }));
  });
});
