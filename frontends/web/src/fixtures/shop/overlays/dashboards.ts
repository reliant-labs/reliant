// Copyright (c) 2025 Reliant Labs

/**
 * Two dashboards the screenshot harness opens by URL under shop's release
 * dashboards, and that no list names (so the gallery does not move). Each is
 * an overlay (../overlays.ts): its GetDashboard and QueryPanel answers come
 * first, and every other dashboard or panel goes to the pack's own handler.
 *
 *   chart-hover          a figure with its trend and a two-series line, which
 *                        the chart hover surfaces point at (the crosshair and
 *                        tooltip on a full chart and on a card's sparkline)
 *   sandboxes-dashboard  the platform's Sandboxes dashboard (control-plane
 *                        deploy/observability/dashboards/sandboxes.json, copied
 *                        verbatim into sandboxes.dashboard.json: re-copy it when
 *                        that file changes), every panel answered in the shape
 *                        its SQL returns through control-plane's panelfold: a
 *                        first column that is a time makes one series per
 *                        (numeric column, group of the text columns); anything
 *                        else is table rows; one row of figures is the panel's
 *                        numbers. The fleet: five sandboxes (workspace pods
 *                        `ws-<id>` on two GKE nodes). The platform's self-hosted
 *                        machines are control-plane's Platform machines
 *                        dashboard, not this one
 *
 * Ported from the visual harness's own fixtures (e2e/visual, before it served
 * this pack). Every time is relative to the pack clock.
 */

import type { MockHandlers } from "@reliantlabs/forge-web-runtime/mock-transport";

import { GetDashboardResponseSchema, type GetDashboardRequest } from "@/gen/controlplane/services/dashboard/v1/dashboard_pb";
import { QueryPanelResponseSchema, type QueryPanelRequest } from "@/gen/controlplane/services/observability/v1/observability_pb";

import { decodeFixtureJson } from "../packJson";
import { shopScenario } from "../index";
import { DAY, demoNow, HOUR, RUNNING } from "../shopStory";
import { msOf } from "../handlers/wire";
import spec from "./sandboxes.dashboard.json?raw";

const GET_DASHBOARD = "controlplane.v1.DashboardService/GetDashboard";
const QUERY_PANEL = "controlplane.v1.ObservabilityService/QueryPanel";

const iso = (ago: number) => new Date(demoNow() - ago).toISOString();

/** A dashboard opened by URL: a repo dashboard of the release prod runs. */
const summary = (key: string, title: string, description: string, panelCount: number) => ({
  ref: { source: "DASHBOARD_SOURCE_REPO", projectId: "shop", key },
  source: "DASHBOARD_SOURCE_REPO",
  title, description, readOnly: true, starred: false, panelCount, previewPanels: [], tags: [], category: "",
  release: { releaseVersion: RUNNING["env-prod"].version, shippedAt: iso(RUNNING["env-prod"].age), filePath: `deploy/observability/dashboards/${key}.json`, envIds: [] },
});

type Json = Record<string, unknown>;

/**
 * `answers` over the pack's own GetDashboard and QueryPanel: a dashboard the
 * overlay names, and a panel it answers, first; everything else as before.
 */
function overlayHandlers(dashboards: Record<string, () => Json>, panel: (req: QueryPanelRequest) => Json | undefined): MockHandlers {
  let pack: MockHandlers | undefined;
  const base = () => (pack ??= shopScenario().handlers as unknown as MockHandlers);
  return {
    [GET_DASHBOARD]: (req: GetDashboardRequest) => {
      const own = dashboards[req.ref?.key ?? ""];
      return own ? decodeFixtureJson(GetDashboardResponseSchema, own()) : base()[GET_DASHBOARD]!(req);
    },
    [QUERY_PANEL]: (req: QueryPanelRequest) => {
      const own = panel(req);
      return own ? decodeFixtureJson(QueryPanelResponseSchema, own) : base()[QUERY_PANEL]!(req);
    },
  };
}

// ── chart-hover ─────────────────────────────────────────────────────────────

const chartHoverSpec = {
  id: "chart-hover", title: "Chart hover", filters: [], panels: [
    { id: "hover-figure", title: "Requests per second", kind: "number", signal: "traces", series: [{ label: "Requests", agg: "rate" }], width: 8, height: 4 },
    { id: "hover-line", title: "Requests and errors per second", kind: "line", signal: "traces", series: [{ label: "Requests", agg: "rate" }, { label: "Errors", agg: "rate" }], width: 16, height: 8 },
  ],
};

/** 24 hourly points of a deterministic wave around `base`. */
const hourlyWave = (base: number, phase: number) =>
  Array.from({ length: 24 }, (_, i) => ({ time: iso((23 - i) * HOUR), value: Math.round(base * (1 + 0.35 * Math.sin((i + phase) / 3.1)) * 100) / 100, partial: false }));

function chartHoverPanel(req: QueryPanelRequest): Json | undefined {
  const id = req.panel?.id ?? "";
  if (id === "hover-figure") return { panelId: id, kind: "number", unit: "rate", stepSeconds: 3600, series: [{ label: "Requests", group: {}, value: 42.5, points: hourlyWave(40, 2) }] };
  if (id === "hover-line") {
    return {
      panelId: id, kind: "line", unit: "rate", stepSeconds: 3600,
      series: [
        { label: "Requests", group: {}, value: 0, points: hourlyWave(40, 2) },
        { label: "Errors", group: {}, value: 0, points: hourlyWave(3, 5) },
      ],
    };
  }
  return undefined;
}

export const chartHoverHandlers = (): MockHandlers =>
  overlayHandlers(
    { "chart-hover": () => ({ summary: summary("chart-hover", "Chart hover", "A figure with its trend, and a two-series line", 2), spec: chartHoverSpec, version: 1, canWrite: false }) },
    chartHoverPanel,
  );

// ── sandboxes ───────────────────────────────────────────────────────────────

const SANDBOXES = JSON.parse(spec) as { title: string; description: string; panels: { id: string; sql?: string; unit?: string }[] };
const PODS = ["ws-2aab1465", "ws-e164d47d", "ws-1e76f570", "ws-929888f7", "ws-b1b21231"];
const NODES = ["gke-prod-daemon-v2-pool-a-7c1f", "gke-prod-daemon-v2-pool-a-9d42"];

/** A deterministic wave in [lo, hi], one cycle about every `period` points, shifted by `phase`. */
const wave = (i: number, lo: number, hi: number, phase = 0, period = 24) =>
  Math.round((lo + ((hi - lo) * (1 + Math.sin(((i + phase) / period) * 2 * Math.PI))) / 2) * 1000) / 1000;

interface Buckets {
  times: string[];
  days: string[];
}

/** The buckets of the asked window (the request's step, else hourly), ending at the pack clock. */
function bucketsOf(req: QueryPanelRequest): Buckets {
  const range = req.scope?.timeRange;
  const end = range?.end ? msOf(range.end) : demoNow();
  const start = range?.start ? msOf(range.start) : end - 7 * DAY;
  const step = Math.max(req.stepSeconds * 1000 || HOUR, (end - start) / 240);
  const times: string[] = [];
  for (let t = Math.ceil(start / step) * step; t < end; t += step) times.push(new Date(t).toISOString());
  const days: string[] = [];
  for (let d = Math.floor(start / DAY) * DAY; d < end; d += DAY) days.push(new Date(d).toISOString());
  return { times, days };
}

type Series = { label: string; group: Record<string, string>; value: number; points: Json[] };

const series = (label: string, group: Record<string, string>, times: string[], value: (i: number) => number | null): Series => ({
  label, group, value: 0,
  points: times.map((time, i) => {
    const v = value(i);
    return v === null ? { time, value: 0, null: true } : { time, value: v };
  }),
});

const row = (group: Record<string, string>, values: (number | null)[]) => ({
  group,
  values: values.map((v) => v ?? 0),
  missingValueIndexes: values.flatMap((v, i) => (v === null ? [i] : [])),
});

/** One panel's answer: its rows or series, in the shape control-plane's sql fold returns. */
function sandboxesAnswer(id: string, w: Buckets): { columns: string[]; rows?: ReturnType<typeof row>[]; series?: Series[]; daily?: boolean } | undefined {
  const T = w.times;
  const perPod = (label: string, f: (i: number, p: number) => number | null) => PODS.map((pod, p) => series(label, { pod }, T, (i) => f(i, p)));
  const perMachine = (key: string, label: string, names: string[], f: (i: number, m: number) => number | null) => names.map((name, m) => series(label, { [key]: name }, T, (i) => f(i, m)));
  const daily = (cols: string[], f: (d: number, c: number) => number) => ({ columns: cols, series: cols.map((c, k) => series(c, {}, w.days, (d) => f(d, k))), daily: true });
  switch (id) {
    case "fleet-now":
      return { columns: ["sandboxes", "users_with_a_sandbox", "cpu_cores_in_use", "memory_gib_in_use"], rows: [row({}, [5, 4, 6.42, 22.8])] };
    case "health-now":
      return { columns: ["sandboxes_sampled", "over_1_5_load_per_cpu", "worst_load_per_cpu", "min_disk_free", "heavy_waiting", "forge_installs"], rows: [row({}, [5, 1, 1.82, 0.124, 2, 3])] };
    case "awake-over-time":
      return { columns: ["awake_sandboxes"], series: [series("awake_sandboxes", {}, T, (i) => Math.round(wave(i, 2, 6, 6)))] };
    case "fleet-cpu-memory":
      return { columns: ["cpu_cores", "memory_gib"], series: [series("cpu_cores", {}, T, (i) => wave(i, 1.2, 7.4, 6)), series("memory_gib", {}, T, (i) => wave(i, 9, 24, 4))] };
    case "cpu-distribution":
      return { columns: ["sandboxes"], rows: ([["1 · under 0.5", 2], ["2 · 0.5 to 1", 1], ["3 · 1 to 2", 1], ["4 · 2 to 4", 1]] as const).map(([band, n]) => row({ cpu_cores_band: band }, [n])) };
    case "memory-distribution":
      return { columns: ["sandboxes"], rows: ([["2 · 1 to 2", 1], ["3 · 2 to 4", 2], ["4 · 4 to 8", 1], ["5 · 8 to 16", 1]] as const).map(([band, n]) => row({ memory_gib_band: band }, [n])) };
    case "cpu-per-box":
      return { columns: ["cpu_cores"], series: perPod("cpu_cores", (i, p) => wave(i, 0.05, 0.4 + p * 0.6, p * 5)) };
    case "memory-per-box":
      return { columns: ["memory"], series: perPod("memory", (i, p) => Math.round(wave(i, 0.8, 2 + p * 2.4, p * 3) * 1073741824)) };
    case "cpu-vs-request":
      return { columns: ["of_request"], series: perPod("of_request", (i, p) => wave(i, 0.05, 0.5 + p * 0.25, p * 4)) };
    case "cpu-vs-limit":
      return { columns: ["of_limit"], series: perPod("of_limit", (i, p) => wave(i, 0.02, 0.2 + p * 0.15, p * 4)) };
    case "memory-vs-limit":
      return { columns: ["of_limit"], series: perPod("of_limit", (i, p) => wave(i, 0.1, 0.35 + p * 0.12, p * 3)) };
    case "daily-load":
      return daily(["p50_load_per_cpu", "p95_load_per_cpu", "share_over_1_5"], (d, c) => [wave(d, 0.3, 0.6, 1, 7), wave(d, 0.9, 1.9, 1, 7), wave(d, 0.01, 0.12, 1, 7)][c]!);
    case "daily-pressure-slots":
      return daily(["cpu_psi", "memory_psi", "io_psi", "mean_slot_wait_s", "p95_slot_wait_s"], (d, c) => [wave(d, 2, 9, 2, 7), wave(d, 0, 3, 3, 7), wave(d, 1, 6, 4, 7), Math.round(wave(d, 4, 40, 2, 7)), Math.round(wave(d, 30, 210, 2, 7))][c]!);
    case "daily-timeouts-disk":
      return daily(["tool_timeouts", "peak_forge_installs", "min_disk_free"], (d, c) => [Math.round(wave(d, 0, 6, 5, 7)), Math.round(wave(d, 1, 4, 2, 7)), wave(d, 0.12, 0.4, 3, 7)][c]!);
    case "load-per-cpu":
      return { columns: ["load_per_cpu"], series: perMachine("machine", "load_per_cpu", PODS, (i, m) => wave(i, 0.1, 0.5 + m * 0.35, m * 5)) };
    case "pressure":
      return { columns: ["cpu_some", "memory_some", "memory_full", "io_some", "io_full"], series: ["cpu_some", "memory_some", "memory_full", "io_some", "io_full"].map((c, k) => series(c, {}, T, (i) => wave(i, 0, [14, 6, 2, 9, 3][k]!, k * 4))) };
    case "heavy-slots":
      return { columns: ["held", "waiting", "slots"], series: [series("held", {}, T, (i) => Math.round(wave(i, 0, 6, 6))), series("waiting", {}, T, (i) => Math.max(0, Math.round(wave(i, -3, 3, 6)))), series("slots", {}, T, () => 8)] };
    case "heavy-wait":
      return { columns: ["oldest_wait"], series: perMachine("machine", "oldest_wait", PODS.slice(0, 3), (i, m) => Math.max(0, Math.round(wave(i, -60, 120 + m * 60, m * 7)))) };
    case "forge-installs":
      return { columns: ["forge_installs"], series: perMachine("machine", "forge_installs", PODS.slice(0, 3), (i, m) => Math.max(0, Math.round(wave(i, -1, 3, m * 6)))) };
    case "tool-timeouts":
      return { columns: ["timeouts"], series: perMachine("machine", "timeouts", PODS.slice(0, 2), (i, m) => ((i + m * 11) % 17 === 0 ? 1 + m : null)) };
    case "disk-free":
      return { columns: ["free"], series: PODS.slice(0, 3).map((pod, p) => series("free", { volume: `${pod} · /home/workspace` }, T, (i) => Math.round((0.62 - p * 0.18 - i * 0.0008) * 1000) / 1000)) };
    case "restart-summary":
      return { columns: ["restarts", "oom_kills", "sandboxes_restarted"], rows: [row({}, [3, 1, 2])] };
    case "restarts-by-reason":
      return { columns: ["restarts"], series: [series("restarts", { exit_reason: "Error" }, T, (i) => (i % 53 === 7 ? 1 : null)), series("restarts", { exit_reason: "OOMKilled" }, T, (i) => (i === T.length - 30 ? 1 : null))] };
    case "oom-kills":
      return { columns: ["oom_kills"], series: [series("oom_kills", { pod: PODS[3]! }, T, (i) => (i === T.length - 30 ? 1 : null))] };
    case "pod-recreations":
      return { columns: ["recreated_pods"], series: [series("recreated_pods", {}, T, (i) => (i % 24 === 9 ? 2 : i % 24 === 14 ? 1 : null))] };
    case "pods-not-running":
      return { columns: ["pods"], series: [series("pods", { phase: "Pending" }, T, (i) => (i % 24 === 9 ? 1 : null))] };
    case "boxes-by-node":
      return { columns: ["sandboxes", "cpu_cores", "memory_gib"], rows: [row({ node: NODES[0]! }, [3, 4.18, 14.2]), row({ node: NODES[1]! }, [2, 2.24, 8.6])] };
    case "noisiest-boxes":
      return {
        columns: ["cpu_avg_cores", "cpu_peak_cores", "memory_peak_gib", "restarts_total"],
        rows: PODS.map((pod, p) =>
          row({ workspace_id: pod.slice(3), user_id: `u-${(4096 + p * 977).toString(16)}`, owner_org_id: p % 2 ? "personal" : "55949942-3ee9-4cfc-bb7c-817bdba75186", node: NODES[p % 2]! }, [
            Math.round((2.6 - p * 0.5) * 100) / 100,
            Math.round((3.9 - p * 0.6) * 100) / 100,
            Math.round((11.8 - p * 2.1) * 10) / 10,
            p === 3 ? 2 : p === 0 ? 1 : 0,
          ]),
        ),
      };
    default:
      return undefined;
  }
}

const PANELS = new Map(SANDBOXES.panels.map((p) => [p.id, p]));

/** A sandboxes panel's answer, or undefined for any other panel (matched by id and statement). */
function sandboxesPanel(req: QueryPanelRequest): Json | undefined {
  const declared = PANELS.get(req.panel?.id ?? "");
  if (!declared || declared.sql !== req.panel?.sql) return undefined;
  const a = sandboxesAnswer(declared.id, bucketsOf(req));
  if (!a) return undefined;
  const step = req.stepSeconds || 3600;
  return {
    panelId: declared.id, kind: "sql", unit: declared.unit ?? "", stepSeconds: step,
    columns: a.columns, rows: a.rows ?? [], series: a.series ?? [],
    ...(a.daily ? { bucketSeconds: 86400, bucketTimezone: "UTC" } : a.series ? { bucketSeconds: step } : {}),
  };
}

export const sandboxesHandlers = (): MockHandlers =>
  overlayHandlers(
    { sandboxes: () => ({ summary: summary("sandboxes", SANDBOXES.title, SANDBOXES.description, SANDBOXES.panels.length), spec: JSON.parse(spec), version: 1, canWrite: false }) },
    sandboxesPanel,
  );
