// Copyright (c) 2025 Reliant Labs

/**
 * THE CONFIRMATION TOKEN, and the two classifications that must not default.
 *
 * confirmationTokenFor is the safety property of this whole feature: it derives
 * the claim that authorises an irrecoverable overwrite from the plan document the
 * reviewer actually saw. These tests pin that exactly one claim is ever produced,
 * and that an ambiguous plan produces NONE rather than a guess — because both
 * available guesses are dangerous, and the dangerous direction is the one a
 * refactor would drift toward.
 */

import { describe, expect, it } from "vitest";

import {
  changeShapeOf,
  commitRangeKind,
  confirmationTokenFor,
  describeToken,
  hasCommitCount,
  imageChangeOf,
  isRollback,
  promoteDirectionOf,
  type ForgePromotePlan,
} from "../promote";

describe("confirmationTokenFor", () => {
  it("claims the release a bound env was shown as running", () => {
    const token = confirmationTokenFor({ current: { bound: true, release: "v1.3.0" } });
    expect(token).toEqual({ expectedCurrentRelease: "v1.3.0" });
    // Exactly one claim: expectUnbound must not be set alongside it, which the
    // server rejects as contradictory.
    expect(token?.expectUnbound).toBeFalsy();
  });

  it("claims unbound ONLY when the plan explicitly said bound:false", () => {
    const token = confirmationTokenFor({ current: { bound: false } });
    expect(token).toEqual({ expectUnbound: true });
    expect(token?.expectedCurrentRelease).toBeUndefined();
  });

  it("produces NO token when the plan does not say whether the env is bound", () => {
    // The dangerous case. `expectUnbound` here would ask the server to
    // blind-overwrite an environment that may well be bound.
    expect(confirmationTokenFor({ current: {} })).toBeNull();
    expect(confirmationTokenFor({})).toBeNull();
    expect(confirmationTokenFor(null)).toBeNull();
    expect(confirmationTokenFor(undefined)).toBeNull();
  });

  it("produces NO token for a binding it cannot name", () => {
    // Bound, but forge reported no release string. A fabricated name would ask
    // the guard to match something nobody saw.
    expect(confirmationTokenFor({ current: { bound: true } })).toBeNull();
    expect(confirmationTokenFor({ current: { bound: true, release: "   " } })).toBeNull();
  });

  it("trims the release so a padded value cannot fail the server's exact match", () => {
    expect(confirmationTokenFor({ current: { bound: true, release: " v1.3.0 " } })).toEqual({
      expectedCurrentRelease: "v1.3.0",
    });
  });

  it("describes the claim in the words the confirm step shows", () => {
    expect(describeToken({ expectedCurrentRelease: "v1.3.0" })).toContain("v1.3.0");
    expect(describeToken({ expectUnbound: true })).toContain("never been promoted");
  });
});

describe("promoteDirectionOf", () => {
  it("reads all five directions", () => {
    expect(promoteDirectionOf("ahead")).toBe("ahead");
    expect(promoteDirectionOf("behind")).toBe("behind");
    expect(promoteDirectionOf("same")).toBe("same");
    expect(promoteDirectionOf("initial")).toBe("initial");
    expect(promoteDirectionOf("unknown")).toBe("unknown");
  });

  it("falls back to unknown, never to same or ahead", () => {
    // `same` would report a rollback as a no-op; `ahead` would report it as a
    // forward promote. Both are worse than admitting ignorance.
    expect(promoteDirectionOf("sideways")).toBe("unknown");
    expect(promoteDirectionOf(undefined)).toBe("unknown");
    expect(promoteDirectionOf("")).toBe("unknown");
  });

  it("identifies a rollback", () => {
    expect(isRollback({ direction: "behind" })).toBe(true);
    expect(isRollback({ direction: "ahead" })).toBe(false);
    expect(isRollback({})).toBe(false);
  });
});

describe("imageChangeOf and changeShapeOf", () => {
  it("never defaults an unrecognised change to unchanged", () => {
    expect(imageChangeOf("rebased")).toBe("unknown");
    expect(imageChangeOf(undefined)).toBe("unknown");
    expect(changeShapeOf(imageChangeOf("rebased"))).toBe("unknown");
  });

  it("separates structural changes from a version move", () => {
    expect(changeShapeOf("added")).toBe("structural");
    expect(changeShapeOf("removed")).toBe("structural");
    expect(changeShapeOf("changed")).toBe("version");
    expect(changeShapeOf("unchanged")).toBe("none");
  });
});

describe("commitRangeKind", () => {
  it("treats only a computed range as measured", () => {
    expect(commitRangeKind("computed")).toBe("measured");
    expect(hasCommitCount({ state: "computed", count: 4 })).toBe(true);
  });

  it("separates empty-by-definition from unavailable", () => {
    // No range exists to compute — zero is true, and it was not measured.
    expect(commitRangeKind("first_promote")).toBe("empty-by-definition");
    expect(commitRangeKind("same_release")).toBe("empty-by-definition");
  });

  it.each([
    "dirty_release",
    "ledger_missing",
    "no_commit",
    "commit_not_found",
    "git_unavailable",
    "unknown",
  ])("treats %s as unavailable, so no count may be shown", (state) => {
    expect(commitRangeKind(state)).toBe("unavailable");
    // `count` is present and zero in every one of these; the guard is what keeps
    // "we could not tell" from rendering as "no changes".
    expect(hasCommitCount({ state, count: 0 })).toBe(false);
  });

  it("treats a state from a newer forge as unavailable", () => {
    expect(commitRangeKind("shallow_clone")).toBe("unavailable");
    expect(hasCommitCount({ state: "shallow_clone", count: 0 })).toBe(false);
  });
});

describe("plan typing", () => {
  it("reads current and target as NESTED objects", () => {
    // Pinned because a flattened fixture is the easy mistake, and it would make
    // every token derivation return null at runtime.
    const plan: ForgePromotePlan = {
      current: { bound: true, release: "v1.3.0" },
      target: { release: "v1.5.15", images: 4 },
    };
    expect(plan.current?.release).toBe("v1.3.0");
    expect(plan.target?.release).toBe("v1.5.15");
  });
});
