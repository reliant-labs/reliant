// Copyright (c) 2025 Reliant Labs

/**
 * A schedule as a sentence: "Every [weekday ▾] at [09:00] [Pacific Time ▾]",
 * then "Next runs: Mon 9:00, Tue 9:00, Wed 9:00". The person thinks in that
 * sentence, so the form is that sentence (report 03 §6.1).
 *
 * Every control is a native element with an accessible name, so the sentence
 * reads as "Repeat, Every weekday; At, 09:00; Time zone, Pacific Time".
 *
 * The automation dialog and the builder's declared-schedule editor both use
 * it, so the same "every weekday at 9" means the same wall-clock time in both
 * (the builder used to default to UTC).
 */

import { useMemo, useState } from "react";

import { describeSchedule } from "@/lib/cronText";
import { cn } from "@/lib/utils";
import { errorTextClass, fieldClass, hintClass, textareaClass } from "../../workflow/run/runFormStyles";
import {
  PRESET_OPTIONS,
  WEEKDAY_OPTIONS,
  scheduleFromForm,
  validateScheduleForm,
  type IntervalUnit,
  type ScheduleFormState,
  type SchedulePreset,
} from "../scheduleForm";
import { validateTimezone } from "../timezone";
import { formatRun, friendlyZoneName, nextRuns } from "./schedulePreview";
import { TimezonePicker } from "./TimezonePicker";

const inline = cn(fieldClass, "w-auto");

const CRON_EXAMPLES: Array<{ cron: string; text: string }> = [
  { cron: "0 9 * * 1-5", text: "Weekdays at 9:00" },
  { cron: "30 8 * * 1", text: "Mondays at 8:30" },
  { cron: "0 */4 * * *", text: "Every 4 hours" },
  { cron: "0 18 1 * *", text: "The 1st of each month at 18:00" },
];

export interface ScheduleSentenceProps {
  /** A prefix for the controls' ids; unique per form. */
  idPrefix: string;
  value: ScheduleFormState;
  timezone: string;
  onChange: (patch: Partial<ScheduleFormState>) => void;
  onTimezoneChange: (zone: string) => void;
  /** A server error about the schedule, shown under it. */
  error?: string;
  /** For the preview's "now"; tests pin it. */
  now?: Date;
  disabled?: boolean;
}

export function ScheduleSentence({
  idPrefix,
  value,
  timezone,
  onChange,
  onTimezoneChange,
  error,
  now,
  disabled = false,
}: ScheduleSentenceProps) {
  const id = (name: string) => `${idPrefix}-${name}`;
  const [examplesOpen, setExamplesOpen] = useState(false);
  const localError = validateScheduleForm(value);
  const zoneError = validateTimezone(timezone);
  // Problems show once the parent says so (on submit); until then the preview
  // line says the schedule is unfinished, which is enough while typing.
  const shownError = error;
  const needsTime = value.preset === "daily" || value.preset === "weekdays" || value.preset === "weekly";

  const preview = useMemo(() => {
    if (localError || zoneError) return null;
    const zone = timezone.trim() || "UTC";
    const runs = nextRuns(value, zone, now ?? new Date());
    if (runs && runs.length > 0) {
      return `Next runs: ${runs.map((r) => formatRun(r, zone)).join(", ")} (${friendlyZoneName(zone)})`;
    }
    return `Runs: ${describeSchedule({ ...scheduleFromForm(value), timezone: zone })}`;
  }, [value, timezone, now, localError, zoneError]);

  return (
    <fieldset className="min-w-0 space-y-2" disabled={disabled}>
      <legend className="sr-only">Schedule</legend>
      <div className="flex flex-wrap items-center gap-2 text-sm text-foreground">
        <label htmlFor={id("schedule")} className="sr-only">
          Repeat
        </label>
        <select
          id={id("schedule")}
          className={inline}
          value={value.preset}
          onChange={(e) => onChange({ preset: e.target.value as SchedulePreset })}
          aria-invalid={!!shownError}
        >
          {PRESET_OPTIONS.map((option) => (
            <option key={option.value} value={option.value}>
              {option.label}
            </option>
          ))}
        </select>

        {value.preset === "weekly" && (
          <>
            <span aria-hidden>on</span>
            <label htmlFor={id("weekday")} className="sr-only">
              Day
            </label>
            <select
              id={id("weekday")}
              className={inline}
              value={value.weekday}
              onChange={(e) => onChange({ weekday: Number(e.target.value) })}
            >
              {WEEKDAY_OPTIONS.map((option) => (
                <option key={option.value} value={option.value}>
                  {option.label}
                </option>
              ))}
            </select>
          </>
        )}

        {needsTime && (
          <>
            <span aria-hidden>at</span>
            <label htmlFor={id("time")} className="sr-only">
              At
            </label>
            <input
              id={id("time")}
              type="time"
              className={cn(inline, "w-32")}
              value={value.time}
              onChange={(e) => onChange({ time: e.target.value })}
            />
          </>
        )}

        {value.preset === "hourly" && (
          <>
            <span aria-hidden>at</span>
            <label htmlFor={id("minute")} className="sr-only">
              Minutes past the hour
            </label>
            <input
              id={id("minute")}
              type="number"
              min={0}
              max={59}
              className={cn(inline, "w-20")}
              value={Number.isNaN(value.minute) ? "" : value.minute}
              onChange={(e) => onChange({ minute: e.target.valueAsNumber })}
            />
            <span aria-hidden>minutes past</span>
          </>
        )}

        {value.preset === "interval" && (
          <>
            <span aria-hidden>every</span>
            <label htmlFor={id("interval-value")} className="sr-only">
              Interval amount
            </label>
            <input
              id={id("interval-value")}
              type="number"
              min={1}
              className={cn(inline, "w-20")}
              value={Number.isNaN(value.intervalValue) ? "" : value.intervalValue}
              onChange={(e) => onChange({ intervalValue: e.target.valueAsNumber })}
            />
            <label htmlFor={id("interval-unit")} className="sr-only">
              Interval unit
            </label>
            <select
              id={id("interval-unit")}
              className={inline}
              value={value.intervalUnit}
              onChange={(e) => onChange({ intervalUnit: e.target.value as IntervalUnit })}
            >
              <option value="m">minutes</option>
              <option value="h">hours</option>
            </select>
          </>
        )}

        {value.preset !== "interval" && (
          <TimezonePicker id={id("timezone")} value={timezone} onChange={onTimezoneChange} invalid={!!zoneError} />
        )}
      </div>

      {value.preset === "advanced" && (
        <div>
          <label htmlFor={id("cron")} className="sr-only">
            Cron expression
          </label>
          <textarea
            id={id("cron")}
            className={cn(textareaClass, "min-h-[44px] font-mono")}
            rows={1}
            value={value.cronText}
            onChange={(e) => onChange({ cronText: e.target.value })}
            spellCheck={false}
            placeholder="0 9 * * 1-5"
            aria-invalid={!!shownError}
            aria-describedby={id("cron-hint")}
          />
          <p id={id("cron-hint")} className={hintClass}>
            One 5-field expression per line: minute, hour, day of month, month, day of week. It runs whenever any line
            matches.{" "}
            <button
              type="button"
              className="rounded-sm font-medium text-primary hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
              aria-expanded={examplesOpen}
              aria-controls={id("cron-examples")}
              onClick={() => setExamplesOpen((o) => !o)}
            >
              See examples
            </button>
          </p>
          {examplesOpen && (
            <ul id={id("cron-examples")} className="mt-2 space-y-1 rounded-md border border-border/60 bg-background px-3 py-2 text-xs">
              {CRON_EXAMPLES.map((example) => (
                <li key={example.cron} className="flex items-baseline gap-3">
                  <button
                    type="button"
                    className="font-mono text-foreground hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
                    onClick={() => onChange({ cronText: example.cron })}
                  >
                    {example.cron}
                  </button>
                  <span className="text-muted-foreground">{example.text}</span>
                </li>
              ))}
            </ul>
          )}
        </div>
      )}

      <p className="text-xs text-muted-foreground" aria-live="polite" data-testid="schedule-preview">
        {preview ?? (zoneError && !localError ? "Fix the time zone to see when this runs." : "Finish the schedule to see when this runs.")}
      </p>
      {shownError && (
        <p className={errorTextClass} role="alert">
          {shownError}
        </p>
      )}
    </fieldset>
  );
}
