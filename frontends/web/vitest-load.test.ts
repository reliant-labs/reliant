import { describe, expect, it } from "vitest";
import { GATE_LANE_MIN_FACTOR, MAX_LOAD_FACTOR, timeoutLoadFactor } from "./vitest-load";

describe("timeoutLoadFactor", () => {
  it("is 1x on an idle host outside a gate", () => {
    expect(timeoutLoadFactor([0.5, 0.4, 0.3], 16, false)).toBe(1);
  });

  it("follows the busiest average, so a quiet minute does not hide sustained load", () => {
    // 48 cores: the 1-minute average alone says 1x, the 15-minute one 3x.
    expect(timeoutLoadFactor([20, 90, 115], 48, false)).toBe(3);
  });

  it("gives a gate lane at least 3x, for the load that starts with vitest", () => {
    expect(timeoutLoadFactor([1, 1, 1], 48, true)).toBe(GATE_LANE_MIN_FACTOR);
    expect(timeoutLoadFactor([250, 200, 180], 48, true)).toBe(MAX_LOAD_FACTOR);
  });

  it("is capped at 6x, and ignores averages a platform cannot report", () => {
    expect(timeoutLoadFactor([1000, 1000, 1000], 4, false)).toBe(MAX_LOAD_FACTOR);
    expect(timeoutLoadFactor([Number.NaN, 0, 0], 0, false)).toBe(1);
  });
});
