// Copyright (c) 2026 Reliant Labs

import { describe, expect, it } from "vitest";

import type { MlFinding } from "@/services/controlPlane/ml/types";

import { findingsWorstFirst, flagWords, seconds, shortDigest } from "../mlModel";

const finding = (id: string, severity: MlFinding["severity"], lastSeenAtMs: number): MlFinding => ({
  id,
  ruleId: "",
  severity,
  title: id,
  summary: "",
  clusterId: "",
  subject: "",
  evidence: [],
  fixKcl: "",
  fixCommand: "",
  firstSeenAtMs: null,
  lastSeenAtMs,
  podStart: null,
});

describe("the ML pages' words", () => {
  it("shortens a digest to 12 hex characters", () => {
    expect(shortDigest(`sha256:${"ab12".repeat(16)}`)).toBe("sha256:ab12ab12ab12…");
    expect(shortDigest("sha256:abcd")).toBe("sha256:abcd");
    expect(shortDigest("latest")).toBe("latest");
  });

  it("says a health flag as a sentence", () => {
    const base = { message: "", firstSeenAtMs: null, lastSeenAtMs: null };
    expect(flagWords({ ...base, kind: "xid", code: "79", device: "GPU-5" })).toBe("Xid 79 on GPU-5");
    expect(flagWords({ ...base, kind: "allocatable_below_capacity", code: "", device: "" })).toBe("Allocatable below capacity");
  });

  it("orders findings worst first, then most recently seen", () => {
    const ordered = findingsWorstFirst([finding("info", "info", 9), finding("warn-old", "warning", 1), finding("crit", "critical", 0), finding("warn-new", "warning", 5)]);
    expect(ordered.map((f) => f.id)).toEqual(["crit", "warn-new", "warn-old", "info"]);
  });

  it("writes a stage's time for a reader", () => {
    expect(seconds(0.85)).toBe("850 ms");
    expect(seconds(4.2)).toBe("4.2 s");
    expect(seconds(190)).toBe("3 min 10 s");
  });
});
