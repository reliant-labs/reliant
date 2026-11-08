// Copyright (c) 2025 Reliant Labs

import { describe, expect, it } from "vitest";

import { envHealth, inFlight, verdictBusy, verdictTone } from "../appParts";

describe("envHealth", () => {
  it("is Healthy, in green, only when converged with nothing firing", () => {
    expect(envHealth("converged", 0)).toEqual({ label: "Healthy", tone: "success", alerting: false, busy: false });
  });

  it("leads with the alerts when converged pods have alerts firing, never green", () => {
    expect(envHealth("converged", 2)).toEqual({ label: "Alerts firing (2)", tone: "warning", alerting: true, busy: false });
    // Firing alerts outrank a rollout in flight: their warning dot, not the spinner.
    expect(envHealth("converging", 1)).toEqual({ label: "Alerts firing (1)", tone: "warning", alerting: true, busy: false });
  });

  it("keeps a worse verdict's own word and tone, with the count after it", () => {
    expect(envHealth("degraded", 3)).toEqual({ label: "Not serving · Alerts firing (3)", tone: "danger", alerting: true, busy: false });
  });

  it("spins while settling, in no colour", () => {
    expect(envHealth("converging", 0)).toEqual({ label: "Settling", tone: "neutral", alerting: false, busy: true });
  });
});

describe("the in-flight rule", () => {
  it("is busy only for converging, so diverged and degraded keep their red dot", () => {
    expect(verdictBusy("converging")).toBe(true);
    for (const verdict of ["converged", "diverged", "degraded", "unknown"] as const) expect(verdictBusy(verdict)).toBe(false);
    expect(verdictTone("diverged")).toBe("danger");
    expect(verdictTone("degraded")).toBe("danger");
    expect(verdictTone("converging")).toBe("neutral");
  });

  it("spins for a deploy in progress unless the environment is already known-bad", () => {
    expect(inFlight("converged", true)).toBe(true);
    expect(inFlight("unknown", true)).toBe(true);
    expect(inFlight("converging", false)).toBe(true);
    expect(inFlight("converged", false)).toBe(false);
    expect(inFlight("diverged", true)).toBe(false);
    expect(inFlight("degraded", true)).toBe(false);
  });
});
