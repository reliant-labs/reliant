// Copyright (c) 2025 Reliant Labs

/**
 * The Observability home (PROPOSAL §4.6) against the fixture app: the strip
 * (requests, error rate, p95 with the change against the previous window,
 * alerts firing), the services sorted by what needs attention, the recent
 * releases and the ways into the explorers — and the ranking rule on its own.
 */

import { describe, expect, it, vi } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Code } from "@connectrpc/connect";

import type { AlertInstance } from "@/services/controlPlane/observability/alertingTypes";
import type { ServiceHealth, Workload } from "@/services/controlPlane/observability/types";

import { createFixtureApi, FIXTURE_ENVS, FIXTURE_PROJECT, type FixtureOptions } from "../../__fixtures__/fixtureApi";
import { renderWithApi } from "../../__tests__/obsTestUtils";
import { byAttention, firingCounts, ratioSeries, serviceRows } from "../homeModel";
import { ObservabilityHome, type ObservabilityHomeHrefs } from "../ObservabilityHome";

vi.mock("uplot", async () => (await import("../../__tests__/obsMocks")).obsMocks.uplot());

const hrefs: ObservabilityHomeHrefs = {
  logs: (s = {}) => `/logs?range=${s.range}&service=${s.service}&severity=${s.severity}`,
  traces: (s = {}) => `/traces?range=${s.range}&minDuration=${s.minDuration}`,
  profiles: () => "/profiles",
  alerts: "/alerts",
  dashboards: "/dashboards",
  workload: (name, env) => `/workloads/${name}${env ? `?env=${env}` : ""}`,
  release: (v) => `/releases/${v}`,
  releases: "/releases",
};

function renderHome(options: FixtureOptions = {}, env = "prod") {
  const onCall = vi.fn();
  const navigateTo = vi.fn();
  const onRange = vi.fn();
  renderWithApi(
    createFixtureApi({ ...options, onCall }),
    <ObservabilityHome project={FIXTURE_PROJECT} env={env} environments={FIXTURE_ENVS} range="1h" onRange={onRange} hrefs={hrefs} navigateTo={navigateTo} />
  );
  return { onCall, navigateTo, onRange };
}

describe("the Observability home", () => {
  it("draws the strip for the scope, each figure against the previous window", async () => {
    const { onCall } = renderHome();
    const strip = await screen.findByTestId("observability-strip");
    await waitFor(() => expect(within(strip).getByTestId("observability-cell-requests")).toHaveTextContent(/vs previous 1h/));
    expect(within(strip).getByTestId("observability-cell-error-rate")).toHaveTextContent("Error rate");
    expect(within(strip).getByTestId("observability-cell-p95-latency")).toHaveTextContent(/ms/);
    expect(within(strip).getByTestId("observability-cell-alerts-firing")).toHaveTextContent("CheckoutP95High");
    const panels = onCall.mock.calls.filter(([rpc]) => rpc === "QueryPanel").map(([, req]) => req);
    expect(panels.some((req) => req.envIds[0] === "env-prod" && req.panel.kind === "number")).toBe(true);
  });

  it("sorts the services by what needs attention, and a row opens the workload", async () => {
    const user = userEvent.setup();
    const { navigateTo } = renderHome();
    const table = await screen.findByTestId("observability-services");
    const rows = await within(table).findAllByTestId(/^observability-service-/);
    // The status is the workload's server-side verdict: prod's api restarted 3x with every replica ready, so degraded.
    expect(rows.map((r) => r.getAttribute("data-tone"))).toEqual(["warning", "success"]);
    expect(rows[0]).toHaveTextContent("Degraded");
    expect(within(table).getByRole("heading", { name: "Process health" })).toBeInTheDocument();
    expect(within(table).getByRole("columnheader", { name: "Memory" })).toBeInTheDocument();
    expect(within(rows[0]).getByTitle("Error logs from api")).toHaveAttribute("href", "/logs?range=1h&service=api&severity=error,fatal");
    await user.click(rows[1].querySelector("td:nth-child(4)")!);
    expect(navigateTo).toHaveBeenCalledWith("/workloads/worker");
  });

  it("shows an Environment column with every environment in scope", async () => {
    renderHome({}, "all");
    const table = await screen.findByTestId("observability-services");
    await within(table).findByText("Environment");
    expect((await within(table).findAllByTestId(/^observability-service-/)).length).toBeGreaterThan(2);
  });

  it("lists the recent releases and the ways in", async () => {
    renderHome();
    expect(await screen.findByRole("link", { name: "v1.8.3" })).toHaveAttribute("href", "/releases/v1.8.3");
    const explore = screen.getByTestId("observability-explore");
    expect(within(explore).getByRole("link", { name: "Find slow traces" })).toHaveAttribute("href", "/traces?range=1h&minDuration=1s");
    expect(within(explore).getByRole("link", { name: "Your dashboards" })).toHaveAttribute("href", "/dashboards");
  });

  it("explains an organization whose telemetry is not set up", async () => {
    renderHome({ failWith: Code.FailedPrecondition });
    expect(await screen.findByTestId("obs-not-set-up")).toBeInTheDocument();
  });

  it("leads the not-set-up answer with what the home shows and the command for this environment", async () => {
    renderHome({ failWith: Code.FailedPrecondition });
    const state = await screen.findByTestId("obs-not-set-up");
    expect(within(state).getByTestId("obs-not-set-up-about")).toHaveTextContent("requests, error rate and latency");
    expect(within(state).getByText("forge env deploy prod")).toBeInTheDocument();
  });

  it("explains an environment with nothing sent", async () => {
    renderHome({ empty: true });
    expect(await screen.findByTestId("observability-no-services")).toHaveTextContent("No service sent telemetry in the last hour");
  });

  it("says how to get started when nothing has arrived this week", async () => {
    renderHome({ empty: true, signals: { services: [], truncated: false } }, "all");
    const state = await screen.findByTestId("obs-nothing-yet");
    // Every environment in scope: the command keeps its placeholder.
    expect(within(state).getByText("forge env deploy <env>")).toBeInTheDocument();
    expect(within(state).getByText("forge obs status <env>")).toBeInTheDocument();
    expect(within(state).getByTestId("obs-nothing-yet-docs")).toHaveAttribute("href", expect.stringContaining("troubleshooting#nothing-shows-up-in-observability"));
    expect(screen.queryByTestId("observability-strip")).not.toBeInTheDocument();
  });

  it("names an environment Reliant has no record of", async () => {
    renderHome({}, "qa");
    expect(await screen.findByTestId("observability-unrecorded-env")).toHaveTextContent("no record of qa");
  });
});

describe("what needs attention", () => {
  const health = (name: string, over: Partial<ServiceHealth> = {}): ServiceHealth => ({
    name,
    group: {},
    requestCount: 100,
    requestRate: 1,
    errorCount: 0,
    errorRate: 0,
    p50Ms: 1,
    p95Ms: 2,
    p99Ms: 3,
    errorLogCount: 0,
    timestamps: [],
    rateSeries: [],
    errorRateSeries: [],
    requestTrend: [],
    errorTrend: [],
    source: "",
    operatorData: false,
    ...over,
  });
  const workload = (name: string, health: Workload["health"], healthReason = ""): Workload =>
    ({ envId: "env-prod", envName: "prod", workload: name, namespace: "", serviceVersion: "", replicasObserved: 1, restartsInWindow: 5, containers: [], latestWarning: null, health, healthReason }) as unknown as Workload;
  const alert = (service: string): AlertInstance => ({ envId: "env-prod", rule: "X", fingerprint: service, labels: { service }, annotations: {}, state: "firing", activeAtMs: null, firingAtMs: null, lastEvalAtMs: null, lastValue: 1 });

  it("puts crash-looping first, then the signals (alerts firing before a high error rate), then the rest", () => {
    const rows = serviceRows(
      [health("quiet", { requestCount: 0 }), health("ok"), health("noisy", { errorRate: 0.2 }), health("paged"), health("looping"), health("operator", { operatorData: true })],
      [workload("looping", "crash_looping", "restarted 5x, last exit OOMKilled")],
      [alert("paged")],
      () => "prod"
    ).sort(byAttention);
    // Only "looping" has a workload, so only it has a verdict. The others say
    // their signal, in words and a tone the verdict never uses.
    expect(rows.map((r) => [r.name, r.status, r.tone])).toEqual([
      ["looping", "Crash-looping", "danger"],
      ["paged", "Alert firing", "neutral"],
      ["noisy", "Errors elevated", "neutral"],
      ["ok", "No workload data", "neutral"],
      ["quiet", "No workload data", "neutral"],
    ]);
    expect(rows[1].reason).toBe("1 alert firing");
    // The workload's verdict and its reason are the server's, never re-derived from restarts.
    expect(rows[0].reason).toBe("restarted 5x, last exit OOMKilled");
  });

  it("takes a workload's word from its verdict even when the request side is quiet", () => {
    const [row] = serviceRows([health("api", { errorRate: 0.2 })], [workload("api", "healthy")], [], () => "prod");
    // Healthy pods, but a high error rate: the verdict stands, the error rate says why it ranks.
    expect(row).toMatchObject({ status: "Healthy", tone: "success", reason: "high error rate" });
  });

  it("derives the error rate and the firing count per bucket", () => {
    expect(ratioSeries([1, null, 3], [10, 0, 30])).toEqual([0.1, null, 0.1]);
    expect(firingCounts([0, 60, 120], [{ startSec: 50, endSec: 130, label: "a" }, { startSec: 125, endSec: 200, label: "b" }])).toEqual([1, 1, 2]);
  });
});
