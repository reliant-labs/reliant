// Copyright (c) 2026 Reliant Labs

/**
 * Pure reading of `ListOrganizationUsage` for the organization usage and
 * billing dashboards.
 *
 * Three rules from the contract shape every function here:
 *
 *   ABSENT IS NEVER ZERO. A point's dollar amount is optional: absent means
 *   not measured (dev boxes carry seconds and no price; static egress, builds
 *   and the registry carry usage and no price). An unavailable source returns
 *   no points at all. So a total exists only when at least one point carried
 *   a figure; otherwise the answer is "not priced", "no usage" or
 *   "unavailable", and the view says which.
 *
 *   BASES ARE NOT ADDED. Managed LLM is settled charges (actual); hosting and
 *   observability are list-price equivalents before allowances (an estimate).
 *   They are never summed into one total, and a chart only stacks series of
 *   one basis.
 *
 *   OMITTED BUCKETS ARE NO USAGE, BUT ONLY WHERE THE SOURCE LOOKED. Inside a
 *   source's measured window a missing bucket is a real zero; outside it, or
 *   for an unavailable source, the bucket is unknown (a gap on the chart).
 */

import type { OrganizationCostSummary } from "@/services/controlPlane/observability/organizationCostsApi";
import type {
  OrganizationUsage,
  OrganizationUsageCoverage,
  OrganizationUsageInventoryItem,
  OrganizationUsagePoint,
  OrganizationUsageRate,
  OrganizationUsageRequest,
  UsageGranularity,
} from "@/services/controlPlane/observability/organizationUsageApi";

import { formatBytes, formatCount } from "../format";

export const DAY_MS = 86_400_000;
const HOUR_MS = 3_600_000;

// ── Ranges ──────────────────────────────────────────────────────────────────

export type UsageRangeId = "7d" | "30d" | "90d" | "12mo";

export const USAGE_RANGES: { value: UsageRangeId; label: string }[] = [
  { value: "7d", label: "7 days" },
  { value: "30d", label: "30 days" },
  { value: "90d", label: "90 days" },
  { value: "12mo", label: "12 months" },
];

export function isUsageRangeId(value: unknown): value is UsageRangeId {
  return USAGE_RANGES.some((range) => range.value === value);
}

/** UTC midnight at or before `ms`. */
export function utcDayStart(ms: number): number {
  return Math.floor(ms / DAY_MS) * DAY_MS;
}

/** The first instant of the UTC month `offset` months from the one holding `ms`. */
export function utcMonthStart(ms: number, offset = 0): number {
  const d = new Date(ms);
  return Date.UTC(d.getUTCFullYear(), d.getUTCMonth() + offset, 1);
}

/**
 * A preset as a request window. Every preset holds more than one bucket and
 * ends at the end of today (or this month), so the newest, still-filling
 * bucket is in range and the chart never opens on a single point.
 */
export function usageWindow(range: UsageRangeId, nowMs: number): { startMs: number; endMs: number; granularity: UsageGranularity } {
  if (range === "12mo") return { startMs: utcMonthStart(nowMs, -11), endMs: utcMonthStart(nowMs, 1), granularity: "month" };
  const days = range === "7d" ? 7 : range === "30d" ? 30 : 90;
  const end = utcDayStart(nowMs) + DAY_MS;
  return { startMs: end - days * DAY_MS, endMs: end, granularity: "day" };
}

/** The current UTC calendar month as one bucket: period-to-date. */
export function monthToDateRequest(orgId: string, nowMs: number): OrganizationUsageRequest {
  return { orgId, startMs: utcMonthStart(nowMs), endMs: utcMonthStart(nowMs, 1), granularity: "month", groupBy: "category" };
}

// ── Categories and coverage ─────────────────────────────────────────────────

export const USAGE_CATEGORIES = ["managed_llm", "machines", "hosted_apps", "databases", "static_hosting", "hosted_other", "observability"] as const;
export type UsageCategory = (typeof USAGE_CATEGORIES)[number];

const CATEGORY_LABELS: Record<string, string> = {
  managed_llm: "Managed LLM",
  machines: "Dev boxes",
  hosted_apps: "Hosted apps",
  databases: "Databases",
  static_hosting: "Static sites",
  hosted_other: "Builds and registry",
  observability: "Observability",
};

export function categoryLabel(category: string): string {
  return CATEGORY_LABELS[category] ?? (category || "Unspecified");
}

const SOURCE_LABELS: Record<string, string> = {
  managed_llm: "Managed LLM",
  machines: "Dev boxes",
  hosted_infrastructure: "Hosted infrastructure",
  observability: "Observability",
  inventory: "Current inventory",
};

export function sourceLabel(source: string): string {
  return SOURCE_LABELS[source] ?? (source || "Unspecified source");
}

/** The coverage entry a point category is measured by. */
export function sourceOf(category: string): string {
  if (category === "hosted_apps" || category === "databases" || category === "static_hosting" || category === "hosted_other") return "hosted_infrastructure";
  return category;
}

export function coverageOf(usage: OrganizationUsage, category: string): OrganizationUsageCoverage | undefined {
  const source = sourceOf(category);
  return usage.coverage.find((c) => c.category === source);
}

export type Completeness = "complete" | "partial" | "unavailable" | "unknown";

export function completenessOf(value: string | undefined): Completeness {
  return value === "complete" || value === "partial" || value === "unavailable" ? value : "unknown";
}

export function completenessLabel(value: string | undefined): string {
  return { complete: "Complete", partial: "Partial", unavailable: "Unavailable", unknown: "Not reported" }[completenessOf(value)];
}

/** Metered dimensions in words ("infra_cpu" is "CPU"). */
export function dimensionLabel(id: string): string {
  const named: Record<string, string> = { infra_cpu: "CPU", infra_memory: "memory", infra_storage: "storage", egress_kib: "egress", build_cpu_milli: "build CPU", build_mem_mib: "build memory", registry_storage_gib: "registry storage" };
  return named[id] ?? id;
}

// ── Money ───────────────────────────────────────────────────────────────────

export type Basis = "actual" | "list_equivalent";

export function basisLabel(basis: string): string {
  return basis === "actual" ? "Settled charges" : basis === "list_equivalent" ? "Estimate at list price" : "Basis not reported";
}

/**
 * USD nanos to dollars and cents, exactly (bigint; Number(int64) loses
 * precision). Absent is "n/a". A positive amount under half a cent is "<$0.01",
 * so a real charge never reads as zero.
 */
export function formatUsd(nanos: bigint | undefined): string {
  if (nanos === undefined) return "n/a";
  const negative = nanos < 0n;
  const magnitude = negative ? -nanos : nanos;
  if (magnitude > 0n && magnitude < 5_000_000n) return negative ? "-<$0.01" : "<$0.01";
  const cents = (magnitude + 5_000_000n) / 10_000_000n;
  const dollars = (cents / 100n).toLocaleString("en-US");
  const fraction = (cents % 100n).toString().padStart(2, "0");
  return `${negative ? "-" : ""}$${dollars}.${fraction}`;
}

/** Nanos as a plain dollar number for a chart axis (precision is a pixel's worth). */
export function nanosToDollars(nanos: bigint): number {
  return Number(nanos) / 1e9;
}

/** What one category's dollar figure is over a response, said honestly. */
export type CategoryAmount =
  | { kind: "amount"; nanos: bigint; basis: Basis; unpricedDimensions: string[] }
  /** Usage was measured but carries no price (dev boxes, static egress, builds). */
  | { kind: "unpriced" }
  /** The source looked and found nothing in range. */
  | { kind: "no-usage" }
  /** The source could not be read, or was not reported. */
  | { kind: "unavailable" };

export function pointAmount(point: OrganizationUsagePoint): Amount | undefined {
  if (point.actualChargesUsdNanos !== undefined) return { nanos: point.actualChargesUsdNanos, basis: "actual", unpricedDimensions: [...point.unpricedDimensions] };
  if (point.listEquivalentUsdNanos !== undefined) return { nanos: point.listEquivalentUsdNanos, basis: "list_equivalent", unpricedDimensions: [...point.unpricedDimensions] };
  return undefined;
}

export interface Amount {
  nanos: bigint;
  basis: Basis;
  /**
   * Metered dimensions the plan declares no rate for, from every point
   * summed (a point with only unpriced usage has no amount but still counts):
   * a non-empty list makes `nanos` a lower bound.
   */
  unpricedDimensions: string[];
}

/** The dimensions a set of points leaves unpriced, sorted and de-duplicated. */
export function unpricedOf(points: readonly OrganizationUsagePoint[]): string[] {
  return [...new Set(points.flatMap((p) => p.unpricedDimensions))].sort();
}

/** Sum the figures of some points; undefined unless at least one point carried one. Settled charges win over list equivalents. */
export function sumAmounts(points: readonly OrganizationUsagePoint[]): Amount | undefined {
  let actual: bigint | undefined;
  let list: bigint | undefined;
  for (const point of points) {
    if (point.actualChargesUsdNanos !== undefined) actual = (actual ?? 0n) + point.actualChargesUsdNanos;
    if (point.listEquivalentUsdNanos !== undefined) list = (list ?? 0n) + point.listEquivalentUsdNanos;
  }
  const unpricedDimensions = unpricedOf(points);
  if (actual !== undefined) return { nanos: actual, basis: "actual", unpricedDimensions };
  if (list !== undefined) return { nanos: list, basis: "list_equivalent", unpricedDimensions };
  return undefined;
}

export function categoryAmount(usage: OrganizationUsage, category: string): CategoryAmount {
  const coverage = coverageOf(usage, category);
  const points = usage.points.filter((p) => p.category === category);
  const sum = sumAmounts(points);
  if (sum) return { kind: "amount", ...sum };
  if (!coverage || completenessOf(coverage.completeness) === "unavailable") return { kind: "unavailable" };
  return points.length > 0 ? { kind: "unpriced" } : { kind: "no-usage" };
}

// ── Buckets and series ──────────────────────────────────────────────────────

function tsMs(ts: { seconds: bigint; nanos: number } | undefined): number | undefined {
  return ts ? Number(ts.seconds) * 1000 + Math.round(ts.nanos / 1e6) : undefined;
}

export function timestampMs(ts: { seconds: bigint; nanos: number } | undefined): number | undefined {
  return tsMs(ts);
}

/** "2026-10-25 12:00 UTC", or "Not reported". */
export function formatUtc(ts: { seconds: bigint; nanos: number } | undefined): string {
  const ms = tsMs(ts);
  if (ms === undefined || !Number.isFinite(ms)) return "Not reported";
  return new Date(ms).toISOString().slice(0, 16).replace("T", " ") + " UTC";
}

export function granularityOf(usage: OrganizationUsage): UsageGranularity {
  // OrganizationUsageGranularity.MONTH = 2; DAY and unspecified read as day.
  return usage.granularity === 2 ? "month" : "day";
}

function nextBucket(ms: number, granularity: UsageGranularity): number {
  return granularity === "month" ? utcMonthStart(ms, 1) : ms + DAY_MS;
}

/** Every bucket start of the response's effective range, in ms. */
export function bucketStarts(usage: OrganizationUsage): number[] {
  const start = tsMs(usage.start);
  const end = tsMs(usage.end);
  if (start === undefined || end === undefined || end <= start) return [];
  const granularity = granularityOf(usage);
  const out: number[] = [];
  for (let at = start; at < end && out.length < 500; at = nextBucket(at, granularity)) out.push(at);
  return out;
}

/** Whether the source behind `category` looked at the bucket starting at `startMs`. */
export function bucketCovered(usage: OrganizationUsage, category: string, startMs: number): boolean {
  const coverage = coverageOf(usage, category);
  if (!coverage || completenessOf(coverage.completeness) === "unavailable") return false;
  const end = nextBucket(startMs, granularityOf(usage));
  const from = tsMs(coverage.coverageStart) ?? tsMs(usage.start) ?? -Infinity;
  const through = tsMs(coverage.measuredThrough) ?? tsMs(coverage.coverageEnd) ?? tsMs(usage.end) ?? Infinity;
  return startMs < through && end > from;
}

export interface UsageSeries {
  key: string;
  label: string;
  values: (number | null)[];
}

/**
 * One series per category whose points carry `basis`, in dollars per bucket:
 * a measured zero inside the source's window, null (a gap) outside it.
 */
export function spendSeries(usage: OrganizationUsage, basis: Basis, categories: readonly string[] = USAGE_CATEGORIES): UsageSeries[] {
  const buckets = bucketStarts(usage);
  const out: UsageSeries[] = [];
  for (const category of categories) {
    const points = usage.points.filter((p) => p.category === category);
    const carries = points.some((p) => pointAmount(p)?.basis === basis);
    if (!carries) continue;
    const values = buckets.map((at) => {
      const inBucket = points.filter((p) => tsMs(p.bucketStart) === at);
      const sum = sumAmounts(inBucket);
      if (sum && sum.basis === basis) return nanosToDollars(sum.nanos);
      return bucketCovered(usage, category, at) ? 0 : null;
    });
    const bounded = unpricedOf(points).length > 0;
    out.push({ key: category, label: bounded ? `${categoryLabel(category)} (lower bound)` : categoryLabel(category), values });
  }
  return out;
}

/** One series per point detail (machine size, signal) of a quantity in one category; `scale` converts the native unit. */
export function quantitySeriesByDetail(usage: OrganizationUsage, category: string, quantityId: string, scale = 1): UsageSeries[] {
  const buckets = bucketStarts(usage);
  const points = usage.points.filter((p) => p.category === category && p.quantities.some((q) => q.id === quantityId));
  const details = [...new Set(points.map((p) => p.detail))].sort();
  return details.map((detail) => ({
    key: detail || "unspecified",
    label: detail || "Unspecified",
    values: buckets.map((at) => {
      const value = quantitySum(points.filter((p) => p.detail === detail && tsMs(p.bucketStart) === at), quantityId);
      if (value !== undefined) return value * scale;
      return bucketCovered(usage, category, at) ? 0 : null;
    }),
  }));
}

/** The sum of one quantity over some points; undefined when none reports it. */
export function quantitySum(points: readonly OrganizationUsagePoint[], quantityId: string): number | undefined {
  let total: number | undefined;
  for (const point of points) {
    for (const q of point.quantities) if (q.id === quantityId) total = (total ?? 0) + q.value;
  }
  return total;
}

/** A quantity's unit as the server named it, from the first point that reports it. */
export function quantityUnit(points: readonly OrganizationUsagePoint[], quantityId: string): string | undefined {
  for (const point of points) for (const q of point.quantities) if (q.id === quantityId) return q.unit;
  return undefined;
}

/** A native quantity in words: seconds as hours, KiB and bytes as sizes. */
export function formatQuantity(value: number | undefined, unit: string | undefined): string {
  if (value === undefined) return "n/a";
  switch (unit) {
    case "seconds":
      return `${formatHours(value / 3600)} h`;
    case "vcpu_hours":
      return `${formatHours(value)} vCPU-h`;
    case "gib_hours":
      return `${formatHours(value)} GiB-h`;
    case "kib":
      return formatBytes(value * 1024);
    case "bytes":
      return formatBytes(value);
    default:
      return formatCount(value);
  }
}

function formatHours(hours: number): string {
  return hours.toLocaleString("en-US", { maximumFractionDigits: hours < 100 ? 1 : 0 });
}

// ── Groups (environment, model) ─────────────────────────────────────────────

export interface GroupTotal {
  key: string;
  label: string;
  category: string;
  nanos: bigint;
  basis: Basis;
  unpricedDimensions: string[];
}

/**
 * Per (category, group_key) dollar totals of a grouped response, highest
 * first. Groups whose points carry no figure are left out (they are not $0);
 * an empty key is "Unattributed".
 */
export function groupTotals(usage: OrganizationUsage, categories: readonly string[]): GroupTotal[] {
  const cells = new Map<string, OrganizationUsagePoint[]>();
  for (const point of usage.points) {
    if (!categories.includes(point.category)) continue;
    const key = `${point.category}\u0000${point.groupKey}`;
    cells.set(key, [...(cells.get(key) ?? []), point]);
  }
  const out: GroupTotal[] = [];
  for (const points of cells.values()) {
    const sum = sumAmounts(points);
    if (!sum) continue;
    const first = points[0]!;
    out.push({ key: first.groupKey, label: first.groupLabel || first.groupKey || "Unattributed", category: first.category, ...sum });
  }
  return out.sort((a, b) => (b.nanos > a.nanos ? 1 : b.nanos < a.nanos ? -1 : a.label.localeCompare(b.label)));
}

// ── Inventory and rates ─────────────────────────────────────────────────────

/**
 * The inventory is its own source ("inventory" coverage), independent of the
 * usage history. A count of what exists now is shown only when that source was
 * read: an unavailable or unreported one means the count is unknown, never 0.
 */
export function inventoryCoverage(usage: OrganizationUsage): OrganizationUsageCoverage | undefined {
  return usage.coverage.find((c) => c.category === SOURCE_INVENTORY);
}

export function inventoryReadable(usage: OrganizationUsage): boolean {
  const level = completenessOf(inventoryCoverage(usage)?.completeness);
  return level === "complete" || level === "partial";
}

const SOURCE_INVENTORY = "inventory";

export function inventoryOf(usage: OrganizationUsage, category: string): OrganizationUsageInventoryItem[] {
  return usage.inventory.filter((item) => item.category === category);
}

export function inventoryCount(items: readonly OrganizationUsageInventoryItem[]): number {
  return items.reduce((total, item) => total + Number(item.count), 0);
}

/** Inventory counts by one key ("size", "state", "environmentName"). */
export function countBy(items: readonly OrganizationUsageInventoryItem[], key: "size" | "state" | "environmentName"): { key: string; count: number }[] {
  const counts = new Map<string, number>();
  for (const item of items) counts.set(item[key] || "", (counts.get(item[key] || "") ?? 0) + Number(item.count));
  return [...counts.entries()].map(([k, count]) => ({ key: k, count })).sort((a, b) => b.count - a.count || a.key.localeCompare(b.key));
}

export function rateOf(usage: OrganizationUsage, category: string): OrganizationUsageRate | undefined {
  return usage.spendRates.find((rate) => rate.category === category);
}

/** A projection is only honest after a day of measurement. */
export const PROJECTION_MIN_HOURS = 24;

/**
 * Month-to-date plus the trailing 7-day daily average over what is left of the
 * UTC month. Undefined without a month-to-date figure, an average, or 24
 * measured hours.
 */
export function projectedMonthEnd(monthToDate: bigint | undefined, rate: OrganizationUsageRate | undefined, nowMs: number): bigint | undefined {
  if (monthToDate === undefined || !rate || rate.averageDaily7dUsdNanos === undefined || rate.measuredHours < PROJECTION_MIN_HOURS) return undefined;
  const remainingSec = BigInt(Math.max(0, Math.round((utcMonthStart(nowMs, 1) - nowMs) / 1000)));
  return monthToDate + (rate.averageDaily7dUsdNanos * remainingSec) / 86_400n;
}

// ── Allowances (from GetOrganizationCostSummary) ────────────────────────────

export interface AllowanceRow {
  source: string;
  id: string;
  unit: string;
  included: number;
  consumed: number | undefined;
  projected: number | undefined;
  periodEndMs: number | undefined;
}

/**
 * Every included allowance of the cost summary, with a straight-line
 * projection to its period end once a day of the period has passed.
 */
export function allowanceRows(summary: OrganizationCostSummary, nowMs: number, sources?: readonly string[]): AllowanceRow[] {
  const rows: AllowanceRow[] = [];
  for (const source of summary.sources) {
    if (sources && !sources.includes(source.category)) continue;
    const period = summary.periods.find((p) => p.id === source.periodId);
    const start = tsMs(period?.start);
    const end = tsMs(period?.end);
    for (const allowance of source.allowances) {
      let projected: number | undefined;
      if (allowance.consumed !== undefined && start !== undefined && end !== undefined && nowMs - start >= PROJECTION_MIN_HOURS * HOUR_MS && end > start) {
        projected = (allowance.consumed * (end - start)) / Math.min(nowMs - start, end - start);
      }
      rows.push({ source: source.category, id: allowance.id, unit: allowance.unit, included: allowance.included, consumed: allowance.consumed, projected, periodEndMs: end });
    }
  }
  return rows;
}

/** Promotional credit granted per cost source, where the summary reports it. */
export function promotionalCredits(summary: OrganizationCostSummary): { source: string; nanos: bigint }[] {
  return summary.sources.flatMap((s) => (s.creditsUsdNanos === undefined ? [] : [{ source: s.category, nanos: s.creditsUsdNanos }]));
}

/** Limitations the server stated, per source, de-duplicated. */
export function limitationsOf(usage: OrganizationUsage): { source: string; completeness: string; limitations: string[] }[] {
  return usage.coverage.map((c) => ({ source: c.category, completeness: c.completeness, limitations: [...new Set(c.limitations)] }));
}
