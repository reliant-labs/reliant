// Copyright (c) 2025 Reliant Labs

/**
 * DEV-ONLY proving harness for the Dashboards index and a dashboard. NOT a
 * product surface.
 *
 * The real DashboardsList and DashboardView inside the
 * real console chrome (ConsolePreviewShell), against the fixture app
 * (createFixtureApi: telemetry, the release ledger, alerting and an in-memory
 * DashboardService) — unauthenticated, no backend — so every state can be
 * looked at in every colour scheme and screenshotted. All state is in the URL:
 *
 *   /forge-dashboards-preview                                  Dashboards
 *   /forge-dashboards-preview?page=dashboard&dash=workspace/checkout-latency
 *   …&dash=release/frontend-web   (funnel, issues, sessions)    …&dash=release/overview
 *   …&compare=period|release  &range=24h  &service=api  &tv=1  &version=2
 *   …&env=all|staging|dev
 *   …&case=empty|read-only|error|loading|not-set-up|no-telemetry|nothing-yet
 *   …&alert=ratio|p95|grouped|formula|gauge|sql|heatmap|counter|funnel   "Create alert from this panel", for a sample panel
 *   …&dark=1&scheme=pure-black
 */

import { useEffect, useMemo } from "react";
import { useNavigate, useSearch } from "@tanstack/react-router";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { Code, ConnectError } from "@connectrpc/connect";
import { BinaryWriter } from "@bufbuild/protobuf/wire";

import { ObservabilityApiProvider, type ObservabilityApi } from "@/services/controlPlane/observability/ObservabilityApiContext";
import type { DashboardRef } from "@/services/controlPlane/observability/dashboardTypes";
import type { Panel } from "@/services/controlPlane/observability/types";

import { ConsolePreviewShell } from "../../../Console/ConsolePreviewShell";
import { withSearch, type ConsolePage } from "../../../Console/paths";
import { createFixtureApi, FIXTURE_ENVS, FIXTURE_PROJECT, type FixtureOptions } from "../../__fixtures__/fixtureApi";
import type { ObsSearch } from "../../obsSearch";
import { compareParam, isDashboardPageKind, rangeParam, refreshParam, type DashboardSearch } from "../dashboardModel";
import { DashboardsList, type DashboardsSearch } from "../DashboardsList";
import { AlertFromPanelDialog } from "../AlertFromPanelDialog";
import { DashboardView } from "../DashboardView";
import ChartCorrectnessPreview from "../../charts/__preview__/ChartCorrectnessPreview";

const BASE = "/forge-dashboards-preview";

const SCHEMES = [
  "professional-blue",
  "refined-neutral",
  "modern-teal",
  "slate",
  "purple-classic",
  "pure-black",
] as const;

/** Emulates control-plane's per-user cap on concurrent console reads: over `limit` in flight is refused as in_flight_limit with a 1s RetryInfo. */
interface ReadCap {
  limit: number;
  ms: number;
}

const CASES: { id: string; label: string; options: FixtureOptions & { loading?: boolean; readCap?: ReadCap } }[] = [
  { id: "data", label: "the fixture app", options: {} },
  { id: "empty", label: "no dashboards yet", options: { dashboardsEmpty: true } },
  { id: "read-only", label: "member, not admin", options: { dashboardsReadOnly: true } },
  { id: "error", label: "dashboards unavailable", options: { dashboardsFailWith: Code.Unavailable } },
  { id: "loading", label: "loading", options: { loading: true } },
  { id: "cap", label: "server cap of 6 concurrent reads, each takes 800ms", options: { readCap: { limit: 6, ms: 800 } } },
  { id: "tight", label: "server cap of 1, each read takes 1.5s (retries, waiting for capacity)", options: { readCap: { limit: 1, ms: 1500 } } },
  { id: "jammed", label: "server always at its cap (waiting, then Retry)", options: { readCap: { limit: 0, ms: 300 } } },
  { id: "not-set-up", label: "observability not set up", options: { failWith: Code.FailedPrecondition } },
  { id: "no-telemetry", label: "nothing sent in the window", options: { empty: true } },
  { id: "nothing-yet", label: "set up, nothing arrived this week (getting started)", options: { empty: true, signals: { services: [], truncated: false } } },
];

type PreviewSearch = DashboardsSearch &
  Omit<DashboardSearch, "range" | "compare" | "refresh" | "version"> & {
    page?: string;
    dash?: string;
    env?: string;
    case?: string;
    alert?: string;
    dark?: boolean | string | number;
    scheme?: string;
    range?: string;
    compare?: string;
    refresh?: string;
    version?: number;
  };

const CONTROL = "rounded-md border border-border bg-surface px-2 py-1 text-sm text-ink";

/** The fixture environments, prod with the rules a project would have declared in its main.k. */
const ALERT_ENVS = FIXTURE_ENVS.map((e) =>
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

declare global {
  interface Window {
    /** The cap emulation's tally, for looking at the scheduler from the console: window.__panelReads. */
    __panelReads?: { live: number; peak: number; refused: number; served: number };
  }
}

function capped(api: ObservabilityApi, cap: ReadCap): ObservabilityApi {
  // One tally per page: StrictMode builds the api twice, and both must count into the same object.
  const stats = (window.__panelReads ??= { live: 0, peak: 0, refused: 0, served: 0 });
  return {
    ...api,
    queryPanel: (req, options) => {
      stats.live++;
      stats.peak = Math.max(stats.peak, stats.live);
      if (stats.live > cap.limit) {
        stats.live--;
        stats.refused++;
        const error = new ConnectError("too many observability queries in flight; retry shortly", Code.ResourceExhausted, { "x-forge-error-reason": "in_flight_limit" });
        const duration = new BinaryWriter().tag(1, 0).int64(1).finish();
        error.details.push({ type: "google.rpc.RetryInfo", value: new BinaryWriter().tag(1, 2).bytes(duration).finish() });
        return Promise.reject(error);
      }
      return new Promise((resolve, reject) => {
        const timer = setTimeout(() => {
          stats.live--;
          stats.served++;
          resolve(api.queryPanel(req));
        }, cap.ms);
        options?.signal?.addEventListener("abort", () => {
          clearTimeout(timer);
          stats.live--;
          reject(options.signal!.reason);
        });
      });
    },
  };
}

/** An API whose every call never answers: the loading state, held. */
function pending(api: ObservabilityApi): ObservabilityApi {
  return Object.fromEntries(Object.keys(api).map((key) => [key, () => new Promise(() => undefined)])) as unknown as ObservabilityApi;
}

export default function DashboardsPreview() {
  const search = useSearch({ strict: false }) as PreviewSearch;
  const navigate = useNavigate();
  const env = search.env ?? "prod";
  const caseId = search.case ?? "data";
  const active = CASES.find((c) => c.id === caseId) ?? CASES[0]!;
  // The router parses `dark=1` as JSON, so it arrives as a number.
  const dark = !!search.dark && String(search.dark) !== "0" && String(search.dark) !== "false";
  const scheme = search.scheme ?? "professional-blue";
  const page = search.page ?? "list";
  const [dashKind, ...dashKey] = (search.dash ?? "workspace/checkout-latency").split("/");
  const dashRef: DashboardRef | null = isDashboardPageKind(dashKind) ? { kind: dashKind, projectId: FIXTURE_PROJECT, key: dashKey.join("/") } : null;

  useEffect(() => {
    document.documentElement.classList.toggle("dark", dark);
    document.documentElement.setAttribute("data-color-scheme", scheme);
  }, [dark, scheme]);

  // A fresh client and fixture per case, so one case's answers (and stars) never leak into the next.
  // eslint-disable-next-line react-hooks/exhaustive-deps
  const queryClient = useMemo(() => new QueryClient({ defaultOptions: { queries: { retry: false, refetchOnWindowFocus: false } } }), [caseId]);
  const api = useMemo(() => {
    const fixture = createFixtureApi(active.options);
    if (active.options.readCap) return capped(fixture, active.options.readCap);
    return active.options.loading ? pending(fixture) : fixture;
  }, [active]);

  const go = (next: Partial<PreviewSearch>, replace = true) =>
    void navigate({ to: BASE, search: (prev: Record<string, unknown>) => ({ ...prev, ...next }), replace });
  const keep = { env, case: search.case, dark: search.dark, scheme: search.scheme };
  const previewHref = (extra: Record<string, unknown>) => withSearch(BASE, { ...keep, ...extra });
  const dashboardHref = (ref: DashboardRef, extra: DashboardSearch = {}) => previewHref({ page: "dashboard", dash: `${ref.kind}/${ref.key}`, ...extra });
  const explore = (view: string) => (s: ObsSearch = {}) => withSearch("/forge-observability-preview", { view, ...s });
  const navigateTo = (href: string) => {
    if (href.startsWith(BASE)) void navigate({ href });
    else window.open(href, "_blank");
  };

  const consolePage: ConsolePage =
    page === "dashboard" && dashRef ? { kind: "dashboard", dashKind: dashRef.kind as "release" | "workspace", key: dashRef.key } : { kind: "obs-view", view: "dashboards" };

  const hrefs = {
    logs: explore("logs"),
    traces: explore("traces"),
    profiles: explore("profiles"),
    alerts: withSearch("/forge-observability-preview", { view: "alerts" }),
    dashboards: previewHref({ page: "list" }),
    list: previewHref({ page: "list" }),
    releases: "/forge-releases-preview",
    release: (version: string) => withSearch("/forge-releases-preview", { version }),
    trace: (traceId: string) => withSearch("/forge-observability-preview", { view: "traces", trace: traceId }),
    session: (sessionId: string) => withSearch("/forge-observability-preview", { view: "sessions", session: sessionId }),
    workload: (name: string) => withSearch("/forge-workloads-preview", { workload: name }),
    dashboard: dashboardHref,
  };

  return (
    <ConsolePreviewShell
      page={consolePage}
      project={FIXTURE_PROJECT}
      envs={FIXTURE_ENVS.map((e) => ({ name: e.name, preview: false }))}
      env={env}
      onEnvChange={(next) => go({ env: next })}
      starred={caseId === "data" ? [{ title: "Checkout latency", dashKind: "workspace", key: "checkout-latency" }, { title: "shop: overview", dashKind: "release", key: "overview" }, { title: "Frontend · web", dashKind: "release", key: "frontend-web" }] : []}
      dashboardTitle={page === "dashboard" ? dashRef?.key : undefined}
      controls={
        <>
          <select data-testid="preview-page" value={page} onChange={(e) => go({ page: e.target.value === "list" ? undefined : e.target.value })} className={CONTROL}>
            <option value="list">Dashboards</option>
            <option value="dashboard">A dashboard</option>
          </select>
          <select data-testid="preview-case" value={caseId} onChange={(e) => go({ case: e.target.value === "data" ? undefined : e.target.value })} className={CONTROL}>
            {CASES.map((c) => (
              <option key={c.id} value={c.id}>
                {c.label}
              </option>
            ))}
          </select>
          <select data-testid="preview-scheme" value={scheme} onChange={(e) => go({ scheme: e.target.value })} className={CONTROL}>
            {SCHEMES.map((s) => (
              <option key={s} value={s}>
                {s}
              </option>
            ))}
          </select>
          <button type="button" data-testid="preview-dark" onClick={() => go({ dark: dark ? undefined : "1" })} className={CONTROL}>
            {dark ? "Dark" : "Light"}
          </button>
        </>
      }
    >
      <QueryClientProvider client={queryClient}>
        <ObservabilityApiProvider api={api}>
          {page === "chart-correctness" ? <ChartCorrectnessPreview /> : page !== "dashboard" || !dashRef ? (
            <DashboardsList
              key={caseId}
              project={FIXTURE_PROJECT}
              env={env}
              environments={FIXTURE_ENVS}
              search={{ q: search.q, source: search.source, owner: search.owner }}
              onSearch={(next) => go(next)}
              hrefs={hrefs}
              navigateTo={navigateTo}
            />
          ) : (
            <DashboardView
              key={`${caseId}-${search.dash}`}
              project={FIXTURE_PROJECT}
              dashRef={dashRef}
              env={env}
              environments={FIXTURE_ENVS}
              search={{
                range: rangeParam(search.range),
                service: search.service,
                rpcService: search.rpcService,
                rpcMethod: search.rpcMethod,
                rpcProcedure: search.rpcProcedure,
                variables: search.variables,
                breakdown: search.breakdown,
                hiddenSections: search.hiddenSections,
                hiddenPanels: search.hiddenPanels,
                releases: search.releases,
                alerts: search.alerts,
                compare: search.compare ? compareParam(search.compare) : undefined,
                refresh: search.refresh ? refreshParam(search.refresh) : undefined,
                tv: !!search.tv || undefined,
                version: typeof search.version === "number" ? search.version : undefined,
                release: search.release,
              }}
              onSearch={(next) => go(next as Partial<PreviewSearch>, false)}
              onEnvChange={(next) => go({ env: next })}
              hrefs={hrefs}
              navigateTo={navigateTo}
            />
          )}
        </ObservabilityApiProvider>
      </QueryClientProvider>
      {search.alert && ALERT_SAMPLES[search.alert] && (
        <AlertFromPanelDialog
          panel={ALERT_SAMPLES[search.alert]}
          dashboardTitle="Checkout latency"
          environments={ALERT_ENVS}
          env={env}
          services={search.service ? [search.service] : []}
          onClose={() => go({ alert: undefined })}
        />
      )}
    </ConsolePreviewShell>
  );
}
