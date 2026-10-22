// Copyright (c) 2025 Reliant Labs

/**
 * Future fire times, the way Temporal will compute them for a trigger's
 * schedule: cron and interval as a union, cron fields ANDed (Temporal does not
 * implement the OR special case for day-of-month and day-of-week), and wall
 * clock matched literally in the trigger's zone, so a time skipped by DST does
 * not fire and a time repeated by DST fires twice.
 */

import { describe, expect, it } from "vitest";

import { nextFireTimes, parseCron } from "../cronSchedule";

const iso = (times: number[]) => times.map((t) => new Date(t).toISOString());
const at = (value: string) => Date.parse(value);
const DAY = 24 * 60 * 60 * 1000;

describe("nextFireTimes", () => {
  it("weekday cron skips the weekend", () => {
    // Friday 2026-10-02, after 09:00 UTC.
    const times = nextFireTimes(
      { cron: ["0 9 * * 1-5"], timezone: "UTC" },
      at("2026-10-02T10:00:00Z"),
      at("2026-10-02T10:00:00Z") + 6 * DAY,
      3,
    );
    expect(iso(times)).toEqual([
      "2026-10-05T09:00:00.000Z",
      "2026-10-06T09:00:00.000Z",
      "2026-10-07T09:00:00.000Z",
    ]);
  });

  it("every-N-hours cron fires on the step", () => {
    const times = nextFireTimes(
      { cron: ["0 */4 * * *"], timezone: "UTC" },
      at("2026-10-02T01:30:00Z"),
      at("2026-10-02T13:00:00Z"),
    );
    expect(iso(times)).toEqual([
      "2026-10-02T04:00:00.000Z",
      "2026-10-02T08:00:00.000Z",
      "2026-10-02T12:00:00.000Z",
    ]);
  });

  it("an interval is aligned to the epoch, strictly after the start", () => {
    const times = nextFireTimes(
      { cron: [], interval: "90m", timezone: "UTC" },
      at("2026-10-02T00:00:00Z"),
      at("2026-10-02T04:00:00Z"),
    );
    expect(iso(times)).toEqual([
      "2026-10-02T01:30:00.000Z",
      "2026-10-02T03:00:00.000Z",
    ]);
  });

  it("interprets cron in the trigger's zone, not the viewer's", () => {
    const window = [at("2026-06-01T00:00:00Z"), at("2026-06-01T23:59:00Z")] as const;
    expect(iso(nextFireTimes({ cron: ["0 9 * * *"], timezone: "Europe/London" }, ...window))).toEqual([
      "2026-06-01T08:00:00.000Z", // 09:00 BST
    ]);
    expect(iso(nextFireTimes({ cron: ["0 9 * * *"], timezone: "America/New_York" }, ...window))).toEqual([
      "2026-06-01T13:00:00.000Z", // 09:00 EDT
    ]);
  });

  it("DST spring-forward: a wall time that does not exist does not fire", () => {
    // Europe/London jumps 01:00 -> 02:00 on 2026-03-29, so there is no 01:30.
    const times = nextFireTimes(
      { cron: ["30 1 * * *"], timezone: "Europe/London" },
      at("2026-03-28T12:00:00Z"),
      at("2026-03-31T12:00:00Z"),
    );
    expect(iso(times)).toEqual([
      "2026-03-30T00:30:00.000Z", // 01:30 BST
      "2026-03-31T00:30:00.000Z",
    ]);
  });

  it("DST fall-back: a wall time that happens twice fires twice", () => {
    // Europe/London repeats 01:00-02:00 on 2026-10-25.
    const times = nextFireTimes(
      { cron: ["30 1 * * *"], timezone: "Europe/London" },
      at("2026-10-24T12:00:00Z"),
      at("2026-10-26T12:00:00Z"),
    );
    expect(iso(times)).toEqual([
      "2026-10-25T00:30:00.000Z", // 01:30 BST
      "2026-10-25T01:30:00.000Z", // 01:30 GMT
      "2026-10-26T01:30:00.000Z",
    ]);
  });

  it("ANDs day-of-month and day-of-week, as Temporal does", () => {
    const times = nextFireTimes(
      { cron: ["0 9 13 * FRI"], timezone: "UTC" },
      at("2026-01-01T00:00:00Z"),
      at("2026-12-31T00:00:00Z"),
    );
    expect(iso(times)).toEqual([
      "2026-02-13T09:00:00.000Z",
      "2026-03-13T09:00:00.000Z",
      "2026-11-13T09:00:00.000Z",
    ]);
  });

  it("unions several crons and an interval, sorted and without duplicates", () => {
    const times = nextFireTimes(
      { cron: ["0 9 * * *", "0 9,12 * * *"], interval: "6h", timezone: "UTC" },
      at("2026-10-02T00:00:00Z"),
      at("2026-10-02T13:00:00Z"),
    );
    expect(iso(times)).toEqual([
      "2026-10-02T06:00:00.000Z",
      "2026-10-02T09:00:00.000Z",
      "2026-10-02T12:00:00.000Z",
    ]);
  });

  it("stops at the limit", () => {
    const times = nextFireTimes(
      { cron: ["* * * * *"], timezone: "UTC" },
      at("2026-10-02T00:00:00Z"),
      at("2026-10-03T00:00:00Z"),
      5,
    );
    expect(times).toHaveLength(5);
  });

  it("enumerates nothing for an expression it cannot parse", () => {
    expect(
      nextFireTimes({ cron: ["0 9 L * *"], timezone: "UTC" }, at("2026-10-02T00:00:00Z"), at("2026-10-09T00:00:00Z")),
    ).toEqual([]);
  });
});

describe("parseCron", () => {
  it("accepts names, ranges, steps and Sunday as 7", () => {
    const spec = parseCron("5/20 9-17/4 * JAN-MAR SUN,6-7")!;
    expect([...spec.minutes]).toEqual([5, 25, 45]);
    expect([...spec.hours]).toEqual([9, 13, 17]);
    expect([...spec.months]).toEqual([1, 2, 3]);
    expect([...spec.weekdays].sort()).toEqual([0, 6]);
  });

  it("rejects anything that is not 5 valid fields", () => {
    expect(parseCron("0 9 * *")).toBeNull();
    expect(parseCron("60 9 * * *")).toBeNull();
    expect(parseCron("0 9 * * MON#2")).toBeNull();
    expect(parseCron("@daily")).toBeNull();
  });
});
