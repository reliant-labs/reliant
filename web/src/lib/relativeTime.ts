// Copyright (c) 2025 Reliant Labs

/**
 * Relative and absolute renderings of an RFC3339 timestamp, for surfaces that
 * show both past events ("3 hours ago") and future ones ("in 20 minutes") —
 * the automation pages show a last run and a next run side by side, so a
 * past-only "3h" formatter is not enough.
 */

const UNITS: Array<[Intl.RelativeTimeFormatUnit, number]> = [
  ["year", 365 * 24 * 60 * 60 * 1000],
  ["month", 30 * 24 * 60 * 60 * 1000],
  ["week", 7 * 24 * 60 * 60 * 1000],
  ["day", 24 * 60 * 60 * 1000],
  ["hour", 60 * 60 * 1000],
  ["minute", 60 * 1000],
];

const relativeFormat = new Intl.RelativeTimeFormat("en", { numeric: "auto" });

/** "in 5 minutes", "3 hours ago", "yesterday", "just now". Empty for an unparseable input. */
export function formatRelativeTime(timestamp: string, now: number = Date.now()): string {
  const time = Date.parse(timestamp);
  if (Number.isNaN(time)) return "";
  const diff = time - now;
  if (Math.abs(diff) < 45_000) return diff >= 0 ? "in a moment" : "just now";
  for (const [unit, size] of UNITS) {
    if (Math.abs(diff) >= size || unit === "minute") {
      return relativeFormat.format(Math.round(diff / size), unit);
    }
  }
  return "";
}

/** The full local date and time, for a tooltip or an events table. */
export function formatAbsoluteTime(timestamp: string): string {
  const time = Date.parse(timestamp);
  if (Number.isNaN(time)) return timestamp;
  return new Date(time).toLocaleString(undefined, {
    dateStyle: "medium",
    timeStyle: "short",
  });
}
