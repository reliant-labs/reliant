// Copyright (c) 2025 Reliant Labs

/**
 * Promote fixtures, built from REAL forge output.
 *
 * The two plans mirror the reference project's actual states: staging promoting
 * forward to a newer release with a meaningless commit range (its current
 * release was cut from a dirty tree), and prod rolling BACK to an older release,
 * which classifies an image as `removed`. Using the real shapes matters because
 * the nesting is the part a hand-written fixture tends to flatten — `current` and
 * `target` are objects, not fields.
 */

import { ForgeReachability } from "@/gen/reliant/v1/forge_pb";
import type { ForgeReportMeta } from "@/gen/reliant/v1/forge_pb";
import type { ForgePromotePlan } from "@/services/forge/promote";
import type { ForgeOutcome } from "@/services/forge/topology";

export function meta(overrides: Partial<ForgeReportMeta> = {}): ForgeReportMeta {
  return {
    isForgeProject: true,
    supported: true,
    forgeVersion: "v0.9.1",
    unsupportedReason: "",
    exitCode: 0,
    reachability: ForgeReachability.UNSPECIFIED,
    unreachableReason: "",
    ...overrides,
  } as ForgeReportMeta;
}

export function planOutcome(plan: ForgePromotePlan): ForgeOutcome<ForgePromotePlan> {
  return { kind: "report", meta: meta(), report: plan };
}

/**
 * staging → v1.5.15: FORWARD, 20 releases ahead, and the commit range is
 * MEANINGLESS because the current release was cut from a dirty tree.
 */
export function forwardPlan(overrides: Partial<ForgePromotePlan> = {}): ForgePromotePlan {
  return {
    env: "staging",
    release: "v1.5.15",
    ledger: ".forge/releases",
    generated_at: "2026-09-10T14:00:00Z",
    dry_run: true,
    applied: false,
    current: {
      bound: true,
      release: "v1.3.0",
      promoted_at: "2026-07-01T00:25:15Z",
      release_known: true,
      release_created_at: "2026-07-01T00:25:07Z",
      git: { commit: "1bdc3226aa11", dirty: true },
    },
    target: {
      release: "v1.5.15",
      created_at: "2026-09-10T13:53:22Z",
      git: { commit: "85853150bb22" },
      images: 4,
    },
    direction: "ahead",
    direction_detail: "FORWARD — v1.5.15 is 20 release(s) NEWER than v1.3.0",
    releases_between: 20,
    images: [
      {
        image: "control-plane",
        change: "changed",
        current_digest: "sha256:c3b0074aaaaa",
        target_digest: "sha256:1ea5668bbbbb",
      },
      { image: "internal-console", change: "added", target_digest: "sha256:1ef891ecccccc" },
    ],
    tally: { unchanged: 0, changed: 3, added: 1, removed: 0 },
    commits: {
      state: "dirty_release",
      detail:
        "commit range is MEANINGLESS — v1.3.0 was cut from a dirty tree, so its recorded commit does not describe what shipped",
      count: 0,
      commits: [],
    },
    changed: true,
    ships_nothing: true,
    next_step: "forge env deploy staging",
    note: "Promoting writes a pointer. Nothing is deployed until `forge env deploy staging` runs.",
    ok: true,
    ...overrides,
  };
}

/**
 * prod → v1.3.0: a ROLLBACK. internal-console is `removed`, and the commit range
 * IS computed with reverts set — those commits are being taken away.
 */
export function rollbackPlan(overrides: Partial<ForgePromotePlan> = {}): ForgePromotePlan {
  return {
    env: "prod",
    release: "v1.3.0",
    ledger: ".forge/releases",
    generated_at: "2026-09-10T14:00:00Z",
    dry_run: true,
    applied: false,
    current: {
      bound: true,
      release: "v1.5.15",
      promoted_at: "2026-09-09T10:00:00Z",
      release_known: true,
      git: { commit: "85853150bb22" },
    },
    target: {
      release: "v1.3.0",
      created_at: "2026-07-01T00:25:07Z",
      git: { commit: "1bdc3226aa11" },
      images: 3,
    },
    direction: "behind",
    direction_detail: "ROLLBACK — v1.3.0 is 20 release(s) OLDER than v1.5.15, which moves the environment BACKWARDS",
    releases_between: 20,
    images: [
      {
        image: "control-plane",
        change: "changed",
        current_digest: "sha256:1ea5668bbbbb",
        target_digest: "sha256:c3b0074aaaaa",
      },
      { image: "internal-console", change: "removed", current_digest: "sha256:1ef891ecccccc" },
    ],
    tally: { unchanged: 2, changed: 1, added: 0, removed: 1 },
    commits: {
      state: "computed",
      reverts: true,
      from_commit: "85853150bb22",
      to_commit: "1bdc3226aa11",
      count: 2,
      commits: ["85853150 billing: guide identity-less users", "5dc5095a daemon: fix drain"],
    },
    changed: true,
    ships_nothing: true,
    next_step: "forge env deploy prod",
    ok: true,
    ...overrides,
  };
}

/** A first promote: no binding at all, so the token must be expectUnbound. */
export function firstPromotePlan(overrides: Partial<ForgePromotePlan> = {}): ForgePromotePlan {
  return {
    env: "preprod",
    release: "v1.5.15",
    dry_run: true,
    applied: false,
    current: { bound: false, release_known: false },
    target: { release: "v1.5.15", images: 4 },
    direction: "initial",
    direction_detail: "INITIAL — preprod has never been promoted",
    releases_between: 0,
    images: [{ image: "control-plane", change: "added", target_digest: "sha256:1ea5668bbbbb" }],
    tally: { unchanged: 0, changed: 0, added: 1, removed: 0 },
    commits: {
      state: "first_promote",
      detail: "preprod has no current binding, so there is no commit range",
      count: 0,
      commits: [],
    },
    changed: true,
    ships_nothing: true,
    next_step: "forge env deploy preprod",
    ok: true,
    ...overrides,
  };
}
