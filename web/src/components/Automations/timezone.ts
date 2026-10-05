// Copyright (c) 2025 Reliant Labs

/**
 * Time-zone validation for the automation form.
 *
 * Kept out of scheduleForm.ts, which is pure on purpose ("no zone lookups"):
 * this asks the runtime's tz database. It is the same question the server
 * asks (Go's time.LoadLocation), so a zone accepted here is one the server
 * will accept; the server stays the final judge.
 *
 * `Intl.DateTimeFormat` is the check, not `Intl.supportedValuesOf`: the
 * latter lists canonical names only, and would reject valid aliases a person
 * may type or a stored trigger may carry ("UTC", "US/Eastern").
 */

/** An error for the time-zone field, or null when the zone is valid. */
export function validateTimezone(zone: string): string | null {
  const trimmed = zone.trim();
  if (!trimmed) return null; // empty means UTC
  try {
    new Intl.DateTimeFormat("en-US", { timeZone: trimmed });
    return null;
  } catch {
    return `"${trimmed}" is not a time zone. Use an IANA name such as America/New_York or UTC.`;
  }
}
