// Copyright (c) 2026 Reliant Labs

/**
 * SITE INSIGHTS › SPEED (U6e, owner: "Vercel's speed insights is nicer than
 * ours"; it was the Speed insights page): /forge/p/:app/site-insights, the
 * page's default view (SiteInsightsPage.tsx) — how fast each page loads and
 * responds for real visitors, laid out after Vercel's:
 *
 *   Real Experience Score   one 0–100 number, coloured, and its chart over
 *                           the window (or one vital's, picked by its card)
 *   a card per vital        LCP, INP, CLS, FCP, TTFB at the chosen
 *                           percentile, rated, with the good / needs
 *                           improvement / poor split of every sample
 *   routes                  each route's score and vitals, worst first
 *
 * Every choice is the URL's: `?device=desktop|mobile` (default desktop),
 * `?pct=p75|p90|p95|p99` (default p75), `?metric=score|lcp|…` (the chart),
 * `?range=`, and the site, `?site=` (siteInsightsParts.tsx; every site when
 * absent). The data and the score's formula: speedInsightsModel.ts.
 *
 * With no samples on either device: what the page will show and how to turn
 * it on (the runtime's telemetry, which a forge frontend scaffolds).
 */

import { useMemo, type ReactNode } from "react";
import { useNavigate, useSearch } from "@tanstack/react-router";
import { Gauge } from "lucide-react";

import Button from "@/components/forge-ui/button";
import Card from "@/components/forge-ui/card";
import Status from "@/components/forge-ui/status";
import { ProjectEmptyCta } from "@/components/EmptyState/ProjectEmptyCta";
import { DeployWithAgentButton } from "@/components/Forge/Observability/AskReliant/AskReliant";
import { usePanelQuery } from "@/hooks/observability-queries";
import { cn } from "@/lib/utils";
import { RANGE_LABELS, RANGE_PRESETS, type RangePreset } from "@/services/controlPlane/observability/window";

import { useForgeConsole } from "../Console/consoleContext";
import { Page } from "../Console/consolePageFrame";
import { ENV_ALL } from "../Console/envScope";
import { UPlotChart } from "../Observability/charts/UPlotChart";
import { Segmented, VariableSelect } from "../Observability/Dashboards/parts";
import { formatCount } from "../Observability/format";
import { ObsError } from "../Observability/ObsStates";
import {
  chartMetricOf,
  DEFAULT_DEVICE,
  DEFAULT_PERCENTILE,
  deviceOf,
  distribution,
  formatVital,
  PERCENTILES,
  percentileOf,
  RATING_LABEL,
  RATING_TONE,
  rateScore,
  rateVital,
  realExperienceScore,
  routesByScore,
  SCORE_LABEL,
  SCORE_WEIGHTS,
  samplesOf,
  speedPanel,
  VITAL_INFO,
  VITALS,
  vitalsRows,
  type ChartMetric,
  type Device,
  type Percentile,
  type Rating,
  type Vital,
  type VitalsRow,
} from "./speedInsightsModel";
import { SiteInsightsHeader, SitePicker, useSiteChoice } from "./siteInsightsParts";
import { SITE_SPEED_DEFAULT_RANGE, useSiteInsightsScope, useSiteSpeedSummary } from "./useSiteSpeedSummary";

const DEFAULT_RANGE: RangePreset = SITE_SPEED_DEFAULT_RANGE;

/** A rating's ink (text) and fill (bars, the score ring): semantic status tokens. */
const INK: Record<Rating, string> = { good: "text-success-ink", ni: "text-warning-ink", poor: "text-danger-ink" };
const FILL: Record<Rating, string> = { good: "bg-success", ni: "bg-warning", poor: "bg-danger" };
const STROKE: Record<Rating, string> = { good: "stroke-success", ni: "stroke-warning", poor: "stroke-danger" };
/** The chart's colour for the score and each vital (the dashboard palette). */
const CHART_COLOR: Record<ChartMetric, string> = { score: "--chart-1", lcp: "--chart-2", inp: "--chart-3", cls: "--chart-4", fcp: "--chart-5", ttfb: "--chart-6" };

interface SpeedSearch {
  range?: RangePreset;
  pct?: string;
  device?: string;
  metric?: string;
}

/** @deprecated Use SiteInsightsPage for the full set of site views. */
export function SpeedInsightsPage() {
  return (
    <Page testId="console-speed-insights">
      <SpeedInsightsView />
    </Page>
  );
}

/** Site insights' Speed view: its header (with the tabs) and its body; SiteInsightsPage frames it. */
export function SpeedInsightsView() {
  const { forgeProject, env, roster } = useForgeConsole();
  const { services } = useSiteChoice();
  const search = useSearch({ strict: false }) as SpeedSearch;
  const navigate = useNavigate();
  const range = search.range ?? DEFAULT_RANGE;
  const pct = percentileOf(search.pct);
  const device = deviceOf(search.device);
  const metric = chartMetricOf(search.metric);
  // One writer for every choice: the default is dropped from the URL, so the bare URL is the default view.
  const set = (patch: Partial<Record<keyof SpeedSearch, string | undefined>>) =>
    void navigate({ to: ".", search: (prev: Record<string, unknown>) => ({ ...prev, ...patch }), replace: true } as never);

  const envId = env === ENV_ALL ? undefined : roster.envs.find((candidate) => candidate.name === env)?.live?.id;
  const scope = useSiteInsightsScope(forgeProject, envId);

  const routesPanel = useMemo(() => speedPanel("routes", device, pct, range, forgeProject), [device, pct, range, forgeProject]);
  const seriesPanel = useMemo(() => speedPanel("series", device, pct, range, forgeProject), [device, pct, range, forgeProject]);
  // Both devices' summaries: the toggle says how much each has, and an empty
  // device is told apart from an app that measured nothing at all.
  const desktop = useSiteSpeedSummary({ project: forgeProject, envId, services, range, device: "desktop", pct }).query;
  const mobile = useSiteSpeedSummary({ project: forgeProject, envId, services, range, device: "mobile", pct }).query;
  const routes = usePanelQuery(scope, range, routesPanel, services);
  const series = usePanelQuery(scope, range, seriesPanel, services);

  const summaries: Record<Device, VitalsRow | undefined> = { desktop: vitalsRows(desktop.data)[0], mobile: vitalsRows(mobile.data)[0] };
  const viewsOf = (d: Device) => summaries[d]?.views ?? 0;
  const summary = summaries[device];
  const windowWords = RANGE_LABELS[range].toLowerCase();
  const loading = desktop.isLoading || mobile.isLoading;
  const failed = (desktop.error && !desktop.data) || (mobile.error && !mobile.data);
  const nothingAtAll = !loading && !failed && viewsOf("desktop") === 0 && viewsOf("mobile") === 0 && !hasSamples(summaries.desktop) && !hasSamples(summaries.mobile);

  return (
    <>
      <SiteInsightsHeader
        view="speed"
        controls={
          <>
            <SitePicker scope={scope} range={range} />
            <Segmented<Device>
              label="Device"
              testId="speed-insights-device"
              value={device}
              options={[
                { value: "desktop", label: viewsOf("desktop") ? `Desktop · ${formatCount(viewsOf("desktop"))}` : "Desktop" },
                { value: "mobile", label: viewsOf("mobile") ? `Mobile · ${formatCount(viewsOf("mobile"))}` : "Mobile" },
              ]}
              onChange={(next) => set({ device: next === DEFAULT_DEVICE ? undefined : next })}
            />
            <Segmented<Percentile>
              label="Percentile"
              testId="speed-insights-percentile"
              value={pct}
              options={PERCENTILES.map((p) => ({ value: p, label: p.toUpperCase() }))}
              onChange={(next) => set({ pct: next === DEFAULT_PERCENTILE ? undefined : next })}
            />
            <VariableSelect
              id="speed-insights-range"
              label="time"
              value={range}
              options={RANGE_PRESETS.map((preset) => ({ value: preset, label: RANGE_LABELS[preset] }))}
              onChange={(value) => set({ range: value === DEFAULT_RANGE ? undefined : value })}
            />
          </>
        }
      />
      {loading ? (
        <div className="h-64 animate-pulse rounded-xl border border-border bg-surface motion-reduce:animate-none" aria-label="Loading site speed" />
      ) : failed ? (
        <ObsError
          error={desktop.error ?? mobile.error}
          subject={forgeProject}
          onRetry={() => {
            void desktop.refetch();
            void mobile.refetch();
          }}
          retrying={desktop.isFetching || mobile.isFetching}
        />
      ) : nothingAtAll ? (
        <SpeedInsightsEmpty windowWords={windowWords} />
      ) : !summary || !hasSamples(summary) ? (
        <Card className="flex flex-wrap items-center justify-between gap-3" data-testid="speed-insights-device-empty">
          <p className="text-ui text-ink-muted">
            No {device} visit was measured in the {windowWords}. {device === "desktop" ? "Mobile" : "Desktop"} visits were.
          </p>
          <Button variant="secondary" size="sm" onClick={() => set({ device: device === "desktop" ? "mobile" : undefined })}>
            Show {device === "desktop" ? "mobile" : "desktop"}
          </Button>
        </Card>
      ) : (
        <>
          <ScoreCard summary={summary} device={device} pct={pct} metric={metric} windowWords={windowWords} series={vitalsRows(series.data)} seriesLoading={series.isLoading} onMetric={(next) => set({ metric: next === "score" ? undefined : next })} />
          <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-5" data-testid="speed-insights-vitals">
            {VITALS.map((vital) => (
              <VitalCard key={vital} vital={vital} summary={summary} pct={pct} selected={metric === vital} onSelect={() => set({ metric: metric === vital ? undefined : vital })} />
            ))}
          </div>
          <RoutesTable rows={vitalsRows(routes.data)} pct={pct} loading={routes.isLoading} failed={!!routes.error && !routes.data} />
          <p className="text-meta text-ink-muted" data-testid="speed-insights-formula">
            The Real Experience Score scores each vital at {pct.toUpperCase()} on a log-normal curve (its good threshold scores 90, its poor threshold 50), then weights them: LCP{" "}
            {SCORE_WEIGHTS.lcp}%, INP {SCORE_WEIGHTS.inp}%, CLS {SCORE_WEIGHTS.cls}%, FCP {SCORE_WEIGHTS.fcp}%. TTFB is shown and not scored. Good / needs improvement / poor follow
            web.dev&apos;s thresholds; 90 and above is great.
          </p>
        </>
      )}
    </>
  );
}

function hasSamples(row: VitalsRow | undefined): boolean {
  return !!row && VITALS.some((v) => samplesOf(row.vitals[v]) > 0);
}

/** The headline: the score, its rating, and the chart of it (or of the vital `metric` names). */
function ScoreCard({
  summary,
  device,
  pct,
  metric,
  windowWords,
  series,
  seriesLoading,
  onMetric,
}: {
  summary: VitalsRow;
  device: Device;
  pct: Percentile;
  metric: ChartMetric;
  windowWords: string;
  series: VitalsRow[];
  seriesLoading: boolean;
  onMetric: (metric: ChartMetric) => void;
}) {
  const score = realExperienceScore(summary.vitals);
  const rating = rateScore(score);
  const points = series.filter((row) => row.t !== null);
  const values = points.map((row) => (metric === "score" ? realExperienceScore(row.vitals) : row.vitals[metric].value));
  const chartLabel = metric === "score" ? "Real Experience Score" : `${VITAL_INFO[metric].short} at ${pct.toUpperCase()}`;
  return (
    <Card padding="none" className="grid min-w-0 gap-0 lg:grid-cols-[16rem_minmax(0,1fr)]" data-testid="speed-insights-score">
      <div className="flex flex-col items-start gap-3 border-border px-5 py-4 max-lg:border-b lg:border-r">
        <h2 className="text-ui font-medium text-ink-muted">Real Experience Score</h2>
        <ScoreRing score={score} rating={rating} />
        <div className="space-y-1 text-meta text-ink-muted">
          {rating && <Status tone={RATING_TONE[rating]} label={SCORE_LABEL[rating]} />}
          <p>
            {formatCount(summary.views)} {device} page loads, {windowWords}
          </p>
        </div>
      </div>
      <div className="min-w-0 px-3 pb-3 pt-3">
        <div className="flex flex-wrap items-center justify-between gap-2 px-2">
          <h3 className="text-section text-ink">{chartLabel} over time</h3>
          <Segmented<ChartMetric>
            label="Chart"
            testId="speed-insights-chart-metric"
            value={metric}
            options={[{ value: "score", label: "Score" }, ...VITALS.map((v) => ({ value: v, label: VITAL_INFO[v].short }))]}
            onChange={onMetric}
          />
        </div>
        <div className="pt-2" data-testid="speed-insights-chart">
          {seriesLoading ? (
            <div className="h-[200px] animate-pulse rounded-lg bg-ink/5 motion-reduce:animate-none" aria-label="Loading the chart" />
          ) : points.length < 2 ? (
            <p className="px-2 py-16 text-center text-meta text-ink-muted">Not enough measured visits in the {windowWords} to draw a trend.</p>
          ) : (
            <UPlotChart
              height={200}
              timestamps={points.map((row) => row.t as number)}
              series={[{ label: chartLabel, values, color: CHART_COLOR[metric] }]}
              unit={metric === "score" || metric === "cls" ? "" : "ms"}
              label={`${chartLabel} over the ${windowWords}`}
              area="fill"
              legend={false}
            />
          )}
        </div>
      </div>
    </Card>
  );
}

/** The score as a coloured ring, Vercel's gauge. */
function ScoreRing({ score, rating }: { score: number | null; rating: Rating | null }) {
  const r = 42;
  const circumference = 2 * Math.PI * r;
  const filled = score === null ? 0 : (score / 100) * circumference;
  return (
    <div className="relative size-28" role="img" aria-label={score === null ? "No score" : `Real Experience Score ${score} of 100`}>
      <svg viewBox="0 0 100 100" className="size-full -rotate-90" aria-hidden="true">
        <circle cx="50" cy="50" r={r} fill="none" strokeWidth="8" className="stroke-ink/10" />
        {rating && <circle cx="50" cy="50" r={r} fill="none" strokeWidth="8" strokeLinecap="round" strokeDasharray={`${filled} ${circumference}`} className={STROKE[rating]} />}
      </svg>
      <span className={cn("absolute inset-0 flex items-center justify-center text-figure tabular-nums", rating ? INK[rating] : "text-ink-muted")} data-testid="speed-insights-score-value">
        {score ?? "—"}
      </span>
    </div>
  );
}

function VitalCard({ vital, summary, pct, selected, onSelect }: { vital: Vital; summary: VitalsRow; pct: Percentile; selected: boolean; onSelect: () => void }) {
  const stat = summary.vitals[vital];
  const rating = rateVital(vital, stat.value);
  const split = distribution(stat);
  const info = VITAL_INFO[vital];
  return (
    <button
      type="button"
      onClick={onSelect}
      aria-pressed={selected}
      title={`${info.label}: chart it`}
      className={cn(
        "min-w-0 rounded-xl border bg-surface px-4 py-3 text-left transition-colors focus:outline-none focus-visible:ring-2 focus-visible:ring-ring",
        selected ? "border-primary" : "border-border hover:border-ink/30"
      )}
      data-testid={`speed-insights-${vital}`}
    >
      <div className="flex items-baseline justify-between gap-2">
        <span className="text-ui font-medium text-ink" title={info.label}>
          {info.short}
        </span>
        <span className="text-meta text-ink-muted">{pct.toUpperCase()}</span>
      </div>
      <div className={cn("mt-0.5 text-2xl font-semibold tabular-nums", rating ? INK[rating] : "text-ink-muted")}>{formatVital(vital, stat.value)}</div>
      <div className="mt-0.5 text-meta text-ink-muted">{rating ? RATING_LABEL[rating] : "No samples"}</div>
      <DistributionBar split={split} />
      <p className="mt-2 text-meta text-ink-muted">{info.about}</p>
    </button>
  );
}

/** Good / needs improvement / poor, as one bar and three shares. */
function DistributionBar({ split }: { split: Record<Rating, number> | null }) {
  if (!split) return <div className="mt-3 h-1.5 rounded-full bg-ink/10" aria-hidden="true" />;
  const pctText = (share: number) => `${Math.round(share * 100)}%`;
  const ratings: Rating[] = ["good", "ni", "poor"];
  return (
    <div className="mt-3 space-y-1" data-testid="vital-distribution">
      <div className="flex h-1.5 gap-px overflow-hidden rounded-full bg-ink/10" aria-hidden="true">
        {ratings.map((r) => (split[r] > 0 ? <span key={r} className={FILL[r]} style={{ width: `${split[r] * 100}%` }} /> : null))}
      </div>
      <p className="flex justify-between gap-1 text-meta tabular-nums text-ink-muted" aria-label={ratings.map((r) => `${pctText(split[r])} ${RATING_LABEL[r].toLowerCase()}`).join(", ")}>
        {ratings.map((r) => (
          <span key={r} className={split[r] > 0 ? INK[r] : undefined}>
            {pctText(split[r])}
          </span>
        ))}
      </p>
    </div>
  );
}

function RoutesTable({ rows, pct, loading, failed }: { rows: VitalsRow[]; pct: Percentile; loading: boolean; failed: boolean }) {
  const sorted = useMemo(() => routesByScore(rows), [rows]);
  const TH = "h-9 px-3 text-meta font-medium text-ink-muted";
  let body: ReactNode;
  if (loading) body = <div className="h-40 animate-pulse rounded-xl border border-border bg-surface motion-reduce:animate-none" />;
  else if (failed) body = <p className="text-ui text-ink-muted">The routes could not be read right now.</p>;
  else if (sorted.length === 0) body = <p className="text-ui text-ink-muted">No route was measured on this device.</p>;
  else
    body = (
      <div className="overflow-x-auto rounded-xl border border-border bg-surface" data-testid="speed-insights-routes">
        <table className="w-full min-w-[48rem] border-collapse">
          <thead>
            <tr className="border-b border-border">
              <th className={cn(TH, "text-left")}>Route</th>
              <th className={cn(TH, "text-right")} aria-sort="ascending">
                Score
              </th>
              {VITALS.map((vital) => (
                <th key={vital} className={cn(TH, "text-right")} title={VITAL_INFO[vital].label}>
                  {VITAL_INFO[vital].short}
                </th>
              ))}
              <th className={cn(TH, "text-right")}>Page loads</th>
            </tr>
          </thead>
          <tbody>
            {sorted.map((row) => {
              const rating = rateScore(row.score);
              return (
                <tr key={row.page} className="border-b border-border-subtle last:border-b-0" data-testid="speed-insights-route">
                  <td className="h-11 max-w-0 truncate px-3 font-mono text-code text-ink">{row.page || "/"}</td>
                  <td className="h-11 px-3 text-right">
                    <span className={cn("inline-flex min-w-9 justify-center rounded-md px-1.5 py-0.5 text-ui font-semibold tabular-nums", rating ? INK[rating] : "text-ink-muted")}>{row.score ?? "—"}</span>
                  </td>
                  {VITALS.map((vital) => {
                    const r = rateVital(vital, row.vitals[vital].value);
                    return (
                      <td key={vital} className={cn("h-11 px-3 text-right text-ui tabular-nums", r && r !== "good" ? INK[r] : "text-ink")}>
                        {formatVital(vital, row.vitals[vital].value)}
                      </td>
                    );
                  })}
                  <td className="h-11 px-3 text-right text-ui tabular-nums text-ink-muted">{formatCount(row.views)}</td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
    );
  return (
    <section className="space-y-2" aria-labelledby="speed-insights-routes-title">
      <h2 id="speed-insights-routes-title" className="text-section text-ink">
        Routes <span className="ml-2 text-meta font-normal text-ink-muted">worst score first · vitals at {pct.toUpperCase()}</span>
      </h2>
      {body}
    </section>
  );
}

function SpeedInsightsEmpty({ windowWords }: { windowWords: string }) {
  return (
    <ProjectEmptyCta
      testId="speed-insights-empty"
      icon={<Gauge />}
      title={`No page has been measured in the ${windowWords}`}
      deploy={<DeployWithAgentButton env={null} />}
      learn="site-insights"
    >
      <p>
        Site insights scores how real visitors experience every page of your frontend: how fast it loads (LCP, FCP), how quickly it responds to a tap or click (INP),
        how much it shifts as it loads (CLS) and how long the server takes to answer (TTFB), on desktop and on mobile.
      </p>
      <p>
        A forge frontend measures them already: the browser runtime (<code className="font-mono text-code">@reliantlabs/forge-web-runtime</code>) sends them with its
        telemetry. Deploy a frontend, open it, and the first figures appear within a minute.
      </p>
    </ProjectEmptyCta>
  );
}
