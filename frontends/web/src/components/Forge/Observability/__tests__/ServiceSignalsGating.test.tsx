// Copyright (c) 2025 Reliant Labs

/**
 * A service only offers the signal views it has: the panel's view tabs, the
 * Profiles service picker, the service page's jumps and the trace's flamegraph
 * buttons all read GetServiceSignals (serviceSignals.ts). Fixtures: a service
 * with only logs and traces (worker), one that declares profiles and sends
 * none (batch), and the all-services scope.
 */

import { describe, expect, it, vi } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";

import { createFixtureApi, FIXTURE_TRACE_ID } from "../__fixtures__/fixtureApi";
import { FIXTURE_SIGNALS } from "../__fixtures__/fixtureSignals";
import { renderPanel } from "./obsTestUtils";

vi.mock("uplot", async () => (await import("./obsMocks")).obsMocks.uplot());
vi.mock("@tanstack/react-virtual", async () => (await import("./obsMocks")).obsMocks.virtual());

const signalsApi = (onCall?: (rpc: string, request: unknown) => void) => createFixtureApi({ signals: FIXTURE_SIGNALS, onCall });


describe("signal views per service", () => {
  it("a service with only logs and traces offers no Profiles", async () => {
    renderPanel(signalsApi(), { view: "logs", service: "worker" });
    await waitFor(() => expect(screen.queryByTestId("obs-view-profiles")).toBeNull());
    expect(screen.getByTestId("obs-view-traces")).toBeEnabled();
    // Views that are not one signal of one service stay.
    expect(screen.getByTestId("obs-view-errors")).toBeInTheDocument();
  });

  it("a service that declares profiles but sent none shows the tab muted, with why", async () => {
    renderPanel(signalsApi(), { view: "logs", service: "batch" });
    const tab = await screen.findByTestId("obs-view-profiles");
    await waitFor(() => expect(tab).toBeDisabled());
    expect(tab).toHaveAttribute("data-signal-state", "idle");
    expect(tab).toHaveTextContent("none in 24h");
    expect(tab).toHaveAttribute("title", "No profiles from batch in the last 24 hours");
  });

  it("the all-services scope keeps every view", async () => {
    const onCall = vi.fn();
    renderPanel(signalsApi(onCall), { view: "logs" });
    // The answer arrives (the service picker asks), and no view leaves.
    await waitFor(() => expect(onCall).toHaveBeenCalledWith("GetServiceSignals", expect.anything()));
    await waitFor(() => expect(within(screen.getByTestId("obs-service-filter")).queryByRole("option", { name: "reliant-daemon" })).toBeNull());
    for (const view of ["logs", "traces", "profiles"]) expect(screen.getByTestId(`obs-view-${view}`)).toBeEnabled();
  });

  it("a link to a view the service lacks still lands on its tab", async () => {
    renderPanel(signalsApi(), { view: "profiles", service: "worker" });
    // Judged absent, and still there, selected.
    await waitFor(() => expect(screen.getByTestId("obs-view-profiles")).toHaveAttribute("data-signal-state", "absent"));
    expect(screen.getByTestId("obs-view-profiles")).toHaveAttribute("aria-selected", "true");
  });

  it("offers every view when the control plane cannot say (it predates GetServiceSignals)", async () => {
    const onCall = vi.fn();
    renderPanel(createFixtureApi({ onCall }), { view: "logs", service: "worker" });
    await waitFor(() => expect(onCall).toHaveBeenCalledWith("GetServiceSignals", expect.anything()));
    expect(screen.getByTestId("obs-view-profiles")).toBeEnabled();
  });

  it("asks once per scope, without the attribute filters, over at least a day", async () => {
    const onCall = vi.fn();
    renderPanel(signalsApi(onCall), { view: "logs", service: "worker", range: "15m", f: ["http.route:/x"] });
    const askedOf = () => onCall.mock.calls.filter(([rpc]) => rpc === "GetServiceSignals").map(([, req]) => req as { filters: unknown[]; window: { startMs: number; endMs: number } });
    await waitFor(() => expect(screen.queryByTestId("obs-view-profiles")).toBeNull());
    const asked = askedOf();
    expect(asked).toHaveLength(1);
    expect(asked[0]!.filters).toEqual([]);
    expect(asked[0]!.window.endMs - asked[0]!.window.startMs).toBe(24 * 60 * 60_000);
  });
});

describe("the Profiles service picker", () => {
  it("lists the services that send profiles, the idle declarers apart, and none of the rest", async () => {
    renderPanel(signalsApi(), { view: "profiles" });
    const picker = await screen.findByTestId("obs-profile-service");
    await waitFor(() => expect(within(picker).queryByRole("option", { name: "worker" })).toBeNull());
    expect(within(picker).getByRole("option", { name: "api" })).toBeEnabled();
    expect(within(picker).queryByRole("option", { name: "reliant-daemon" })).toBeNull();
    const idle = within(picker).getByTestId("obs-profile-service-idle");
    expect(idle).toHaveAttribute("label", "No profiles in the last 24 hours");
    expect(within(idle).getByRole("option", { name: "batch" })).toBeDisabled();
  });

  it("the Logs picker keeps a logs-only service", async () => {
    renderPanel(signalsApi(), { view: "logs" });
    const picker = await screen.findByTestId("obs-service-filter");
    await waitFor(() => expect(within(picker).queryByRole("option", { name: "reliant-daemon" })).toBeNull());
    expect(within(picker).getByRole("option", { name: "worker" })).toBeEnabled();
    expect(within(picker).getByRole("option", { name: "All services" })).toBeInTheDocument();
  });
});

describe("a service's page and a trace", () => {
  it("jumps only to the signals the service has", async () => {
    renderPanel(signalsApi(), { service: "worker" });
    const jumps = await screen.findByTestId("obs-service-signals");
    await waitFor(() => expect(within(jumps).queryByTestId("obs-service-profiles")).toBeNull());
    expect(within(jumps).getByTestId("obs-service-logs")).toBeEnabled();
    expect(within(jumps).getByTestId("obs-service-traces")).toBeEnabled();
  });

  it("offers a Profiles jump to a service that sends them", async () => {
    renderPanel(signalsApi(), { service: "api" });
    expect(await screen.findByTestId("obs-service-profiles")).toBeEnabled();
  });

  it("drops the flamegraph from a trace whose service sends no profiles", async () => {
    const api = createFixtureApi({
      signals: { truncated: false, services: FIXTURE_SIGNALS.services.map((s) => ({ ...s, emitting: s.emitting.filter((x) => x !== "profiles"), declared: [], declarationKnown: false })) },
    });
    const onCall = vi.fn();
    const real = api.getServiceSignals;
    api.getServiceSignals = async (req) => (onCall(req), real(req));
    renderPanel(api, { view: "traces", trace: FIXTURE_TRACE_ID });
    await screen.findByTestId("trace-detail");
    await waitFor(() => expect(onCall).toHaveBeenCalled());
    await waitFor(() => expect(screen.queryByTestId("trace-flamegraph")).toBeNull());
  });
});
