// Copyright (c) 2026 Reliant Labs

/**
 * The query and arithmetic behind Speed insights (SpeedInsightsPage.tsx, U6e:
 * after Vercel's). Pure: the page renders what these return.
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
 * THE REAL EXPERIENCE SCORE (realExperienceScore below), 0–100:
 *
 *   metric score  = 100 × (good + ½ × needs improvement) / samples
 *                   (good counts in full, needs improvement half, poor not at
 *                   all; good / poor follow web.dev's thresholds, VITAL_INFO)
 *   RES           = Σ weight × metric score / Σ weight, over the scored
 *                   vitals that have samples
 *   weights       LCP 30 · INP 30 · CLS 25 · FCP 15 (TTFB is shown, and not
 *                 scored), Vercel's weighting of the Lighthouse vitals
 *   rating        90–100 great, 50–89 needs improvement, 0–49 poor
 *
 * Vercel scores each sample on Lighthouse's log-normal curve; we score the
 * three web.dev buckets instead, which needs only the counts ClickHouse
 * already returns and moves the same way. The score does not depend on the
 * percentile shown: it reads every sample, not one percentile of them.
 */

import type { StatusTone } from "@/components/forge-ui/status";
import type { Panel, PanelResult } from "@/services/controlPlane/observability/types";
import type { RangePreset } from "@/services/controlPlane/observability/window";

// ── Vitals ──────────────────────────────────────────────────────────────────

export type Vital = "lcp" | "inp" | "cls" | "fcp" | "ttfb";
/** In card order: the three Core Web Vitals, then the two loading ones. */
export const VITALS: readonly Vital[] = ["lcp", "inp", "cls", "fcp", "ttfb"];

/** web.dev's thresholds: good at or under the first, poor over the second. CLS is unitless; the rest milliseconds. */
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

const WHERE = (device: Device) => [
  "FROM otel_traces",
  "WHERE ScopeName = 'webvitals'",
  "  AND Timestamp >= {from:DateTime64(9)} AND Timestamp < {to:DateTime64(9)}",
  "  AND (empty({env_ids:Array(String)}) OR has({env_ids:Array(String)}, ResourceAttributes['reliant.env.id']))",
  `  AND ${deviceWhere(device)}`,
];

/** Page loads: one TTFB per navigation, the one vital every load reports. */
const VIEWS = `countIf(${attr("ttfb")} != '') AS views`;

export type SpeedPanelKind = "summary" | "routes" | "series";

/** The id each panel carries (the fixtures and the cache key read it): `speed-insights-<kind>`. */
export function speedPanelId(kind: SpeedPanelKind): string {
  return `speed-insights-${kind}`;
}

/** A chart bucket that gives about 24–60 points over the window. */
export function bucketSeconds(range: RangePreset): number {
  return { "15m": 60, "1h": 120, "6h": 600, "24h": 3600, "7d": 6 * 3600, "30d": 24 * 3600 }[range];
}

export function speedPanel(kind: SpeedPanelKind, device: Device, percentile: Percentile, range: RangePreset): Panel {
  const key =
    kind === "summary"
      ? "'all' AS scope"
      : kind === "routes"
        ? "path(SpanAttributes['location.href']) AS page"
        : `toUnixTimestamp(toStartOfInterval(Timestamp, INTERVAL ${bucketSeconds(range)} SECOND)) AS t`;
  const tail = kind === "routes" ? ["GROUP BY page", "ORDER BY views DESC", "LIMIT 100"] : kind === "series" ? ["GROUP BY t", "ORDER BY t"] : [];
  const sql = [`SELECT ${[key, ...vitalColumns(percentile), VIEWS].join(",\n       ")}`, ...WHERE(device), ...tail].join("\n");
  return { id: speedPanelId(kind), title: `Web vitals: ${kind}`, kind: "sql", signal: "", series: [], sql };
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

/** One vital's score, 0–100 (header); null with no sample. */
export function vitalScore(stat: VitalStat): number | null {
  const n = samplesOf(stat);
  return n > 0 ? (100 * (stat.good + 0.5 * stat.ni)) / n : null;
}

/** The Real Experience Score, 0–100 and rounded (header); null when no scored vital has a sample. */
export function realExperienceScore(vitals: Record<Vital, VitalStat>): number | null {
  let weight = 0;
  let sum = 0;
  for (const v of VITALS) {
    const score = vitalScore(vitals[v]);
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
