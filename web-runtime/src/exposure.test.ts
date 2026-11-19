// The contract of feature-flag exposures: one flag_evaluated per person,
// flag and variant per day, recorded through track()'s gates, and the
// OpenFeature hook's reading of an evaluation.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  FLAG_EVALUATED_EVENT,
  forgeExposureHook,
  resetExposureState,
  trackExposure,
} from "./exposure.js";
import {
  optOutOfAnalytics,
  resetTrackState,
  setTelemetrySink,
  setTelemetryUser,
  type TelemetrySink,
} from "./track.js";

let events: Array<[string, Record<string, unknown>]>;
const warn = vi.fn();

beforeEach(() => {
  resetTrackState();
  resetExposureState();
  events = [];
  vi.stubGlobal("window", {});
  vi.stubGlobal("navigator", {});
  vi.spyOn(console, "warn").mockImplementation(warn);
  vi.useFakeTimers();
  vi.setSystemTime(new Date("2026-11-10T09:00:00Z"));
  const sink: TelemetrySink = {
    track: (name, attributes) => events.push([name, attributes]),
    setUser: () => {},
    disable: () => {},
  };
  setTelemetrySink(sink);
});

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  warn.mockReset();
});

describe("trackExposure", () => {
  it("records one exposure per person, flag and variant per UTC day", () => {
    trackExposure("new-checkout", "treatment", { provider: "LaunchDarkly" });
    trackExposure("new-checkout", "treatment");
    trackExposure("new-checkout", "control");
    setTelemetryUser("u-1");
    trackExposure("new-checkout", "treatment");
    vi.setSystemTime(new Date("2026-11-11T00:00:01Z"));
    trackExposure("new-checkout", "treatment");

    expect(events).toEqual([
      [
        FLAG_EVALUATED_EVENT,
        {
          flag: "new-checkout",
          variant: "treatment",
          provider: "launchdarkly",
        },
      ],
      [FLAG_EVALUATED_EVENT, { flag: "new-checkout", variant: "control" }],
      [FLAG_EVALUATED_EVENT, { flag: "new-checkout", variant: "treatment" }],
      [FLAG_EVALUATED_EVENT, { flag: "new-checkout", variant: "treatment" }],
    ]);
    // A runtime event: no reserved-prefix warning.
    expect(warn).not.toHaveBeenCalled();
  });

  it("records booleans and numbers by value and ignores an empty flag", () => {
    trackExposure("dark-mode", true);
    trackExposure("limit", 3);
    trackExposure("", "on");
    expect(events.map(([, p]) => p.variant)).toEqual(["true", "3"]);
  });

  it("records nothing after optOutOfAnalytics", () => {
    optOutOfAnalytics();
    trackExposure("new-checkout", "treatment");
    expect(events).toEqual([]);
  });
});

describe("forgeExposureHook", () => {
  it("records the variant name, else a scalar value, with the provider", () => {
    forgeExposureHook.after(
      { flagKey: "a", providerMetadata: { name: "flagd" } },
      { flagKey: "a", value: "blue", variant: "v2" },
    );
    forgeExposureHook.after({ flagKey: "b" }, { flagKey: "b", value: false });
    expect(events).toEqual([
      [FLAG_EVALUATED_EVENT, { flag: "a", variant: "v2", provider: "flagd" }],
      [FLAG_EVALUATED_EVENT, { flag: "b", variant: "false" }],
    ]);
  });

  it("records an object flag with no variant as `object`, never its contents", () => {
    forgeExposureHook.after(
      { flagKey: "config" },
      { flagKey: "config", value: { email: "jane@example.com" } },
    );
    expect(events).toEqual([
      [FLAG_EVALUATED_EVENT, { flag: "config", variant: "object" }],
    ]);
  });

  it("records no failed evaluation", () => {
    forgeExposureHook.after(
      { flagKey: "a" },
      { flagKey: "a", value: false, errorCode: "FLAG_NOT_FOUND" },
    );
    expect(events).toEqual([]);
  });
});
