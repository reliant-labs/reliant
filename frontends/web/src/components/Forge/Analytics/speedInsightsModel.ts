// Copyright (c) 2026 Reliant Labs

/**
 * The query and arithmetic behind Site insights' Speed view
 * (SpeedInsightsPage.tsx, U6e: after Vercel's; it was Speed insights). Pure:
 * the page renders what these return.
 *
 * SOURCE. forge's browser runtime (@reliantlabs/forge-web-runtime) records web
 * vitals as spans of the `webvitals` tracer, one attribute per vital (`lcp`,
 * `inp`, `cls`, `fcp`, `ttfb`), each stamped with the page's `location.href`.
 * The control plane's ingest proxy stamps the device class it parsed from the
 * request's User-Agent on the resource as `reliant.device.type` (desktop,
 * mobile, tablet or bot; control-plane internal/proxy/otel_useragent.go).
 * Three SQL QueryPanels read them, for one device class and one percentile:
 *
 *   summary   the app: each vital at the percentile, and how many samples
 *             were good / needed improvement / were poor
 *   routes    the same, per route (the page's path)
 *   series    the same, per time bucket, for the chart
 *
 * and a fourth lists the sites that sent any, for the site picker:
 *
 *   sites     each `service.name` with page loads, busiest first
 *
 * THE SITE. Each frontend's telemetry carries its own `service.name`
 * ("<project>-<frontend>", forge naming.FrontendServiceName). Every panel
 * keeps to the query's services (QueryScope.services, the `{services}`
 * parameter the control plane binds; none = every site), so a project with a
 * static site and an app frontend reads one at a time instead of both mixed.
 *
 * THE REAL EXPERIENCE SCORE (realExperienceScore below), 0–100:
 *
 *   metric score  = 100 × getLogNormalScore(curve, the vital at the selected
 *                   percentile), Lighthouse's log-normal curve: the vital's
 *                   good threshold scores 90 and its poor threshold 50
 *                   (VITAL_INFO: LCP 2500 / 4000 ms, INP 200 / 500 ms,
 *                   CLS 0.1 / 0.25, FCP 1800 / 3000 ms)
 *   RES           = Σ weight × metric score / Σ weight, over the scored
 *                   vitals that have samples
 *   weights       LCP 30 · INP 30 · CLS 25 · FCP 15 (TTFB is shown, and not
 *                 scored), Vercel's weighting of the Lighthouse vitals
 *   rating        90–100 great, 50–89 needs improvement, 0–49 poor
 *
 * So the score follows the percentile shown (P75 by default): P95 scores the
 * slower tail, and the score drops with it.
 *
 * Vercel publishes the weights, the bands and each vital's "90 ~ 50"
 * thresholds, and says each vital is scored on a log-normal curve, but not the
 * curve's parameters. Reading those thresholds as the curve's two control
 * points (Lighthouse's p10 → 0.90 and median → 0.50) is our inference:
 *   https://vercel.com/docs/speed-insights/metrics
 *   https://vercel.com/docs/speed-insights/migrating-from-legacy (Updated Scoring Criteria)
 *   https://developer.chrome.com/docs/lighthouse/performance/performance-scoring
 * The curve is Lighthouse's lib/statistics.js getLogNormalScore.
 */

import type { StatusTone } from "@/components/forge-ui/status";
import type { Panel, PanelResult } from "@/services/controlPlane/observability/types";
import type { RangePreset } from "@/services/controlPlane/observability/window";

// ── Vitals ──────────────────────────────────────────────────────────────────

export type Vital = "lcp" | "inp" | "cls" | "fcp" | "ttfb";
/** In card order: the three Core Web Vitals, then the two loading ones. */
export const VITALS: readonly Vital[] = ["lcp", "inp", "cls", "fcp", "ttfb"];

/**
 * web.dev's thresholds: good at or under the first, poor over the second. CLS
 * is unitless; the rest milliseconds. They are also the score curve's control
 * points: a vital at `good` scores 90, at `poor` 50 (header).
 */
export const VITAL_INFO: Record<Vital, { short: string; label: string; about: string; good: number; poor: number; unit: "ms" | "score" }> = {
  lcp: { short: "LCP", label: "Largest Contentful Paint", about: "When the main content has loaded", good: 2500, poor: 4000, unit: "ms" },
  inp: { short: "INP", label: "Interaction to Next Paint", about: "How quickly the page responds to a tap or click", good: 200, poor: 500, unit: "ms" },
  cls: { short: "CLS", label: "Cumulative Layout Shift", about: "How much the page jumps around as it loads", good: 0.1, poor: 0.25, unit: "score" },
  fcp: { short: "FCP", label: "First Contentful Paint", about: "When the first text or image appears", good: 1800, poor: 3000, unit: "ms" },
  ttfb: { short: "TTFB", label: "Time to First Byte", about: "How long the server takes to start answering", good: 800, poor: 1800, unit: "ms" },
};

/** The score's weights (header). TTFB has none: it is shown, not scored. */
export const SCORE_WEIGHTS: Record<Vital, number> = { lcp: 30, inp: 30, cls: 25, fcp: 15, ttfb: 0 };

export type Rating = "good" | "ni" | "poor";

export const RATING_LABEL: Record<Rating, string> = { good: "Good", ni: "Needs improvement", poor: "Poor" };
export const RATING_TONE: Record<Rating, StatusTone> = { good: "success", ni: "warning", poor: "danger" };

export function rateVital(vital: Vital, value: number | null): Rating | null {
  if (value === null || !Number.isFinite(value)) return null;
  const { good, poor } = VITAL_INFO[vital];
  return value <= good ? "good" : value <= poor ? "ni" : "poor";
}

export function formatVital(vital: Vital, value: number | null): string {
  if (value === null || !Number.isFinite(value)) return "—";
  if (VITAL_INFO[vital].unit === "score") return value.toFixed(2);
  return value >= 1000 ? `${(value / 1000).toFixed(2)} s` : `${Math.round(value)} ms`;
}

// ── The URL's choices ───────────────────────────────────────────────────────

export type Percentile = "p75" | "p90" | "p95" | "p99";
export const PERCENTILES: readonly Percentile[] = ["p75", "p90", "p95", "p99"];
export const DEFAULT_PERCENTILE: Percentile = "p75";
const QUANTILE: Record<Percentile, number> = { p75: 0.75, p90: 0.9, p95: 0.95, p99: 0.99 };

/** Desktop is Vercel's default view; mobile includes tablets. */
export type Device = "desktop" | "mobile";
export const DEVICES: readonly Device[] = ["desktop", "mobile"];
export const DEFAULT_DEVICE: Device = "desktop";

/** `?metric=`: what the chart draws, the score or one vital. */
export type ChartMetric = "score" | Vital;

export function percentileOf(value: unknown): Percentile {
  return (PERCENTILES as readonly unknown[]).includes(value) ? (value as Percentile) : DEFAULT_PERCENTILE;
}
export function deviceOf(value: unknown): Device {
  return value === "mobile" ? "mobile" : DEFAULT_DEVICE;
}
export function chartMetricOf(value: unknown): ChartMetric {
  return (VITALS as readonly unknown[]).includes(value) ? (value as Vital) : "score";
}

// ── The query ───────────────────────────────────────────────────────────────

/**
 * Which device classes a toggle reads. Desktop also takes spans with no class:
 * a frontend whose telemetry did not pass Reliant's ingest proxy (local
 * development) carries none, and most of that is a developer's desktop.
 * Bots are never counted.
 */
function deviceWhere(device: Device): string {
  return device === "mobile"
    ? "ResourceAttributes['reliant.device.type'] IN ('mobile', 'tablet')"
    : "ResourceAttributes['reliant.device.type'] IN ('desktop', '')";
}

const attr = (vital: Vital) => `SpanAttributes['${vital}']`;
const num = (vital: Vital) => `toFloat64OrZero(${attr(vital)})`;

/** Per vital: its percentile, and its good / needs-improvement / poor counts. */
function vitalColumns(percentile: Percentile): string[] {
  const q = QUANTILE[percentile];
  return VITALS.flatMap((v) => {
    const { good, poor } = VITAL_INFO[v];
    const has = `${attr(v)} != ''`;
    return [
      `quantileIf(${q})(${num(v)}, ${has}) AS ${v}`,
      `countIf(${has} AND ${num(v)} <= ${good}) AS ${v}_good`,
      `countIf(${has} AND ${num(v)} > ${good} AND ${num(v)} <= ${poor}) AS ${v}_ni`,
      `countIf(${has} AND ${num(v)} > ${poor}) AS ${v}_poor`,
    ];
  });
}

/**
 * The site filter: the query's services (QueryScope.services, bound as
 * `{services}`), or every site when it names none. A frontend's spans carry
 * its own `service.name` (header).
 */
export const SERVICE_WHERE = "(empty({services:Array(String)}) OR has({services:Array(String)}, ResourceAttributes['service.name']))";

// SQL panels have no project parameter: only time, environment and service
// parameters are supported. Encode the trusted console project as a SQL string
// literal (escaping backslashes before quotes), never the URL's site choice.
// An omitted project fails closed rather than reading the whole organization.
const projectWhere = (project: string) =>
  project ? `ResourceAttributes['service.namespace'] = '${project.replace(/\\/g, "\\\\").replace(/'/g, "\\'")}'` : "0";

const WHERE = (device: Device | "any", project: string) => [
  "FROM otel_traces",
  "WHERE ScopeName = 'webvitals'",
  `  AND ${projectWhere(project)}`,
  "  AND Timestamp >= {from:DateTime64(9)} AND Timestamp < {to:DateTime64(9)}",
  // QueryScope.project_id resolves to authorized environment ids server-side.
  // Require that trusted stamp even when the resolved list is empty: namespace
  // and service names are producer-controlled and can collide across projects.
  "  AND has({env_ids:Array(String)}, ResourceAttributes['reliant.env.id'])",
  `  AND ${SERVICE_WHERE}`,
  // The site list counts every device but bots, which no toggle ever shows.
  `  AND ${device === "any" ? "ResourceAttributes['reliant.device.type'] != 'bot'" : deviceWhere(device)}`,
];

/** Page loads: one TTFB per navigation, the one vital every load reports. */
const VIEWS = `countIf(${attr("ttfb")} != '') AS views`;

export type SpeedPanelKind = "summary" | "routes" | "series";

/** The id each panel carries (the fixtures and the cache key read it): `speed-insights-<kind>`. */
export function speedPanelId(kind: SpeedPanelKind | "sites"): string {
  return `speed-insights-${kind}`;
}

/** A chart bucket that gives about 24–60 points over the window. */
export function bucketSeconds(range: RangePreset): number {
  return { "15m": 60, "1h": 120, "6h": 600, "24h": 3600, "7d": 6 * 3600, "30d": 24 * 3600 }[range];
}

export function speedPanel(kind: SpeedPanelKind, device: Device, percentile: Percentile, range: RangePreset, project = ""): Panel {
  const key =
    kind === "summary"
      ? "'all' AS scope"
      : kind === "routes"
        ? "path(SpanAttributes['location.href']) AS page"
        : `toUnixTimestamp(toStartOfInterval(Timestamp, INTERVAL ${bucketSeconds(range)} SECOND)) AS t`;
  const tail = kind === "routes" ? ["GROUP BY page", "ORDER BY views DESC", "LIMIT 100"] : kind === "series" ? ["GROUP BY t", "ORDER BY t"] : [];
  const sql = [`SELECT ${[key, ...vitalColumns(percentile), VIEWS].join(",\n       ")}`, ...WHERE(device, project), ...tail].join("\n");
  return { id: speedPanelId(kind), title: `Web vitals: ${kind}`, kind: "sql", signal: "", series: [], sql };
}

/** The sites that sent web vitals in the window, busiest first: the site picker's choices (header). */
export function sitesPanel(project = ""): Panel {
  const sql = ["SELECT ResourceAttributes['service.name'] AS service,\n       " + VIEWS, ...WHERE("any", project), "GROUP BY service", "ORDER BY views DESC", "LIMIT 50"].join("\n");
  return { id: speedPanelId("sites"), title: "Web vitals: sites", kind: "sql", signal: "", series: [], sql };
}

export interface SiteRow {
  /** The site's telemetry `service.name`. */
  service: string;
  views: number;
}

/** The sites panel's rows; a span with no service.name is not a site anyone can pick. */
export function siteRows(result: PanelResult | undefined): SiteRow[] {
  if (!result) return [];
  const views = result.columns.indexOf("views");
  return result.rows
    .map((row) => ({ service: row.group.service ?? "", views: views < 0 ? 0 : (row.values[views] ?? 0) }))
    .filter((row) => row.service !== "");
}

/**
 * The `service.name`s a frontend's telemetry may carry: forge names it
 * "<project>-<frontend>" (naming.FrontendServiceName), and a frontend that
 * starts the SDK itself may report its bare name (Sessions/replayStatus.ts
 * servesFrontend). What a caller that knows a site by its frontend name (the
 * static site's page) passes as the services to read.
 */
export function siteServiceNames(project: string, frontend: string): string[] {
  return project ? [`${project}-${frontend}`, frontend] : [frontend];
}

// ── Reading it ──────────────────────────────────────────────────────────────

export interface VitalStat {
  /** The vital at the chosen percentile; null with no sample. */
  value: number | null;
  good: number;
  ni: number;
  poor: number;
}

export interface VitalsRow {
  /** The route for the routes panel; "" otherwise. */
  page: string;
  /** The bucket's start, epoch seconds, for the series panel. */
  t: number | null;
  views: number;
  vitals: Record<Vital, VitalStat>;
}

export function samplesOf(stat: VitalStat): number {
  return stat.good + stat.ni + stat.poor;
}

/** The panel's rows; a vital with no sample reads value null and zero counts. */
export function vitalsRows(result: PanelResult | undefined): VitalsRow[] {
  if (!result) return [];
  const index = new Map(result.columns.map((name, i) => [name, i]));
  return result.rows.map((row) => {
    const read = (name: string) => {
      const i = index.get(name);
      const v = i === undefined ? null : (row.values[i] ?? null);
      return v === null || !Number.isFinite(v) ? null : v;
    };
    const vitals = {} as Record<Vital, VitalStat>;
    for (const v of VITALS) {
      const good = read(`${v}_good`) ?? 0;
      const ni = read(`${v}_ni`) ?? 0;
      const poor = read(`${v}_poor`) ?? 0;
      // quantileIf over no row answers 0 (or nan): no sample, not a perfect 0 ms.
      vitals[v] = { value: good + ni + poor > 0 ? read(v) : null, good, ni, poor };
    }
    return { page: row.group.page ?? "", t: read("t"), views: read("views") ?? 0, vitals };
  });
}

/** The error function, Abramowitz & Stegun 7.1.26 (|error| < 1.5e-7), as Lighthouse's lib/statistics.js. */
function erf(x: number): number {
  const sign = Math.sign(x);
  const ax = Math.abs(x);
  const t = 1 / (1 + 0.3275911 * ax);
  const y = t * (0.254829592 + t * (-0.284496736 + t * (1.421413741 + t * (-1.453152027 + t * 1.061405429))));
  return sign * (1 - y * Math.exp(-ax * ax));
}

/** erfc⁻¹(0.2): puts the p10 control point at a score of 0.9. */
const INVERSE_ERFC_ONE_FIFTH = 0.9061938024368232;

/**
 * Lighthouse's getLogNormalScore: 0–1 for `value` on the log-normal curve that
 * scores `p10` 0.9 and `median` 0.5. Like Lighthouse, it clamps the result into
 * the value's band, so the approximate erf cannot move a value across 0.9 or 0.5.
 */
export function getLogNormalScore({ p10, median }: { p10: number; median: number }, value: number): number {
  if (value <= 0) return 1;
  const xLogRatio = Math.log(Math.max(Number.MIN_VALUE, value / median));
  const p10LogRatio = -Math.log(Math.max(Number.MIN_VALUE, p10 / median));
  const standardizedX = (xLogRatio * INVERSE_ERFC_ONE_FIFTH) / p10LogRatio;
  const complementaryPercentile = (1 - erf(standardizedX)) / 2;
  if (value <= p10) return Math.max(0.9, Math.min(1, complementaryPercentile));
  if (value <= median) return Math.max(0.5, Math.min(0.8999999999999999, complementaryPercentile));
  return Math.max(0, Math.min(0.49999999999999994, complementaryPercentile));
}

/** One vital's score, 0–100: its value at the selected percentile on its curve (header); null with no sample. */
export function vitalScore(vital: Vital, stat: VitalStat): number | null {
  if (stat.value === null || !Number.isFinite(stat.value)) return null;
  const { good, poor } = VITAL_INFO[vital];
  return 100 * getLogNormalScore({ p10: good, median: poor }, stat.value);
}

/** The Real Experience Score, 0–100 and rounded (header); null when no scored vital has a sample. */
export function realExperienceScore(vitals: Record<Vital, VitalStat>): number | null {
  let weight = 0;
  let sum = 0;
  for (const v of VITALS) {
    const score = vitalScore(v, vitals[v]);
    if (score === null || SCORE_WEIGHTS[v] === 0) continue;
    weight += SCORE_WEIGHTS[v];
    sum += SCORE_WEIGHTS[v] * score;
  }
  return weight ? Math.round(sum / weight) : null;
}

export function rateScore(score: number | null): Rating | null {
  if (score === null) return null;
  return score >= 90 ? "good" : score >= 50 ? "ni" : "poor";
}

export const SCORE_LABEL: Record<Rating, string> = { good: "Great", ni: "Needs improvement", poor: "Poor" };

/** A distribution's three shares, 0–1, summing to 1; null with no sample. */
export function distribution(stat: VitalStat): Record<Rating, number> | null {
  const n = samplesOf(stat);
  return n > 0 ? { good: stat.good / n, ni: stat.ni / n, poor: stat.poor / n } : null;
}

export interface RouteRow extends VitalsRow {
  score: number | null;
}

/** The routes, worst score first (the ones to fix lead); unscored last, then by views. */
export function routesByScore(rows: readonly VitalsRow[]): RouteRow[] {
  return rows
    .map((row) => ({ ...row, score: realExperienceScore(row.vitals) }))
    .sort((a, b) => {
      if (a.score === null || b.score === null) return a.score === null ? (b.score === null ? b.views - a.views : 1) : -1;
      return a.score - b.score || b.views - a.views;
    });
}
