// Copyright (c) 2025 Reliant Labs

/**
 * DEV-ONLY proving harness for the metrics explorer. NOT a product surface.
 *
 * The real MetricsExplorer inside the real console chrome
 * (ConsolePreviewShell), against the explorer's fixture app
 * (createExplorerFixtureApi: the metric catalog, a metric's detail, exemplars,
 * grouped and reduced panels, explain, an in-memory DashboardService) —
 * unauthenticated, no backend — so every state can be looked at in every
 * colour scheme and screenshotted. All state is in the URL:
 *
 *   /forge-metrics-preview                     p95 checkout.latency, its detail open
 *   /forge-metrics-preview?blank=1             nothing picked yet
 *   …&case=cap|read-only|conflict|not-set-up|no-telemetry|nothing-yet|loading
 *   …&env=all|staging|dev     …&range=24h     …&compare=period
 *   …&dark=1&scheme=pure-black
 *
 * Every explorer key (q, m, bd, hide, chart, tab, sql) works as it does in the
 * console: a link copied from the console's explorer opens the same page here.
 */

import { useEffect, useMemo } from "react";
import { useNavigate, useSearch } from "@tanstack/react-router";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { Code } from "@connectrpc/connect";

import { ObservabilityApiProvider, type ObservabilityApi } from "@/services/controlPlane/observability/ObservabilityApiContext";

import { ConsolePreviewShell } from "../../../Console/ConsolePreviewShell";
import { withSearch } from "../../../Console/paths";
import { FIXTURE_ENVS, FIXTURE_PROJECT } from "../../__fixtures__/fixtureApi";
import { createExplorerFixtureApi, type ExplorerFixtureOptions } from "../__fixtures__/explorerFixtures";
import { MetricsExplorer } from "../MetricsExplorerPage";
import { emptyState, searchFromState } from "../model/explorerState";

const BASE = "/forge-metrics-preview";

const SCHEMES = [
  "professional-blue",
  "refined-neutral",
  "modern-teal",
  "slate",
  "purple-classic",
  "pure-black",
] as const;

const CASES: { id: string; label: string; options: ExplorerFixtureOptions & { loading?: boolean } }[] = [
  { id: "data", label: "the fixture app", options: {} },
  { id: "cap", label: "series cap and warnings", options: { seriesTotal: 1204, warnings: ["mixed_bounds", "step_raised"] } },
  { id: "read-only", label: "member, not admin", options: { dashboardsReadOnly: true } },
  { id: "conflict", label: "dashboard saved meanwhile (twice)", options: { saveConflicts: 2 } },
  { id: "not-set-up", label: "observability not set up", options: { failWith: Code.FailedPrecondition } },
  { id: "no-telemetry", label: "nothing sent in the window", options: { empty: true } },
  { id: "nothing-yet", label: "set up, nothing arrived this week (getting started)", options: { empty: true, signals: { services: [], truncated: false } } },
  { id: "loading", label: "loading", options: { loading: true } },
];

/** The page a reviewer lands on: a latency histogram at p95 by status, its detail open. */
const OPENING = searchFromState({
  ...emptyState(),
  panel: {
    id: "explore",
    title: "Explore",
    kind: "line",
    signal: "metrics",
    series: [{ label: "A", agg: "quantile", quantile: 0.95, field: "checkout.latency", metricType: "histogram" }],
    groupBy: ["metric.attr.status"],
  },
  detailMetric: "checkout.latency",
});

const CONTROL = "rounded-md border border-border bg-surface px-2 py-1 text-sm text-ink";

/** An API whose every call never answers: the loading state, held. */
function pending(api: ObservabilityApi): ObservabilityApi {
  return Object.fromEntries(Object.keys(api).map((key) => [key, () => new Promise(() => undefined)])) as unknown as ObservabilityApi;
}

export default function MetricsExplorerPreview() {
  const search = useSearch({ strict: false }) as Record<string, unknown>;
  const navigate = useNavigate();
  const env = typeof search.env === "string" ? search.env : "prod";
  const caseId = typeof search.case === "string" ? search.case : "data";
  const active = CASES.find((c) => c.id === caseId) ?? CASES[0]!;
  // The router parses `dark=1` as JSON, so it arrives as a number.
  const dark = !!search.dark && String(search.dark) !== "0" && String(search.dark) !== "false";
  const scheme = typeof search.scheme === "string" ? search.scheme : "professional-blue";

  useEffect(() => {
    document.documentElement.classList.toggle("dark", dark);
    document.documentElement.setAttribute("data-color-scheme", scheme);
  }, [dark, scheme]);

  // A fresh client and fixture per case, so one case's answers (and saves) never leak into the next.
  // eslint-disable-next-line react-hooks/exhaustive-deps
  const queryClient = useMemo(() => new QueryClient({ defaultOptions: { queries: { retry: false, refetchOnWindowFocus: false } } }), [caseId]);
  const api = useMemo(() => {
    const fixture = createExplorerFixtureApi({ ...active.options });
    return active.options.loading ? pending(fixture) : fixture;
  }, [active]);

  const go = (next: Record<string, unknown>) =>
    void navigate({ to: BASE, search: (prev: Record<string, unknown>) => ({ ...prev, ...next }), replace: true });
  const opening = !search.q && !search.blank && !search.m;
  const explorerSearch = opening ? { ...search, ...OPENING } : search;

  return (
    <ConsolePreviewShell
      page={{ kind: "obs-view", view: "metrics" }}
      project={FIXTURE_PROJECT}
      envs={FIXTURE_ENVS.map((e) => ({ name: e.name, preview: false }))}
      env={env}
      onEnvChange={(next) => go({ env: next })}
      controls={
        <>
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
          <a href={withSearch(BASE, { case: search.case, dark: search.dark, scheme: search.scheme, env: search.env, blank: "1" })} className={CONTROL}>
            Start blank
          </a>
        </>
      }
    >
      <div className="space-y-6" data-testid="console-metrics">
        <h1 className="text-xl font-semibold tracking-tight text-ink">Metrics</h1>
        <QueryClientProvider client={queryClient}>
          <ObservabilityApiProvider api={api}>
            <MetricsExplorer
              key={caseId}
              project={FIXTURE_PROJECT}
              env={env}
              environments={FIXTURE_ENVS}
              search={explorerSearch}
              onSearch={go}
              hrefs={{
                trace: (traceId) => withSearch("/forge-observability-preview", { view: "traces", trace: traceId }),
                dashboard: (ref) => withSearch("/forge-dashboards-preview", { page: "dashboard", dash: `${ref.kind}/${ref.key}` }),
              }}
            />
          </ObservabilityApiProvider>
        </QueryClientProvider>
      </div>
    </ConsolePreviewShell>
  );
}
