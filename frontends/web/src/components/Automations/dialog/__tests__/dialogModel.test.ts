// Copyright (c) 2025 Reliant Labs

/**
 * The automation dialog's pure parts: the WHEN model and its wire mapping,
 * the schedule preview's concrete next runs, the derived name, the machine
 * default, and a draft as starting values.
 */

import { describe, expect, it } from "vitest";
import { create } from "@bufbuild/protobuf";

import { automationDraftFromJson, type TriggerSource } from "@/hooks/trigger-model";
import { IntegrationSourceSchema } from "@/gen/reliant/v1/trigger_pb";
import { NO_MACHINE, type DaemonChoice } from "../../daemonChoices";
import { DEFAULT_SCHEDULE_FORM, formFromSchedule } from "../../scheduleForm";
import { autoName, shortWhen, uniqueName, whatFromPrompt } from "../autoName";
import { draftToPrefill } from "../draftToPrefill";
import { defaultMachine, lastUsedDaemon, machineOptionLabel, NO_MACHINE_OPTION_LABEL } from "../machineDefault";
import { formatRun, friendlyZoneName, nextRuns } from "../schedulePreview";
import { defaultWhen, filterOfWhen, sourceFromWhen, whenError, whenFromSource, withKind } from "../whenModel";


describe("whenModel", () => {
  it("defaults to every weekday at 09:00", () => {
    const when = defaultWhen();
    expect(when.kind).toBe("schedule");
    expect(sourceFromWhen(when)).toMatchObject({ kind: "schedule", schedule: { cron: ["0 9 * * 1-5"], overlap: "skip" } });
  });

  it("round-trips a stored schedule, keeping what the form does not edit", () => {
    const stored: TriggerSource = {
      kind: "schedule",
      schedule: { cron: ["0 9 * * 1-5"], interval: "30m", timezone: "Europe/Berlin", overlap: "allow", catchupWindow: "30m" },
    };
    const when = whenFromSource(stored)!;
    // Cron and an interval together open as custom, verbatim ("also every" is not offered, but kept).
    expect(when.schedule.preset).toBe("advanced");
    expect(sourceFromWhen(when)).toEqual(stored);
  });

  it("reads an empty drafted zone as the browser's, never silently UTC", () => {
    const when = whenFromSource({ kind: "schedule", schedule: { cron: ["0 9 * * *"], timezone: "", overlap: "skip" } })!;
    expect(when.timezone).toBe(Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC");
  });

  it("carries an integration event and its filter", () => {
    const arm = { case: "integration" as const, value: create(IntegrationSourceSchema, { integration: "github", events: ["pull_request.opened"] }) };
    const when = whenFromSource({ kind: "passthrough", arm }, { filter: "true", catalogRef: "github/pull_request@1" })!;
    expect(when.kind).toBe("integration");
    expect(when.catalogRef).toBe("github/pull_request@1");
    expect(filterOfWhen(when)).toBe("true");
    const back = sourceFromWhen(when);
    expect(back.kind).toBe("passthrough");
    expect(back.kind === "passthrough" && back.arm.case === "integration" && back.arm.value.events).toEqual(["pull_request.opened"]);
  });

  it("asks for an app and an event before an integration WHEN can be saved", () => {
    const empty = withKind(defaultWhen(), "integration");
    expect(whenError(empty)).toMatch(/Choose the app/);
    expect(whenError(withKind(defaultWhen(), "webhook"))).toBeNull();
  });

  it("switching kinds keeps the schedule sentence for a switch back", () => {
    const start = { ...defaultWhen(), schedule: { ...DEFAULT_SCHEDULE_FORM, time: "07:30" } };
    const back = withKind(withKind(start, "webhook"), "schedule");
    expect(back.schedule.time).toBe("07:30");
    expect(back.event).toBeUndefined();
  });
});

describe("schedulePreview", () => {
  // Friday 2026-10-23 12:00 UTC.
  const now = new Date(Date.UTC(2026, 9, 23, 12, 0));

  it("lists the next weekday runs in the schedule's own zone", () => {
    // Noon UTC is 8:00 in New York, so today's 9:00 is still ahead; the weekend is skipped.
    const runs = nextRuns(formFromSchedule({ cron: ["0 9 * * 1-5"] }), "America/New_York", now)!;
    expect(runs.map((r) => formatRun(r, "America/New_York"))).toEqual(["Fri 9:00", "Mon 9:00", "Tue 9:00"]);
    // 9:00 in New York in October is 13:00 UTC.
    expect(runs[0]!.toISOString()).toBe("2026-10-23T13:00:00.000Z");
  });

  it("includes today when the time is still ahead", () => {
    const runs = nextRuns(formFromSchedule({ cron: ["30 18 * * *"] }), "UTC", now)!;
    expect(runs.map((r) => formatRun(r, "UTC"))).toEqual(["Fri 18:30", "Sat 18:30", "Sun 18:30"]);
  });

  it("steps an interval and an hourly schedule", () => {
    expect(nextRuns(formFromSchedule({ cron: [], interval: "30m" }), "UTC", now)!.map((r) => formatRun(r, "UTC"))).toEqual([
      "Fri 12:30",
      "Fri 13:00",
      "Fri 13:30",
    ]);
    expect(nextRuns(formFromSchedule({ cron: ["15 * * * *"] }), "UTC", now)!.map((r) => formatRun(r, "UTC"))).toEqual([
      "Fri 12:15",
      "Fri 13:15",
      "Fri 14:15",
    ]);
  });

  it("leaves custom cron to words", () => {
    expect(nextRuns(formFromSchedule({ cron: ["0 9 1 * *"] }), "UTC", now)).toBeNull();
  });

  it("names zones as people do", () => {
    expect(friendlyZoneName("UTC")).toBe("UTC");
    expect(friendlyZoneName("")).toBe("UTC");
    expect(friendlyZoneName("America/Los_Angeles")).toBe("Pacific Time");
  });
});

describe("autoName", () => {
  it("names from when and what", () => {
    expect(autoName({ when: "Every weekday at 9:00 AM", prompt: "Summarise failed CI runs. Then open issues." })).toBe(
      "Weekdays 9 AM · Summarise failed CI runs",
    );
    expect(autoName({ when: "GitHub: Pull request opened", workflowName: "PR review", prompt: "" })).toBe(
      "GitHub: Pull request opened · PR review",
    );
    expect(autoName({ when: "Webhook", prompt: "" })).toBe("Webhook · Agent");
  });

  it("keeps the prompt to a few words", () => {
    expect(whatFromPrompt("Look at every failed run from yesterday and report")).toBe("Look at every failed run from…");
    expect(shortWhen("Every Monday at 8:30 AM")).toBe("Mondays 8:30 AM");
  });

  it("is unique in the project", () => {
    expect(uniqueName("Daily · Agent", ["daily · agent", "Daily · Agent 2"])).toBe("Daily · Agent 3");
    expect(uniqueName("Fresh", ["Other"])).toBe("Fresh");
  });
});

describe("machineDefault", () => {
  const choice = (daemonId: string, extra: Partial<DaemonChoice> = {}): DaemonChoice => ({
    daemonId,
    label: daemonId,
    statusLabel: "online",
    installed: true,
    eligible: true,
    ...extra,
  });

  it("runs without a machine when the workflow needs none", () => {
    expect(defaultMachine({ choices: [choice("a")], needsNoMachine: true })).toBe(NO_MACHINE);
  });

  it("prefers the machine last used here, when it is still eligible", () => {
    const choices = [choice("a"), choice("b")];
    expect(defaultMachine({ choices, needsNoMachine: false, lastUsed: "b" })).toBe("b");
    expect(defaultMachine({ choices: [choice("a"), choice("b", { eligible: false })], needsNoMachine: false, lastUsed: "b" })).toBe("a");
    // Two equal choices and no history: the person picks.
    expect(defaultMachine({ choices, needsNoMachine: false })).toBe("");
    expect(defaultMachine({ choices: [choice("a", { statusLabel: "failed" })], needsNoMachine: false, lastUsed: "a" })).toBe("");
  });

  it("finds the newest use", () => {
    expect(lastUsedDaemon([{ daemonId: "a", at: "2026-10-01T00:00:00Z" }, { daemonId: "b", at: "2026-10-02T00:00:00Z" }, { at: "2026-10-03T00:00:00Z" }])).toBe("b");
  });

  it("drops status jargon from the options", () => {
    expect(machineOptionLabel(choice("MacBook"))).toBe("MacBook");
    expect(machineOptionLabel(choice("MacBook", { statusLabel: "idle" }))).toBe("MacBook");
    expect(machineOptionLabel(choice("MacBook", { statusLabel: "suspended" }))).toBe("MacBook (offline: woken when it runs)");
    expect(machineOptionLabel(choice("MacBook", { eligible: false, ineligibleReason: "project not installed" }))).toBe("MacBook (project not installed)");
    expect(NO_MACHINE_OPTION_LABEL).toBe("No machine (web and integrations only)");
  });
});

describe("draftToPrefill", () => {
  it("takes only the when and the what from a drafted automation", () => {
    const draft = automationDraftFromJson({
      definition: {
        name: "",
        message: "Summarise failed CI",
        schedule: { cron: ["0 9 * * 1-5"] },
        params: { depth: 2 },
      },
      summary: "Every weekday at 9:00, summarise failed CI",
      assumptions: ["Assumed 9:00 in your time zone"],
    })!;
    expect(draft.summary).toBe("Every weekday at 9:00, summarise failed CI");
    const prefill = draftToPrefill(draft, { projectId: "proj-1" });
    expect(prefill).toMatchObject({
      projectId: "proj-1",
      message: "Summarise failed CI",
      workflow: "",
      params: { depth: 2 },
      assumptions: ["Assumed 9:00 in your time zone"],
    });
    expect(prefill.source).toMatchObject({ kind: "schedule", schedule: { cron: ["0 9 * * 1-5"], timezone: "" } });
    expect(prefill.machine).toBeUndefined();
    expect(prefill.name).toBeUndefined();
  });

  it("ignores what is not a draft", () => {
    expect(automationDraftFromJson(undefined)).toBeUndefined();
    expect(automationDraftFromJson("nope")).toBeUndefined();
  });
});
