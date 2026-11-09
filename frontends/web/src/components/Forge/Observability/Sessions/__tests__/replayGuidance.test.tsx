// Copyright (c) 2026 Reliant Labs

/**
 * The switches the Sessions notes name. Today's mode names only what the
 * pinned forge has (the build flag and ObservabilitySignals.replay); the
 * "telemetry" mode, for a forge with forge.BrowserTelemetry.replay
 * (forge#1983), is ready for the one-line flip of REPLAY_SWITCH.
 */

import { describe, expect, it } from "vitest";
import { render } from "@testing-library/react";

import { REPLAY_SWITCH, ReplayBlockedFix, ReplayBuildFlagNote, ReplayOffFix, ReplayTurnOnSteps, turnOnPhrase } from "../replayGuidance";

const text = (node: React.ReactElement) => render(<p>{node}</p>).container.textContent;

describe("replay guidance", () => {
  it("names only the pinned forge's switches", () => {
    expect(REPLAY_SWITCH).toBe("build-flag");
    for (const node of [<ReplayOffFix env="prod" />, <ReplayBuildFlagNote />, <ReplayBlockedFix env="prod" />, <ReplayTurnOnSteps />]) {
      expect(text(node)).not.toMatch(/BrowserTelemetry|telemetry\.replay/);
    }
    expect(text(<ReplayOffFix env="prod" />)).toContain("VITE_OTEL_REPLAY=true");
    expect(text(<ReplayOffFix env="prod" />)).toContain("signals = forge.ObservabilitySignals {replay = True}");
    expect(text(<ReplayTurnOnSteps />)).toContain("NEXT_PUBLIC_OTEL_REPLAY=true");
    expect(turnOnPhrase()).toBe("turn on both switches");
  });

  it("names telemetry.replay, and the build flag as deprecated, in telemetry mode", () => {
    expect(text(<ReplayOffFix env="prod" mode="telemetry" />)).toBe("set telemetry = forge.BrowserTelemetry {replay = True} on that frontend and redeploy.");
    expect(text(<ReplayBuildFlagNote mode="telemetry" />)).toContain("deprecated build flag");
    expect(text(<ReplayBlockedFix env="prod" mode="telemetry" />)).toContain("upgrade forge");
    expect(text(<ReplayTurnOnSteps mode="telemetry" />)).not.toContain("VITE_OTEL_REPLAY");
    expect(turnOnPhrase("telemetry")).toBe("turn it on");
  });
});
