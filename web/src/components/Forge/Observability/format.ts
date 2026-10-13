// Copyright (c) 2025 Reliant Labs

/** Number and time formatting shared by every observability view. */

import type { Unit } from "@/services/controlPlane/observability/types";

/** 0.042 → "42µs", 12.3 → "12.3ms", 1530 → "1.53s", 95000 → "1m 35s". */
export function formatDuration(ms: number): string {
  if (!Number.isFinite(ms)) return "—";
  if (ms < 1) return `${Math.round(ms * 1000)}µs`;
  if (ms < 10) return `${trim(ms.toFixed(2))}ms`;
  if (ms < 1000) return `${trim(ms.toFixed(1))}ms`;
  if (ms < 60_000) return `${trim((ms / 1000).toFixed(2))}s`;
  const minutes = Math.floor(ms / 60_000);
  const seconds = Math.round((ms % 60_000) / 1000);
  return seconds ? `${minutes}m ${seconds}s` : `${minutes}m`;
}

/** Requests per second, with a unit a reader can say aloud. */
export function formatRate(perSecond: number): string {
  if (!Number.isFinite(perSecond)) return "—";
  if (perSecond === 0) return "0/s";
  if (perSecond < 1) return `${trim((perSecond * 60).toFixed(1))}/min`;
  if (perSecond < 1000) return `${trim(perSecond.toFixed(1))}/s`;
  return `${trim((perSecond / 1000).toFixed(1))}k/s`;
}

/** A fraction in [0, 1] as a percentage. Small non-zero rates keep a digit so 0.04% does not read as 0%. */
export function formatPercent(fraction: number): string {
  if (!Number.isFinite(fraction)) return "—";
  const pct = fraction * 100;
  if (pct === 0) return "0%";
  if (pct < 0.1) return "<0.1%";
  if (pct < 10) return `${trim(pct.toFixed(1))}%`;
  return `${Math.round(pct)}%`;
}

export function formatCount(value: number): string {
  if (!Number.isFinite(value)) return "—";
  if (Math.abs(value) < 1000) return trim(value.toFixed(value % 1 === 0 ? 0 : 2));
  if (Math.abs(value) < 1_000_000) return `${trim((value / 1000).toFixed(1))}k`;
  if (Math.abs(value) < 1_000_000_000) return `${trim((value / 1_000_000).toFixed(1))}M`;
  return `${trim((value / 1_000_000_000).toFixed(1))}B`;
}

export function formatBytes(value: number): string {
  if (!Number.isFinite(value)) return "—";
  const units = ["B", "KB", "MB", "GB", "TB"];
  let scaled = value;
  let index = 0;
  while (Math.abs(scaled) >= 1024 && index < units.length - 1) {
    scaled /= 1024;
    index += 1;
  }
  return `${trim(scaled.toFixed(index === 0 ? 0 : 1))} ${units[index]}`;
}

/** A panel's units, plus the per-second rate the overview draws and the CPU cores workloads report. */
export type ValueUnit = Unit | "rate" | "cores";

/** A value in its declared unit. */
export function formatValue(value: number | null | undefined, unit: ValueUnit | undefined): string {
  if (value === null || value === undefined) return "—";
  switch (unit) {
    case "rate":
      return formatRate(value);
    case "ms":
      return formatDuration(value);
    case "percent":
      return formatPercent(value);
    case "bytes":
      return formatBytes(value);
    case "cores":
      return formatCores(value);
    default:
      return formatCount(value);
  }
}

/** A log or span timestamp: local wall clock to the millisecond, the date only when it is not today. */
export function formatTimestamp(ms: number, now: number = Date.now()): string {
  const date = new Date(ms);
  const time = date.toLocaleTimeString(undefined, {
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
    hour12: false,
  });
  const millis = String(date.getMilliseconds()).padStart(3, "0");
  const sameDay = new Date(now).toDateString() === date.toDateString();
  if (sameDay) return `${time}.${millis}`;
  const day = date.toLocaleDateString(undefined, { month: "short", day: "numeric" });
  return `${day} ${time}.${millis}`;
}

function trim(fixed: string): string {
  return fixed.includes(".") ? fixed.replace(/\.?0+$/, "") : fixed;
}

/** CPU in cores: 0.25 → "250m", 1.5 → "1.5 cores" — Kubernetes' own millicore spelling below one core. */
export function formatCores(cores: number): string {
  if (!Number.isFinite(cores)) return "—";
  if (cores < 1) return `${Math.round(cores * 1000)}m`;
  return `${trim(cores.toFixed(2))} core${cores === 1 ? "" : "s"}`;
}

/** "3m ago", "2h ago", "4d ago" — for events, where the moment matters less than how recent it is. */
export function formatAgo(ms: number, now: number = Date.now()): string {
  const seconds = Math.max(0, Math.round((now - ms) / 1000));
  if (seconds < 60) return `${seconds}s ago`;
  if (seconds < 3600) return `${Math.round(seconds / 60)}m ago`;
  if (seconds < 86_400) return `${Math.round(seconds / 3600)}h ago`;
  return `${Math.round(seconds / 86_400)}d ago`;
}
