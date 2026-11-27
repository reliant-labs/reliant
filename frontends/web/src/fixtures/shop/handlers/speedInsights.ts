// Copyright (c) 2025 Reliant Labs

/**
 * The shop pack's Speed insights: web vitals for six routes of shop's
 * storefront (shop-web), as the page's three SQL QueryPanels read them from
 * the browser runtime's `webvitals` spans (Forge/Analytics/speedInsightsModel.ts):
 *
 *   speed-insights-summary   the app, one row: each vital at the percentile,
 *                            its good / needs-improvement / poor counts, and
 *                            page loads
 *   speed-insights-routes    the same per route
 *   speed-insights-series    the same per time bucket over the window
 *
 * The device (`reliant.device.type`), the percentile (`quantileIf(0.9)`) and
 * the bucket (`INTERVAL n SECOND`) are read back out of the request's SQL, so
 * every toggle answers differently and consistently. Each route's vital is a
 * log-normal around its median: its percentiles and its three shares come
 * from the same curve. Desktop is mostly good (checkout's LCP and search's
 * INP need work, one product page shifts badly); mobile is ~45% slower.
 * Every time is relative to the pack clock.
 *
 * Ported from the visual harness's own fixture (e2e/visual), so the demo and
 * the screenshots show one Speed insights page. `withSpeedInsights` answers
 * these three panels and hands every other QueryPanel to the pack's handler.
 */

import { create } from "@bufbuild/protobuf";
import type { MockHandlers } from "@reliantlabs/forge-web-runtime/mock-transport";

import { ObservabilityService, QueryPanelResponseSchema, type QueryPanelRequest } from "@/gen/controlplane/services/observability/v1/observability_pb";

import { DAY, demoNow } from "../shopStory";
import { msOf, windowOf } from "./wire";

type Vital = "lcp" | "inp" | "cls" | "fcp" | "ttfb";
const VITALS: Vital[] = ["lcp", "inp", "cls", "fcp", "ttfb"];
// web.dev's thresholds (good at or under, poor over), as the model's VITAL_INFO.
const THRESHOLDS: Record<Vital, [number, number]> = { lcp: [2500, 4000], inp: [200, 500], cls: [0.1, 0.25], fcp: [1800, 3000], ttfb: [800, 1800] };
// The spread of each vital around its median (log-normal sigma).
const SIGMA: Record<Vital, number> = { lcp: 0.5, inp: 0.7, cls: 0.9, fcp: 0.5, ttfb: 0.55 };

// route, desktop p75 of lcp ms, inp ms, cls, fcp ms, ttfb ms, desktop page loads over 7 days
const ROUTES: [string, number, number, number, number, number, number][] = [
  ["/", 1820, 96, 0.02, 1080, 310, 18_420],
  ["/products", 2140, 142, 0.05, 1290, 380, 9_870],
  ["/products/view", 2390, 168, 0.31, 1420, 410, 7_215],
  ["/cart", 1610, 88, 0.01, 990, 290, 3_402],
  ["/checkout", 3120, 184, 0.04, 1880, 640, 1_288],
  ["/search", 2010, 356, 0.07, 1210, 350, 2_960],
];

const Z: Record<string, number> = { "0.75": 0.6745, "0.9": 1.2816, "0.95": 1.6449, "0.99": 2.3263 };

/** The standard normal CDF (Abramowitz–Stegun 7.1.26 erf). */
function phi(x: number): number {
  const t = 1 / (1 + 0.3275911 * (Math.abs(x) / Math.SQRT2));
  const erf = 1 - ((((1.061405429 * t - 1.453152027) * t + 1.421413741) * t - 0.284496736) * t + 0.254829592) * t * Math.exp(-(x * x) / 2);
  return x >= 0 ? (1 + erf) / 2 : (1 - erf) / 2;
}

interface Stat {
  value: number;
  good: number;
  ni: number;
  poor: number;
}

/** One vital of one route: its percentile and its sample split, from the p75 it is described by. */
function stat(vital: Vital, p75: number, samples: number, z: number): Stat {
  const sigma = SIGMA[vital];
  const median = p75 / Math.exp(sigma * 0.6745);
  const [good, poor] = THRESHOLDS[vital];
  const g = Math.round(samples * phi(Math.log(good / median) / sigma));
  const p = Math.round(samples * (1 - phi(Math.log(poor / median) / sigma)));
  const value = median * Math.exp(sigma * z);
  return { value: vital === "cls" ? Math.round(value * 1000) / 1000 : Math.round(value), good: g, ni: Math.max(0, samples - g - p), poor: p };
}

/** Every vital's columns, in the model's order, then page loads. */
const COLUMNS = [...VITALS.flatMap((v) => [v, `${v}_good`, `${v}_ni`, `${v}_poor`]), "views"];

const values = (stats: Record<Vital, Stat>, views: number): number[] => [...VITALS.flatMap((v) => [stats[v].value, stats[v].good, stats[v].ni, stats[v].poor]), views];

interface Ask {
  device: "desktop" | "mobile";
  z: number;
  bucket: number;
}

function askOf(sql: string): Ask {
  const q = /quantileIf\(([\d.]+)\)/.exec(sql)?.[1] ?? "0.75";
  return { device: sql.includes("'mobile'") ? "mobile" : "desktop", z: Z[q] ?? Z["0.75"], bucket: Number(/INTERVAL (\d+) SECOND/.exec(sql)?.[1] ?? 3600) };
}

type Part = { stats: Record<Vital, Stat>; views: number };

/** A route's vitals over `share` of the 7-day traffic, on a device. */
function routeStats(route: (typeof ROUTES)[number], ask: Ask, share: number, slow = 1): Part {
  const [, lcp, inp, cls, fcp, ttfb, loads] = route;
  const mobile = ask.device === "mobile";
  const views = Math.max(0, Math.round(loads * share * (mobile ? 0.7 : 1)));
  const p75: Record<Vital, number> = { lcp, inp, cls, fcp, ttfb };
  const stats = {} as Record<Vital, Stat>;
  for (const v of VITALS) {
    // Not every load reports every vital: INP needs an interaction, CLS a layout.
    const samples = Math.round(views * (v === "inp" ? 0.62 : v === "cls" ? 0.9 : 1));
    stats[v] = stat(v, p75[v] * (mobile ? 1.45 : 1) * slow, samples, ask.z);
  }
  return { stats, views };
}

/** Sum routes' stats: counts add; the percentile is the load-weighted mean (an estimate, fixture only). */
function combine(parts: Part[]): Part {
  const stats = {} as Record<Vital, Stat>;
  for (const v of VITALS) {
    const n = parts.reduce((acc, p) => acc + p.stats[v].good + p.stats[v].ni + p.stats[v].poor, 0);
    const weighted = parts.reduce((acc, p) => acc + p.stats[v].value * (p.stats[v].good + p.stats[v].ni + p.stats[v].poor), 0);
    stats[v] = {
      value: n ? (v === "cls" ? Math.round((weighted / n) * 1000) / 1000 : Math.round(weighted / n)) : 0,
      good: parts.reduce((acc, p) => acc + p.stats[v].good, 0),
      ni: parts.reduce((acc, p) => acc + p.stats[v].ni, 0),
      poor: parts.reduce((acc, p) => acc + p.stats[v].poor, 0),
    };
  }
  return { stats, views: parts.reduce((acc, p) => acc + p.views, 0) };
}

/** Speed insights' answer to one QueryPanel, or undefined when the panel is not one of its three. */
export function speedInsightsPanel(req: QueryPanelRequest, now: number = demoNow()) {
  const id = req.panel?.id ?? "";
  if (!id.startsWith("speed-insights-")) return undefined;
  const ask = askOf(req.panel?.sql ?? "");
  const range = req.scope?.timeRange;
  const window = windowOf(range, now);
  const end = window?.endMs ?? (range?.end ? msOf(range.end) : now);
  const start = window?.startMs ?? (range?.start ? msOf(range.start) : end - 7 * DAY);
  const share = Math.min(4, (end - start) / (7 * DAY));
  const answer = (rows: { group: Record<string, string>; values: number[] }[], columns = COLUMNS) =>
    create(QueryPanelResponseSchema, { kind: "sql", columns, rows: rows.map((row) => ({ ...row, missingValueIndexes: [] })) });
  if (id === "speed-insights-summary") {
    const all = combine(ROUTES.map((route) => routeStats(route, ask, share)));
    return answer([{ group: { scope: "all" }, values: values(all.stats, all.views) }]);
  }
  if (id === "speed-insights-routes") {
    return answer(
      ROUTES.map((route) => {
        const { stats, views } = routeStats(route, ask, share);
        return { group: { page: route[0] }, values: values(stats, views) };
      }),
    );
  }
  if (id === "speed-insights-series") {
    const step = ask.bucket * 1000;
    const rows: { group: Record<string, string>; values: number[] }[] = [];
    for (let t = Math.floor(start / step) * step, i = 0; t < end && i < 400; t += step, i += 1) {
      // A slow afternoon wave, and one bad deploy two days ago that a fix undid.
      const wave = 1 + 0.08 * Math.sin(i / 3);
      const regression = now - t < 2.2 * DAY && now - t > 1.7 * DAY ? 1.35 : 1;
      const bucket = combine(ROUTES.map((route) => routeStats(route, ask, step / (7 * DAY), wave * regression)));
      rows.push({ group: {}, values: [Math.round(t / 1000), ...values(bucket.stats, bucket.views)] });
    }
    return answer(rows, ["t", ...COLUMNS]);
  }
  return undefined;
}

const QUERY_PANEL = `${ObservabilityService.typeName}/QueryPanel`;

/** `handlers` with Speed insights' three panels answered first, every other QueryPanel by the handler it had. */
export function withSpeedInsights(handlers: MockHandlers): MockHandlers {
  const rest = handlers[QUERY_PANEL];
  return {
    ...handlers,
    [QUERY_PANEL]: (req: QueryPanelRequest) => speedInsightsPanel(req) ?? rest?.(req),
  };
}
