// Copyright (c) 2025 Reliant Labs

/**
 * The automation form's schedule model, and its two-way mapping to the wire
 * schedule (cron expressions + an optional interval).
 *
 * The form offers presets because almost every automation is one of a handful
 * of shapes, and a cron string is the wrong thing to ask a person for. The
 * presets are not a separate concept on the server: each one compiles to a
 * plain 5-field cron (or an interval), so `formFromSchedule` can recognise a
 * stored schedule and reopen it on the preset that made it. A schedule no
 * preset produced — hand-written cron, several expressions, cron AND an
 * interval — opens in "advanced", verbatim, so editing never rewrites it.
 *
 * Pure: no React, no clock, no zone lookups. That is what makes it testable.
 */

export type SchedulePreset = "hourly" | "daily" | "weekdays" | "weekly" | "interval" | "advanced";

export type IntervalUnit = "m" | "h";

export interface ScheduleFormState {
  preset: SchedulePreset;
  /** "HH:MM", 24-hour, for daily / weekdays / weekly. */
  time: string;
  /** Minute past the hour, for hourly. */
  minute: number;
  /** 0 = Sunday … 6 = Saturday, for weekly. */
  weekday: number;
  /** For interval. */
  intervalValue: number;
  intervalUnit: IntervalUnit;
  /** For advanced: one 5-field cron expression per line. */
  cronText: string;
  /** For advanced: an optional Go duration fired IN ADDITION to the cron lines. */
  advancedInterval: string;
}

export interface WireSchedule {
  cron: string[];
  interval?: string;
}

export const DEFAULT_SCHEDULE_FORM: ScheduleFormState = {
  preset: "weekdays",
  time: "09:00",
  minute: 0,
  weekday: 1,
  intervalValue: 30,
  intervalUnit: "m",
  cronText: "",
  advancedInterval: "",
};

export const PRESET_OPTIONS: Array<{ value: SchedulePreset; label: string }> = [
  { value: "hourly", label: "Every hour" },
  { value: "daily", label: "Every day" },
  { value: "weekdays", label: "Every weekday" },
  { value: "weekly", label: "Every week" },
  { value: "interval", label: "Every N minutes or hours" },
  { value: "advanced", label: "Advanced (cron)" },
];

export const WEEKDAY_OPTIONS = [
  { value: 1, label: "Monday" },
  { value: 2, label: "Tuesday" },
  { value: 3, label: "Wednesday" },
  { value: 4, label: "Thursday" },
  { value: 5, label: "Friday" },
  { value: 6, label: "Saturday" },
  { value: 0, label: "Sunday" },
];

/** Mirrors the server's floor (internal/triggers/config.go MinInterval). */
export const MIN_INTERVAL_MINUTES = 1;

function parseTime(time: string): { hour: number; minute: number } | null {
  const match = /^(\d{1,2}):(\d{2})$/.exec(time.trim());
  if (!match) return null;
  const hour = Number(match[1]);
  const minute = Number(match[2]);
  if (hour > 23 || minute > 59) return null;
  return { hour, minute };
}

function pad2(n: number): string {
  return n.toString().padStart(2, "0");
}

/**
 * Validate the form's schedule. Returns a message for the first problem, or
 * null. The server validates again and its message wins if it disagrees —
 * this exists so the common mistakes never need a round-trip.
 */
export function validateScheduleForm(state: ScheduleFormState): string | null {
  switch (state.preset) {
    case "hourly":
      return Number.isInteger(state.minute) && state.minute >= 0 && state.minute <= 59
        ? null
        : "Minute must be between 0 and 59.";
    case "daily":
    case "weekdays":
    case "weekly":
      return parseTime(state.time) ? null : "Choose a time of day.";
    case "interval": {
      if (!Number.isInteger(state.intervalValue) || state.intervalValue < 1) {
        return "The interval must be a whole number of at least 1.";
      }
      const minutes = state.intervalUnit === "h" ? state.intervalValue * 60 : state.intervalValue;
      return minutes >= MIN_INTERVAL_MINUTES ? null : "The interval must be at least 1 minute.";
    }
    case "advanced": {
      const lines = cronLines(state.cronText);
      for (const line of lines) {
        if (line.split(/\s+/).length !== 5) {
          return `"${line}" is not a 5-field cron expression (minute hour day month weekday).`;
        }
      }
      if (lines.length === 0 && state.advancedInterval.trim() === "") {
        return "Add at least one cron expression or an interval.";
      }
      return null;
    }
  }
}

function cronLines(text: string): string[] {
  return text
    .split("\n")
    .map((line) => line.trim().replace(/\s+/g, " "))
    .filter((line) => line !== "");
}

/** Compile the form to the wire schedule. Call validateScheduleForm first. */
export function scheduleFromForm(state: ScheduleFormState): WireSchedule {
  const time = parseTime(state.time) ?? { hour: 9, minute: 0 };
  switch (state.preset) {
    case "hourly":
      return { cron: [`${state.minute} * * * *`] };
    case "daily":
      return { cron: [`${time.minute} ${time.hour} * * *`] };
    case "weekdays":
      return { cron: [`${time.minute} ${time.hour} * * 1-5`] };
    case "weekly":
      return { cron: [`${time.minute} ${time.hour} * * ${state.weekday}`] };
    case "interval":
      return { cron: [], interval: `${state.intervalValue}${state.intervalUnit}` };
    case "advanced": {
      const interval = state.advancedInterval.trim();
      return { cron: cronLines(state.cronText), interval: interval || undefined };
    }
  }
}

/**
 * Reopen a stored schedule in the form, on the preset that produces it when
 * there is one, and verbatim in "advanced" when there is not.
 */
export function formFromSchedule(schedule: WireSchedule): ScheduleFormState {
  const base = DEFAULT_SCHEDULE_FORM;
  const cron = schedule.cron.map((c) => c.trim().replace(/\s+/g, " ")).filter(Boolean);
  const interval = schedule.interval?.trim() || "";

  const advanced: ScheduleFormState = {
    ...base,
    preset: "advanced",
    cronText: cron.join("\n"),
    advancedInterval: interval,
  };

  if (cron.length === 0) {
    const match = /^(\d+)(m|h)$/.exec(interval);
    if (match) {
      return {
        ...base,
        preset: "interval",
        intervalValue: Number(match[1]),
        intervalUnit: match[2] as IntervalUnit,
      };
    }
    return advanced;
  }

  if (cron.length !== 1 || interval) return advanced;

  const fields = cron[0]!.split(" ");
  if (fields.length !== 5) return advanced;
  const [minute, hour, dayOfMonth, month, dayOfWeek] = fields as [
    string,
    string,
    string,
    string,
    string,
  ];
  if (dayOfMonth !== "*" || month !== "*" || !/^\d{1,2}$/.test(minute)) return advanced;
  const minuteValue = Number(minute);
  if (minuteValue > 59) return advanced;

  if (hour === "*" && dayOfWeek === "*") {
    return { ...base, preset: "hourly", minute: minuteValue };
  }
  if (!/^\d{1,2}$/.test(hour) || Number(hour) > 23) return advanced;
  const time = `${pad2(Number(hour))}:${pad2(minuteValue)}`;

  if (dayOfWeek === "*") return { ...base, preset: "daily", time };
  if (dayOfWeek === "1-5") return { ...base, preset: "weekdays", time };
  if (/^[0-6]$/.test(dayOfWeek)) {
    return { ...base, preset: "weekly", time, weekday: Number(dayOfWeek) };
  }
  return advanced;
}

/**
 * Which part of the form a server validation error is about, so it can be
 * shown beside that field. The server's ConfigError renders as
 * "<field>: <reason>" (internal/triggers/config.go).
 */
export function serverErrorField(
  message: string,
): "schedule" | "timezone" | "daemon" | "catchup" | "form" {
  // validateTriggerDaemon: "daemon_id is required…", "daemon not found",
  // "project is not installed on that daemon".
  if (/^daemon(_id)?\b/.test(message) || /installed on that daemon/.test(message)) return "daemon";
  if (/^catchup_window\b/.test(message)) return "catchup";
  if (/^(cron|interval)\b/.test(message) || /schedule needs/.test(message)) {
    return "schedule";
  }
  if (/^timezone\b/.test(message)) return "timezone";
  return "form";
}
