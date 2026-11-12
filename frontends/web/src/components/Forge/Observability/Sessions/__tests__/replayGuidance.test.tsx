// Copyright (c) 2026 Reliant Labs

/**
 * The switches the Sessions notes name. Today's mode names only what the
 * pinned forge has (the frontend's runtime_config.OTEL_REPLAY and
 * ObservabilitySignals.replay), never the deprecated build flag: the pinned
 * SDK reads the runtime config first, so a build flag cannot override the
 * explicit OTEL_REPLAY = "false" a scaffolded environment writes. The
 * "telemetry" mode, for a forge with forge.BrowserTelemetry.replay
 * (forge#1983), is ready for the one-line flip of REPLAY_SWITCH.
 */

import { describe, expect, it } from "vitest";
import { render } from "@testing-library/react";

import { REPLAY_SWITCH, ReplayBlockedFix, ReplayBuildFlagNote, ReplayOffFix, ReplayTurnOnSteps, turnOnPhrase } from "../replayGuidance";

const text = (node: React.ReactElement) => render(<p>{node}</p>).container.textContent;

describe("replay guidance", () => {
  it("names only the pinned forge's switches", () => {
    expect(REPLAY_SWITCH).toBe("runtime-config");
    for (const node of [<ReplayOffFix env="prod" />, <ReplayBuildFlagNote />, <ReplayBlockedFix env="prod" />, <ReplayTurnOnSteps />]) {
      expect(text(node)).not.toMatch(/BrowserTelemetry|telemetry\.replay/);
    }
    expect(text(<ReplayOffFix env="prod" />)).toContain("signals = forge.ObservabilitySignals {replay = True}");
    expect(turnOnPhrase()).toBe("turn on both switches");
  });

  it("turns on a scaffolded frontend whose runtime config says OTEL_REPLAY = \"false\"", () => {
    // forge's scaffolded deploy KCL writes runtime_config = {OTEL_REPLAY = "true" if _session_replay else "false"},
    // and its browser SDK reads that before any build flag: the remedy is the runtime config, never the build flag.
    for (const node of [<ReplayOffFix env="prod" />, <ReplayTurnOnSteps />]) {
      expect(text(node)).toContain('runtime_config = {OTEL_REPLAY = "true"}');
      expect(text(node)).toContain("_session_replay = True");
      expect(text(node)).not.toMatch(/VITE_OTEL_REPLAY|NEXT_PUBLIC_OTEL_REPLAY/);
    }
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
