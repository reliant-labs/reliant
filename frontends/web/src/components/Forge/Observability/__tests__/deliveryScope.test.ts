// Copyright (c) 2025 Reliant Labs

/**
 * The delivery views' pure rules: which environments a ledger request covers
 * (the scope on screen → DeliveryScope), the before/after windows of a release
 * comparison, how a change reads, and the DORA formatting.
 */

import { describe, expect, it, vi } from "vitest";

import type { Promotion } from "@/services/controlPlane/observability/deliveryTypes";
import type { ObsScope } from "@/services/controlPlane/observability/types";

import {
  changeText,
  compareParam,
  comparisonWindows,
  deliveryScopeOf,
  formatPerDay,
  formatSpan,
  movement,
  neighboursOf,
  parseCompareParam,
} from "../deliveryScope";
import { visibleMarkers } from "../charts/UPlotChart";
import { describePromotion, toChartMarkers } from "../releaseMarkers";

vi.mock("uplot", async () => (await import("./obsMocks")).obsMocks.uplot());

const ENVS = [
  { id: "env-dev", name: "dev" },
  { id: "env-staging", name: "staging" },
  { id: "env-prod", name: "prod" },
];
const scope = (patch: Partial<ObsScope> = {}): ObsScope => ({ orgId: "org-1", projectId: "shop", envIds: [], filters: [], ...patch });

describe("the ledger request follows the scope on screen", () => {
  it("takes the org, project and environments in view, and nothing else", () => {
    expect(deliveryScopeOf(scope({ envIds: ["env-prod"], filters: [{ key: "service.name", op: "eq", value: "api" }] }), ENVS)).toEqual({
      orgId: "org-1",
      projectId: "shop",
      envIds: ["env-prod"],
    });
    // Empty: every environment of the project (the server's own default).
    expect(deliveryScopeOf(scope(), ENVS)?.envIds).toEqual([]);
  });

  it("maps an environment filter to env ids, narrowing the picked ones", () => {
    expect(deliveryScopeOf(scope({ filters: [{ key: "deployment.environment.name", op: "eq", value: "staging" }] }), ENVS)?.envIds).toEqual(["env-staging"]);
    expect(
      deliveryScopeOf(scope({ filters: [{ key: "deployment.environment.name", op: "in", values: ["staging", "prod"] }] }), ENVS)?.envIds
    ).toEqual(["env-staging", "env-prod"]);
    expect(
      deliveryScopeOf(scope({ envIds: ["env-prod", "env-dev"], filters: [{ key: "deployment.environment.name", op: "in", values: ["staging", "prod"] }] }), ENVS)?.envIds
    ).toEqual(["env-prod"]);
    expect(deliveryScopeOf(scope({ filters: [{ key: "reliant.env.id", op: "eq", value: "env-dev" }] }), ENVS)?.envIds).toEqual(["env-dev"]);
    expect(deliveryScopeOf(scope({ filters: [{ key: "deployment.environment.name", op: "neq", value: "dev" }] }), ENVS)?.envIds).toEqual([
      "env-staging",
      "env-prod",
    ]);
  });

  it("asks for nothing when the filters exclude every environment, rather than for all of them", () => {
    expect(deliveryScopeOf(scope({ envIds: ["env-prod"], filters: [{ key: "deployment.environment.name", op: "eq", value: "dev" }] }), ENVS)).toBeNull();
    expect(deliveryScopeOf(scope({ filters: [{ key: "deployment.environment.name", op: "eq", value: "nowhere" }] }), ENVS)).toBeNull();
  });
});

const promotion = (patch: Partial<Promotion> = {}): Promotion => ({
  promotionId: "promo-1",
  envId: "env-prod",
  envName: "prod",
  releaseId: "rel-v1.8.3",
  version: "v1.8.3",
  gitCommit: "3f2a9c1d0e8b7a6f5e4d3c2b1a0f9e8d7c6b5a49",
  gitTag: "v1.8.3",
  promotedAtMs: Date.UTC(2026, 9, 14, 12),
  promotedBy: "Ada Lovelace",
  promotedByUserId: "user-ada",
  kind: "promote",
  ...patch,
});

describe("release markers", () => {
  it("name the version, the environment when several are in view, and a rollback as one", () => {
    const [plain, rollback] = toChartMarkers([promotion(), promotion({ promotionId: "promo-2", kind: "rollback", version: "v1.8.2", envName: "staging" })], true);
    expect(plain).toMatchObject({ timeSec: Date.UTC(2026, 9, 14, 12) / 1000, label: "prod v1.8.3", tone: "primary" });
    expect(plain.description).toBe("v1.8.3 (commit 3f2a9c1) promoted to prod by Ada Lovelace");
    expect(rollback).toMatchObject({ label: "staging ↩ v1.8.2", tone: "warning" });
    expect(rollback.description).toBe("staging rolled back to v1.8.2 (commit 3f2a9c1) by Ada Lovelace");
    expect(toChartMarkers([promotion()], false)[0].label).toBe("v1.8.3");
  });

  it("land on the chart when they fall in the newest bucket, not only before its start", () => {
    // Buckets start at 0, 60, 120: the chart spans to 180.
    const shown = visibleMarkers([{ timeSec: 150, label: "now-ish" }, { timeSec: 200, label: "future" }, { timeSec: -5, label: "before" }], [0, 60, 120]);
    expect(shown.map((m) => m.label)).toEqual(["now-ish"]);
  });

  it("say so when the ledger recorded no commit or actor", () => {
    expect(describePromotion(promotion({ gitCommit: "", promotedBy: "" }))).toBe("v1.8.3 promoted to prod");
  });
});

describe("a comparison is a link", () => {
  it("round-trips a promotion through the URL, and refuses a mangled one", () => {
    const p = promotion();
    expect(parseCompareParam(compareParam(p))).toEqual({ envId: "env-prod", promotedAtMs: p.promotedAtMs, promotionId: "promo-1" });
    expect(parseCompareParam("env~with~tildes~1760000000000~promo-9")).toEqual({ envId: "env~with~tildes", promotedAtMs: 1760000000000, promotionId: "promo-9" });
    expect(parseCompareParam("nonsense")).toBeNull();
    expect(parseCompareParam("env-prod~notatime~promo-1")).toBeNull();
    expect(parseCompareParam(undefined)).toBeNull();
  });
});

describe("comparison windows", () => {
  const at = Date.UTC(2026, 9, 14, 12);
  const min = 60_000;

  it("puts equal windows either side of the promotion", () => {
    const w = comparisonWindows(at, 30 * min, at + 5 * 60 * min);
    expect(w).toMatchObject({ before: { startMs: at - 30 * min, endMs: at }, after: { startMs: at, endMs: at + 30 * min }, lengthMs: 30 * min, clampedBy: null, tooShort: false });
  });

  it("stops at the present, and keeps both sides as long as the after side", () => {
    const w = comparisonWindows(at, 60 * min, at + 40 * min);
    expect(w).toMatchObject({ lengthMs: 40 * min, clampedBy: "now", before: { startMs: at - 40 * min } });
  });

  it("stops short of the next and the previous promotion into the environment", () => {
    expect(comparisonWindows(at, 6 * 60 * min, at + 48 * 60 * min, { nextMs: at + 3 * 60 * min })).toMatchObject({ lengthMs: 3 * 60 * min, clampedBy: "next" });
    expect(comparisonWindows(at, 6 * 60 * min, at + 48 * 60 * min, { previousMs: at - 2 * 60 * min })).toMatchObject({ lengthMs: 2 * 60 * min, clampedBy: "previous" });
  });

  it("says there is nothing to compare under a minute", () => {
    expect(comparisonWindows(at, 30 * min, at + 20_000)).toMatchObject({ tooShort: true, clampedBy: "now" });
  });

  it("finds the neighbours in the same environment only", () => {
    const list = [
      promotion({ promotionId: "a", promotedAtMs: at - 5 * min }),
      promotion({ promotionId: "x", envId: "env-dev", promotedAtMs: at - min }),
      promotion({ promotionId: "self", promotedAtMs: at }),
      promotion({ promotionId: "b", promotedAtMs: at + 7 * min }),
    ];
    const { previous, next } = neighboursOf({ envId: "env-prod", promotedAtMs: at, promotionId: "self" }, list);
    expect([previous?.promotionId, next?.promotionId]).toEqual(["a", "b"]);
  });
});

describe("how a change reads", () => {
  it("calls latency and error rate going up worse, and noise flat", () => {
    expect(movement(118, 171, true)).toBe("up-bad");
    expect(movement(171, 118, true)).toBe("down-good");
    expect(movement(100, 105, true)).toBe("flat");
    expect(movement(11.5, 12.1, null)).toBe("flat");
    expect(movement(10, 20, null)).toBe("up-good");
    expect(movement(null, 3, true)).toBe("none");
    expect(changeText(118, 171)).toBe("+45%");
    expect(changeText(200, 150)).toBe("−25%");
    expect(changeText(0, 5)).toBe("new");
    expect(changeText(0.004, 0.016, "points")).toBe("+1.2 pts");
  });
});

describe("DORA formatting", () => {
  it("spells spans and frequencies as a person says them", () => {
    expect(formatSpan(3 * 3_600_000)).toBe("3h");
    expect(formatSpan(3 * 3_600_000 + 12 * 60_000)).toBe("3h 12m");
    expect(formatSpan(52 * 3_600_000)).toBe("2d 4h");
    expect(formatSpan(45_000)).toBe("45s");
    expect(formatSpan(null)).toBe("—");
    expect(formatPerDay(13 / 30)).toBe("1 every 2.3 days");
    expect(formatPerDay(2.43)).toBe("2.4 a day");
    expect(formatPerDay(0)).toBe("None");
  });
});
