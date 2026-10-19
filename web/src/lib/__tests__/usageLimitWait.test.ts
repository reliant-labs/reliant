import { describe, expect, it } from "vitest";
import {
  formatCountdown,
  usageLimitHeadline,
  usageLimitResumeLine,
  usageLimitWaitOf,
} from "../usageLimitWait";

const NOW = new Date("2026-10-18T12:00:00").getTime();

describe("usageLimitWaitOf", () => {
  it("reads the wait the chat carries", () => {
    const at = new Date(NOW + 72 * 60_000).toISOString();
    expect(usageLimitWaitOf({ usageLimitResumesAt: at, usageLimitProvider: "Anthropic", usageLimitProbing: false })).toEqual({
      provider: "Anthropic",
      resumesAt: Date.parse(at),
      probing: false,
    });
  });

  it("is undefined when the chat carries none, or a time that does not parse", () => {
    expect(usageLimitWaitOf(undefined)).toBeUndefined();
    expect(usageLimitWaitOf({ usageLimitProbing: false })).toBeUndefined();
    expect(usageLimitWaitOf({ usageLimitResumesAt: "soon", usageLimitProbing: false })).toBeUndefined();
  });
});

describe("formatCountdown", () => {
  it.each([
    [0, "0s"],
    [40_000, "40s"],
    [5 * 60_000 + 5_000, "5m 5s"],
    [12 * 60_000 + 5_000, "12m"],
    [72 * 60_000, "1h 12m"],
    [26 * 3_600_000, "1d 2h"],
    [-5_000, "0s"],
  ])("%d ms reads %s", (ms, text) => {
    expect(formatCountdown(ms)).toBe(text);
  });
});

describe("the wait line", () => {
  const wait = { provider: "Anthropic", resumesAt: NOW + 72 * 60_000, probing: false };

  it("names the provider, and the reset with a live countdown", () => {
    expect(usageLimitHeadline(wait)).toBe("Waiting for Anthropic usage limit");
    expect(usageLimitResumeLine(wait, NOW)).toMatch(/^resumes .+ \(in 1h 12m\)$/);
    expect(usageLimitResumeLine(wait, NOW + 60_000)).toMatch(/\(in 1h 11m\)$/);
  });

  it("does not name a provider it does not know", () => {
    expect(usageLimitHeadline({ ...wait, provider: "" })).toBe("Waiting for usage limit");
  });

  it("does not promise a reset the provider never gave", () => {
    const probing = { ...wait, probing: true, resumesAt: NOW + 5 * 60_000 };
    const line = usageLimitResumeLine(probing, NOW);
    expect(line).toContain("no reset time given");
    expect(line).toContain("trying again");
    expect(line).not.toMatch(/^resumes/);
  });

  it("says it is resuming once the time has passed", () => {
    expect(usageLimitResumeLine(wait, wait.resumesAt + 1)).toBe("resuming now");
    expect(usageLimitResumeLine({ ...wait, probing: true }, wait.resumesAt + 1)).toBe("retrying now");
  });
});
