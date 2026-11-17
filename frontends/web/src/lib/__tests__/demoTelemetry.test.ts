// Copyright (c) 2025 Reliant Labs

import { describe, expect, it, vi } from "vitest";

const config = vi.hoisted(() => ({ DEMO_TELEMETRY: "replay" }));
vi.mock("../config_gen", () => ({ loadConfig: () => config }));

import { DEMO_REPLAY_ON_PHONES, demoTelemetry, isPhone } from "../demoTelemetry";

/** A window whose media queries answer as a device with this pointer and width would. */
const device = (pointer: "coarse" | "fine", width: number) => ({
  matchMedia: (query: string) => ({
    matches:
      query === `(pointer: ${pointer})` || (query.startsWith("(max-width: ") && width <= Number(/\d+/.exec(query)![0])),
  }),
});

describe("isPhone", () => {
  it("is a touch screen no wider than 768px", () => {
    expect(isPhone(device("coarse", 390))).toBe(true);
    expect(isPhone(device("coarse", 768))).toBe(true);
  });

  it("is not a tablet, a narrow desktop window, or a browser without media queries", () => {
    expect(isPhone(device("coarse", 1024))).toBe(false);
    expect(isPhone(device("fine", 390))).toBe(false);
    expect(isPhone({})).toBe(false);
    expect(isPhone(undefined)).toBe(false);
  });
});

describe("demoTelemetry on a phone", () => {
  it("records replay on phones by default", () => {
    expect(DEMO_REPLAY_ON_PHONES).toBe(true);
    config.DEMO_TELEMETRY = "replay";
    expect(demoTelemetry(false, true)).toBe("replay");
    expect(demoTelemetry(false, false)).toBe("replay");
  });

  it("the switch turns replay off on a phone only, and back on", () => {
    config.DEMO_TELEMETRY = "replay";
    expect(demoTelemetry(false, true, false)).toBe("analytics");
    expect(demoTelemetry(false, false, false)).toBe("replay");
    expect(demoTelemetry(false, true, true)).toBe("replay");
    expect(demoTelemetry(false, false, true)).toBe("replay");
  });

  it("leaves a lower configured level, and an opted-out browser, as they are", () => {
    for (const replayOnPhones of [true, false]) {
      config.DEMO_TELEMETRY = "analytics";
      expect(demoTelemetry(false, true, replayOnPhones)).toBe("analytics");
      config.DEMO_TELEMETRY = "off";
      expect(demoTelemetry(false, true, replayOnPhones)).toBe("off");
      config.DEMO_TELEMETRY = "replay";
      expect(demoTelemetry(true, true, replayOnPhones)).toBe("off");
      expect(demoTelemetry(true, false, replayOnPhones)).toBe("off");
    }
  });
});
