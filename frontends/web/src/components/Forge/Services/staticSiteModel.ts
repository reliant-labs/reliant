// Copyright (c) 2026 Reliant Labs

/**
 * What a static site's page says about its hosting (Site insights R3, design
 * D4): its usage against the free allowance, its CDN traffic, and how the
 * platform serves it. Pure functions over control-plane's StaticSiteService
 * answers (GetStaticSite, GetStaticSiteTraffic), so the copy and the meter
 * thresholds are tested without rendering (__tests__/staticSiteModel.test.ts).
 */

import { timestampMs } from "@bufbuild/protobuf/wkt";

import {
  StaticSiteHosting,
  StaticSiteNotFoundMode,
  StaticSiteTrafficRange,
  type GetStaticSiteTrafficResponse,
  type StaticSiteConfiguration,
  type StaticSiteTrafficCounts,
  type StaticSiteUsage,
} from "@/gen/controlplane/services/static_site/v1/static_site_pb";
import { settingsPath } from "@/lib/paths";

// ── Usage and free tier ──────────────────────────────────────────────────────

/** Where hosting is set up and paid for: Settings › Billing › Hosting (it also holds the free allowance). */
export const HOSTING_BILLING_HREF = `${settingsPath("billing")}?tab=hosting`;

/** From this share of a cap on, a meter warns. */
export const NEAR_CAP = 0.8;

/**
 * Where a figure stands against its cap:
 *
 *   uncapped  no cap applies (paid hosting, or the plan is unknown)
 *   under     below NEAR_CAP of it
 *   near      from NEAR_CAP up to the cap
 *   over      at the cap or past it (capped)
 */
export type MeterState = "uncapped" | "under" | "near" | "over";

export function meterState(used: number, limit: number): MeterState {
  if (!(limit > 0)) return "uncapped";
  const share = used / limit;
  if (share >= 1) return "over";
  if (share >= NEAR_CAP) return "near";
  return "under";
}

export interface UsageMeter {
  id: "site-storage" | "org-storage" | "org-transfer";
  label: string;
  /** Bytes used; null while not measured yet. */
  used: number | null;
  /** The cap in bytes; 0 when none applies. */
  limit: number;
  state: MeterState;
  /** The figure as read: "180 MiB", "at least 412 MiB", "Not measured yet". */
  usedLabel: string;
  /** "of 1 GiB", or "" with no cap. */
  limitLabel: string;
  /** A line under the meter: this site's share, the reset date, what it counts. */
  note: string;
}

export interface UsageView {
  hosting: StaticSiteHosting;
  /** One sentence on what the figures are measured against. */
  summary: string;
  meters: UsageMeter[];
  /** The worst meter's state (uncapped when nothing has a cap). */
  worst: MeterState;
  /** Show the billing link: something is capped, or nothing covers this org's hosting. */
  billing: boolean;
}

const STATE_RANK: Record<MeterState, number> = { uncapped: 0, under: 1, near: 2, over: 3 };

const HOSTING_SUMMARY: Record<StaticSiteHosting, string> = {
  [StaticSiteHosting.FREE]: "Free static hosting: this site counts against the organization's free allowance.",
  [StaticSiteHosting.PAID]: "Paid hosting: storage and transfer are billed, with no free caps.",
  [StaticSiteHosting.NOT_COVERED]: "This organization has neither the free allowance nor hosting billing, so its new deploys wait for billing.",
  [StaticSiteHosting.UNSPECIFIED]: "The organization's hosting plan could not be read, so no caps are shown.",
};

/** Bytes in binary units, as the free caps are stated: "180 MiB", "2.1 GiB". */
export function formatBinaryBytes(bytes: number): string {
  if (!Number.isFinite(bytes)) return "—";
  const units = ["B", "KiB", "MiB", "GiB", "TiB"];
  let value = bytes;
  let unit = 0;
  while (Math.abs(value) >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit += 1;
  }
  const digits = unit === 0 || value >= 100 ? 0 : 1;
  return `${Number(value.toFixed(digits))} ${units[unit]}`;
}

/** "1 December" (UTC: the transfer period is a UTC month). */
function utcDay(ms: number): string {
  return new Date(ms).toLocaleDateString(undefined, { day: "numeric", month: "long", timeZone: "UTC" });
}

function meter(id: UsageMeter["id"], label: string, used: number | null, limit: number, note: string, { lowerBound = false } = {}): UsageMeter {
  const state = used === null ? (limit > 0 ? "under" : "uncapped") : meterState(used, limit);
  return {
    id,
    label,
    used,
    limit,
    state,
    usedLabel: used === null ? "Not measured yet" : `${lowerBound ? "at least " : ""}${formatBinaryBytes(used)}`,
    limitLabel: limit > 0 ? `of ${formatBinaryBytes(limit)}` : "",
    note,
  };
}

/** GetStaticSite's usage as the page's meters. A cap is shown only where the server sends one (never under paid hosting). */
export function usageView(usage: StaticSiteUsage): UsageView {
  const paid = usage.hosting === StaticSiteHosting.PAID;
  // Paid hosting has no free caps; a stray limit on one is not a free cap, so it is not drawn.
  const cap = (limit: bigint) => (paid ? 0 : Number(limit));
  const transferCap = Number(usage.orgTransferLimitBytes);
  const site = usage.siteTransferBytes === undefined ? null : Number(usage.siteTransferBytes);
  const reset = usage.periodEnd ? `Resets ${utcDay(timestampMs(usage.periodEnd))}.` : "";
  const share = site === null ? "" : `This site: ${formatBinaryBytes(site)}.`;
  const meters = [
    meter("site-storage", "This site's storage", usage.storageBytes === undefined ? null : Number(usage.storageBytes), cap(usage.siteStorageLimitBytes), "The live release plus every retained release."),
    meter("org-storage", "Organization storage", Number(usage.orgStorageBytes), cap(usage.orgStorageLimitBytes), "Every static site of the organization, retained releases included.", { lowerBound: !usage.orgStorageMeasured }),
    // A paid plan may declare a transfer limit of its own (the proto's only paid cap): drawn when set.
    meter("org-transfer", "Organization transfer this month", Number(usage.orgTransferBytes), transferCap, [share, reset].filter(Boolean).join(" ")),
  ];
  const worst = meters.reduce<MeterState>((acc, m) => (STATE_RANK[m.state] > STATE_RANK[acc] ? m.state : acc), "uncapped");
  return {
    hosting: usage.hosting,
    summary: HOSTING_SUMMARY[usage.hosting] ?? HOSTING_SUMMARY[StaticSiteHosting.UNSPECIFIED],
    meters,
    worst,
    billing: usage.hosting === StaticSiteHosting.NOT_COVERED || (!paid && worst === "over"),
  };
}

/** What the page says beside the billing link, by the worst meter. */
export function capNote(view: UsageView): string | null {
  if (view.hosting === StaticSiteHosting.NOT_COVERED) return "Set up hosting billing to deploy this site again.";
  if (view.hosting === StaticSiteHosting.PAID) return null;
  if (view.worst === "over") return "Over the free allowance. Hosting past it needs billing.";
  if (view.worst === "near") return "Close to the free allowance.";
  return null;
}

// ── Configuration ────────────────────────────────────────────────────────────

export interface NotFoundModeCopy {
  label: string;
  explainer: string;
}

/** The 404 mode, named for what a visitor gets (design D4). */
export const NOT_FOUND_MODE_COPY: Record<StaticSiteNotFoundMode, NotFoundModeCopy> = {
  [StaticSiteNotFoundMode.NOT_FOUND_PAGE]: {
    label: "404.html: real 404s",
    explainer: "The build ships a 404.html, so a path with no file gets that page with a 404 status. Search engines drop a page that is gone.",
  },
  [StaticSiteNotFoundMode.SPA_FALLBACK]: {
    label: "SPA fallback: unknown paths serve index.html with 200",
    explainer:
      "The build has no 404.html, so an unknown route gets index.html with a 200 and your client-side router draws it. A missing asset is still a 404. Search engines can index a mistyped URL as a real page: ship a 404.html to answer real 404s.",
  },
  [StaticSiteNotFoundMode.UNSPECIFIED]: {
    label: "Unknown",
    explainer: "The live build could not be read, so what an unknown path gets is not known here.",
  },
};

export function notFoundModeCopy(mode: StaticSiteNotFoundMode): NotFoundModeCopy {
  return NOT_FOUND_MODE_COPY[mode] ?? NOT_FOUND_MODE_COPY[StaticSiteNotFoundMode.UNSPECIFIED];
}

export interface RuntimeConfigEntry {
  key: string;
  /** The literal value, or the workload whose URL the platform fills in. */
  value: string;
  workloadUrl: boolean;
}

/** The runtime config's entries by key: literal values and workload URLs together. */
export function runtimeConfigEntries(config: StaticSiteConfiguration): RuntimeConfigEntry[] {
  const literal = Object.entries(config.runtimeConfig).map(([key, value]) => ({ key, value, workloadUrl: false }));
  const urls = Object.entries(config.runtimeConfigWorkloadUrls).map(([key, value]) => ({ key, value, workloadUrl: true }));
  return [...literal, ...urls].sort((a, b) => (a.key < b.key ? -1 : a.key > b.key ? 1 : 0));
}

/** A rules file's count as read: unset when the build could not be read, 0 for none. */
export function ruleCount(count: number | undefined, noun: string): string {
  if (count === undefined) return "Not read";
  if (count === 0) return "None";
  return `${count} ${noun}${count === 1 ? "" : "s"}`;
}

/** The mount point as read: "" is the root. */
export function basePathLabel(basePath: string): string {
  return basePath === "" || basePath === "/" ? "/ (the root)" : basePath;
}

// ── Traffic ──────────────────────────────────────────────────────────────────

export const TRAFFIC_RANGES = ["24h", "7d", "30d"] as const;
export type TrafficRange = (typeof TRAFFIC_RANGES)[number];
export const DEFAULT_TRAFFIC_RANGE: TrafficRange = "7d";

export const TRAFFIC_RANGE_WIRE: Record<TrafficRange, StaticSiteTrafficRange> = {
  "24h": StaticSiteTrafficRange.LAST_24_HOURS,
  "7d": StaticSiteTrafficRange.LAST_7_DAYS,
  "30d": StaticSiteTrafficRange.LAST_30_DAYS,
};

export const TRAFFIC_RANGE_LABEL: Record<TrafficRange, string> = { "24h": "24h", "7d": "7d", "30d": "30d" };

/** The hit ratio's denominator, which the edge's log shape forces (control-plane CP5). */
export const HIT_RATIO_NOTE = "Edge cache hits over edge hits plus origin requests. The edge logs only its hits, so the origin's own count stands for the misses.";

export interface TrafficSeries {
  /** Epoch seconds, one per step. */
  timestamps: number[];
  requests: (number | null)[];
  hitRatio: (number | null)[];
  bytes: (number | null)[];
  rate4xx: (number | null)[];
  rate5xx: (number | null)[];
}

export interface TrafficView {
  series: TrafficSeries;
  totals: {
    requests: number;
    hitRatio: number | null;
    bytes: number;
    rate4xx: number | null;
    rate5xx: number | null;
    requests4xx: number;
    requests5xx: number;
  };
  /** No request in the range. */
  empty: boolean;
  historyDays: number;
  /** The plan keeps less history than the range asked for; the series starts later. */
  clamped: boolean;
}

const ratio = (part: bigint, whole: bigint): number | null => (whole > 0n ? Number(part) / Number(whole) : null);

function totalsOf(counts: StaticSiteTrafficCounts | undefined): TrafficView["totals"] {
  if (!counts) return { requests: 0, hitRatio: null, bytes: 0, rate4xx: null, rate5xx: null, requests4xx: 0, requests5xx: 0 };
  return {
    requests: Number(counts.requests),
    hitRatio: counts.cacheHitRatio ?? ratio(counts.cacheHits, counts.requests),
    bytes: Number(counts.bytes),
    rate4xx: counts.rate4xx ?? ratio(counts.requests4xx, counts.requests),
    rate5xx: counts.rate5xx ?? ratio(counts.requests5xx, counts.requests),
    requests4xx: Number(counts.requests4xx),
    requests5xx: Number(counts.requests5xx),
  };
}

/** GetStaticSiteTraffic's answer as the charts' series and the cards' figures. A step with no request has no ratio (a gap, not 0%). */
export function trafficView(res: GetStaticSiteTrafficResponse): TrafficView {
  const series: TrafficSeries = { timestamps: [], requests: [], hitRatio: [], bytes: [], rate4xx: [], rate5xx: [] };
  for (const point of res.points) {
    if (!point.start) continue;
    const t = totalsOf(point.counts);
    series.timestamps.push(Math.floor(timestampMs(point.start) / 1000));
    series.requests.push(t.requests);
    series.hitRatio.push(t.hitRatio);
    series.bytes.push(t.bytes);
    series.rate4xx.push(t.rate4xx);
    series.rate5xx.push(t.rate5xx);
  }
  const totals = totalsOf(res.totals);
  return { series, totals, empty: totals.requests === 0, historyDays: res.historyDays, clamped: res.historyClamped };
}

/** The upgrade note under a clamped range (design §3: 7 days free, 30 with paid hosting). */
export function historyNote(view: TrafficView): string | null {
  if (!view.clamped) return null;
  return `Your plan keeps ${view.historyDays} days of traffic history, so this shows the last ${view.historyDays}. Paid hosting keeps 30.`;
}
