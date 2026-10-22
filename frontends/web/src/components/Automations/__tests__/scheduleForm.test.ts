import { describe, expect, it } from "vitest";
import {
  DEFAULT_SCHEDULE_FORM,
  formFromSchedule,
  scheduleFromForm,
  serverErrorField,
  validateScheduleForm,
  type ScheduleFormState,
} from "../scheduleForm";

const form = (patch: Partial<ScheduleFormState>): ScheduleFormState => ({
  ...DEFAULT_SCHEDULE_FORM,
  ...patch,
});

describe("scheduleFromForm", () => {
  it("compiles each preset to a 5-field cron or an interval", () => {
    expect(scheduleFromForm(form({ preset: "hourly", minute: 15 }))).toEqual({ cron: ["15 * * * *"] });
    expect(scheduleFromForm(form({ preset: "daily", time: "07:05" }))).toEqual({ cron: ["5 7 * * *"] });
    expect(scheduleFromForm(form({ preset: "weekdays", time: "09:00" }))).toEqual({ cron: ["0 9 * * 1-5"] });
    expect(scheduleFromForm(form({ preset: "weekly", time: "18:30", weekday: 0 }))).toEqual({
      cron: ["30 18 * * 0"],
    });
    expect(scheduleFromForm(form({ preset: "interval", intervalValue: 45, intervalUnit: "m" }))).toEqual({
      cron: [],
      interval: "45m",
    });
  });

  it("passes advanced cron lines through, normalising whitespace and dropping blanks", () => {
    expect(
      scheduleFromForm(
        form({ preset: "advanced", cronText: "  0  9 * * 1-5 \n\n0 12 * * 6", advancedInterval: " 2h " }),
      ),
    ).toEqual({ cron: ["0 9 * * 1-5", "0 12 * * 6"], interval: "2h" });
  });
});

describe("formFromSchedule", () => {
  it("reopens each preset's output on that preset", () => {
    for (const state of [
      form({ preset: "hourly", minute: 15 }),
      form({ preset: "daily", time: "07:05" }),
      form({ preset: "weekdays", time: "09:00" }),
      form({ preset: "weekly", time: "18:30", weekday: 3 }),
      form({ preset: "interval", intervalValue: 2, intervalUnit: "h" }),
    ]) {
      const reopened = formFromSchedule(scheduleFromForm(state));
      expect(reopened.preset).toBe(state.preset);
      expect(scheduleFromForm(reopened)).toEqual(scheduleFromForm(state));
    }
  });

  it("opens anything else verbatim in advanced, so editing never rewrites it", () => {
    for (const schedule of [
      { cron: ["*/10 9-17 * * 1-5"] },
      { cron: ["0 9 * * 1-5", "0 12 * * 6"] },
      { cron: ["0 9 * * *"], interval: "6h" },
      { cron: [], interval: "1h30m" },
    ]) {
      const reopened = formFromSchedule(schedule);
      expect(reopened.preset).toBe("advanced");
      expect(scheduleFromForm(reopened)).toEqual({ cron: schedule.cron, interval: schedule.interval });
    }
  });
});

describe("validateScheduleForm", () => {
  it("accepts the defaults", () => {
    expect(validateScheduleForm(DEFAULT_SCHEDULE_FORM)).toBeNull();
  });

  it("catches the common mistakes before a round-trip", () => {
    expect(validateScheduleForm(form({ preset: "hourly", minute: 60 }))).toMatch(/0 and 59/);
    expect(validateScheduleForm(form({ preset: "daily", time: "" }))).toMatch(/time of day/);
    expect(validateScheduleForm(form({ preset: "interval", intervalValue: 0 }))).toMatch(/at least 1/);
    expect(validateScheduleForm(form({ preset: "advanced", cronText: "0 9 * *" }))).toMatch(/5-field/);
    expect(validateScheduleForm(form({ preset: "advanced", cronText: "" }))).toMatch(/at least one/);
  });
});

describe("serverErrorField", () => {
  it("puts a no-machine refusal beside the machine picker", () => {
    expect(serverErrorField("this workflow needs a machine: input `tools` includes shell")).toBe("daemon");
    expect(serverErrorField("no_machine and daemon_id are mutually exclusive")).toBe("daemon");
  });

  it("routes the server's ConfigError fields to the right control", () => {
    expect(serverErrorField("cron: invalid expression")).toBe("schedule");
    expect(serverErrorField("interval: must be at least 1m0s, got 30s")).toBe("schedule");
    expect(serverErrorField("schedule needs at least one cron expression or an interval")).toBe("schedule");
    expect(serverErrorField("timezone: unknown IANA time zone: Mars/Base")).toBe("timezone");
    expect(serverErrorField("project not found")).toBe("form");
  });
});
