// Copyright (c) 2025 Reliant Labs

/**
 * The metrics explorer (DESIGN §3.6): any metric, filtered, grouped, combined
 * and charted — and then saved, alerted on, linked or run from a terminal.
 *
 *   /forge/p/:project/observability/metrics
 *
 *   scope     the console's environment (top bar) and the shared scope bar's
 *             attribute filters; the range; Compare (previous period or the
 *             window before the current release); Refresh.
 *   catalog   every metric the scope sent in the last day, grouped and badged.
 *             Picking one sets query A with its type-correct default (a counter
 *             → rate, a histogram → p95) and opens its detail.
 *   detail    the metric's kind, unit, interval and attributes with their
 *             cardinality; group by or break down from here.
 *   query     the builder (one dashspec series per row, a formula, group-by,
 *             chart kind) over the chart, the series table and the actions:
 *             Save to dashboard, Alert from this, Copy CLI, Copy link. The SQL
 *             tab shows what the query compiles to and opens it as SQL.
 *
 * The query IS a forge.dashboards/v1 panel (DESIGN §3.1); everything else is
 * view state. Both live in the URL (model/explorerState): a link reopens the
 * same page. The component knows nothing about routing — the console page
 * below (and the preview harness, and the tests) hand it the scope, the URL's
 * search and the hrefs.
 *
 * Local environments are not charted here (owner decision O1): their numbers
 * are `forge obs metrics query` and HyperDX, the same evaluator.
 */

import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { useNavigate, useSearch } from "@tanstack/react-router";
import { useQueryClient } from "@tanstack/react-query";
import { Bell, Check, Link2, RefreshCw, Terminal } from "lucide-react";

import Card from "@/components/forge-ui/card";
import SkeletonLoader from "@/components/forge-ui/skeleton_loader";
import Button from "@/components/forge-ui/button";
import { useDashboardIndex } from "@/hooks/dashboard-queries";
import { obsKeys, useMetric, useMetricCatalog, useMetricExemplars, useObservabilityOrg, useReleases } from "@/hooks/observability-queries";
import { useDebounce } from "@/hooks/useDebounce";
import { cn } from "@/lib/utils";
import type { DashboardRef } from "@/services/controlPlane/observability/dashboardTypes";
import { obsErrorKind } from "@/services/controlPlane/observability/errors";
import { keepsLastResult } from "@/services/controlPlane/observability/quota";
import type { MetricCatalogEntry, MetricExemplar, MetricKind } from "@/services/controlPlane/observability/metricsTypes";
import type { ObsScope, Panel } from "@/services/controlPlane/observability/types";
import { DEFAULT_RANGE, RANGE_LABELS, RANGE_PRESETS, type RangePreset } from "@/services/controlPlane/observability/window";

import { useForgeConsole } from "../../Console/consoleContext";
import { ENV_ALL } from "../../Console/envScope";
import { CloudNotice } from "../../SourceNotices";
import { useUnmountSafeTimeout } from "@/hooks/useUnmountSafeTimeout";
import { ObsChartMarkersProvider } from "../charts/UPlotChart";
import { AlertFromPanelDialog } from "../Dashboards/AlertFromPanelDialog";
import { compareTarget, rangeParam, COMPARE_LABELS, type CompareMode } from "../Dashboards/dashboardModel";
import { BUTTON_SMALL, Segmented } from "../Dashboards/parts";
import { deliveryScopeOf } from "../deliveryScope";
import { filterParam, filterTokensOf, scopeFiltersOf, type FilterToken } from "../obsFilters";
import type { ObsEnvironment } from "../obsPlace";
import { ObsScopeBar } from "../ObsScopeBar";
import { ObsAsOf, ObsError, ObsLoadingRows, ObsNoData, ObsNoEnvironments, ObsNothingYet, ObsNotSetUp } from "../ObsStates";
import { useNothingArrived } from "../serviceSignals";
import { useReleaseMarkers } from "../releaseMarkers";
import { Breakdown } from "./Breakdown";
import { Catalog } from "./Catalog";
import { ExemplarPopover } from "./ExemplarPopover";
import { displaySeries, ExplorerChart } from "./ExplorerChart";
import { useDebouncedPanel, useExplorerPanelQuery } from "./explorerQueries";
import { MetricDetail } from "./MetricDetail";
import { canonicalPanel, cliCommand, defaultSeriesFor, linkProblem, searchFromState, sqlPanelOf, stateFromSearch, type ChartKind, type ExplorerState } from "./model/explorerState";
import { seriesLabel } from "./model/metricSemantics";
import { formulaProblem, quantileLabel, QueryBuilder } from "./QueryBuilder";
import { SaveToDashboard } from "./SaveToDashboardDialog";
import { SeriesTable } from "./SeriesTable";
import { chartUnitOf } from "./seriesView";
import { SqlTab } from "./SqlTab";

// ── The console page ─────────────────────────────────────────────────────────

/** The explorer as a console page: the project, scope and roster from the console, the state from the URL. */
export function MetricsExplorerPage() {
  const { forgeProject, env, roster, href } = useForgeConsole();
  const navigate = useNavigate();
  const search = useSearch({ strict: false }) as Record<string, unknown>;
  const onSearch = useCallback(
    (next: Record<string, unknown>) =>
      void navigate({
        to: ".",
        search: (prev: Record<string, unknown>) => ({ ...prev, ...next }),
        replace: true,
      }),
    [navigate]
  );
  const environments = useMemo<ObsEnvironment[]>(
    () => roster.envs.flatMap((candidate) => (candidate.live ? [{ id: candidate.live.id, name: candidate.name, declared: candidate.live.declaredShape }] : [])),
    [roster.envs]
  );
  const hrefs = useMemo<MetricsExplorerHrefs>(
    () => ({
      trace: (traceId) => href({ kind: "obs-view", view: "traces" }, { trace: traceId, range: "7d" }),
      dashboard: (ref) => (ref.kind === "template" ? href({ kind: "obs-view", view: "dashboards" }) : href({ kind: "dashboard", dashKind: ref.kind, key: ref.key })),
    }),
    [href]
  );

  if (roster.isLoading || roster.resolvingName) {
    return (
      <Card padding="none" aria-label="Loading" aria-busy="true" data-testid="metrics-explorer-loading">
        <SkeletonLoader variant="table-row" count={4} />
      </Card>
    );
  }
  const scoped = env === ENV_ALL ? null : roster.envs.find((candidate) => candidate.name === env);
  return (
    <div className="space-y-4">
      <CloudNotice availability={roster.live.data?.availability} detail={roster.live.data?.detail} />
      {scoped?.lifecycle === "local" ? (
        <LocalEnvNote env={env} />
      ) : environments.length === 0 ? (
        <div data-testid="metrics-no-envs">
          <ObsNoEnvironments topic="metrics" />
        </div>
      ) : (
        <MetricsExplorer project={forgeProject} env={env} environments={environments} search={search} onSearch={onSearch} hrefs={hrefs} />
      )}
    </div>
  );
}

/** Owner decision O1: a local environment is explored from the terminal and HyperDX, which run the same evaluator. */
function LocalEnvNote({ env }: { env: string }) {
  return (
    <div className="rounded-lg border border-dashed border-border bg-surface px-4 py-5 text-sm text-ink-muted" data-testid="metrics-local-env">
      <p>
        <span className="font-medium text-ink">{env}</span> runs locally from your working tree, so its metrics stay on your machine. Explore them with{" "}
        <span className="font-mono text-ink">forge obs metrics query {env} &lt;metric&gt;</span>, or in HyperDX (<span className="font-mono text-ink">forge obs open {env}</span>):
        the same evaluator, so the same numbers.
      </p>
    </div>
  );
}

// ── The explorer ─────────────────────────────────────────────────────────────

export interface MetricsExplorerHrefs {
  trace: (traceId: string) => string;
  dashboard: (ref: DashboardRef) => string;
}

export interface MetricsExplorerProps {
  project: string;
  /** The console scope: an environment name or "all". */
  env: string;
  /** The project's recorded environments (id ↔ name). */
  environments: readonly ObsEnvironment[];
  /** The URL's search, as the router parsed it. */
  search: Record<string, unknown>;
  /** Merge these keys into the URL (undefined removes one), without a history entry. */
  onSearch: (next: Record<string, unknown>) => void;
  hrefs: MetricsExplorerHrefs;
}

/** The URL keys the explorer's own state is spelled in (model/explorerState). */
const STATE_KEYS = ["q", "m", "bd", "hide", "chart", "compare", "tab", "sql"] as const;

/** The router parses `?x=1` into a number: every value back to the string the model reads. */
function stringsOf(search: Record<string, unknown>): Record<string, string | undefined> {
  const out: Record<string, string | undefined> = {};
  for (const [key, value] of Object.entries(search)) {
    if (value === undefined || value === null || typeof value === "object") continue;
    out[key] = String(value);
  }
  return out;
}

function stateKeyOf(search: Record<string, string | undefined>): string {
  return JSON.stringify(STATE_KEYS.map((key) => search[key] ?? null));
}

const KIND_OF_CHART: Record<ChartKind, string> = { line: "line", area: "line", bar: "bar", number: "number", table: "table" };

const AGG_WORDS: Record<string, string> = { rate: "rate of", increase: "increase of", sum: "sum of", avg: "avg", min: "min", max: "max", last: "last", count: "count of" };

/** A title for the query as a reader would say it: "p95 rpc.server.duration", or the formula. */
export function titleOf(panel: Panel): string {
  if (panel.formula?.trim()) return panel.formula.trim();
  const parts = panel.series.filter((s) => s.field).map((s) => `${s.agg === "quantile" ? quantileLabel(s.quantile ?? 0.95) : (AGG_WORDS[s.agg] ?? s.agg)} ${s.field}`);
  const base = parts.join(", ") || "Metrics";
  return panel.groupBy?.length ? `${base} by ${panel.groupBy.map((g) => g.replace(/^(metric|resource)\.attr\./, "")).join(", ")}` : base;
}

/** Whether every series names a metric and an aggregation, and the formula parses: a query worth sending. */
function runnable(panel: Panel): boolean {
  return panel.series.length > 0 && panel.series.every((s) => !!s.field && !!s.agg) && formulaProblem(panel) === null;
}

const COMPARE_OPTIONS: { value: CompareMode; label: string }[] = (["off", "period", "release"] as const).map((value) => ({ value, label: COMPARE_LABELS[value] }));

export function MetricsExplorer({ project, env, environments, search, onSearch, hrefs }: MetricsExplorerProps) {
  const queryClient = useQueryClient();
  const org = useObservabilityOrg();
  const orgId = org.orgId;
  const strings = useMemo(() => stringsOf(search), [search]);
  const range: RangePreset = rangeParam(search.range) ?? DEFAULT_RANGE;
  const filterTokens = useMemo(() => filterTokensOf(Array.isArray(search.f) ? search.f.map(String) : typeof search.f === "string" ? [search.f] : undefined), [search.f]);

  // ── State: the URL's, mirrored locally so a query too large for a link still works ──
  const incoming = stateKeyOf(strings);
  const [state, setLocal] = useState<ExplorerState>(() => stateFromSearch(strings));
  const written = useRef(incoming);
  useEffect(() => {
    if (incoming === written.current) return;
    written.current = incoming;
    setLocal(stateFromSearch(strings));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [incoming]);
  const setState = useCallback(
    (next: ExplorerState) => {
      setLocal(next);
      const keys = searchFromState(next);
      written.current = stateKeyOf(keys);
      onSearch(keys);
    },
    [onSearch]
  );
  const update = (patch: Partial<ExplorerState>) => setState({ ...state, ...patch });

  // ── Scope ──
  const envId = env === ENV_ALL ? null : (environments.find((candidate) => candidate.name === env)?.id ?? null);
  const unrecorded = env !== ENV_ALL && !envId;
  const scopeFilters = useMemo(() => scopeFiltersOf(filterTokens), [filterTokens]);
  const scope = useMemo<ObsScope | null>(
    () => (orgId && !unrecorded ? { orgId, projectId: project, envIds: envId ? [envId] : [], filters: scopeFilters } : null),
    [orgId, project, envId, unrecorded, scopeFilters]
  );
  const subject = env === ENV_ALL ? project : env;

  // ── Catalog and detail ──
  const [catalogQuery, setCatalogQuery] = useState("");
  const needle = useDebounce(catalogQuery.trim(), 250);
  const catalog = useMetricCatalog(scope, range);
  const searched = useMetricCatalog(needle && catalog.data?.truncated ? scope : null, range, needle);
  // Asked only of an empty catalog: an empty window, or an app that has sent nothing yet?
  const nothingYet = useNothingArrived(catalog.data && catalog.data.metrics.length === 0 ? scope : null);
  const metrics = useMemo(() => {
    const all = [...(catalog.data?.metrics ?? [])];
    for (const m of searched.data?.metrics ?? []) if (!all.some((e) => e.name === m.name && e.kind === m.kind)) all.push(m);
    return all;
  }, [catalog.data, searched.data]);
  const entryOf = (name: string | undefined) => (name ? metrics.find((m) => m.name === name) : undefined);
  const detailName = state.detailMetric ?? state.panel.series[0]?.field;
  const detailEntry = entryOf(detailName);
  const detail = useMetric(scope, range, detailName, detailEntry?.kind);
  const attributes = detail.data?.attributes ?? [];

  // ── The query ──
  const kind = KIND_OF_CHART[state.chart];
  const queryPanel = useMemo(() => canonicalPanel({ ...state.panel, kind }), [state.panel, kind]);
  const title = titleOf(queryPanel);
  const debounced = useDebouncedPanel(runnable(queryPanel) ? queryPanel : null);
  const sqlTab = state.tab === "sql";
  const main = useExplorerPanelQuery(scope, range, debounced, { enabled: !sqlTab });

  // ── Comparison and markers ──
  const releases = useReleaseMarkers(scope, range, environments, true);
  const delivery = useMemo(() => (scope ? deliveryScopeOf(scope, environments) : null), [scope, environments]);
  const promotions = useReleases(delivery, "days:30", () => ({ startMs: Date.now() - 30 * 86_400_000, endMs: Date.now() }), { enabled: !!envId });
  const now = Date.now();
  const target = compareTarget(state.compare, range, promotions.data?.promotions ?? [], envId, now);
  const releaseTarget = compareTarget("release", range, promotions.data?.promotions ?? [], envId, now);
  const comparing = target.mode === "period" || target.mode === "release";
  const compareQuery = useExplorerPanelQuery(scope, range, debounced, {
    enabled: comparing && !sqlTab,
    offsetKey: target.mode === "period" ? "period" : target.mode === "release" ? `release:${target.promotion.promotionId}` : "",
    offsetMs: () => (comparing ? target.offsetMs : 0),
  });

  // ── Exemplars (a histogram's first query, on a line chart) ──
  const histogram = debounced?.kind === "line" ? debounced.series.find((s) => s.metricType === "histogram" || s.metricType === "exp_histogram") : undefined;
  const histogramValues = main.data?.series.filter((s) => s.label === histogram?.label).flatMap((s) => s.values.filter((v): v is number => v !== null)) ?? [];
  // A latency histogram's exemplars at or above the drawn percentile are the slow traces the tail sampler keeps.
  const minValue = histogram?.agg === "quantile" && histogramValues.length ? histogramValues.reduce((a, b) => a + b, 0) / histogramValues.length : undefined;
  const exemplarRequest = histogram?.field
    ? { name: histogram.field, kind: histogram.metricType as MetricKind, where: [...(debounced?.where ?? []), ...(histogram.where ?? [])], minValue, limit: 20 }
    : null;
  const rail = useMetricExemplars(scope, range, exemplarRequest, { enabled: !!main.data && !main.isPlaceholderData && !sqlTab });
  const [exemplarAt, setExemplarAt] = useState<{ exemplar: MetricExemplar; at: { x: number; y: number }; bucketStartMs: number } | null>(null);
  const bucketRequest = exemplarAt && exemplarRequest ? { ...exemplarRequest, minValue: undefined, range: { startMs: exemplarAt.bucketStartMs, endMs: exemplarAt.bucketStartMs + Math.max(main.data?.stepSeconds ?? 60, 1) * 1000 } } : null;
  const bucket = useMetricExemplars(scope, range, bucketRequest, { enabled: !!exemplarAt });
  const closeExemplars = useCallback(() => setExemplarAt(null), []);

  // ── Save, alert, copy ──
  const index = useDashboardIndex(orgId, project, envId ?? undefined);
  const [alertOpen, setAlertOpen] = useState(false);
  const [copied, setCopied] = useState<"cli" | "link" | null>(null);
  const later = useUnmountSafeTimeout();
  const copy = (what: "cli" | "link", text: string) =>
    void navigator.clipboard?.writeText(text).then(() => {
      setCopied(what);
      later(() => setCopied(null), 1500);
    });
  const savePanel = useMemo(() => canonicalPanel({ ...queryPanel, id: "explore", title }), [queryPanel, title]);
  const tooLarge = linkProblem(state);

  // ── Picking ──
  const pick = (entry: MetricCatalogEntry) => {
    const series = [...state.panel.series];
    series[0] = defaultSeriesFor(entry, seriesLabel(0));
    setState({ ...state, panel: { ...state.panel, series }, detailMetric: entry.name, hidden: [] });
  };
  const onPanel = (panel: Panel) => setState({ ...state, panel, hidden: [] });
  const addGroupBy = (field: string) => {
    const groupBy = state.panel.groupBy ?? [];
    if (groupBy.includes(field) || groupBy.length >= 3) return;
    onPanel({ ...state.panel, groupBy: [...groupBy, field] });
  };
  const refresh = () => void queryClient.invalidateQueries({ queryKey: obsKeys.all(scope) });

  // ── Page-level states ──
  if (org.isLoading) return <ObsLoadingRows testId="metrics-org-loading" />;
  if (!orgId) {
    return org.error ? (
      <ObsError error={org.error} subject={subject} onRetry={() => void org.refetch()} />
    ) : (
      <ObsNotSetUp subject={subject} topic="metrics" env={env === ENV_ALL ? undefined : env} />
    );
  }
  if (unrecorded) {
    return (
      <p className="rounded-lg border border-dashed border-border bg-surface px-4 py-5 text-sm text-ink-muted" data-testid="metrics-unrecorded-env">
        Reliant has no record of <span className="font-medium text-ink">{env}</span>, so no metrics are filed under it. Pick another environment.
      </p>
    );
  }
  const catalogError = keepsLastResult(catalog.error, catalog.data) ? null : catalog.error;
  if (catalogError && obsErrorKind(catalogError) === "not-set-up") return <ObsNotSetUp subject={subject} topic="metrics" env={env === ENV_ALL ? undefined : env} />;

  const toolbar = (
    <div className="flex flex-wrap items-center justify-between gap-2" data-testid="metrics-toolbar">
      {main.data && <ObsAsOf updatedAt={main.dataUpdatedAt} />}
      {scope && (
        <ObsScopeBar
          scope={scope}
          range={range}
          selectedEnvs={[]}
          onEnvsChange={() => undefined}
          filters={filterTokens}
          onFiltersChange={(tokens: FilterToken[]) => onSearch({ f: filterParam(tokens) })}
          groupBy={[]}
          onGroupByChange={() => undefined}
          groupable={false}
        />
      )}
      <div className="flex flex-wrap items-center gap-2">
        <Segmented
          label="Compare"
          value={state.compare}
          options={COMPARE_OPTIONS}
          onChange={(compare) => update({ compare })}
          disabledReason={releaseTarget.mode === "unavailable" ? { release: releaseTarget.reason } : undefined}
          testId="metrics-compare"
        />
        <label htmlFor="metrics-range" className="sr-only">
          Time range
        </label>
        <select
          id="metrics-range"
          value={range}
          onChange={(event) => onSearch({ range: event.target.value === DEFAULT_RANGE ? undefined : event.target.value })}
          className="h-8 rounded-md border border-border bg-surface-sunken px-2 text-sm text-ink focus:outline-none focus-visible:ring-2 focus-visible:ring-accent"
          data-testid="metrics-range"
        >
          {RANGE_PRESETS.map((preset) => (
            <option key={preset} value={preset}>
              {RANGE_LABELS[preset]}
            </option>
          ))}
        </select>
        <Button variant="ghost" size="sm" onClick={refresh} leftIcon={<RefreshCw />} data-testid="metrics-refresh">
          Refresh
        </Button>
      </div>
    </div>
  );

  if (catalogError) {
    return (
      <div className="space-y-4" data-testid="metrics-explorer">
        {toolbar}
        <ObsError error={catalogError} subject={subject} onRetry={() => void catalog.refetch()} retrying={catalog.isFetching} />
      </div>
    );
  }
  if (catalog.data && catalog.data.metrics.length === 0 && state.panel.series.length === 0) {
    return (
      <div className="space-y-4" data-testid="metrics-explorer">
        {toolbar}
        <div data-testid="metrics-catalog-empty">
          {nothingYet ? (
            <ObsNothingYet subject={subject} topic="metrics" env={env === ENV_ALL ? undefined : env} />
          ) : (
            <ObsNoData what="metrics" range={range} filtered={filterTokens.length > 0} onWiden={() => onSearch({ range: "24h" })} onClearFilters={() => onSearch({ f: undefined })} />
          )}
        </div>
      </div>
    );
  }

  const unit = chartUnitOf(main.data?.seriesMeta ?? []);
  const lines = main.data && queryPanel.kind === "line" ? displaySeries(queryPanel, main.data) : null;
  const cli = env === ENV_ALL ? null : cliCommand(env, state, range);
  const compare = comparing ? { query: compareQuery, label: target.label, offsetSec: Math.round(target.offsetMs / 1000) } : undefined;

  return (
    <div className="space-y-4" data-testid="metrics-explorer">
      {toolbar}
      {target.mode === "unavailable" && (
        <p className="text-xs text-ink-muted" data-testid="metrics-compare-unavailable">
          {target.reason}
        </p>
      )}
      <ObsChartMarkersProvider markers={releases.markers}>
        <div className="grid grid-cols-1 gap-4 lg:grid-cols-[20rem_minmax(0,1fr)]">
          <aside className="min-w-0 space-y-4" aria-label="Metrics catalog">
            <Card padding="sm">
              {catalog.isLoading ? (
                <ObsLoadingRows rows={8} testId="metrics-catalog-loading" />
              ) : (
                <Catalog
                  metrics={metrics}
                  selected={detailName}
                  onPick={pick}
                  query={catalogQuery}
                  onQuery={setCatalogQuery}
                  truncated={catalog.data?.truncated && !needle}
                  isFetching={searched.isFetching}
                />
              )}
            </Card>
            {detailName && (
              <Card padding="sm">
                <MetricDetail
                  name={detailName}
                  entry={detailEntry}
                  detail={detail.data}
                  isLoading={detail.isLoading}
                  error={detail.error}
                  onRetry={() => void detail.refetch()}
                  range={range}
                  onWiden={() => onSearch({ range: "7d" })}
                  groupBy={state.panel.groupBy ?? []}
                  onGroupBy={addGroupBy}
                  breakdownKey={state.breakdownKey}
                  onBreakdown={(field) => update({ breakdownKey: state.breakdownKey === field ? undefined : field })}
                  subject={subject}
                />
              </Card>
            )}
          </aside>

          <div className="min-w-0 space-y-4">
            <Card padding="sm" className="space-y-4">
              <Tabs
                tab={sqlTab ? "sql" : "chart"}
                onTab={(tab) => update({ tab: tab === "sql" ? "sql" : undefined, sqlPanel: tab === "sql" ? state.sqlPanel : undefined })}
              />
              {sqlTab ? (
                <div role="tabpanel" id="metrics-tab-sql" aria-labelledby="metrics-tab-sql-label">
                  <SqlTab
                    panel={debounced}
                    sqlPanel={state.sqlPanel}
                    scope={scope}
                    range={range}
                    onOpenSql={(sql) => update({ tab: "sql", sqlPanel: sqlPanelOf(sql, title) })}
                    onBack={() => update({ tab: undefined, sqlPanel: undefined })}
                    subject={subject}
                  />
                </div>
              ) : (
                <div role="tabpanel" id="metrics-tab-chart" aria-labelledby="metrics-tab-chart-label" className="space-y-4">
                  <QueryBuilder
                    panel={state.panel}
                    chart={state.chart}
                    catalog={metrics}
                    attributes={attributes}
                    scope={scope}
                    range={range}
                    onPanel={onPanel}
                    onChart={(chart) => update({ chart })}
                  />
                  <div className="border-t border-border/70 pt-4">
                    {!runnable(queryPanel) ? (
                      <p className="py-10 text-center text-sm text-ink-muted" data-testid="metrics-pick-prompt">
                        {queryPanel.series.length === 0
                          ? "Pick a metric from the catalog to chart it."
                          : formulaProblem(queryPanel)
                            ? "Fix the formula to run the query."
                            : "Pick a metric for every query to run it."}
                      </p>
                    ) : (
                      <div className={cn("transition-opacity", main.isPlaceholderData && "opacity-60")} aria-busy={main.isFetching || undefined}>
                        <ExplorerChart
                          panel={{ ...queryPanel, title }}
                          query={{ data: main.data, isLoading: main.isLoading || (!main.data && !main.error), error: main.error }}
                          compare={compare}
                          unit={unit}
                          hidden={state.hidden}
                          attributes={attributes}
                          chart={state.chart}
                          exemplars={
                            lines && rail.data?.length
                              ? {
                                  items: rail.data,
                                  stepSeconds: main.data?.stepSeconds ?? 60,
                                  onOpen: (exemplar, at) => {
                                    const step = Math.max(main.data?.stepSeconds ?? 60, 1);
                                    const first = lines.timestamps[0] ?? 0;
                                    const startSec = first + Math.floor((exemplar.timeMs / 1000 - first) / step) * step;
                                    setExemplarAt({ exemplar, at, bucketStartMs: startSec * 1000 });
                                  },
                                }
                              : undefined
                          }
                        />
                      </div>
                    )}
                  </div>
                  {lines && lines.series.length > 0 && !main.error && (
                    <SeriesTable series={lines.series} unit={unit} hidden={state.hidden} onHidden={(hidden) => update({ hidden })} />
                  )}
                </div>
              )}
              <div className="flex flex-wrap items-center gap-2 border-t border-border/70 pt-3" data-testid="metrics-actions">
                <SaveToDashboard
                  orgId={orgId}
                  project={project}
                  env={env}
                  environments={environments}
                  panel={savePanel}
                  canWrite={!!index.data?.canWrite}
                  dashboards={index.data?.dashboards ?? []}
                  dashboardHref={hrefs.dashboard}
                  disabled={!runnable(queryPanel)}
                  disabledReason="Pick a metric first."
                />
                <ActionButton
                  icon={<Bell className="h-3 w-3" aria-hidden="true" />}
                  onClick={() => setAlertOpen(true)}
                  disabled={!runnable(queryPanel)}
                  title={!runnable(queryPanel) ? "Pick a metric first." : "Declare an alert rule from this query, as KCL"}
                  testId="metrics-alert"
                >
                  Alert from this
                </ActionButton>
                <ActionButton
                  icon={copied === "cli" ? <Check className="h-3 w-3" aria-hidden="true" /> : <Terminal className="h-3 w-3" aria-hidden="true" />}
                  onClick={() => cli && copy("cli", cli.text)}
                  disabled={!cli || !runnable(queryPanel)}
                  title={!cli ? "Pick one environment: a CLI command reads one." : cli.kind === "panel" ? "forge obs query --panel -: the query is beyond the metrics flags" : cli.text}
                  testId="metrics-copy-cli"
                >
                  {copied === "cli" ? "Copied" : "Copy CLI"}
                </ActionButton>
                <ActionButton
                  icon={copied === "link" ? <Check className="h-3 w-3" aria-hidden="true" /> : <Link2 className="h-3 w-3" aria-hidden="true" />}
                  onClick={() => copy("link", window.location.href)}
                  disabled={!!tooLarge}
                  title={tooLarge ?? "A link that reopens this page as it is"}
                  testId="metrics-copy-link"
                >
                  {copied === "link" ? "Copied" : "Copy link"}
                </ActionButton>
                {main.isFetching && runnable(queryPanel) && <span className="text-xs text-ink-muted" aria-live="polite">Updating…</span>}
              </div>
            </Card>

            {state.breakdownKey && debounced && !sqlTab && (
              <Card padding="sm">
                <Breakdown
                  panel={debounced}
                  field={state.breakdownKey}
                  scope={scope}
                  range={range}
                  unit={unit}
                  onClose={() => update({ breakdownKey: undefined })}
                  onFilter={(field, value) =>
                    setState({ ...state, breakdownKey: undefined, panel: { ...state.panel, where: [...(state.panel.where ?? []), { field, op: "eq", value }] } })
                  }
                  subject={subject}
                />
              </Card>
            )}
          </div>
        </div>
      </ObsChartMarkersProvider>

      {exemplarAt && (
        <ExemplarPopover
          at={exemplarAt.at}
          bucketStartMs={exemplarAt.bucketStartMs}
          exemplars={bucket.data}
          isLoading={bucket.isLoading}
          error={bucket.error}
          onRetry={() => void bucket.refetch()}
          unit={unit}
          traceHref={hrefs.trace}
          onClose={closeExemplars}
          subject={subject}
        />
      )}
      {alertOpen && <AlertFromPanelDialog panel={savePanel} environments={environments} env={env} services={[]} onClose={() => setAlertOpen(false)} />}
    </div>
  );
}

function Tabs({ tab, onTab }: { tab: "chart" | "sql"; onTab: (tab: "chart" | "sql") => void }) {
  const tabs = [
    { id: "chart" as const, label: "Query" },
    { id: "sql" as const, label: "SQL" },
  ];
  return (
    <div
      role="tablist"
      aria-label="Query or SQL"
      className="inline-flex rounded-md border border-border bg-surface-sunken p-0.5"
      onKeyDown={(event) => {
        if (event.key !== "ArrowRight" && event.key !== "ArrowLeft") return;
        event.preventDefault();
        const next = tab === "chart" ? "sql" : "chart";
        onTab(next);
        (event.currentTarget.querySelector(`#metrics-tab-${next}-label`) as HTMLElement | null)?.focus();
      }}
    >
      {tabs.map((t) => (
        <button
          key={t.id}
          type="button"
          role="tab"
          id={`metrics-tab-${t.id}-label`}
          aria-selected={tab === t.id}
          aria-controls={`metrics-tab-${t.id}`}
          tabIndex={tab === t.id ? 0 : -1}
          onClick={() => onTab(t.id)}
          className={cn(
            "h-7 rounded px-3 text-xs transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent",
            tab === t.id ? "bg-surface font-medium text-ink shadow-sm" : "text-ink-muted hover:text-ink"
          )}
          data-testid={`metrics-tab-${t.id}`}
        >
          {t.label}
        </button>
      ))}
    </div>
  );
}

function ActionButton({ icon, children, onClick, disabled, title, testId }: { icon: ReactNode; children: ReactNode; onClick: () => void; disabled?: boolean; title?: string; testId: string }) {
  return (
    <button type="button" className={BUTTON_SMALL} onClick={onClick} disabled={disabled} title={title} data-testid={testId}>
      {icon}
      {children}
    </button>
  );
}
