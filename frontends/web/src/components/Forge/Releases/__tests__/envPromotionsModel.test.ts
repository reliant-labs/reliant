// Copyright (c) 2025 Reliant Labs

import { describe, expect, it } from "vitest";

import type { ReleaseGate, ReleasePromotion } from "@/services/forge/releases";

import { HEALTH_GATE, envPromotionRows, promotionHealthOf } from "../envPromotionsModel";

const gate = (name: string, status: ReleaseGate["status"], finishedAt = "2026-11-01T12:00:00Z", summary = ""): ReleaseGate => ({
  name,
  status,
  rawStatus: "",
  summary,
  url: "",
  recordedBy: "",
  finishedAt,
  source: "recorded",
});

let seq = 0;
const promo = (version: string, over: Partial<ReleasePromotion> = {}): ReleasePromotion => ({
  id: `p${++seq}`,
  environmentId: "env-prod",
  releaseVersion: version,
  fromEnvironmentName: "",
  promotedByUserId: "",
  promotedByActor: "",
  approvedBy: "",
  note: "",
  createdAt: "2026-11-01T12:00:00Z",
  targets: [],
  gates: [],
  bundleId: "",
  bundleDigest: "",
  resolvedArtifacts: {},
  ...over,
});

// The release list, newest first: v1.41 is newer than v1.40.
const RANK = new Map([
  ["v1.42", 0],
  ["v1.41", 1],
  ["v1.40", 2],
]);

describe("envPromotionRows", () => {
  it("reads v1.40 → v1.41 → rollback to v1.40, with who and what each replaced", () => {
    const rows = envPromotionRows(
      [
        promo("v1.40", { promotedByActor: "bot" }),
        promo("v1.41", { promotedByUserId: "u-alice" }),
        promo("v1.40", { promotedByUserId: "u-alice" }),
      ],
      RANK,
      new Map([["u-alice", "alice"]])
    );
    expect(rows.map((row) => [row.version, row.by, row.previousVersion, row.rollback])).toEqual([
      ["v1.40", "bot", "v1.41", true],
      ["v1.41", "alice", "v1.40", false],
      ["v1.40", "alice", null, false],
    ]);
  });

  it("judges a scoped deploy against what last moved the same components, not the whole env", () => {
    const rows = envPromotionRows(
      [
        promo("v1.40", { targets: ["api"] }), // api goes back from v1.42: a rollback
        promo("v1.42", { targets: ["web"] }), // web only: says nothing about api
        promo("v1.42", { targets: ["api"] }),
        promo("v1.41"),
      ],
      RANK,
      new Map()
    );
    expect(rows[0]).toMatchObject({ previousVersion: "v1.42", rollback: true, targets: ["api"] });
    expect(rows[1]).toMatchObject({ previousVersion: "v1.41", rollback: false });
    expect(rows[2]).toMatchObject({ previousVersion: "v1.41", rollback: false });
  });

  it("keeps a redeploy as its own row, and does not judge a release outside the list", () => {
    const rows = envPromotionRows([promo("v1.41"), promo("v1.41"), promo("v0.9"), promo("v1.42")], RANK, new Map());
    expect(rows).toHaveLength(4);
    expect(rows[0]).toMatchObject({ redeploy: true, rollback: false });
    // v0.9 is not in the release list: unjudged, never guessed.
    expect(rows[2]).toMatchObject({ previousVersion: "v1.42", rollback: false });
  });

  it("keeps the ledger's checks apart from the health verdict", () => {
    const [row] = envPromotionRows([promo("v1.41", { gates: [gate("ci", "passed"), gate(HEALTH_GATE, "failed", undefined, "release health FAIL: p95 doubled")] })], RANK, new Map());
    expect(row!.checks).toMatchObject({ passed: 1, total: 1, failed: 0 });
    expect(row!.health).toEqual({ verdict: "regressed", summary: "release health FAIL: p95 doubled" });
  });
});

describe("promotionHealthOf", () => {
  it("is unknown — never healthy — when no health check was recorded, even with every other check green", () => {
    expect(promotionHealthOf([gate("ci", "passed")]).verdict).toBe("unknown");
    expect(promotionHealthOf([]).verdict).toBe("unknown");
  });

  it("takes the newest recorded verdict, and an inconclusive check is not a pass", () => {
    expect(promotionHealthOf([gate(HEALTH_GATE, "failed", "2026-11-01T12:00:00Z"), gate(HEALTH_GATE, "passed", "2026-11-01T12:30:00Z")]).verdict).toBe("healthy");
    expect(promotionHealthOf([gate(HEALTH_GATE, "error")]).verdict).toBe("inconclusive");
    expect(promotionHealthOf([gate(HEALTH_GATE, "skipped")]).verdict).toBe("unknown");
  });

  it("reads a gate recorded under the command's name as the same verdict", () => {
    expect(promotionHealthOf([gate("forge obs check", "passed")]).verdict).toBe("healthy");
    const [row] = envPromotionRows([promo("v1.41", { gates: [gate("forge obs check", "passed"), gate("ci", "passed")] })], RANK, new Map());
    expect(row!.checks).toMatchObject({ passed: 1, total: 1 });
  });
});
