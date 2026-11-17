// Copyright (c) 2025 Reliant Labs

/**
 * The time windows the observability area offers.
 *
 * A window is a PRESET ("last hour"), never two timestamps: a link to the last
 * hour should mean the last hour when it is opened, and a preset is what makes
 * live tail and Refresh a re-resolve rather than a URL rewrite.
 */

import type { TimeWindow } from "./types";

// "30d": the product dashboards declare 30 days (forge dashspec MaxDefaultRange), which the console used to round down to 7.
export const RANGE_PRESETS = ["15m", "1h", "6h", "24h", "7d", "30d"] as const;
export type RangePreset = (typeof RANGE_PRESETS)[number];

const RANGE_MS: Record<RangePreset, number> = {
  "15m": 15 * 60_000,
  "1h": 60 * 60_000,
  "6h": 6 * 60 * 60_000,
  "24h": 24 * 60 * 60_000,
  "7d": 7 * 24 * 60 * 60_000,
  "30d": 30 * 24 * 60 * 60_000,
};

export const RANGE_LABELS: Record<RangePreset, string> = {
  "15m": "Last 15 minutes",
  "1h": "Last hour",
  "6h": "Last 6 hours",
  "24h": "Last 24 hours",
  "7d": "Last 7 days",
  "30d": "Last 30 days",
};

export const DEFAULT_RANGE: RangePreset = "1h";

/** A preset's length in ms. */
export function rangeMs(range: RangePreset): number {
  return RANGE_MS[range];
}

/**
 * The longest window each kind of read serves (forge obsquery.MaxRange*):
 * traces, logs and what is computed from them (the service overview,
 * workloads, signals, exemplars) are kept 7 days; metrics, profiles,
 * dashboards' sql panels, issues and sessions 30. Every picker offers every
 * preset ("Last 30 days" included), so a view reading something kept for
 * less reads what there is (readRange) and says so (rangeCapNote).
 */
export type RangeRead = "traces" | "logs" | "overview" | "metrics" | "profiles" | "sql" | "issues" | "sessions";

export const READ_MAX_RANGE: Record<RangeRead, RangePreset> = {
  traces: "7d",
  logs: "7d",
  overview: "7d",
  metrics: "30d",
  profiles: "30d",
  sql: "30d",
  issues: "30d",
  sessions: "30d",
};

/** The range a read is asked for: the picked one, or the longest the read keeps when that is shorter. */
export function readRange(range: RangePreset, read: RangeRead): RangePreset {
  const max = READ_MAX_RANGE[read];
  return RANGE_MS[range] > RANGE_MS[max] ? max : range;
}

/**
 * What a capped view says beside its picker: "Logs are kept for 7 days, so
 * this shows the last 7 days." `subject` names what the view reads ("Logs
 * are"). Null when the picked range is covered.
 */
export function rangeCapNote(range: RangePreset, read: RangeRead, subject: string): string | null {
  const capped = readRange(range, read);
  if (capped === range) return null;
  return `${subject} kept for ${RANGE_LABELS[capped].replace(/^Last /, "")}, so this shows the ${RANGE_LABELS[capped].toLowerCase()}.`;
}

/** The window a preset names, ending at `now`. */
export function resolveWindow(preset: RangePreset, now: number = Date.now()): TimeWindow {
  return { startMs: now - RANGE_MS[preset], endMs: now };
}

export function rangeOf(param: string | undefined): RangePreset {
  return (RANGE_PRESETS as readonly string[]).includes(param ?? "") ? (param as RangePreset) : DEFAULT_RANGE;
}

const SPAN_UNIT_MS = { s: 1_000, m: 60_000, h: 3_600_000, d: 86_400_000 } as const;

/** A spec duration ("90m", "7d"; forge dashspec.ParseSpan) in ms, or undefined when it is not one. */
export function spanMs(span: string | undefined): number | undefined {
  const m = /^([1-9][0-9]{0,3})([smhd])$/.exec(span ?? "");
  return m ? Number(m[1]) * SPAN_UNIT_MS[m[2] as keyof typeof SPAN_UNIT_MS] : undefined;
}

/** The smallest preset that covers `ms`; the longest when none does (a preset is all the console offers). */
export function presetCovering(ms: number): RangePreset {
  return RANGE_PRESETS.find((preset) => RANGE_MS[preset] >= ms) ?? RANGE_PRESETS[RANGE_PRESETS.length - 1]!;
}

/** The range a dashboard opens on when the URL names none: its `defaultRange`, rounded up to a preset, else the console's. */
export function dashboardDefaultRange(defaultRange: string | undefined): RangePreset {
  const ms = spanMs(defaultRange);
  return ms === undefined ? DEFAULT_RANGE : presetCovering(ms);
}

/**
 * The range a panel with a fixed bucket (`minStep`: a sql panel grouped by
 * toDate, say) is read over. A window narrower than one bucket can only ever
 * return that single bucket, so it is widened to the smallest preset holding
 * two; the caller says so (`widened`) rather than drawing one point silently.
 */
export function rangeForBucket(range: RangePreset, minStep: string | undefined): { range: RangePreset; widened: boolean } {
  const bucket = spanMs(minStep);
  if (bucket === undefined || RANGE_MS[range] >= bucket) return { range, widened: false };
  const next = presetCovering(2 * bucket);
  return { range: next, widened: next !== range };
}

/** "1-day", "6-hour": how a panel names its bucket. */
export function bucketLabel(minStep: string | undefined): string {
  const m = /^([1-9][0-9]{0,3})([smhd])$/.exec(minStep ?? "");
  if (!m) return "";
  const unit = { s: "second", m: "minute", h: "hour", d: "day" }[m[2] as "s" | "m" | "h" | "d"];
  return `${m[1]}-${unit}`;
}
