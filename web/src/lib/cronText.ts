// Copyright (c) 2025 Reliant Labs

/**
 * Human-readable text for an automation's schedule.
 *
 * The server stores schedules as 5-field cron expressions (a union of them)
 * plus an optional Go-duration interval, interpreted in an IANA zone — see
 * ScheduleSource in proto/reliant/v1/trigger.proto. A list row has to say
 * "Every weekday at 9:00 AM ET", not "0 9 * * 1-5".
 *
 * This covers the shapes the create form produces and the common hand-written
 * ones. Anything else falls back to the raw expression rather than guessing:
 * a description that is subtly wrong about when an unattended run fires is
 * worse than an honest cron string.
 *
 * Pure, and deliberately locale-fixed (en-US clock text), so the same trigger
 * reads the same on every machine and the tests are deterministic.
 */

import { sourceKindLabel, type TriggerSource } from "@/api/trigger-grpc";

const WEEKDAY_NAMES = [
  "Sunday",
  "Monday",
  "Tuesday",
  "Wednesday",
  "Thursday",
  "Friday",
  "Saturday",
] as const;

const WEEKDAY_ALIASES: Record<string, number> = {
  SUN: 0,
  MON: 1,
  TUE: 2,
  WED: 3,
  THU: 4,
  FRI: 5,
  SAT: 6,
};

/** A non-negative integer field within [min, max], or null. */
function parseInteger(field: string, min: number, max: number): number | null {
  if (!/^\d+$/.test(field)) return null;
  const value = Number(field);
  return value >= min && value <= max ? value : null;
}

/** A comma-separated list of integers within [min, max], sorted and unique. */
function parseIntegerList(field: string, min: number, max: number): number[] | null {
  const values: number[] = [];
  for (const item of field.split(",")) {
    const value = parseInteger(item, min, max);
    if (value === null) return null;
    values.push(value);
  }
  return Array.from(new Set(values)).sort((a, b) => a - b);
}

function parseWeekday(item: string): number | null {
  const alias = WEEKDAY_ALIASES[item.toUpperCase()];
  if (alias !== undefined) return alias;
  const value = parseInteger(item, 0, 7);
  // Cron accepts both 0 and 7 for Sunday.
  return value === 7 ? 0 : value;
}

/** Day-of-week field: numbers, names and ranges ("1-5", "MON-FRI"), comma-joined. */
function parseWeekdays(field: string): number[] | null {
  const days = new Set<number>();
  for (const item of field.split(",")) {
    const range = item.split("-");
    if (range.length === 1) {
      const day = parseWeekday(item);
      if (day === null) return null;
      days.add(day);
    } else if (range.length === 2) {
      const start = parseWeekday(range[0]!);
      // "5-7" means Friday through Sunday, so the end keeps 7 rather than
      // folding it to 0 before the range is expanded.
      const rawEnd = range[1]!.toUpperCase() === "SUN" ? 7 : parseInteger(range[1]!, 0, 7);
      const end = rawEnd ?? parseWeekday(range[1]!);
      if (start === null || end === null || end < start) return null;
      for (let day = start; day <= end; day++) days.add(day % 7);
    } else {
      return null;
    }
  }
  return Array.from(days);
}

/** Monday-first ordering, which is how a week reads in a sentence. */
function mondayFirst(days: number[]): number[] {
  return [...days].sort((a, b) => ((a + 6) % 7) - ((b + 6) % 7));
}

function joinList(items: string[]): string {
  if (items.length <= 1) return items[0] ?? "";
  if (items.length === 2) return `${items[0]} and ${items[1]}`;
  return `${items.slice(0, -1).join(", ")} and ${items[items.length - 1]}`;
}

function ordinal(n: number): string {
  const lastTwo = n % 100;
  if (lastTwo >= 11 && lastTwo <= 13) return `${n}th`;
  switch (n % 10) {
    case 1:
      return `${n}st`;
    case 2:
      return `${n}nd`;
    case 3:
      return `${n}rd`;
    default:
      return `${n}th`;
  }
}

function pad2(n: number): string {
  return n.toString().padStart(2, "0");
}

/** "9:00 AM", "12:30 PM", "12:00 AM". */
export function formatClockTime(hour: number, minute: number): string {
  const suffix = hour < 12 ? "AM" : "PM";
  const hour12 = hour % 12 === 0 ? 12 : hour % 12;
  return `${hour12}:${pad2(minute)} ${suffix}`;
}

/**
 * The short label for an IANA zone: "ET", "PT", "UTC".
 *
 * Uses the generic (DST-independent) name when the platform has a compact one,
 * which it does for the North American zones. Elsewhere that name is a phrase
 * ("Germany Time") and the specific one is a bare offset that changes with the
 * season ("GMT+2"), so the IANA id itself is the clearest thing to show.
 */
export function timezoneLabel(timezone?: string): string {
  if (!timezone || timezone === "UTC" || timezone === "Etc/UTC") return "UTC";
  try {
    const part = new Intl.DateTimeFormat("en-US", {
      timeZone: timezone,
      timeZoneName: "shortGeneric",
    })
      .formatToParts(new Date(0))
      .find((p) => p.type === "timeZoneName");
    if (part && /^[A-Z]{1,5}$/.test(part.value)) return part.value;
  } catch {
    // Unknown zone: fall through to the raw id, which is what the server holds.
  }
  return timezone;
}

interface FieldDescription {
  text: string;
  /** Whether the text names a wall-clock time or day, so the zone matters. */
  zoned: boolean;
}

function describeFields(fields: string[]): FieldDescription | null {
  const [minute, hour, dayOfMonth, month, dayOfWeek] = fields as [
    string,
    string,
    string,
    string,
    string,
  ];
  // Month restrictions are rare enough in automations that a sentence for them
  // is not worth the risk of misdescribing one.
  if (month !== "*") return null;

  const everyDay = dayOfMonth === "*" && dayOfWeek === "*";

  if (minute === "*" && hour === "*" && everyDay) {
    return { text: "Every minute", zoned: false };
  }

  const minuteStep = /^\*\/(\d+)$/.exec(minute);
  if (minuteStep && hour === "*" && everyDay) {
    const step = Number(minuteStep[1]);
    if (step < 1 || step > 59) return null;
    return { text: step === 1 ? "Every minute" : `Every ${step} minutes`, zoned: false };
  }

  const atMinute = parseInteger(minute, 0, 59);
  if (atMinute === null) return null;

  if (hour === "*" && everyDay) {
    return {
      text: atMinute === 0 ? "Every hour" : `Every hour at :${pad2(atMinute)}`,
      zoned: false,
    };
  }

  const hourStep = /^\*\/(\d+)$/.exec(hour);
  if (hourStep && everyDay) {
    const step = Number(hourStep[1]);
    if (step < 1 || step > 23) return null;
    const every = step === 1 ? "Every hour" : `Every ${step} hours`;
    return { text: atMinute === 0 ? every : `${every} at :${pad2(atMinute)}`, zoned: false };
  }

  const hours = parseIntegerList(hour, 0, 23);
  if (!hours) return null;
  const times = joinList(hours.map((h) => formatClockTime(h, atMinute)));

  if (everyDay) {
    return { text: `Every day at ${times}`, zoned: true };
  }

  if (dayOfMonth === "*") {
    const days = parseWeekdays(dayOfWeek);
    if (!days) return null;
    const ordered = mondayFirst(days);
    const key = ordered.join(",");
    if (key === "1,2,3,4,5") return { text: `Every weekday at ${times}`, zoned: true };
    if (ordered.length === 7) return { text: `Every day at ${times}`, zoned: true };
    const names = ordered.map((d) => WEEKDAY_NAMES[d]!);
    return { text: `Every ${joinList(names)} at ${times}`, zoned: true };
  }

  if (dayOfWeek === "*") {
    const dates = parseIntegerList(dayOfMonth, 1, 31);
    if (!dates) return null;
    return {
      text: `Monthly on the ${joinList(dates.map(ordinal))} at ${times}`,
      zoned: true,
    };
  }

  // Both day fields set: cron ORs them, which no sentence states clearly.
  return null;
}

/**
 * Describe one 5-field cron expression, e.g. "Every weekday at 9:00 AM ET".
 * Unrecognised shapes come back as the raw expression with its zone.
 */
export function describeCron(expression: string, timezone?: string): string {
  const trimmed = expression.trim();
  const fields = trimmed.split(/\s+/);
  const zone = timezoneLabel(timezone);
  const described = fields.length === 5 ? describeFields(fields) : null;
  if (!described) return `Cron ${trimmed} (${zone})`;
  return described.zoned ? `${described.text} ${zone}` : described.text;
}

/**
 * Describe a Go duration interval: "15m" → "Every 15 minutes", "1h" →
 * "Every hour", "1h30m" → "Every 1 hour 30 minutes".
 */
export function describeInterval(interval: string): string {
  const trimmed = interval.trim();
  const match = /^(?:(\d+)h)?(?:(\d+)m)?(?:(\d+)s)?$/.exec(trimmed);
  if (!trimmed || !match) return `Every ${trimmed}`;
  const [hours, minutes, seconds] = [match[1], match[2], match[3]].map((v) => Number(v ?? 0)) as [
    number,
    number,
    number,
  ];
  const parts: string[] = [];
  if (hours) parts.push(`${hours} ${hours === 1 ? "hour" : "hours"}`);
  if (minutes) parts.push(`${minutes} ${minutes === 1 ? "minute" : "minutes"}`);
  if (seconds) parts.push(`${seconds} ${seconds === 1 ? "second" : "seconds"}`);
  if (parts.length === 0) return `Every ${trimmed}`;
  if (parts.length === 1 && parts[0] === "1 hour") return "Every hour";
  if (parts.length === 1 && parts[0] === "1 minute") return "Every minute";
  return `Every ${parts.join(" ")}`;
}

export interface ScheduleText {
  cron: string[];
  interval?: string;
  timezone?: string;
}

/**
 * Describe a whole schedule. Cron entries and the interval are a UNION on the
 * server, so each is described and they are listed together.
 */
export function describeSchedule(schedule: ScheduleText): string {
  const parts = schedule.cron
    .filter((expression) => expression.trim() !== "")
    .map((expression) => describeCron(expression, schedule.timezone));
  if (schedule.interval) parts.push(describeInterval(schedule.interval));
  return parts.length > 0 ? parts.join("; ") : "No schedule";
}

/**
 * One line for what makes a trigger fire: the schedule in words, or the kind
 * of source when this client has no editor for it yet.
 */
export function describeTriggerSource(source: TriggerSource): string {
  if (source.kind === "activation") return source.declared ? describeTriggerSource(source.declared) : sourceKindLabel(source);
  return source.kind === "schedule" ? describeSchedule(source.schedule) : sourceKindLabel(source);
}
