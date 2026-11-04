// Copyright (c) 2025 Reliant Labs

import { describe, expect, it } from "vitest";

import { previousReleaseOf, promotionIsSuspect, rollbackTargets, SUSPECT_WINDOW_MS } from "../rollback";

const ledger = (...versions: string[]) => versions.map((releaseVersion) => ({ releaseVersion }));

describe("what an environment rolls back to", () => {
  it("is the release it ran before its current one, not the one cut before it", () => {
    // prod went v1 → v1 (re-promoted) → v4: it rolls back to v1, the last one known to work there.
    expect(previousReleaseOf(ledger("v4", "v1", "v1"), "v4")).toBe("v1");
  });

  it("skips component promotions when selecting a whole-environment rollback", () => {
    expect(previousReleaseOf([
      { releaseVersion: "v3", targets: [] },
      { releaseVersion: "v2", targets: ["api"] },
      { releaseVersion: "v1", targets: [] },
    ], "v3")).toBe("v1");
    expect(previousReleaseOf([
      { releaseVersion: "v2", targets: ["api"] },
      { releaseVersion: "v1", targets: [] },
    ], "v2")).toBeNull();
    expect(previousReleaseOf([
      { releaseVersion: "v3", targets: ["api"] },
      { releaseVersion: "v3", targets: [] },
      { releaseVersion: "v2", targets: ["web"] },
      { releaseVersion: "v1", targets: [] },
    ], "v3")).toBe("v1");
  });

  it("is nothing for an environment that only ever ran one release", () => {
    expect(previousReleaseOf(ledger("v1", "v1"), "v1")).toBeNull();
    expect(previousReleaseOf(undefined, "v1")).toBeNull();
  });

  it("is nothing when the ledger page does not name the current release", () => {
    expect(previousReleaseOf(ledger("v3", "v2"), "v4")).toBeNull();
  });

  it("is collected per environment, by name", () => {
    const targets = rollbackTargets(
      [
        { id: "e-prod", name: "prod", release: "v4" },
        { id: "e-dev", name: "dev", release: "v5" },
      ],
      new Map([
        ["e-prod", ledger("v4", "v3")],
        ["e-dev", ledger("v5")],
      ])
    );
    expect([...targets.keys()]).toEqual(["prod"]);
    expect(targets.get("prod")).toEqual({ env: "prod", current: "v4", previous: "v3" });
  });
});

describe("whether trouble followed a promotion", () => {
  const promotedAt = "2026-10-26T10:00:00Z";
  const at = Date.parse(promotedAt);
  it("names the promotion within the window after it, and not before it or long after", () => {
    expect(promotionIsSuspect(promotedAt, at + 4 * 60_000)).toBe(true);
    expect(promotionIsSuspect(promotedAt, at - 60_000)).toBe(false);
    expect(promotionIsSuspect(promotedAt, at + SUSPECT_WINDOW_MS + 1)).toBe(false);
    expect(promotionIsSuspect(undefined, at)).toBe(false);
  });
});
