// Copyright (c) 2025 Reliant Labs

/**
 * "Next runs: Mon 9:00, Tue 9:00, Wed 9:00 (Pacific Time)" — concrete next
 * times for the schedule sentence, and friendly time-zone names.
 *
 * Only the presets are computed here (hourly, daily, weekdays, weekly, every
 * N): they are what the sentence builds, and each is a fixed wall-clock rule.
 * A custom cron falls back to describeSchedule's words; the server's own
 * next_fire_at is the truth once the automation exists.
 */

import type { ScheduleFormState } from "../scheduleForm";

const WEEKDAY_SHORT = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"];

interface ZonedParts {
  year: number;
  month: number;
  day: number;
  hour: number;
  minute: number;
  weekday: number;
}

const formatters = new Map<string, Intl.DateTimeFormat>();

function formatterFor(timeZone: string): Intl.DateTimeFormat {
  let fmt = formatters.get(timeZone);
  if (!fmt) {
    fmt = new Intl.DateTimeFormat("en-US", {
      timeZone,
      hourCycle: "h23",
      year: "numeric",
      month: "numeric",
      day: "numeric",
      hour: "numeric",
      minute: "numeric",
      weekday: "short",
    });
    formatters.set(timeZone, fmt);
  }
  return fmt;
}

function partsIn(date: Date, timeZone: string): ZonedParts {
  const parts = formatterFor(timeZone).formatToParts(date);
  const get = (type: string) => parts.find((p) => p.type === type)?.value ?? "";
  return {
    year: Number(get("year")),
    month: Number(get("month")),
    day: Number(get("day")),
    hour: Number(get("hour")) % 24,
    minute: Number(get("minute")),
    weekday: WEEKDAY_SHORT.indexOf(get("weekday")),
  };
}

/** The instant a wall-clock time in `timeZone` names (the earlier one across a DST overlap). */
function instantOf(year: number, month: number, day: number, hour: number, minute: number, timeZone: string): Date {
  const guess = Date.UTC(year, month - 1, day, hour, minute);
  let t = guess;
  // Two corrections converge for every real zone, including across a change.
  for (let i = 0; i < 2; i++) {
    const p = partsIn(new Date(t), timeZone);
    const shown = Date.UTC(p.year, p.month - 1, p.day, p.hour, p.minute);
    t += guess - shown;
  }
  return new Date(t);
}

function parseTime(time: string): { hour: number; minute: number } | null {
  const m = /^(\d{1,2}):(\d{2})$/.exec(time.trim());
  if (!m || Number(m[1]) > 23 || Number(m[2]) > 59) return null;
  return { hour: Number(m[1]), minute: Number(m[2]) };
}

/**
 * The next `count` runs of a preset schedule after `now`, or null when the
 * schedule is custom cron (or invalid) and only words can be shown.
 */
export function nextRuns(form: ScheduleFormState, timeZone: string, now: Date, count = 3): Date[] | null {
  const zone = timeZone.trim() || "UTC";
  try {
    new Intl.DateTimeFormat("en-US", { timeZone: zone });
  } catch {
    return null;
  }
  const out: Date[] = [];
  if (form.preset === "interval") {
    if (!Number.isInteger(form.intervalValue) || form.intervalValue < 1) return null;
    const step = form.intervalValue * (form.intervalUnit === "h" ? 3_600_000 : 60_000);
    // Temporal's interval schedules are aligned to the epoch.
    let t = Math.floor(now.getTime() / step) * step + step;
    while (out.length < count) {
      out.push(new Date(t));
      t += step;
    }
    return out;
  }
  if (form.preset === "hourly") {
    if (!Number.isInteger(form.minute) || form.minute < 0 || form.minute > 59) return null;
    const start = partsIn(now, zone);
    for (let h = 0; out.length < count && h < 72; h++) {
      const at = instantOf(start.year, start.month, start.day, start.hour + h, form.minute, zone);
      if (at > now) out.push(at);
    }
    return out;
  }
  if (form.preset === "daily" || form.preset === "weekdays" || form.preset === "weekly") {
    const time = parseTime(form.time);
    if (!time) return null;
    const today = partsIn(now, zone);
    for (let d = 0; out.length < count && d < 21; d++) {
      // Noon of the candidate day, so a DST shift never skips or repeats a date.
      const noon = instantOf(today.year, today.month, today.day + d, 12, 0, zone);
      const day = partsIn(noon, zone);
      if (form.preset === "weekdays" && (day.weekday === 0 || day.weekday === 6)) continue;
      if (form.preset === "weekly" && day.weekday !== form.weekday) continue;
      const at = instantOf(day.year, day.month, day.day, time.hour, time.minute, zone);
      if (at > now) out.push(at);
    }
    return out;
  }
  return null;
}

/** "Mon 9:00", in the schedule's own zone. */
export function formatRun(date: Date, timeZone: string): string {
  const p = partsIn(date, timeZone.trim() || "UTC");
  return `${WEEKDAY_SHORT[p.weekday]} ${p.hour}:${String(p.minute).padStart(2, "0")}`;
}

/** "Pacific Time", "Central European Time", "UTC" — the zone as a person names it. */
export function friendlyZoneName(timeZone: string): string {
  const zone = timeZone.trim();
  if (!zone || zone === "UTC" || zone === "Etc/UTC") return "UTC";
  try {
    const part = new Intl.DateTimeFormat("en-US", { timeZone: zone, timeZoneName: "longGeneric" })
      .formatToParts(new Date())
      .find((p) => p.type === "timeZoneName");
    if (part?.value && !/^GMT[+-]/.test(part.value)) return part.value;
  } catch {
    // An unknown zone is shown as typed; validation says what is wrong.
  }
  return zone;
}

/** "Los Angeles" from "America/Los_Angeles". */
export function zoneCity(timeZone: string): string {
  const last = timeZone.split("/").pop() ?? timeZone;
  return last.replace(/_/g, " ");
}

export interface ZoneOption {
  zone: string;
  /** "Pacific Time — Los Angeles" */
  label: string;
}

let zoneOptionsCache: ZoneOption[] | null = null;

/** Every zone the runtime knows, as friendly options; built once per session. */
export function zoneOptions(): ZoneOption[] {
  if (zoneOptionsCache) return zoneOptionsCache;
  let zones: string[];
  try {
    zones = Intl.supportedValuesOf("timeZone");
  } catch {
    zones = [];
  }
  if (!zones.includes("UTC")) zones = ["UTC", ...zones];
  zoneOptionsCache = zones.map((zone) => {
    const name = friendlyZoneName(zone);
    return { zone, label: zone === "UTC" ? "UTC" : name === zone ? zone : `${name} — ${zoneCity(zone)}` };
  });
  return zoneOptionsCache;
}
