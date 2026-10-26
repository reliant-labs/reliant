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

import { createFixtureApi, FIXTURE_ORG } from "../__fixtures__/fixtureApi";
import { renderPanel } from "./obsTestUtils";

vi.mock("uplot", async () => (await import("./obsMocks")).obsMocks.uplot());
vi.mock("@tanstack/react-virtual", async () => (await import("./obsMocks")).obsMocks.virtual());

describe("ObservabilityPanel", () => {
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
    const user = userEvent.setup();
    renderPanel(createFixtureApi({ failWith: Code.FailedPrecondition }));
    expect(await screen.findByTestId("obs-not-set-up")).toHaveTextContent("isn't set up for this organization yet");
    // Not presented as an error, and nothing to retry.
    expect(screen.getByTestId("obs-not-set-up")).toHaveAttribute("role", "status");
    expect(screen.queryByTestId("obs-retry")).not.toBeInTheDocument();
    await user.click(screen.getByTestId("obs-view-logs"));
    expect(screen.getAllByTestId("obs-not-set-up")).toHaveLength(1);
    expect(screen.queryByTestId("obs-logs")).not.toBeInTheDocument();
  });

  it("has a designed empty state that offers a wider window", async () => {
    const user = userEvent.setup();
    const harness = renderPanel(createFixtureApi({ empty: true }));
    expect(await screen.findByTestId("obs-no-data")).toHaveTextContent("No telemetry in the last hour");
    await user.click(screen.getByTestId("obs-widen"));
    expect(harness.current().range).toBe("7d");
  });

  it("explains a quota hit and offers to retry it", async () => {
    renderPanel(createFixtureApi({ failWith: Code.ResourceExhausted }));
    const error = await screen.findByTestId("obs-error-over-quota");
    expect(error).toHaveAttribute("role", "alert");
    expect(error).toHaveTextContent("shorter time range");
    expect(within(error).getByTestId("obs-retry")).toBeInTheDocument();
  });

  it("moves between views with the arrow keys and clears the previous view's filters", async () => {
    const user = userEvent.setup();
    const harness = renderPanel(createFixtureApi(), { view: "logs", q: "timeout", service: "api" });
    await screen.findByTestId("obs-logs");
    screen.getByTestId("obs-view-logs").focus();
    await user.keyboard("{ArrowRight}");
    await waitFor(() => expect(screen.getByTestId("obs-panel")).toHaveAttribute("data-view", "traces"));
    expect(screen.getByTestId("obs-view-traces")).toHaveFocus();
    // The window and the service travel; the log query does not.
    expect(harness.current()).toMatchObject({ view: "traces", service: "api" });
    expect(harness.current().q).toBeUndefined();
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
