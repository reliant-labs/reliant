// Copyright (c) 2025 Reliant Labs

/**
 * ONE ENVIRONMENT'S PROMOTION TIMELINE, as pure functions over its ledger
 * (ListPromotions, newest first): every promotion, whole-release and scoped,
 * with what it replaced, the rollbacks marked, and the release-health verdict
 * the promotion recorded — "prod: v1.40 → v1.41 (alice, 14:02, healthy) →
 * rollback to v1.40 (bot, 14:30, regressed)".
 *
 * releaseTimeline (releaseModel.ts) is the other axis: one release across
 * every environment. This is one environment across every release, so no
 * row is dropped or merged — a redeploy of the same release is its own row.
 *
 * Everything here comes from the ledger rows and the release list's cut
 * order; nothing is read per row. What a row cannot say from that (the
 * rollout, the bundle's objects) is one click away, on the release page.
 */

import type { ReleaseGate, ReleasePromotion } from "@/services/forge/releases";

import { checksOf, promoterOf, type ChecksSummary } from "./releaseModel";

/**
 * The gate `forge obs check <env> --gate-json` writes and `forge gate record`
 * puts on the promotion (forge internal/cli/obs_check.go, writeCheckGate):
 * passed is PASS, failed is FAIL, error is INCONCLUSIVE or could not look.
 */
export const HEALTH_GATE = "release-health";

/**
 * Every gate name that carries the obs check's verdict: forge's own, and the
 * command's name, which a hand-recorded gate (and the demo ledger) uses.
 */
const HEALTH_GATES: readonly string[] = [HEALTH_GATE, "forge obs check"];

/** Whether the gate is a release-health verdict rather than another check. */
export function isHealthGate(gate: Pick<ReleaseGate, "name">): boolean {
  return HEALTH_GATES.includes(gate.name);
}

/** What the promotion's recorded health check said; "unknown" when it recorded none — never a pass. */
export type PromotionHealth = "healthy" | "regressed" | "inconclusive" | "unknown";

export interface EnvPromotionRow {
  id: string;
  version: string;
  /** Who: the automation actor, else the person's name; "" when neither is known. */
  by: string;
  at?: string;
  /** The components a scoped deploy moved; empty for a whole-release deploy. */
  targets: string[];
  /** The release this promotion replaced for what it moved; null for the first one loaded. */
  previousVersion: string | null;
  /** Put an OLDER release in place than the one it replaced (cut order). Unjudged when either is outside the release list. */
  rollback: boolean;
  /** The same release again. */
  redeploy: boolean;
  /** The environment it was promoted from ("" on a direct deploy). */
  from: string;
  note: string;
  /** The ledger's gates, the health gate excluded: the raw evidence, kept apart from health. */
  checks: ChecksSummary | null;
  health: { verdict: PromotionHealth; summary: string };
  bundleDigest: string;
  /** The artifacts the promotion froze: name → digest. */
  artifacts: [string, string][];
}

/** The promotion's release-health verdict: the newest recorded `release-health` gate, else unknown. */
export function promotionHealthOf(gates: readonly ReleaseGate[]): EnvPromotionRow["health"] {
  const newest = gates
    .filter(isHealthGate)
    .reduce<ReleaseGate | null>((best, gate) => (!best || (gate.finishedAt ?? "") >= (best.finishedAt ?? "") ? gate : best), null);
  if (!newest) return { verdict: "unknown", summary: "" };
  switch (newest.status) {
    case "passed":
      return { verdict: "healthy", summary: newest.summary };
    case "failed":
      return { verdict: "regressed", summary: newest.summary };
    case "error":
      return { verdict: "inconclusive", summary: newest.summary };
    default:
      return { verdict: "unknown", summary: newest.summary };
  }
}

/** A promotion moved what `p` moved: a whole-release one moved everything; scoped ones overlap by name. */
function covers(earlier: ReleasePromotion, p: ReleasePromotion): boolean {
  if (p.targets.length === 0) return earlier.targets.length === 0;
  return earlier.targets.length === 0 || earlier.targets.some((target) => p.targets.includes(target));
}

/**
 * Every promotion in one environment's ledger (newest first), as timeline
 * rows. What a promotion replaced: for a whole-release one, the whole-release
 * promotion before it (the release the env was on); for a scoped one, the
 * newest earlier promotion that moved any of the same components. A rollback
 * is putting an older release there than that — cut order, `rank` being the
 * release list's index (newest first); a release outside the list is not judged.
 */
export function envPromotionRows(
  promotions: readonly ReleasePromotion[],
  rank: ReadonlyMap<string, number>,
  names: ReadonlyMap<string, string>
): EnvPromotionRow[] {
  return promotions.map((p, index) => {
    const replaced = promotions.slice(index + 1).find((earlier) => covers(earlier, p));
    const now = rank.get(p.releaseVersion);
    const before = replaced ? rank.get(replaced.releaseVersion) : undefined;
    const redeploy = !!replaced && replaced.releaseVersion === p.releaseVersion;
    return {
      id: p.id,
      version: p.releaseVersion,
      by: promoterOf(p, names),
      at: p.createdAt,
      targets: [...p.targets],
      previousVersion: replaced?.releaseVersion ?? null,
      rollback: !redeploy && now !== undefined && before !== undefined && now > before,
      redeploy,
      from: p.fromEnvironmentName,
      note: p.note,
      checks: checksOf(p.gates.filter((gate) => !isHealthGate(gate))),
      health: promotionHealthOf(p.gates),
      bundleDigest: p.bundleDigest,
      artifacts: Object.entries(p.resolvedArtifacts ?? {}).sort(([a], [b]) => a.localeCompare(b)),
    };
  });
}
