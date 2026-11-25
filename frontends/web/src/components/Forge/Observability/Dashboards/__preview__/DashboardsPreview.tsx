// Copyright (c) 2025 Reliant Labs

/**
 * DEV-ONLY proving harness for the Dashboards index and a dashboard. NOT a
 * product surface.
 *
 * The real console's Dashboards page and a dashboard, in the real console, on
 * their real clients over the shop fixture pack (ConsoleFixturePreview) —
 * unauthenticated, no backend — so every state can be looked at in every
 * colour scheme and screenshotted. A case is a named overlay over the pack
 * (src/fixtures/shop/overlays.ts observabilityOverlays), or for the server's
 * read cap, an interceptor (readCap). The harness's URL says where it opens:
 *
 *   /forge-dashboards-preview                                  Dashboards (the grouped list)
 *   /forge-dashboards-preview?group=services                   narrowed to one group
 *   /forge-dashboards-preview?page=dashboard&dash=workspace/payments-provider
 *   …&dash=release/frontend-web   (sessions)   …&dash=release/checkout (funnel)   …&dash=release/product (day buckets)
 *   …&compare=period|release  &range=24h  &service=api  &tv=1  &version=2
 *   …&env=all|staging|dev
 *   …&case=few|empty|read-only|error|loading|cap|tight|jammed|not-set-up|no-telemetry|nothing-yet
 *   …&alert=ratio|p95|grouped|formula|gauge|sql|heatmap|counter|funnel   "Create alert from this panel", for a sample panel
 *   …&dark=1&scheme=pure-black
 *   /forge-dashboards-preview?page=chart-gallery              every panel chart (ChartGalleryPreview)
 *   /forge-dashboards-preview?page=panel-editor&kind=sql      the panel editor, for every kind (PanelEditorPreview)
 *   /forge-dashboards-preview?page=chart-correctness          chart edge cases (ChartCorrectnessPreview)
 *
 * The galleries draw from props; what the panel editor reads (attribute
 * values, a metric's names) the pack answers too.
 */

import { useMemo, type ReactNode } from "react";
import { useNavigate } from "@tanstack/react-router";

import { SHOP_SERVE, shopPack } from "@/fixtures/shop";
import { DEMO_ENVS, DEMO_PROJECT } from "@/fixtures/shop/handlers/scope";
import { observabilityOverlays, readCap } from "@/fixtures/shop/overlays";
import type { FixtureServeOptions } from "@/fixtures/preview";
import type { FixturePack } from "@/fixtures/transport";
import type { Panel } from "@/services/controlPlane/observability/types";

import { ConsolePreviewFrame } from "../../../__preview__/PreviewShellFrame";
import { consolePath } from "../../../Console/paths";
import { CONSOLE_SERVE, ConsoleFixturePreview, isDark, PreviewControls, usePageSearch, withPageSearch } from "../../../__preview__/ConsoleFixturePreview";
import { isDashboardPageKind } from "../dashboardModel";
import { AlertFromPanelDialog } from "../AlertFromPanelDialog";
import ChartCorrectnessPreview from "../../charts/__preview__/ChartCorrectnessPreview";
import ChartGalleryPreview from "./ChartGalleryPreview";
import PanelEditorPreview from "./PanelEditorPreview";

const BASE = "/forge-dashboards-preview";

/** The console's world, for the galleries (no app layer: they sit in the preview chrome, not the console). */
const SHOP = shopPack();

const o = observabilityOverlays;

/** The console harness's serving, with control-plane's per-user cap on concurrent panel reads emulated. */
const capped = (limit: number, ms: number): FixtureServeOptions => ({ ...SHOP_SERVE, interceptors: [...(CONSOLE_SERVE.interceptors ?? []), readCap(limit, ms)] });

const CASES: { id: string; label: string; overlay?: FixturePack | readonly FixturePack[]; serve?: FixtureServeOptions }[] = [
  { id: "data", label: "the shop" },
  { id: "few", label: "a young app: two dashboards", overlay: o.dashboards.few },
  { id: "empty", label: "no dashboards yet", overlay: o.dashboards.empty },
  { id: "read-only", label: "member, not admin", overlay: o.dashboards["read-only"] },
  { id: "error", label: "dashboards unavailable", overlay: o.dashboards.error },
  { id: "loading", label: "loading", overlay: o.dashboards.loading },
  { id: "cap", label: "server cap of 6 concurrent reads, each takes 800ms", serve: capped(6, 800) },
  { id: "tight", label: "server cap of 1, each read takes 1.5s (retries, waiting for capacity)", serve: capped(1, 1500) },
  { id: "jammed", label: "server always at its cap (waiting, then Retry)", serve: capped(0, 300) },
  { id: "not-set-up", label: "observability not set up", overlay: o.telemetry["not-set-up"] },
  { id: "no-telemetry", label: "nothing sent in the window", overlay: o.telemetry.empty },
  { id: "nothing-yet", label: "set up, nothing arrived this week (getting started)", overlay: o.telemetry["nothing-yet"] },
];

const PAGES = [
  { id: "list", label: "Dashboards" },
  { id: "dashboard", label: "A dashboard" },
  { id: "chart-gallery", label: "Chart gallery" },
  { id: "panel-editor", label: "Panel editor" },
  { id: "chart-correctness", label: "Chart correctness" },
] as const;

const CONTROL = "rounded-md border border-border bg-surface px-2 py-1 text-sm text-ink";

/** The pack's environments, prod with the rules a project would have declared in its main.k. */
const ALERT_ENVS = DEMO_ENVS.map((e) =>
  e.name === "prod"
    ? {
        ...e,
        declared: {
          alerts: [
            { alert: "OrdersBacklog", expr: "SELECT 1 AS value", for: "10m", interval: "1m", keepFiringFor: "", labels: { severity: "critical" }, annotations: {}, source: "user" as const, defaultId: "" },
            { alert: "ServiceSilent", expr: "SELECT 1 AS value", for: "5m", interval: "1m", keepFiringFor: "", labels: { severity: "warning" }, annotations: {}, source: "default" as const, defaultId: "service-silent" },
          ],
          alertRoutes: [],
          observability: null,
        },
      }
    : { ...e },
);

/** Panels the fixture dashboards do not have, for looking at the alert dialog on each kind. */
const ALERT_SAMPLES: Record<string, Panel> = {
  p95: { id: "p95", title: "p95 checkout", kind: "number", signal: "traces", where: [{ field: "span.kind", op: "eq", value: "server" }, { field: "span.name", op: "eq", value: "orders.v1.OrdersService/CreateOrder" }], series: [{ label: "p95", agg: "quantile", quantile: 0.95, field: "span.duration" }], unit: "ms" },
  ratio: { id: "error-rate", title: "Checkout error rate", kind: "line", signal: "traces", ratio: true, unit: "percent", where: [{ field: "span.kind", op: "eq", value: "server" }], groupBy: ["service.name"], series: [{ label: "errors", agg: "count", where: [{ field: "span.status", op: "eq", value: "error" }] }, { label: "requests", agg: "count" }] },
  grouped: { id: "slowest", title: "Slowest routes", kind: "table", signal: "traces", where: [{ field: "span.kind", op: "eq", value: "server" }], groupBy: ["span.name"], series: [{ label: "p95", agg: "quantile", quantile: 0.95, field: "span.duration" }, { label: "requests", agg: "count" }] },
  formula: { id: "failed-pct", title: "Failed requests, percent", kind: "line", signal: "traces", formula: "A / B * 100", groupBy: ["service.name"], series: [{ label: "failed", agg: "count", where: [{ field: "span.status", op: "eq", value: "error" }] }, { label: "all", agg: "count" }] },
  gauge: { id: "goroutines", title: "Goroutines", kind: "line", signal: "metrics", groupBy: ["service.name"], series: [{ label: "goroutines", agg: "max", field: "go.goroutine.count", metricType: "gauge" }] },
  sql: {
    id: "slow-queries",
    title: "Slow database calls",
    kind: "sql",
    signal: "",
    series: [],
    sql: "SELECT ServiceName AS service,\n       quantile(0.95)(Duration) / 1e6 AS value\nFROM otel_traces\nWHERE Timestamp >= {from:DateTime64(9)} AND Timestamp < {to:DateTime64(9)}\n  AND SpanAttributes['db.system'] != ''\n  AND (empty({services:Array(String)}) OR has({services:Array(String)}, ServiceName))\nGROUP BY service",
  },
  heatmap: { id: "latency-dist", title: "Latency distribution", kind: "heatmap", signal: "traces", series: [{ label: "latency", agg: "count", field: "span.duration" }], unit: "ms" },
  counter: { id: "requests", title: "Requests", kind: "line", signal: "metrics", series: [{ label: "requests", agg: "sum", field: "rpc.server.calls", metricType: "sum" }] },
  funnel: { id: "conversion", title: "Conversion", kind: "funnel", signal: "traces", series: [], funnel: { steps: ["begin_checkout", "purchase"], window: "1h", by: "session" } },
};

/** The harness's own keys, which never reach the console. */
const OWN = ["page", "dash", "alert"];

export default function DashboardsPreview() {
  const params = usePageSearch();
  const navigate = useNavigate();
  const go = (next: Record<string, unknown>) => void navigate({ to: BASE, search: (prev: Record<string, unknown>) => ({ ...prev, ...next }), replace: true } as never);
  const caseId = params.get("case") ?? "data";
  const active = CASES.find((c) => c.id === caseId) ?? CASES[0]!;
  const page = params.get("page") ?? "list";
  const scheme = params.get("scheme") ?? "professional-blue";
  const dark = isDark(params.get("dark"));
  const env = params.get("env") ?? "prod";
  const alert = params.get("alert");
  const [dashKind, ...dashKey] = (params.get("dash") ?? "workspace/payments-provider").split("/");

  const consoleTarget = page === "dashboard" && isDashboardPageKind(dashKind) ? ({ kind: "dashboard", dashKind, key: dashKey.join("/") } as const) : ({ kind: "obs-view", view: "dashboards" } as const);
  const path = withPageSearch(consolePath(DEMO_PROJECT, consoleTarget), OWN, params.toString());

  const pagePicker = (
    <select
      data-testid="preview-page"
      value={page}
      onChange={(e) => go({ page: e.target.value === "list" ? undefined : e.target.value })}
      className={CONTROL}
    >
      {PAGES.map((p) => (
        <option key={p.id} value={p.id}>
          {p.label}
        </option>
      ))}
    </select>
  );
  const controls = (
    <PreviewControls base={BASE} cases={CASES} caseId={active.id} defaultCase="data" scheme={scheme} dark={dark}>
      {pagePicker}
    </PreviewControls>
  );

  // The galleries: the real console chrome around them, the pack under the panel editor's reads.
  if (page === "chart-gallery" || page === "panel-editor" || page === "chart-correctness") {
    return (
      <ConsolePreviewFrame page={{ kind: "obs-view", view: "dashboards" }} controls={controls} pack={SHOP} serve={SHOP_SERVE}>
        {page === "chart-gallery" ? <ChartGalleryPreview /> : page === "panel-editor" ? <PanelEditorPreview /> : <ChartCorrectnessPreview />}
      </ConsolePreviewFrame>
    );
  }

  const sample = alert ? ALERT_SAMPLES[alert] : undefined;
  return (
    <DashboardsCase
      key={`${active.id}|${path}`}
      path={path}
      active={active}
      controls={controls}
      dialog={sample ? <AlertFromPanelDialog panel={sample} dashboardTitle="Payment provider" environments={ALERT_ENVS} env={env} services={params.get("service") ? [params.get("service")!] : []} onClose={() => go({ alert: undefined })} /> : null}
    />
  );
}

function DashboardsCase({ path, active, controls, dialog }: { path: string; active: (typeof CASES)[number]; controls: ReactNode; dialog: ReactNode }) {
  const serve = useMemo(() => active.serve ?? CONSOLE_SERVE, [active]);
  return (
    <ConsoleFixturePreview
      path={path}
      overlay={active.overlay}
      serve={serve}
      controls={controls}
      wrap={(console) => (
        <>
          {console}
          {dialog}
        </>
      )}
    />
  );
}
