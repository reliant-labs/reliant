// Copyright (c) 2026 Reliant Labs

/**
 * What the Tests pages compute from TestsService's answers, free of React:
 * lane order, the figures' words, and the trend chart's series and markers.
 */

import type { ChartMarker, ChartSeries } from "../Observability/charts/UPlotChart";
import type { CommitReport, TestRun, TestSummary } from "@/services/controlPlane/tests/types";

/** The scaffolded lanes first, in the order they run; any other lane after them, by name. */
const LANE_ORDER = ["unit", "integration", "e2e"];

export function orderLanes(lanes: Iterable<string>): string[] {
  const rank = (lane: string) => {
    const index = LANE_ORDER.indexOf(lane);
    return index === -1 ? LANE_ORDER.length : index;
  };
  return [...new Set(lanes)].sort((a, b) => rank(a) - rank(b) || a.localeCompare(b));
}

/** A percentage, one decimal: "74.2%". */
export function formatPercent(percent: number): string {
  return `${(Math.round(percent * 10) / 10).toFixed(1)}%`;
}

/** A coverage delta in points: "+0.6", "−8.0", "±0.0". */
export function formatDelta(points: number): string {
  const rounded = Math.round(points * 10) / 10;
  if (rounded === 0) return "±0.0";
  return `${rounded > 0 ? "+" : "−"}${Math.abs(rounded).toFixed(1)}`;
}

/** A pass rate (0–1) as a percentage that never rounds a failure away: 0.9996 is "99.9%", not "100%". */
export function formatPassRate(rate: number): string {
  if (rate >= 1) return "100%";
  return `${(Math.floor(rate * 1000) / 10).toFixed(1)}%`;
}

export const shortSha = (sha: string) => sha.slice(0, 7);

/** The combined figure of a report and the lanes behind it, or null when the report carries no coverage. */
export function headlineCoverage(report: CommitReport | null): { percent: number; delta: number | null; lanes: { lane: string; percent: number }[] } | null {
  const combined = report?.coverage?.combined;
  if (!report || !combined || combined.statements === 0) return null;
  const lanes = orderLanes(Object.keys(report.coverage!.lanes)).map((lane) => ({ lane, percent: report.coverage!.lanes[lane]!.percent }));
  return { percent: combined.percent, delta: report.coverage!.deltaPercent, lanes };
}

/** A run's verdict in words, with the tone its status dot takes. Undetermined is never green. */
export function runVerdict(run: TestRun): { words: string; tone: "success" | "danger" | "neutral" } {
  if (run.ingestState === "pending") return { words: "Processing", tone: "neutral" };
  if (run.ingestState === "failed") return { words: "Couldn't be read", tone: "neutral" };
  switch (run.outcome) {
    case "passed":
      return { words: "Passed", tone: "success" };
    case "failed":
      return { words: `${run.totals.failed} failed`, tone: "danger" };
    default:
      return { words: "Undetermined", tone: "neutral" };
  }
}

/** Where a run came from, in a few words: "CI #4411", "local", "preview env". */
export function runSourceWords(run: TestRun): string {
  switch (run.source) {
    case "ci":
      return run.ci?.runId ? `CI #${run.ci.runId}` : "CI";
    case "local":
      return "local";
    case "env":
      return "against an environment";
    case "confirm":
      return "flake confirm";
    default:
      return "unknown source";
  }
}

/** The palette token of each lane on the trend: combined is the page's hue, each lane its own (they are different series). */
const LANE_COLOR: Record<string, string> = { unit: "--chart-2", integration: "--chart-3", e2e: "--chart-4" };

/** A lane's palette token, the same on every Tests page (the trend, the coverage tree's bars). */
export const laneColor = (lane: string): string => LANE_COLOR[lane] ?? "--chart-5";

/**
 * The coverage trend as chart input: one timestamp per commit that has a
 * time, combined first, then each lane the trend carries, as fractions (the
 * chart's "percent" unit). A lane missing on a commit is a gap, never a zero.
 */
export function trendChart(summary: TestSummary): { timestamps: number[]; series: ChartSeries[]; markers: ChartMarker[] } {
  const points = summary.trend.filter((point) => point.atMs !== null);
  const lanes = orderLanes(points.flatMap((point) => Object.keys(point.lanesPercent)));
  const series: ChartSeries[] = [
    { label: "Combined", values: points.map((point) => point.combinedPercent / 100), color: "--chart-1" },
    ...lanes.map((lane) => ({
      label: lane,
      values: points.map((point) => (point.lanesPercent[lane] === undefined ? null : point.lanesPercent[lane]! / 100)),
      color: LANE_COLOR[lane] ?? "--chart-5",
    })),
  ];
  const markers: ChartMarker[] = summary.releaseMarkers
    .filter((marker) => marker.cutMs !== null)
    .map((marker) => ({ timeSec: marker.cutMs! / 1000, label: marker.releaseVersion, description: `release ${marker.releaseVersion} cut`, tone: "muted" }));
  return { timestamps: points.map((point) => point.atMs! / 1000), series, markers };
}
