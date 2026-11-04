// Copyright (c) 2025 Reliant Labs

/**
 * A service only offers the signal views it has: the Profiles service
 * picker, the service page's jumps and the trace's flamegraph buttons all
 * read GetServiceSignals (serviceSignals.ts). Fixtures: a service
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
