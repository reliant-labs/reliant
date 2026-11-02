// Copyright (c) 2025 Reliant Labs

/**
 * DEV-ONLY: every chart a dashboard panel can be, drawn by the real PanelBody
 * from deterministic answers, at a half-width panel's size — the busy grouped
 * line Sean flagged ("Reconciles by result", ~20 series), its stacked and
 * barred displays, bars, pie and donut, gauge, stat with sparkline, heatmap,
 * both funnels, a text panel and a kind this console does not know.
 *
 *   /forge-dashboards-preview?page=chart-gallery  (&dark=1&scheme=pure-black)
 */

import type { ReactNode } from "react";

import type { Panel, PanelResult, Series } from "@/services/controlPlane/observability/types";

import { PanelBody } from "../PanelBody";

const NOW = Math.floor(Date.UTC(2026, 9, 30, 15) / 1000);
const STEP = 60;
const POINTS = 60;
const TIMES = Array.from({ length: POINTS }, (_, i) => NOW - (POINTS - i) * STEP);
const WINDOW = { startMs: (NOW - POINTS * STEP) * 1000, endMs: NOW * 1000 };

/** A repeatable wobble: no Math.random, so screenshots compare. */
const wave = (seed: number, i: number, base: number, swing: number) => Math.max(0, base + swing * Math.sin(i / (3 + (seed % 5)) + seed) + (swing / 3) * Math.cos(i / 2 + seed * 1.7));

function result(partial: Partial<PanelResult>): PanelResult {
  return {
    kind: "",
    unit: "",
    series: [],
    columns: [],
    rows: [],
    stepSeconds: STEP,
    truncated: false,
    funnelSteps: [],
    issues: [],
    sessions: [],
    seriesMeta: [],
    seriesTotal: 0,
    warnings: [],
    compiledSql: [],
    window: WINDOW,
    ...partial,
  };
}

const CONTROLLERS = ["kustomization", "helmrelease", "gitrepository", "ocirepository", "helmchart", "helmrepository", "bucket"];
const RESULTS = ["success", "requeue", "requeue_after", "error"];

const reconcileSeries: Series[] = CONTROLLERS.flatMap((controller, c) =>
  RESULTS.filter((_result, k) => !(c > 4 && k === 1)).map((r, k) => ({
    label: "reconciles",
    group: { "metric.attr.controller": controller, "metric.attr.result": r },
    timestamps: TIMES,
    values: TIMES.map((_, i) => Math.round(wave(c * 4 + k, i, (r === "success" ? 40 : r === "error" ? 3 : 12) / (c + 1), (r === "error" ? 3 : 8) / (c + 1)))),
    value: null,
  }))
);

const reconciles: Panel = {
  id: "reconcile-total",
  title: "Reconciles by result",
  kind: "line",
  signal: "metrics",
  groupBy: ["metric.attr.controller", "metric.attr.result"],
  series: [{ label: "reconciles", agg: "sum", field: "controller_runtime_reconcile_total", metricType: "sum" }],
  height: 9,
};
const reconcileData = result({ series: reconcileSeries });

const latency: Panel = { id: "lat", title: "p95 latency by route", kind: "line", signal: "traces", unit: "ms", groupBy: ["span.name"], series: [{ label: "p95", agg: "quantile", quantile: 0.95, field: "span.duration" }] };
const latencyData = result({
  series: ["CreateOrder", "GetOrder", "ListOrders", "Checkout"].map((name, k) => ({ label: "p95", group: { "span.name": `orders.v1.OrdersService/${name}` }, timestamps: TIMES, values: TIMES.map((_, i) => wave(k, i, 80 + k * 40, 30)), value: null })),
});

const byResult: Panel = { id: "by-result", title: "Reconciles by result (last hour)", kind: "bar", signal: "metrics", groupBy: ["metric.attr.result"], series: [{ label: "reconciles", agg: "sum", field: "controller_runtime_reconcile_total", metricType: "sum" }] };
const byResultData = result({ columns: ["reconciles"], rows: [["success", 1840], ["requeue_after", 620], ["requeue", 210], ["error", 96]].map(([r, n]) => ({ group: { "metric.attr.result": String(r) }, values: [Number(n)] })) });

const routes = ["/checkout/payment", "/products/:id", "/cart", "/search", "/account/orders", "/"];
const byRoute: Panel = {
  id: "by-route",
  title: "Requests and errors by route",
  kind: "bar",
  signal: "traces",
  groupBy: ["span.attr.http.route"],
  series: [{ label: "requests", agg: "count" }, { label: "errors", agg: "count", where: [{ field: "span.status", op: "eq", value: "error" }] }],
  display: { horizontal: true },
};
const byRouteData = result({ columns: ["requests", "errors"], rows: routes.map((route, k) => ({ group: { "span.attr.http.route": route }, values: [4200 / (k + 1), 180 / (k + 2)] })) });

const services = ["api", "web", "worker", "gateway", "billing", "auth", "search", "mailer", "scheduler", "exporter", "indexer"];
const share: Panel = { id: "share", title: "Spans by service", kind: "bar", signal: "traces", groupBy: ["service.name"], series: [{ label: "spans", agg: "count" }], display: { style: "donut" } };
const shareData = result({ columns: ["spans"], rows: services.map((name, k) => ({ group: { "service.name": name }, values: [Math.round(9000 / (k + 1) ** 1.2)] })) });

const availability: Panel = { id: "avail", title: "Availability", kind: "number", signal: "traces", unit: "percent", formula: "(A - B) / A", series: [{ label: "all", agg: "count" }, { label: "failed", agg: "count" }], display: { style: "gauge" } };
const availabilityData = result({ series: [{ label: "formula", group: {}, timestamps: [], values: [], value: 0.9934 }] });

const vitals: Panel = {
  id: "lcp",
  title: "LCP p75 by page",
  kind: "number",
  signal: "traces",
  unit: "ms",
  groupBy: ["span.attr.location.pathname"],
  series: [{ label: "lcp", agg: "quantile", quantile: 0.75, field: "span.attr.lcp", thresholds: { good: 2500, poor: 4000 } }],
  display: { style: "gauge", max: 6000 },
};
const vitalsData = result({ series: [["/", 1900], ["/checkout", 3100], ["/search", 4700]].map(([page, v]) => ({ label: "lcp", group: { "span.attr.location.pathname": String(page) }, timestamps: [], values: [], value: Number(v) })) });

const requests: Panel = { id: "req", title: "Requests", kind: "number", signal: "traces", unit: "count", series: [{ label: "requests", agg: "count" }] };
const requestsData = result({ series: [{ label: "requests", group: {}, timestamps: TIMES, values: TIMES.map((_, i) => Math.round(wave(3, i, 900, 300))), value: 54_210 }] });
const requestsBefore = result({ series: [{ label: "requests", group: {}, timestamps: [], values: [], value: 48_900 }] });

const heatmap: Panel = { id: "heat", title: "Latency distribution", kind: "heatmap", signal: "traces", unit: "ms", series: [{ label: "requests", agg: "count", field: "span.duration" }] };
const bounds = [1, 2, 5, 10, 20, 50, 100, 200, 500, 1000, 2000, 5000];
const heatmapData = result({
  heatmap: TIMES.flatMap((t, i) =>
    bounds.slice(0, -1).flatMap((low, b) => {
      const peak = 6 + Math.round(Math.sin(i / 9));
      const count = Math.round(400 * Math.exp(-((b - peak) ** 2) / 2) + (b > 8 && i % 13 === 0 ? 6 : 0));
      return count > 0 ? [{ timeSec: t, low, high: bounds[b + 1], count }] : [];
    })
  ),
});

const funnel: Panel = { id: "funnel", title: "In-app: signup, onboarding, first message (per user)", kind: "funnel", signal: "traces", series: [], funnel: { steps: ["user_signed_up", "onboarding_completed", "first_message_sent", "subscription_started"], window: "30d", by: "user" } };
const funnelData = result({
  funnelSteps: [
    { event: "user_signed_up", count: 1280, rateFromFirst: 1, rateFromPrevious: 1 },
    { event: "onboarding_completed", count: 902, rateFromFirst: 902 / 1280, rateFromPrevious: 902 / 1280 },
    { event: "first_message_sent", count: 611, rateFromFirst: 611 / 1280, rateFromPrevious: 611 / 902 },
    { event: "subscription_started", count: 97, rateFromFirst: 97 / 1280, rateFromPrevious: 97 / 611 },
  ],
});

const stitched: Panel = { id: "stitched", title: "Acquisition funnel: landing visitor to first message (sql)", kind: "sql", signal: "", series: [], sql: "SELECT label, users FROM …", display: { style: "funnel" } };
const stitchedData = result({
  columns: ["users"],
  rows: [["1 visited landing", 5400], ["2 clicked download", 1210], ["3 signed in", 640], ["4 onboarded", 455], ["5 first message", 301]].map(([label, n]) => ({ group: { label: String(label) }, values: [Number(n)] })),
});

const text: Panel = {
  id: "about",
  title: "About this dashboard",
  kind: "text",
  signal: "",
  series: [],
  markdown: "Flux reconciles every **minute**. A sustained rise in `error` results pages the on-call.\n\n- [Runbook: Flux reconcile errors](https://example.com/runbooks/flux)\n- Owners: platform team",
};

const scatter: Panel = { id: "scatter", title: "A kind a newer forge added", kind: "scatter", signal: "traces", series: [] };

const ready = (data: PanelResult) => ({ data, isLoading: false, error: null });
const noDrill = { onDrill: () => {} };

function Tile({ title, note, wide = false, children }: { title: string; note?: string; wide?: boolean; children: ReactNode }) {
  return (
    <section className={wide ? "col-span-full min-w-0 rounded-lg border border-border bg-surface p-4" : "min-w-0 rounded-lg border border-border bg-surface p-4"}>
      <h2 className="text-sm font-medium text-ink">{title}</h2>
      {note && <p className="text-xs text-ink-muted">{note}</p>}
      <div className="mt-2">{children}</div>
    </section>
  );
}

export default function ChartGalleryPreview() {
  return (
    <div className="grid grid-cols-1 gap-4 p-4 lg:grid-cols-2" data-testid="chart-gallery-preview">
      <Tile title={reconciles.title} note="line, 26 series: top 8 + Other, errors in danger, legend with Last / Max / Total">
        <PanelBody panel={reconciles} query={ready(reconcileData)} links={noDrill} />
      </Tile>
      <Tile title={`${reconciles.title} — stacked area`} note='display: {style: "area", stack: true, topN: 6}'>
        <PanelBody panel={{ ...reconciles, display: { style: "area", stack: true, topN: 6 } }} query={ready(reconcileData)} links={noDrill} />
      </Tile>
      <Tile title={`${reconciles.title} — stacked bars`} note='display: {style: "bars", stack: true}'>
        <PanelBody panel={{ ...reconciles, display: { style: "bars", stack: true } }} query={ready(reconcileData)} links={noDrill} />
      </Tile>
      <Tile title={latency.title} note="non-additive (p95): Last / Max / Avg, nothing summed">
        <PanelBody panel={latency} query={ready(latencyData)} links={noDrill} />
      </Tile>
      <Tile title={byResult.title} note="bar, one series: the error group in danger">
        <PanelBody panel={byResult} query={ready(byResultData)} links={noDrill} />
      </Tile>
      <Tile title={byRoute.title} note='bar, two series, display: {horizontal: true}'>
        <PanelBody panel={byRoute} query={ready(byRouteData)} links={noDrill} />
      </Tile>
      <Tile title={share.title} note='display: {style: "donut"}, 11 groups: top 8 + Other'>
        <PanelBody panel={share} query={ready(shareData)} links={noDrill} />
      </Tile>
      <Tile title={`${share.title} — pie`} note='display: {style: "pie", topN: 5}'>
        <PanelBody panel={{ ...share, display: { style: "pie", topN: 5 } }} query={ready(shareData)} links={noDrill} />
      </Tile>
      <Tile title={availability.title} note='number, display: {style: "gauge"} (percent: 0 to 1)'>
        <PanelBody panel={availability} query={ready(availabilityData)} />
      </Tile>
      <Tile title={vitals.title} note="gauges graded by the series' thresholds (good 2.5s, poor 4s)">
        <PanelBody panel={vitals} query={ready(vitalsData)} />
      </Tile>
      <Tile title={requests.title} note="stat with its sparkline and the change vs the previous period">
        <PanelBody panel={requests} query={ready(requestsData)} compare={{ query: ready(requestsBefore), label: "previous period", offsetSec: 3600 }} />
      </Tile>
      <Tile title={heatmap.title} note="heatmap: time across, log-spaced value buckets up">
        <PanelBody panel={heatmap} query={ready(heatmapData)} />
      </Tile>
      <Tile title={funnel.title} note="funnel kind: conversion and drop-off between steps">
        <PanelBody panel={funnel} query={ready(funnelData)} />
      </Tile>
      <Tile title={stitched.title} note='sql rows, display: {style: "funnel"}'>
        <PanelBody panel={stitched} query={ready(stitchedData)} />
      </Tile>
      <Tile title={text.title} note='kind: "text"'>
        <PanelBody panel={text} query={{ data: undefined, isLoading: false, error: null }} />
      </Tile>
      <Tile title={scatter.title} note="an unknown kind is a note, never an error">
        <PanelBody panel={scatter} query={{ data: undefined, isLoading: false, error: null }} />
      </Tile>
    </div>
  );
}
