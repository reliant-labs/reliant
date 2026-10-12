// Copyright (c) 2025 Reliant Labs

/**
 * Dashboards render forge's spec through panelPlan's translation (per-rpc RED
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
  it("draws the overview dashboard: numbers, per-service lines, a RED table, a metric and a way into Logs", async () => {
    renderPanel(createFixtureApi(), { view: "dashboards" });
    const requests = await screen.findByTestId("obs-panel-requests");
    expect(requests).toHaveAttribute("data-plan", "overview");
    await waitFor(() => expect(within(requests).getByTestId("obs-panel-number")).toBeInTheDocument());
    expect(screen.getByTestId("obs-panel-request-rate").querySelector("figure")).toHaveAccessibleName("Requests per service");
    await waitFor(() =>
      expect(within(screen.getByTestId("obs-panel-red-by-service")).getByTestId("obs-panel-table")).toHaveTextContent("worker")
    );
    expect(screen.getByTestId("obs-panel-go-goroutines")).toHaveAttribute("data-plan", "metric");
    expect(screen.getByTestId("obs-panel-error-logs")).toHaveAttribute("data-plan", "link");
    expect(screen.getByTestId("obs-dashboards-synthesized")).toBeInTheDocument();
  });

  it("opens a service's own dashboard from the service, with per-rpc RED", async () => {
    const onCall = vi.fn();
    renderPanel(createFixtureApi({ onCall }), { view: "dashboards", service: "api" });
    const table = await screen.findByTestId("obs-panel-red-by-rpc");
    expect(table).toHaveAttribute("data-plan", "rpcTable");
    await waitFor(() => expect(within(table).getByTestId("obs-panel-table")).toHaveTextContent("orders.v1.OrdersService/CreateOrder"));
    const latency = screen.getByTestId("obs-panel-rpc-CreateOrder-latency");
    expect(latency).toHaveAttribute("data-plan", "red");
    await waitFor(() => expect(latency).toHaveTextContent(/p95/));
    expect(latency).toHaveTextContent("per-rpc trends aren't available yet");
    expect(onCall).toHaveBeenCalledWith(
      "ListTraces",
      expect.objectContaining({ rootSpanName: "orders.v1.OrdersService/CreateOrder", limit: 1 })
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
