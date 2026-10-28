// Copyright (c) 2025 Reliant Labs

/**
 * The panel's own contract: the org-scoped request, the one "not set up"
 * explanation, the designed empty and failure states, the overview's numbers
 * and their text alternatives, and keyboard movement between views.
 */

import { describe, expect, it, vi } from "vitest";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Code } from "@connectrpc/connect";

import { formatClock, quotaPausedError } from "@/services/controlPlane/observability/quota";

import { createFixtureApi, FIXTURE_ORG } from "../__fixtures__/fixtureApi";
import { renderPanel } from "./obsTestUtils";

vi.mock("uplot", async () => (await import("./obsMocks")).obsMocks.uplot());
vi.mock("@tanstack/react-virtual", async () => (await import("./obsMocks")).obsMocks.virtual());

describe("ObservabilityPanel", () => {
  it("Refresh advances a relative RPC catalog window to now", async () => {
    let now = Date.UTC(2026, 9, 27, 12);
    const clock = vi.spyOn(Date, "now").mockImplementation(() => now);
    const onCall = vi.fn();
    try {
      renderPanel(createFixtureApi({ onCall }), { view: "rpc", range: "15m" });
      await screen.findByTestId("rpc-explorer");
      const first = onCall.mock.calls.find(([rpc]) => rpc === "GetServiceGraphCatalog")![1].window;
      now += 60000;
      fireEvent.click(screen.getByTestId("obs-refresh"));
      await waitFor(() => expect(onCall.mock.calls.some(([rpc, req]) => rpc === "GetServiceGraphCatalog" && req.window.endMs > first.endMs)).toBe(true));
    } finally { clock.mockRestore(); }
  });
  it("scopes every request to the caller's org and this environment", async () => {
    const onCall = vi.fn();
    renderPanel(createFixtureApi({ onCall }));
    await screen.findByTestId("obs-overview");
    expect(onCall).toHaveBeenCalledWith(
      "GetServiceOverview",
      expect.objectContaining({ orgId: FIXTURE_ORG, envIds: ["env-prod"], filters: [] })
    );
  });

  it("lists services with rate, error rate and latency, and states the sparklines in words", async () => {
    renderPanel(createFixtureApi());
    const table = await screen.findByTestId("obs-overview");
    const api = within(table).getByTestId("obs-service-api");
    expect(api).toHaveTextContent(/\/s/);
    expect(api).toHaveTextContent(/p95/);
    // The canvas is aria-hidden; the figure's caption carries the numbers.
    const figure = within(api).getByRole("group", { name: "Request rate for api" });
    expect(figure).toHaveTextContent(/Requests per second: latest .*, between .* and .*\./);
  });

  it("badges Reliant's own daemon telemetry and explains a service that serves no requests", async () => {
    renderPanel(createFixtureApi());
    const daemon = await screen.findByTestId("obs-service-reliant-daemon");
    expect(within(daemon).getByTestId("obs-operator-badge")).toHaveTextContent("daemon");
    expect(daemon).toHaveTextContent("No requests served");
  });

  it("opens a service in place, with ways into its logs, traces and dashboard", async () => {
    const harness = renderPanel(createFixtureApi());
    const service = await screen.findByTestId("obs-service-api");
    // This tests navigation, not pointer sequencing. Dispatch clicks directly:
    // user-event's synthetic timer gaps consumed the whole test budget when
    // the shared host was busy, despite the fixture API resolving immediately.
    fireEvent.click(within(service).getByRole("button", { name: "api" }));
    const detail = await screen.findByTestId("obs-service-detail");
    expect(detail).toBeInTheDocument();
    expect(harness.current()).toMatchObject({ service: "api" });
    expect(within(detail).getByTestId("obs-service-latency")).toHaveTextContent(/p50.*p95.*p99/);
    expect(within(detail).getByRole("button", { name: "Logs" })).toBeInTheDocument();
    fireEvent.click(within(detail).getByRole("button", { name: "Traces" }));
    expect(harness.current()).toMatchObject({ view: "traces", service: "api" });
  });

  it("says ONCE that observability is not set up, in place of every view", async () => {
    renderPanel(createFixtureApi({ failWith: Code.FailedPrecondition }));
    expect(await screen.findByTestId("obs-not-set-up")).toHaveTextContent("isn't set up for this organization yet");
    // Not presented as an error, and nothing to retry.
    expect(screen.getByTestId("obs-not-set-up")).toHaveAttribute("role", "status");
    expect(screen.queryByTestId("obs-retry")).not.toBeInTheDocument();
  });

  it("says it once on another view too, in place of the view rather than beside it", async () => {
    renderPanel(createFixtureApi({ failWith: Code.FailedPrecondition }), { view: "logs" });
    expect(await screen.findByTestId("obs-not-set-up")).toBeInTheDocument();
    expect(screen.getAllByTestId("obs-not-set-up")).toHaveLength(1);
    expect(screen.queryByTestId("obs-logs")).not.toBeInTheDocument();
  });

  it("leads the not-set-up answer with what the view shows, then how to get started", async () => {
    renderPanel(createFixtureApi({ failWith: Code.FailedPrecondition }));
    const state = await screen.findByTestId("obs-not-set-up");
    expect(within(state).getByTestId("obs-not-set-up-about")).toHaveTextContent("requests, error rate and latency");
    // The pinned environment names itself in the command to copy.
    expect(within(state).getByText("forge env deploy prod")).toBeInTheDocument();
    expect(within(state).getByTestId("obs-not-set-up-docs")).toHaveAttribute("href", "https://docs.reliantlabs.io/forge/observability");
  });

  it("leads the not-set-up answer with what THAT view shows on another view", async () => {
    // The console nav picks the view (the panel draws no tabs): open it by URL.
    renderPanel(createFixtureApi({ failWith: Code.FailedPrecondition }), { view: "traces" });
    expect(await screen.findByTestId("obs-not-set-up-about")).toHaveTextContent("Traces follows every request");
  });

  it("says once that nothing has arrived yet, with the commands that deploy and check", async () => {
    renderPanel(createFixtureApi({ empty: true, signals: { services: [], truncated: false } }));
    const state = await screen.findByTestId("obs-nothing-yet");
    expect(state).toHaveAttribute("role", "status");
    expect(state).toHaveTextContent("Nothing has arrived from prod yet");
    expect(state).toHaveTextContent("set up, but nothing in prod has sent telemetry in the last 7 days");
    expect(within(state).getByText("forge env deploy prod")).toBeInTheDocument();
    expect(within(state).getByText("forge obs status prod")).toBeInTheDocument();
    expect(screen.queryByTestId("obs-no-data")).not.toBeInTheDocument();
  });

  it("says how a view's own data arrives when nothing has: Sessions' replay is opt-in, in two places, and masked", async () => {
    // The console nav picks the view (the panel draws no tabs): open it by URL.
    renderPanel(createFixtureApi({ empty: true, signals: { services: [], truncated: false } }), { view: "sessions" });
    const sessions = await screen.findByTestId("obs-nothing-yet");
    expect(within(sessions).getByTestId("obs-nothing-yet-about")).toHaveTextContent("real users' browser sessions");
    expect(sessions).toHaveTextContent("VITE_OTEL_REPLAY=true");
    expect(sessions).toHaveTextContent("signals = forge.ObservabilitySignals {replay = True}");
    expect(within(sessions).getByTestId("obs-nothing-yet-docs")).toHaveAttribute("href", expect.stringContaining("#session-replay"));
    expect(screen.getAllByTestId("obs-nothing-yet")).toHaveLength(1);
  });

  it("keeps Alerts its own page when nothing has arrived", async () => {
    renderPanel(createFixtureApi({ empty: true, signals: { services: [], truncated: false } }), { view: "alerts" });
    await screen.findByTestId("obs-alerts");
    expect(screen.queryByTestId("obs-nothing-yet")).not.toBeInTheDocument();
  });

  it("asks about the week only when the window is empty", async () => {
    const onCall = vi.fn();
    renderPanel(createFixtureApi({ onCall, signals: { services: [], truncated: false } }));
    await screen.findByTestId("obs-overview");
    expect(onCall).not.toHaveBeenCalledWith("GetServiceSignals", expect.anything());
    expect(screen.queryByTestId("obs-nothing-yet")).not.toBeInTheDocument();
  });

  it("calls an empty window an empty window when the week has data", async () => {
    renderPanel(createFixtureApi({ empty: true, signals: { services: [{ service: "api", emitting: ["logs"], declared: [], declarationKnown: false }], truncated: false } }));
    expect(await screen.findByTestId("obs-no-data")).toHaveTextContent("No telemetry in the last hour");
    expect(screen.queryByTestId("obs-nothing-yet")).not.toBeInTheDocument();
  });

  it("never calls an app empty on a service that only declares signals, or a truncated answer", async () => {
    // A declared-only service sent nothing: still nothing-yet.
    const declared = renderPanel(createFixtureApi({ empty: true, signals: { services: [{ service: "api", emitting: [], declared: ["logs"], declarationKnown: true }], truncated: false } }));
    expect(await screen.findByTestId("obs-nothing-yet")).toBeInTheDocument();
    declared.unmount();
    // A truncated list is not "none": the window-empty state stays.
    renderPanel(createFixtureApi({ empty: true, signals: { services: [], truncated: true } }));
    expect(await screen.findByTestId("obs-no-data")).toBeInTheDocument();
  });

  it("still opens a deep link when nothing has arrived this week", async () => {
    const onCall = vi.fn();
    renderPanel(createFixtureApi({ onCall, empty: true, signals: { services: [], truncated: false } }), { view: "traces", trace: "abc123" });
    await waitFor(() => expect(onCall).toHaveBeenCalledWith("GetServiceSignals", expect.anything()));
    expect(await screen.findByTestId("trace-detail")).toBeInTheDocument();
    expect(screen.queryByTestId("obs-nothing-yet")).not.toBeInTheDocument();
  });

  it("has a designed empty state that offers a wider window", async () => {
    const user = userEvent.setup();
    const harness = renderPanel(createFixtureApi({ empty: true }));
    expect(await screen.findByTestId("obs-no-data")).toHaveTextContent("No telemetry in the last hour");
    await user.click(screen.getByTestId("obs-widen"));
    expect(harness.current().range).toBe("7d");
  });

  it("says when the hourly allowance refills, neutrally, where a view has no result yet", async () => {
    const resetsAtMs = Date.now() + 5 * 60_000;
    renderPanel(createFixtureApi({ failError: quotaPausedError({ limit: "queries", lane: "interactive", resetsAtMs, soft: false }) }));
    const state = await screen.findByTestId("obs-error-over-quota");
    // Not the red error: nothing is broken and there is nothing to retry before the reset.
    expect(state).toHaveAttribute("role", "status");
    expect(state).toHaveTextContent(`Will load at ${formatClock(resetsAtMs)}`);
    expect(state).toHaveTextContent("nothing is lost");
    expect(within(state).queryByTestId("obs-retry")).not.toBeInTheDocument();
  });

  it("explains any other refusal and offers to retry it", async () => {
    renderPanel(createFixtureApi({ failWith: Code.ResourceExhausted }));
    const error = await screen.findByTestId("obs-error-busy");
    expect(error).toHaveAttribute("role", "alert");
    expect(error).toHaveTextContent("shorter time range");
    expect(within(error).getByTestId("obs-retry")).toBeInTheDocument();
  });

  it("re-reads the window on Refresh", async () => {
    const user = userEvent.setup();
    const onCall = vi.fn();
    renderPanel(createFixtureApi({ onCall }));
    await screen.findByTestId("obs-overview");
    const before = onCall.mock.calls.filter(([rpc]) => rpc === "GetServiceOverview").length;
    await user.click(screen.getByTestId("obs-refresh"));
    await waitFor(() =>
      expect(onCall.mock.calls.filter(([rpc]) => rpc === "GetServiceOverview").length).toBeGreaterThan(before)
    );
  });
});
