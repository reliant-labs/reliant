// Copyright (c) 2025 Reliant Labs

/**
 * Future fire times of an automation's schedule, computed client-side for the
 * "Coming up" timeline (research/WORKFLOW_UI.md §7.2).
 *
 * The server hands us only the NEXT fire (`Trigger.next_fire_at`, from the
 * Temporal schedule). The timeline needs every fire in the next 24 hours, so
 * this enumerates them from the stored ScheduleSource, matching how Temporal
 * evaluates a ScheduleSpec rather than how classic cron does:
 *
 *   - Cron entries and the interval are a UNION.
 *   - Cron fields are ANDed. Temporal does not implement the classic special
 *     case that ORs day-of-month with day-of-week when both are restricted.
 *   - Wall-clock time is matched literally in the schedule's zone, with no DST
 *     adjustment: a wall time skipped by spring-forward does not fire, and one
 *     repeated by fall-back fires twice.
 *   - An interval fires at epoch + n * interval (no phase; the server sets
 *     none).
 *
 * Callers should still anchor on the server's `next_fire_at`: if this module
 * and the server disagree, the server is what will actually happen.
 *
 * Only the 5-field form the server accepts (validateCron in
 * internal/triggers/config.go) is parsed. Anything else enumerates nothing,
 * rather than a guess about when an unattended run fires.
 */

const MONTH_ALIASES: Record<string, number> = {
  JAN: 1,
  FEB: 2,
  MAR: 3,
  APR: 4,
  MAY: 5,
  JUN: 6,
  JUL: 7,
  AUG: 8,
  SEP: 9,
  OCT: 10,
  NOV: 11,
  DEC: 12,
};

const WEEKDAY_ALIASES: Record<string, number> = {
  SUN: 0,
  MON: 1,
  TUE: 2,
  WED: 3,
  THU: 4,
  FRI: 5,
  SAT: 6,
};

export interface CronSpec {
  minutes: Set<number>;
  hours: Set<number>;
  daysOfMonth: Set<number>;
  months: Set<number>;
  /** 0 = Sunday; a 7 in the expression is folded to 0. */
  weekdays: Set<number>;
}

function parseValue(token: string, aliases: Record<string, number> | undefined): number | null {
  if (/^\d+$/.test(token)) return Number(token);
  const alias = aliases?.[token.toUpperCase()];
  return alias ?? null;
}

/**
 * One cron field: comma-separated items, each "*", "N", "A-B", with an
 * optional "/STEP" ("*\/15", "9-17/2", "5/20" meaning 5 through max).
 */
function parseField(
  field: string,
  min: number,
  max: number,
  aliases?: Record<string, number>,
): Set<number> | null {
  const values = new Set<number>();
  for (const item of field.split(",")) {
    const [range, stepText, ...rest] = item.split("/");
    if (rest.length > 0 || range === undefined || range === "") return null;
    let step = 1;
    if (stepText !== undefined) {
      if (!/^\d+$/.test(stepText)) return null;
      step = Number(stepText);
      if (step < 1) return null;
    }
    let start: number;
    let end: number;
    if (range === "*") {
      start = min;
      end = max;
    } else {
      const bounds = range.split("-");
      if (bounds.length > 2) return null;
      const low = parseValue(bounds[0]!, aliases);
      if (low === null) return null;
      start = low;
      if (bounds.length === 2) {
        const high = parseValue(bounds[1]!, aliases);
        if (high === null) return null;
        end = high;
      } else {
        // "5/20" runs to the end of the field; a bare "5" is just 5.
        end = stepText !== undefined ? max : low;
      }
    }
    if (start < min || end > max || start > end) return null;
    for (let value = start; value <= end; value += step) values.add(value);
  }
  return values;
}

/** Parse a 5-field cron expression, or null when it is not one this module understands. */
export function parseCron(expression: string): CronSpec | null {
  const fields = expression.trim().split(/\s+/);
  if (fields.length !== 5) return null;
  const [minute, hour, dayOfMonth, month, dayOfWeek] = fields as [string, string, string, string, string];
  const minutes = parseField(minute, 0, 59);
  const hours = parseField(hour, 0, 23);
  const daysOfMonth = parseField(dayOfMonth, 1, 31);
  const months = parseField(month, 1, 12, MONTH_ALIASES);
  const rawWeekdays = parseField(dayOfWeek, 0, 7, WEEKDAY_ALIASES);
  if (!minutes || !hours || !daysOfMonth || !months || !rawWeekdays) return null;
  const weekdays = new Set([...rawWeekdays].map((day) => day % 7));
  return { minutes, hours, daysOfMonth, months, weekdays };
}

/** A Go duration ("15m", "1h30m", "45s") in milliseconds, or null. */
export function parseGoDuration(text: string): number | null {
  const trimmed = text.trim();
  const match = /^(?:(\d+)h)?(?:(\d+)m)?(?:(\d+)s)?$/.exec(trimmed);
  if (!trimmed || !match) return null;
  const [hours, minutes, seconds] = [match[1], match[2], match[3]].map((v) => Number(v ?? 0)) as [
    number,
    number,
    number,
  ];
  const ms = ((hours * 60 + minutes) * 60 + seconds) * 1000;
  return ms > 0 ? ms : null;
}

// ── Zone arithmetic ─────────────────────────────────────────────────────────

const formatters = new Map<string, Intl.DateTimeFormat>();

function wallClockFormatter(timezone: string): Intl.DateTimeFormat {
  let formatter = formatters.get(timezone);
  if (!formatter) {
    formatter = new Intl.DateTimeFormat("en-US", {
      timeZone: timezone,
      hourCycle: "h23",
      year: "numeric",
      month: "numeric",
      day: "numeric",
      hour: "numeric",
      minute: "numeric",
    });
    formatters.set(timezone, formatter);
  }
  return formatter;
}

/** The wall clock at `instant` in `timezone`, encoded as a UTC epoch (minute precision). */
function wallClockAsUtc(instant: number, timezone: string): number {
  const parts: Record<string, number> = {};
  for (const part of wallClockFormatter(timezone).formatToParts(new Date(instant))) {
    if (part.type !== "literal") parts[part.type] = Number(part.value);
  }
  return Date.UTC(parts.year!, parts.month! - 1, parts.day!, parts.hour!, parts.minute!);
}

/**
 * Every instant whose wall clock in `timezone` reads `wall` (a UTC-encoded
 * wall time): none in a spring-forward gap, two in a fall-back overlap.
 */
function instantsForWallClock(wall: number, timezone: string): number[] {
  const HALF_DAY = 12 * 60 * 60 * 1000;
  // Zone offsets either side of the wall time cover any transition near it.
  const offsets = new Set([
    wallClockAsUtc(wall - HALF_DAY, timezone) - (wall - HALF_DAY),
    wallClockAsUtc(wall + HALF_DAY, timezone) - (wall + HALF_DAY),
  ]);
  const instants: number[] = [];
  for (const offset of offsets) {
    const candidate = wall - offset;
    if (wallClockAsUtc(candidate, timezone) === wall) instants.push(candidate);
  }
  return instants;
}

function resolveZone(timezone: string | undefined): string | null {
  const zone = timezone || "UTC";
  try {
    wallClockFormatter(zone);
    return zone;
  } catch {
    return null;
  }
}

// ── Enumeration ─────────────────────────────────────────────────────────────

const DAY_MS = 24 * 60 * 60 * 1000;

function cronFireTimes(spec: CronSpec, timezone: string, after: number, until: number): number[] {
  const times: number[] = [];
  // Walk the zone's calendar days spanning the window, a day either side so a
  // wall time near midnight that maps across the boundary is not missed.
  const firstDay = Math.floor(wallClockAsUtc(after, timezone) / DAY_MS) - 1;
  const lastDay = Math.floor(wallClockAsUtc(until, timezone) / DAY_MS) + 1;
  const hours = [...spec.hours].sort((a, b) => a - b);
  const minutes = [...spec.minutes].sort((a, b) => a - b);
  for (let day = firstDay; day <= lastDay; day++) {
    const date = new Date(day * DAY_MS);
    if (
      !spec.months.has(date.getUTCMonth() + 1) ||
      !spec.daysOfMonth.has(date.getUTCDate()) ||
      !spec.weekdays.has(date.getUTCDay())
    ) {
      continue;
    }
    for (const hour of hours) {
      for (const minute of minutes) {
        const wall = day * DAY_MS + (hour * 60 + minute) * 60_000;
        for (const instant of instantsForWallClock(wall, timezone)) {
          if (instant > after && instant <= until) times.push(instant);
        }
      }
    }
  }
  return times;
}

function intervalFireTimes(every: number, after: number, until: number, limit: number): number[] {
  const times: number[] = [];
  for (let instant = (Math.floor(after / every) + 1) * every; instant <= until; instant += every) {
    times.push(instant);
    if (times.length >= limit) break;
  }
  return times;
}

export interface FireSchedule {
  cron: string[];
  interval?: string;
  /** IANA zone the cron entries are read in. Empty means UTC, as on the server. */
  timezone?: string;
}

/**
 * Fire times strictly after `after` and no later than `until` (epoch ms),
 * ascending and de-duplicated, at most `limit` of them.
 */
export function nextFireTimes(
  schedule: FireSchedule,
  after: number,
  until: number,
  limit = Number.POSITIVE_INFINITY,
): number[] {
  const timezone = resolveZone(schedule.timezone);
  const all = new Set<number>();
  if (timezone) {
    for (const expression of schedule.cron) {
      if (expression.trim() === "") continue;
      const spec = parseCron(expression);
      if (!spec) continue;
      for (const instant of cronFireTimes(spec, timezone, after, until)) all.add(instant);
    }
  }
  if (schedule.interval) {
    const every = parseGoDuration(schedule.interval);
    if (every !== null) {
      for (const instant of intervalFireTimes(every, after, until, limit)) all.add(instant);
    }
  }
  return [...all].sort((a, b) => a - b).slice(0, limit);
}

/** "09:00" in `timezone` (24-hour), for a timeline tick. */
export function formatZonedClock(instant: number, timezone?: string): string {
  const zone = resolveZone(timezone) ?? "UTC";
  return new Intl.DateTimeFormat("en-GB", {
    timeZone: zone,
    hour: "2-digit",
    minute: "2-digit",
    hourCycle: "h23",
  }).format(new Date(instant));
}
