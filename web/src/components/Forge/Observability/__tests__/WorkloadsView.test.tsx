// Copyright (c) 2025 Reliant Labs

/**
 * Workloads: customer metadata per workload — image, restarts and the last
 * exit reason, CPU and memory against limits, the latest warning — and a
 * workload's detail with its time series, rollouts and Kubernetes events.
 */

import { describe, expect, it, vi } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Code } from "@connectrpc/connect";

import { createFixtureApi, fixtureWorkloadDetail } from "../__fixtures__/fixtureApi";
import { describeMarkers, visibleMarkers } from "../charts/UPlotChart";
import { formatCores } from "../format";
import { imageRef, usageTone, versionMarkers } from "../WorkloadsView";
import { PROJECT_PLACE, renderPanel } from "./obsTestUtils";

vi.mock("uplot", async () => (await import("./obsMocks")).obsMocks.uplot());
vi.mock("@tanstack/react-virtual", async () => (await import("./obsMocks")).obsMocks.virtual());

describe("WorkloadsView: the list", () => {
  it("lists each workload with its image, restarts, exit reason, resource use and latest warning", async () => {
    const onCall = vi.fn();
    renderPanel(createFixtureApi({ onCall }), { view: "workloads" });
    const api = await screen.findByTestId("obs-workload-prod-api");
    expect(api).toHaveTextContent("shop-api:v1.8.3");
    expect(api).toHaveTextContent("v1.8.3 · shop-prod · 3 replicas");
    // The exit reason that matters most, marked as a kill.
    expect(within(api).getByTestId("obs-exit-reason")).toHaveTextContent("OOMKilled");
    expect(within(api).getByTestId("obs-exit-reason").className).toContain("text-destructive");
    // Memory at 97% of its limit reads as an alert; CPU at 42% does not.
    const [cpu, memory] = within(api).getAllByTestId("obs-usage");
    expect(cpu).toHaveAttribute("data-tone", "ok");
    expect(cpu).toHaveTextContent("420m of 1 core");
    expect(memory).toHaveAttribute("data-tone", "alert");
    expect(memory).toHaveTextContent("497 MB of 512 MB");
    expect(memory).toHaveTextContent("97%");
    expect(api).toHaveTextContent("OOMKilling");
    // A container with no limit or request says so instead of drawing a bar.
    expect(within(screen.getByTestId("obs-workload-prod-web")).getAllByTestId("obs-usage")[0]).toHaveTextContent("no limit");
    // Env page: scoped to that one environment, and no environment column.
    expect(onCall.mock.calls.find(([rpc]) => rpc === "ListWorkloads")?.[1]).toMatchObject({ envIds: ["env-prod"] });
    expect(screen.queryByRole("columnheader", { name: "Environment" })).not.toBeInTheDocument();
    expect(screen.getByTestId("obs-workloads-summary")).toHaveTextContent(/Restarts\s*3/);
    expect(screen.getByTestId("obs-workloads-summary")).toHaveTextContent(/Out of memory\s*1/);
  });

  it("lists every environment's workloads on the project area, with the environment named", async () => {
    renderPanel(createFixtureApi(), { view: "workloads" }, PROJECT_PLACE);
    await screen.findByTestId("obs-workload-staging-worker");
    expect(screen.getByRole("columnheader", { name: "Environment" })).toBeInTheDocument();
    const worker = screen.getByTestId("obs-workload-staging-worker");
    expect(within(worker).getByTestId("obs-exit-reason")).toHaveTextContent("Error");
    expect(worker).toHaveTextContent("BackOff");
    expect(worker).toHaveTextContent("×11");
    expect(screen.getByTestId("obs-workload-dev-api")).toBeInTheDocument();
  });

  it("explains what customer metadata is when there is none", async () => {
    const user = userEvent.setup();
    const harness = renderPanel(createFixtureApi({ empty: true }), { view: "workloads" });
    const empty = await screen.findByTestId("obs-no-customer-metadata");
    expect(empty).toHaveTextContent("No customer metadata in the last hour");
    expect(empty).toHaveTextContent("opted out");
    await user.click(within(empty).getByTestId("obs-widen"));
    expect(harness.current().range).toBe("7d");
  });

  it("offers to clear filters when they are what emptied it", async () => {
    const user = userEvent.setup();
    const harness = renderPanel(createFixtureApi({ empty: true }), { view: "workloads", f: ["k8s.workload.name:nope"] });
    const empty = await screen.findByTestId("obs-no-customer-metadata");
    expect(empty).toHaveTextContent("No workloads match");
    await user.click(within(empty).getByTestId("obs-clear-filters"));
    expect(harness.current().f).toBeUndefined();
  });

  it("says when it may not read the data", async () => {
    renderPanel(createFixtureApi({ failWith: Code.PermissionDenied }), { view: "workloads" });
    expect(await screen.findByTestId("obs-error-denied")).toHaveTextContent("can't read");
  });
});

describe("WorkloadsView: one workload", () => {
  it("opens into resource use over time, rollouts and Kubernetes events", async () => {
    const user = userEvent.setup();
    const onCall = vi.fn();
    const harness = renderPanel(createFixtureApi({ onCall }), { view: "workloads" });
    const api = await screen.findByTestId("obs-workload-prod-api");
    await user.click(within(api).getByRole("button", { name: "api" }));
    expect(harness.current().workload).toBe("api");
    await screen.findByTestId("obs-workload-detail");
    await waitFor(() => expect(onCall.mock.calls.some(([rpc]) => rpc === "GetWorkload")).toBe(true));
    expect(onCall.mock.calls.find(([rpc]) => rpc === "GetWorkload")?.[1]).toMatchObject({ workload: "api", envIds: ["env-prod"] });
    // Charts, each a figure described in words, with the version change marked.
    const memory = await screen.findByTestId("obs-workload-chart-memory_limit_utilization");
    await waitFor(() => expect(memory.querySelector("figcaption")).toHaveTextContent(/of limit: latest/));
    expect(memory.querySelector("figcaption")).toHaveTextContent(/Marked: .*v1\.8\.3 first seen in prod/);
    expect(screen.getByTestId("obs-workload-rollouts")).toHaveTextContent("service.version v1.8.3");
    // Events, newest first, warnings flagged; the toggle narrows to them.
    const events = screen.getByTestId("obs-workload-events");
    expect(events).toHaveTextContent("OOMKilling");
    expect(events).toHaveTextContent("Evicted");
    expect(within(events).getAllByTestId("obs-workload-event").length).toBeGreaterThan(2);
    await user.click(within(events).getByTestId("obs-events-warnings-only"));
    for (const row of within(events).getAllByTestId("obs-workload-event")) expect(row).toHaveAttribute("data-type", "Warning");
    // Facts: request and limit spelled out.
    expect(screen.getByTestId("obs-workload-facts")).toHaveTextContent("request 256 MB · limit 512 MB");
    await user.click(screen.getByTestId("obs-workload-back"));
    expect(harness.current().workload).toBeUndefined();
  });

  it("compares one workload across environments on the project area", async () => {
    renderPanel(createFixtureApi(), { view: "workloads", workload: "api" }, PROJECT_PLACE);
    const cpu = await screen.findByTestId("obs-workload-chart-cpu_usage");
    await waitFor(() => expect(cpu.querySelector("figcaption")).toHaveTextContent(/dev: latest .*staging: latest .*prod: latest/));
    expect(screen.getByTestId("obs-workload-detail")).toHaveTextContent("in dev, staging, prod");
  });
});

describe("helpers", () => {
  it("shortens an image to its name and tag", () => {
    expect(imageRef({ imageName: "ghcr.io/acme/shop-api", imageTag: "v1" })).toBe("shop-api:v1");
    expect(imageRef({ imageName: "", imageTag: "" })).toBe("unknown image");
  });

  it("reads usage against a limit as ok, warning or alert", () => {
    expect(usageTone(null)).toBe("ok");
    expect(usageTone(0.5)).toBe("ok");
    expect(usageTone(0.85)).toBe("warn");
    expect(usageTone(0.97)).toBe("alert");
    expect(formatCores(0.25)).toBe("250m");
    expect(formatCores(2)).toBe("2 cores");
  });

  it("marks version changes on the charts, and the chart seam keeps only markers in range", () => {
    const window = { startMs: Date.UTC(2026, 9, 12, 6), endMs: Date.UTC(2026, 9, 12, 7) };
    const detail = fixtureWorkloadDetail({ envIds: ["env-prod"] }, "api", undefined, window);
    const markers = versionMarkers(detail, new Map([["env-prod", "prod"]]), false);
    expect(markers.map((m) => m.label)).toEqual(["v1.8.2", "v1.8.3"]);
    const inRange = visibleMarkers([...markers, { timeSec: 1, label: "ancient" }], [window.startMs / 1000, window.endMs / 1000]);
    expect(inRange.map((m) => m.label)).toEqual(["v1.8.2", "v1.8.3"]);
    expect(describeMarkers(inRange)).toMatch(/^Marked: v1\.8\.2 first seen in prod at .*; v1\.8\.3 first seen in prod at .*\.$/);
    expect(describeMarkers([])).toBe("");
  });
});
