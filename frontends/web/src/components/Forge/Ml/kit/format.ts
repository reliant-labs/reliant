// Copyright (c) 2026 Reliant Labs

/**
 * How the ML tab writes numbers: GPU-hours, money, ratios, steps, bytes,
 * durations, and the generated messages' int64s and Timestamps. One place, so
 * every page reads "1,204 GPU-h · $5.1k" the same way.
 *
 * NOT COLLECTED IS NOT ZERO. A NaN (the contract's "not collected" for a
 * headline double) formats as "—" everywhere here; callers that can name the
 * missing source show <NotCollected> instead.
 */

import { timestampMs, type Timestamp } from "@bufbuild/protobuf/wkt";

/** An int64 (bigint) or a number, as a number: counts and sizes stay far below 2^53. */
export const num = (value: bigint | number | undefined | null): number => (value === undefined || value === null ? 0 : Number(value));

/** A Timestamp as epoch ms; the zero or absent one is null, never 1970. */
export function tsMs(ts: Timestamp | undefined): number | null {
  if (!ts) return null;
  const value = timestampMs(ts);
  return value > 0 ? value : null;
}

/** Whether a headline double was collected (NaN is the contract's "not collected"). */
export const has = (value: number | undefined | null): value is number => typeof value === "number" && Number.isFinite(value);

const DASH = "—";

export function fmtInt(value: number | bigint | undefined | null): string {
  if (value === undefined || value === null) return DASH;
  const n = Number(value);
  return has(n) ? Math.round(n).toLocaleString("en-US") : DASH;
}

/** 41200 → "41.2k", 2_700_000_000 → "2.7B". */
export function fmtCompact(value: number | bigint | undefined | null, digits = 1): string {
  if (value === undefined || value === null) return DASH;
  const n = Number(value);
  if (!has(n)) return DASH;
  const abs = Math.abs(n);
  const cut = (d: number, s: string) => `${(n / d).toFixed(digits).replace(/\.0+$/, "")}${s}`;
  if (abs >= 1e12) return cut(1e12, "T");
  if (abs >= 1e9) return cut(1e9, "B");
  if (abs >= 1e6) return cut(1e6, "M");
  if (abs >= 1e3) return cut(1e3, "k");
  return abs < 10 && !Number.isInteger(n) ? n.toFixed(2) : `${Math.round(n)}`;
}

/** 0.834 → "83%"; `digits` for "83.4%". */
export function fmtPct(ratio: number | undefined | null, digits = 0): string {
  return has(ratio) ? `${(ratio * 100).toFixed(digits)}%` : DASH;
}

/** 5100 → "$5.1k", 41 → "$41", 0.41 → "$0.41". */
export function fmtUsd(value: number | undefined | null): string {
  if (!has(value)) return DASH;
  const abs = Math.abs(value);
  if (abs >= 1000) return `$${fmtCompact(value)}`;
  if (abs >= 100) return `$${Math.round(value)}`;
  if (abs >= 10) return `$${value.toFixed(0)}`;
  return `$${value.toFixed(2)}`;
}

/** 1204.3 → "1,204 GPU-h", 3.25 → "3.3 GPU-h". */
export function fmtGpuHours(value: number | undefined | null): string {
  if (!has(value)) return DASH;
  return `${value >= 100 ? Math.round(value).toLocaleString("en-US") : value.toFixed(1)} GPU-h`;
}

/** Bytes, binary: 212 GiB. */
export function fmtBytes(value: number | bigint | undefined | null): string {
  if (value === undefined || value === null) return DASH;
  let n = Number(value);
  if (!has(n)) return DASH;
  const units = ["B", "KiB", "MiB", "GiB", "TiB", "PiB"];
  let i = 0;
  while (n >= 1024 && i < units.length - 1) {
    n /= 1024;
    i++;
  }
  return `${n >= 100 || i === 0 ? Math.round(n) : n.toFixed(1)} ${units[i]}`;
}

/** Seconds as "6h 10m", "4m 12s", "38s", "820 ms". */
export function fmtDuration(seconds: number | undefined | null): string {
  if (!has(seconds)) return DASH;
  if (seconds < 1) return `${Math.round(seconds * 1000)} ms`;
  if (seconds < 60) return `${seconds < 10 ? seconds.toFixed(1) : Math.round(seconds)}s`;
  const m = Math.floor(seconds / 60);
  if (m < 60) return `${m}m ${Math.round(seconds % 60)}s`;
  const h = Math.floor(m / 60);
  if (h < 48) return `${h}h ${m % 60}m`;
  return `${Math.floor(h / 24)}d ${h % 24}h`;
}

/** Milliseconds as "411 ms" or "4.1 s". */
export function fmtMs(value: number | undefined | null): string {
  if (!has(value)) return DASH;
  if (value >= 1000) return `${(value / 1000).toFixed(value >= 10_000 ? 0 : 1)} s`;
  return value < 10 ? `${value.toFixed(1)} ms` : `${Math.round(value)} ms`;
}

/** How long ago `ms` was, against `now`: "9h ago", "3d ago", "just now". */
export function fmtAgo(ms: number | null | undefined, now: number): string {
  if (!ms) return DASH;
  const s = Math.max(0, (now - ms) / 1000);
  if (s < 60) return "just now";
  if (s < 3600) return `${Math.floor(s / 60)}m ago`;
  if (s < 172_800) return `${Math.floor(s / 3600)}h ago`;
  return `${Math.floor(s / 86400)}d ago`;
}

/** A wall-clock time: "Nov 26, 14:05". */
export function fmtTime(ms: number | null | undefined): string {
  if (!ms) return DASH;
  return new Date(ms).toLocaleString("en-US", { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit", hour12: false });
}

/** A step: 41200 → "41.2k"; exact under 10k. */
export const fmtStep = (step: number | bigint | undefined | null): string => {
  const n = num(step ?? 0);
  return n < 10_000 ? fmtInt(n) : fmtCompact(n);
};

/** A value in an MlSeries unit ("ratio", "percent", "ms", "bytes", "W", "C", ...). */
export function fmtUnit(value: number | null | undefined, unit: string): string {
  if (!has(value)) return DASH;
  return noNegativeZero(fmtUnitRaw(value, unit));
}

/** "-0.0 GB/s" → "0.0 GB/s": a value that rounds to zero carries no sign. */
export function noNegativeZero(text: string): string {
  return /^-0(?:\.0+)?(?![\d.])/.test(text) ? text.slice(1) : text;
}

function fmtUnitRaw(value: number, unit: string): string {
  switch (unit) {
    case "ratio":
      return fmtPct(value, value < 0.1 ? 1 : 0);
    case "percent":
      return `${value.toFixed(value < 10 ? 1 : 0)}%`;
    case "ms":
      return fmtMs(value);
    case "s":
      return fmtDuration(value);
    case "bytes":
      return fmtBytes(value);
    case "usd":
      return fmtUsd(value);
    case "W":
      return `${Math.round(value)} W`;
    case "C":
      return `${Math.round(value)}°C`;
    case "MHz":
      return `${Math.round(value)} MHz`;
    case "GB":
      return `${value.toFixed(value < 10 ? 1 : 0)} GB`;
    case "GB/s":
      return `${value.toFixed(value < 10 ? 1 : 0)} GB/s`;
    case "TFLOPs":
    case "TFLOP/s":
      return `${Math.round(value)} TFLOP/s`;
    case "tokens/s":
      return `${fmtCompact(value)} tok/s`;
    case "count":
      return fmtCompact(value);
    case "":
      return Math.abs(value) >= 1000 ? fmtCompact(value) : Math.abs(value) < 0.01 && value !== 0 ? value.toExponential(1) : value.toFixed(Math.abs(value) < 10 ? 3 : 1);
    default:
      return `${fmtCompact(value)} ${unit}`;
  }
}
