// Copyright (c) 2026 Reliant Labs

/**
 * A chart lays its previous-period series onto the current axis by the
 * period its comparison read went back by (cardRead's shift), not by the
 * dashboard's range: a traces line on "Last 30 days" reads 7 days and
 * compares with the 7 before them, so it shifts them forward 7 days. Shifted
 * by 30, every previous point would land outside the axis and alignShifted
 * would drop it. The same holds for a fixed-bucket panel widened past a
 * short range.
 */

import { describe, expect, it, vi } from "vitest";
import { cleanup, screen, waitFor } from "@testing-library/react";

import type { DashboardRef } from "@/services/controlPlane/observability/dashboardTypes";
import type { Panel } from "@/services/controlPlane/observability/types";
import { rangeMs } from "@/services/controlPlane/observability/window";

import { createFixtureApi, FIXTURE_ENVS, FIXTURE_PROJECT } from "../../__fixtures__/fixtureApi";
import { renderWithApi } from "../../__tests__/obsTestUtils";
import type { DashboardSearch } from "../dashboardModel";
import { DashboardView, type DashboardHrefs } from "../DashboardView";
import { alignShifted } from "../seriesModel";

vi.mock("uplot", async () => (await import("../../__tests__/obsMocks")).obsMocks.uplot());
vi.mock("@/lib/logger", () => ({ logger: { error: vi.fn(), warn: vi.fn(), info: vi.fn(), debug: vi.fn() } }));

// The comparison shift each card hands its chart, by panel id.
const shiftSec: Record<string, number | undefined> = {};
vi.mock("../DataPanel", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../DataPanel")>();
  return {
    ...actual,
    DataPanelBody: (props: { panel: Panel; compare?: { offsetSec: number } }) => {
      shiftSec[props.panel.id] = props.compare?.offsetSec;
      return null;
    },
  };
});

const hrefs: DashboardHrefs = {
  logs: () => "/logs",
  traces: () => "/traces",
  workload: (name) => `/workloads/${name}`,
  list: "/dashboards",
  release: (v) => `/releases/${v}`,
  trace: (id) => `/trace/${id}`,
  dashboard: (ref) => `/d/${ref.kind}/${ref.key}`,
};
const ref: DashboardRef = { kind: "workspace", projectId: FIXTURE_PROJECT, key: "checkout-latency" };
// A metrics line bucketed by day: on "Last hour" it is read over 7 days.
const daily = { id: "daily", title: "Daily", kind: "line", signal: "metrics", minStep: "1d", series: [{ label: "n", agg: "count" }] } as unknown as Panel;

function render(search: DashboardSearch) {
  for (const key of Object.keys(shiftSec)) delete shiftSec[key];
  const base = createFixtureApi();
  const api = {
    ...base,
    getDashboard: async (req: Parameters<typeof base.getDashboard>[0]) => {
      const doc = await base.getDashboard(req);
      return { ...doc, spec: { ...doc.spec, panels: [...doc.spec.panels, daily] } };
    },
  };
  renderWithApi(
    api,
    <DashboardView project={FIXTURE_PROJECT} dashRef={ref} env="prod" environments={FIXTURE_ENVS} search={search} onSearch={vi.fn()} onEnvChange={vi.fn()} hrefs={hrefs} navigateTo={vi.fn()} />
  );
}

const sec = (ms: number) => Math.round(ms / 1000);

describe("the previous period on a chart", () => {
  it("shifts a capped traces line by the 7 days it read, so its points land on the axis", async () => {
    render({ range: "30d", compare: "period" });
    await screen.findByTestId("dashboard-grid");
    await waitFor(() => expect(shiftSec["latency-by-route"]).toBe(sec(rangeMs("7d"))));

    // Its 7 days, hourly, and the 7 before them: shifted by what was read they line up; by 30 days, nothing does.
    const end = 1_800_000_000;
    const step = 3600;
    const now = Array.from({ length: 168 }, (_, i) => end - (168 - i) * step);
    const before = { timestamps: now.map((t) => t - sec(rangeMs("7d"))), values: now.map((_, i) => i) };
    expect(alignShifted(now, before, shiftSec["latency-by-route"]!)).toEqual(before.values);
    expect(alignShifted(now, before, sec(rangeMs("30d"))).every((v) => v === null)).toBe(true);
    cleanup();
  });

  it("shifts a fixed-bucket line by the range it was widened to", async () => {
    render({ range: "1h", compare: "period" });
    await screen.findByTestId("dashboard-grid");
    await waitFor(() => expect(shiftSec.daily).toBe(sec(rangeMs("7d"))));
    cleanup();
  });
});
