// Copyright (c) 2025 Reliant Labs

/**
 * The time windows the observability area offers.
 *
 * A window is a PRESET ("last hour"), never two timestamps: a link to the last
 * hour should mean the last hour when it is opened, and a preset is what makes
 * live tail and Refresh a re-resolve rather than a URL rewrite.
 */

import type { TimeWindow } from "./types";

export const RANGE_PRESETS = ["15m", "1h", "6h", "24h", "7d"] as const;
export type RangePreset = (typeof RANGE_PRESETS)[number];

const RANGE_MS: Record<RangePreset, number> = {
  "15m": 15 * 60_000,
  "1h": 60 * 60_000,
  "6h": 6 * 60 * 60_000,
  "24h": 24 * 60 * 60_000,
  "7d": 7 * 24 * 60 * 60_000,
};

export const RANGE_LABELS: Record<RangePreset, string> = {
  "15m": "Last 15 minutes",
  "1h": "Last hour",
  "6h": "Last 6 hours",
  "24h": "Last 24 hours",
  "7d": "Last 7 days",
};

export const DEFAULT_RANGE: RangePreset = "1h";

/** The window a preset names, ending at `now`. */
export function resolveWindow(preset: RangePreset, now: number = Date.now()): TimeWindow {
  return { startMs: now - RANGE_MS[preset], endMs: now };
}

export function rangeOf(param: string | undefined): RangePreset {
  return (RANGE_PRESETS as readonly string[]).includes(param ?? "") ? (param as RangePreset) : DEFAULT_RANGE;
}
