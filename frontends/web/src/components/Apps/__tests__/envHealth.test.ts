// Copyright (c) 2025 Reliant Labs

import { describe, expect, it } from "vitest";

import { envHealth } from "../appParts";

describe("envHealth", () => {
  it("is Healthy, in green, only when converged with nothing firing", () => {
    expect(envHealth("converged", 0)).toEqual({ label: "Healthy", tone: "success", alerting: false });
  });

  it("leads with the alerts when converged pods have alerts firing, never green", () => {
    expect(envHealth("converged", 2)).toEqual({ label: "Alerts firing (2)", tone: "warning", alerting: true });
    expect(envHealth("converging", 1)).toEqual({ label: "Alerts firing (1)", tone: "warning", alerting: true });
  });

  it("keeps a worse verdict's own word and tone, with the count after it", () => {
    expect(envHealth("degraded", 3)).toEqual({ label: "Not serving · Alerts firing (3)", tone: "danger", alerting: true });
  });
});
