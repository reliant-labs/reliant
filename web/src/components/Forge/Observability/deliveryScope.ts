// Copyright (c) 2025 Reliant Labs

/**
 * The pure rules of the delivery views: which environments a delivery request
 * covers, the before/after windows of a release comparison, and how a change
 * between them reads.
 *
 * ── THE SCOPE FOLLOWS THE READER ─────────────────────────────────────────────
 *
 * Release markers must describe the telemetry on screen, so the ledger request
 * takes the same org, project and environments as the charts. An attribute
 * filter on the environment (`deployment.environment.name is prod`, or the env
 * id) narrows the charts to it, so it narrows the markers too: it is mapped to
 * env ids through the environments the place knows. Every other filter (a
 * service, a version) means nothing to the ledger, which records promotions of
 * whole releases into environments, and is not sent.
 */

import type { DeliveryScope, Promotion } from "@/services/controlPlane/observability/deliveryTypes";
import type { AttributeFilter, ObsScope, TimeWindow } from "@/services/controlPlane/observability/types";

import type { ObsEnvironment } from "./obsPlace";

const ENV_NAME_KEY = "deployment.environment.name";
const ENV_ID_KEY = "reliant.env.id";

/**
 * The delivery scope of the telemetry scope on screen. Null when the filters
 * exclude every environment: there is nothing to ask the ledger for, and the
 * answer is "no releases", not "every environment's".
 */
export function deliveryScopeOf(scope: ObsScope, environments: readonly ObsEnvironment[]): DeliveryScope | null {
  const idOf = new Map(environments.map((env) => [env.name, env.id]));
  const ids = (filter: AttributeFilter): string[] => {
    const values = filter.op === "in" ? (filter.values ?? []) : [filter.value ?? ""];
    return filter.key === ENV_ID_KEY ? values : values.flatMap((name) => (idOf.has(name) ? [idOf.get(name)!] : []));
  };
  let envIds: string[] = [...scope.envIds];
  for (const filter of scope.filters) {
    if (filter.key !== ENV_NAME_KEY && filter.key !== ENV_ID_KEY) continue;
    if (filter.op === "eq" || filter.op === "in") {
      const wanted = ids(filter);
      envIds = envIds.length ? envIds.filter((id) => wanted.includes(id)) : wanted;
      if (envIds.length === 0) return null;
    } else if (filter.op === "neq") {
      const unwanted = ids(filter);
      const from = envIds.length ? envIds : environments.map((env) => env.id);
      // Without the place's environments there is no list to subtract from: leave it wide.
      if (from.length === 0) continue;
      envIds = from.filter((id) => !unwanted.includes(id));
      if (envIds.length === 0) return null;
    }
  }
  return { orgId: scope.orgId, projectId: scope.projectId, envIds: [...new Set(envIds)] };
}

// ── Release comparison ───────────────────────────────────────────────────────

/** The widths a comparison offers on each side of a promotion. */
export const COMPARE_WINDOWS = ["15m", "30m", "1h", "6h", "24h"] as const;
export type CompareWindow = (typeof COMPARE_WINDOWS)[number];
export const DEFAULT_COMPARE_WINDOW: CompareWindow = "30m";

export const COMPARE_WINDOW_MS: Record<CompareWindow, number> = {
  "15m": 15 * 60_000,
  "30m": 30 * 60_000,
  "1h": 60 * 60_000,
  "6h": 6 * 60 * 60_000,
  "24h": 24 * 60 * 60_000,
};

export function compareWindowOf(param: string | undefined): CompareWindow {
  return (COMPARE_WINDOWS as readonly string[]).includes(param ?? "") ? (param as CompareWindow) : DEFAULT_COMPARE_WINDOW;
}

/**
 * The URL's `compare`: one promotion, written so it can be found again by
 * asking the ledger for that environment around that moment —
 * `<envId>~<promotedAtMs>~<promotionId>`.
 */
export interface CompareRef {
  envId: string;
  promotedAtMs: number;
  promotionId: string;
}

export function compareParam(p: Pick<Promotion, "envId" | "promotedAtMs" | "promotionId">): string {
  return `${p.envId}~${Math.round(p.promotedAtMs)}~${p.promotionId}`;
}

export function parseCompareParam(param: string | undefined): CompareRef | null {
  if (!param) return null;
  const parts = param.split("~");
  if (parts.length < 3) return null;
  const promotionId = parts.pop()!;
  const at = Number(parts.pop());
  const envId = parts.join("~");
  if (!envId || !promotionId || !Number.isFinite(at) || at <= 0) return null;
  return { envId, promotedAtMs: at, promotionId };
}

/** Why the windows are shorter than asked. */
export type ComparisonClamp = "now" | "next" | "previous" | null;

export interface ComparisonWindows {
  before: TimeWindow;
  after: TimeWindow;
  /** Both sides have this length, so rates and percentiles compare like for like. */
  lengthMs: number;
  /** What shortened them, if anything: too little time since, or a neighbouring promotion. */
  clampedBy: ComparisonClamp;
  /** Less than a minute on a side: nothing worth comparing yet. */
  tooShort: boolean;
}

/** Under this, a side holds too little traffic to say anything. */
export const MIN_COMPARE_MS = 60_000;

/**
 * Equal windows either side of a promotion at `atMs`. Each side stops short of
 * anything that would mix another release into it: the present (the after
 * side cannot reach the future), the next promotion into the same environment
 * (after it, a third release is running) and the previous one (before it, a
 * release older than the one replaced was).
 */
export function comparisonWindows(
  atMs: number,
  widthMs: number,
  nowMs: number,
  neighbours: { previousMs?: number | null; nextMs?: number | null } = {}
): ComparisonWindows {
  const limits: [number, ComparisonClamp][] = [
    [widthMs, null],
    [nowMs - atMs, "now"],
    ...(neighbours.nextMs != null ? ([[neighbours.nextMs - atMs, "next"]] as [number, ComparisonClamp][]) : []),
    ...(neighbours.previousMs != null ? ([[atMs - neighbours.previousMs, "previous"]] as [number, ComparisonClamp][]) : []),
  ];
  const [length, clampedBy] = limits.reduce((shortest, candidate) => (candidate[0] < shortest[0] ? candidate : shortest));
  const lengthMs = Math.max(0, length);
  return {
    before: { startMs: atMs - lengthMs, endMs: atMs },
    after: { startMs: atMs, endMs: atMs + lengthMs },
    lengthMs,
    clampedBy: lengthMs < widthMs ? clampedBy : null,
    tooShort: lengthMs < MIN_COMPARE_MS,
  };
}

/** The promotions into the same environment just before and just after `target`. */
export function neighboursOf(target: Pick<Promotion, "envId" | "promotedAtMs" | "promotionId">, promotions: readonly Promotion[]) {
  const same = promotions.filter((p) => p.envId === target.envId && p.promotionId !== target.promotionId);
  const previous = same.filter((p) => p.promotedAtMs < target.promotedAtMs).at(-1) ?? null;
  const next = same.find((p) => p.promotedAtMs > target.promotedAtMs) ?? null;
  return { previous, next };
}

/** How a figure moved, for colouring: which way, and whether that way is worse. */
export type Movement = "up-bad" | "up-good" | "down-bad" | "down-good" | "flat" | "none";

/**
 * Before → after for a figure where `higherIsWorse` (latency, error rate,
 * memory) or better (throughput is neither: pass `null` to only say which way).
 * A change within `tolerance` (relative) is flat: noise is not a finding.
 */
export function movement(before: number | null, after: number | null, higherIsWorse: boolean | null, tolerance = 0.1): Movement {
  if (before == null || after == null || !Number.isFinite(before) || !Number.isFinite(after)) return "none";
  const base = Math.abs(before);
  const change = after - before;
  if (base === 0 ? change === 0 : Math.abs(change) / base <= tolerance) return "flat";
  if (higherIsWorse === null) return change > 0 ? "up-good" : "down-good";
  if (change > 0) return higherIsWorse ? "up-bad" : "up-good";
  return higherIsWorse ? "down-good" : "down-bad";
}

/** "+42%", "−8%", or a percentage-point change for a figure that is itself a share. */
export function changeText(before: number | null, after: number | null, kind: "relative" | "points" = "relative"): string {
  if (before == null || after == null || !Number.isFinite(before) || !Number.isFinite(after)) return "";
  if (kind === "points") {
    const pts = (after - before) * 100;
    if (Math.abs(pts) < 0.05) return "±0 pts";
    return `${pts > 0 ? "+" : "−"}${Math.abs(pts).toFixed(Math.abs(pts) < 1 ? 2 : 1)} pts`;
  }
  if (before === 0) return after === 0 ? "±0%" : "new";
  const pct = ((after - before) / Math.abs(before)) * 100;
  if (Math.abs(pct) < 0.5) return "±0%";
  return `${pct > 0 ? "+" : "−"}${Math.round(Math.abs(pct))}%`;
}

// ── DORA ─────────────────────────────────────────────────────────────────────

/** The DORA windows offered: a week, a month, a quarter (the server allows up to 366 days). */
export const DORA_WINDOWS = ["7d", "30d", "90d"] as const;
export type DoraWindow = (typeof DORA_WINDOWS)[number];
export const DEFAULT_DORA_WINDOW: DoraWindow = "30d";
export const DORA_WINDOW_DAYS: Record<DoraWindow, number> = { "7d": 7, "30d": 30, "90d": 90 };
export const DORA_WINDOW_LABELS: Record<DoraWindow, string> = { "7d": "Last 7 days", "30d": "Last 30 days", "90d": "Last 90 days" };

export function doraWindowOf(param: string | undefined): DoraWindow {
  return (DORA_WINDOWS as readonly string[]).includes(param ?? "") ? (param as DoraWindow) : DEFAULT_DORA_WINDOW;
}

/** The N a reader can pick for "rolled back within N hours", all inside the server's 1–168. */
export const FAILURE_HOURS_CHOICES = [1, 4, 12, 24, 48, 72, 168] as const;

/** A failure window from the URL, clamped into the server's bounds; absent or invalid is the server's 24. */
export function failureHoursOf(param: number | undefined): number {
  if (param === undefined || !Number.isFinite(param)) return 24;
  return Math.min(168, Math.max(1, Math.round(param)));
}

/** "3h 12m", "2d 4h", "45m", "30s" — a span a person reads, for lead time and restore time. */
export function formatSpan(ms: number | null): string {
  if (ms == null || !Number.isFinite(ms)) return "—";
  const s = Math.round(ms / 1000);
  if (s < 60) return `${s}s`;
  const m = Math.round(s / 60);
  if (m < 60) return `${m}m`;
  const h = Math.floor(m / 60);
  if (h < 48) return m % 60 ? `${h}h ${m % 60}m` : `${h}h`;
  const d = Math.floor(h / 24);
  return h % 24 ? `${d}d ${h % 24}h` : `${d}d`;
}

/** Deployments per day as a reader says it: "2.4 a day", or "1 every 3.5 days" below one. */
export function formatPerDay(perDay: number): string {
  if (!Number.isFinite(perDay) || perDay <= 0) return "None";
  if (perDay >= 1) return `${perDay >= 10 ? Math.round(perDay) : Number(perDay.toFixed(1))} a day`;
  const every = 1 / perDay;
  return `1 every ${every >= 10 ? Math.round(every) : Number(every.toFixed(1))} days`;
}

/** The commit as it is usually read: the first 7 characters. */
export function shortCommit(commit: string): string {
  return /^[0-9a-f]{8,}$/i.test(commit) ? commit.slice(0, 7) : commit;
}
