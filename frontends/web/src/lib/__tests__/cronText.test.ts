import { describe, expect, it } from "vitest";
import {
  describeCron,
  describeInterval,
  describeSchedule,
  formatClockTime,
  timezoneLabel,
} from "../cronText";

describe("formatClockTime", () => {
  it("renders a 12-hour clock with the midnight and noon edges", () => {
    expect(formatClockTime(9, 0)).toBe("9:00 AM");
    expect(formatClockTime(0, 5)).toBe("12:05 AM");
    expect(formatClockTime(12, 30)).toBe("12:30 PM");
    expect(formatClockTime(17, 45)).toBe("5:45 PM");
  });
});

describe("timezoneLabel", () => {
  it("uses the compact generic name where one exists", () => {
    expect(timezoneLabel("America/New_York")).toBe("ET");
    expect(timezoneLabel("America/Los_Angeles")).toBe("PT");
  });

  it("treats empty and UTC as UTC, which is the server's default", () => {
    expect(timezoneLabel("")).toBe("UTC");
    expect(timezoneLabel(undefined)).toBe("UTC");
    expect(timezoneLabel("UTC")).toBe("UTC");
  });

  it("falls back to the IANA id rather than a seasonal offset", () => {
    expect(timezoneLabel("Europe/Berlin")).toBe("Europe/Berlin");
    expect(timezoneLabel("Not/AZone")).toBe("Not/AZone");
  });
});

describe("describeCron", () => {
  const ET = "America/New_York";

  it("describes the shapes the create form produces", () => {
    expect(describeCron("0 * * * *", ET)).toBe("Every hour");
    expect(describeCron("15 * * * *", ET)).toBe("Every hour at :15");
    expect(describeCron("0 9 * * *", ET)).toBe("Every day at 9:00 AM ET");
    expect(describeCron("0 9 * * 1-5", ET)).toBe("Every weekday at 9:00 AM ET");
    expect(describeCron("30 14 * * 3", ET)).toBe("Every Wednesday at 2:30 PM ET");
  });

  it("describes minute and hour steps without a zone, since none applies", () => {
    expect(describeCron("* * * * *")).toBe("Every minute");
    expect(describeCron("*/15 * * * *")).toBe("Every 15 minutes");
    expect(describeCron("0 */6 * * *")).toBe("Every 6 hours");
    expect(describeCron("5 */2 * * *")).toBe("Every 2 hours at :05");
  });

  it("lists several days Monday-first and several times in order", () => {
    expect(describeCron("0 9 * * 1,3,5", ET)).toBe(
      "Every Monday, Wednesday and Friday at 9:00 AM ET",
    );
    expect(describeCron("0 10 * * 0,6", "UTC")).toBe("Every Saturday and Sunday at 10:00 AM UTC");
    expect(describeCron("0 17,9 * * *", "UTC")).toBe("Every day at 9:00 AM and 5:00 PM UTC");
  });

  it("accepts day names and Sunday as 7", () => {
    expect(describeCron("0 9 * * MON-FRI", ET)).toBe("Every weekday at 9:00 AM ET");
    expect(describeCron("0 8 * * 7", ET)).toBe("Every Sunday at 8:00 AM ET");
    expect(describeCron("0 8 * * 0-6", ET)).toBe("Every day at 8:00 AM ET");
  });

  it("describes monthly schedules with ordinals", () => {
    expect(describeCron("0 9 1 * *", ET)).toBe("Monthly on the 1st at 9:00 AM ET");
    expect(describeCron("0 9 2,22,13 * *", ET)).toBe(
      "Monthly on the 2nd, 13th and 22nd at 9:00 AM ET",
    );
  });

  it("falls back to the raw expression for shapes it cannot state exactly", () => {
    // Both day fields set: cron ORs them.
    expect(describeCron("0 9 1 * 1", ET)).toBe("Cron 0 9 1 * 1 (ET)");
    // Month restriction.
    expect(describeCron("0 9 * 1 *", ET)).toBe("Cron 0 9 * 1 * (ET)");
    // Hour ranges and the wrong field count.
    expect(describeCron("0 9-17 * * *", "UTC")).toBe("Cron 0 9-17 * * * (UTC)");
    expect(describeCron("0 0 9 * * *", "UTC")).toBe("Cron 0 0 9 * * * (UTC)");
    expect(describeCron("70 9 * * *", "UTC")).toBe("Cron 70 9 * * * (UTC)");
  });
});

describe("describeInterval", () => {
  it("describes Go durations", () => {
    expect(describeInterval("15m")).toBe("Every 15 minutes");
    expect(describeInterval("1h")).toBe("Every hour");
    expect(describeInterval("1m")).toBe("Every minute");
    expect(describeInterval("1h30m")).toBe("Every 1 hour 30 minutes");
    expect(describeInterval("24h")).toBe("Every 24 hours");
  });

  it("passes through what it cannot parse", () => {
    expect(describeInterval("1.5h")).toBe("Every 1.5h");
  });
});

describe("describeSchedule", () => {
  it("joins every cron entry and the interval, which the server treats as a union", () => {
    expect(
      describeSchedule({
        cron: ["0 9 * * 1-5", "0 12 * * 6"],
        interval: "6h",
        timezone: "America/New_York",
      }),
    ).toBe("Every weekday at 9:00 AM ET; Every Saturday at 12:00 PM ET; Every 6 hours");
  });

  it("says so when nothing is scheduled", () => {
    expect(describeSchedule({ cron: [] })).toBe("No schedule");
  });
});
