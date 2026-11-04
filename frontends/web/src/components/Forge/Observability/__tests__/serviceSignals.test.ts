// Copyright (c) 2025 Reliant Labs

/**
 * The per-service signal verdict: sent is offered, declared-but-idle is muted
 * with a hint, neither is not offered, and anything uncertain is offered.
 */

import { describe, expect, it } from "vitest";

import type { ServiceSignalsResult } from "@/services/controlPlane/observability/types";

import { servicesFor, signalsIndex, signalsRange, signalState } from "../serviceSignals";
import { FIXTURE_SIGNALS } from "../__fixtures__/fixtureSignals";

describe("signalState", () => {
  const index = signalsIndex(FIXTURE_SIGNALS, "1h");

  it("offers what a service sends, declared or not", () => {
    expect(signalState(index, "api", "profiles")).toEqual({ kind: "available" });
    expect(signalState(index, "worker", "logs")).toEqual({ kind: "available" });
    expect(signalState(index, "worker", "traces")).toEqual({ kind: "available" });
  });

  it("hides a signal a service neither sends nor declares", () => {
    expect(signalState(index, "worker", "profiles")).toEqual({ kind: "absent" });
    // Not in a complete answer at all: it sent nothing and declares nothing.
    expect(signalState(index, "reliant-daemon", "logs")).toEqual({ kind: "absent" });
  });

  it("mutes a declared signal that is idle, saying over which window", () => {
    expect(signalState(index, "batch", "profiles")).toEqual({
      kind: "idle",
      badge: "none in 24h",
      hint: "No profiles from batch in the last 24 hours",
    });
    expect(signalState(signalsIndex(FIXTURE_SIGNALS, "7d"), "batch", "profiles")).toMatchObject({ badge: "none in 7d" });
  });

  it("offers everything when it cannot judge", () => {
    // all services
    expect(signalState(index, undefined, "profiles")).toEqual({ kind: "available" });
    // unknown: loading, failed, or a control plane that predates the RPC
    expect(signalState(null, "worker", "profiles")).toEqual({ kind: "available" });
    // a service missing from a truncated answer
    const truncated: ServiceSignalsResult = { ...FIXTURE_SIGNALS, truncated: true };
    expect(signalState(signalsIndex(truncated, "1h"), "reliant-daemon", "logs")).toEqual({ kind: "available" });
  });

  it("never judges over less than a day", () => {
    expect(signalsRange("15m")).toBe("24h");
    expect(signalsRange("24h")).toBe("24h");
    expect(signalsRange("7d")).toBe("7d");
  });
});

describe("servicesFor", () => {
  const index = signalsIndex(FIXTURE_SIGNALS, "1h");
  const seen = ["api", "worker", "reliant-daemon"];

  it("lists the senders, then the idle declarers, and drops the rest", () => {
    expect(servicesFor(index, "profiles", seen, undefined).map((row) => [row.name, row.state.kind])).toEqual([
      ["api", "available"],
      ["batch", "idle"],
    ]);
  });

  it("keeps the current selection, even one with nothing to show", () => {
    expect(servicesFor(index, "profiles", seen, "worker").map((row) => row.name)).toContain("worker");
  });

  it("keeps every service it knew when it cannot judge", () => {
    expect(servicesFor(null, "profiles", seen, undefined).map((row) => row.name)).toEqual(seen);
  });
});
