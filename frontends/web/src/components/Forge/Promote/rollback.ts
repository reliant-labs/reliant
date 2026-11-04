// Copyright (c) 2025 Reliant Labs

/**
 * ROLL BACK AT THE POINT OF PAIN: which release an environment would roll
 * back to, named, so no button ever says "roll back" and leaves the reader to
 * go and find out to what.
 *
 * The target is the release the environment ran BEFORE its current one — the
 * newest promotion in its ledger that is not the current release. Not "the
 * release cut before it": an environment that skipped a release (staging got
 * v3, prod went v1 → v4) rolls back to what IT ran, v1, which is the one known
 * to have worked there.
 *
 * A rollback is a promotion to an older release, so it goes through the same
 * plan-and-confirm flow as any promote (usePromoteFlow): forge plans it, the
 * reader sees the diff labelled a rollback, and only the confirmed plan
 * applies.
 *
 * Pure, so the rule is tested on object literals.
 */

/** What the rule reads of a promotion: the ledgers of both clients carry it. */
export interface PromotedRelease {
  releaseVersion: string;
  /** Nonempty means only these components were promoted, not the environment. */
  targets?: readonly string[];
}

export interface RollbackTarget {
  env: string;
  /** What the environment runs now. */
  current: string;
  /** What it ran before: the release a rollback lands on. */
  previous: string;
}

/**
 * The release `current` replaced in an environment, from its promotion ledger
 * (newest first), or null when it never ran another one.
 */
export function previousReleaseOf(promotions: readonly PromotedRelease[] | undefined, current: string): string | null {
  if (!promotions || !current) return null;
  // Component promotions never establish a whole-environment release. Ignore
  // them both when locating the current baseline and selecting its predecessor.
  const wholeEnvironment = promotions.filter((p) => !p.targets?.length);
  const at = wholeEnvironment.findIndex((p) => p.releaseVersion === current);
  // A component-only current release or a page predating it proves no baseline.
  if (at < 0) return null;
  for (const p of wholeEnvironment.slice(at + 1)) {
    if (p.releaseVersion && p.releaseVersion !== current) return p.releaseVersion;
  }
  return null;
}

/** The rollback target of each environment that has one, by environment name. */
export function rollbackTargets(
  envs: readonly { id: string; name: string; release: string }[],
  promotionsById: ReadonlyMap<string, readonly PromotedRelease[] | undefined>
): Map<string, RollbackTarget> {
  const out = new Map<string, RollbackTarget>();
  for (const env of envs) {
    const previous = previousReleaseOf(promotionsById.get(env.id), env.release);
    if (previous) out.set(env.name, { env: env.name, current: env.release, previous });
  }
  return out;
}

/**
 * How long after a promotion a workload going unhealthy is read as CAUSED by
 * it, for the "started after <release>" link. Long enough for a slow rollout
 * plus a crash loop to show, short enough that a release that ran fine for an
 * hour is not blamed for what broke later.
 */
export const SUSPECT_WINDOW_MS = 30 * 60_000;

/**
 * Whether trouble seen at `troubleAtMs` (a workload's latest warning, or now
 * when none is timestamped) followed the environment's current promotion
 * closely enough to name it as the suspect.
 */
export function promotionIsSuspect(promotedAt: string | undefined, troubleAtMs: number): boolean {
  if (!promotedAt) return false;
  const at = Date.parse(promotedAt);
  if (Number.isNaN(at)) return false;
  return troubleAtMs >= at && troubleAtMs - at <= SUSPECT_WINDOW_MS;
}
