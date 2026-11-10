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
  /** Each component it moved, from which release and which way. */
  moves: TargetMove[];
  /** The release it replaced, when every component it moved ran the same one; null otherwise or for the first loaded. */
  previousVersion: string | null;
  /** Put an OLDER release (cut order) in place for at least one component; `rollbackTargets` names which. */
  rollback: boolean;
  /** The components rolled back (REST for a whole-release deploy's remainder). */
  rollbackTargets: string[];
  /** The same release again, for every component it moved. */
  redeploy: boolean;
  /** Its components moved different ways, one of them a rollback or a redeploy. */
  mixed: boolean;
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

/**
 * The rest of a whole-release deploy: every component no scoped deploy since
 * the previous whole-release one named, which therefore ran that one's release.
 */
export const REST = "";

/** What one component went through: from which release, and which way. */
export interface TargetMove {
  /** The component, or REST for a whole-release deploy's unnamed remainder. */
  target: string;
  /** The release it ran before; null when nothing earlier is loaded. */
  from: string | null;
  /** first: nothing earlier loaded. unjudged: a release outside the release list. */
  change: "redeploy" | "rollback" | "forward" | "unjudged" | "first";
}

/** Each component's move. One promotion's components can move different ways (a scoped deploy of api and web, where api had a newer release of its own). */
function movesOf(promotions: readonly ReleasePromotion[], index: number, rank: ReadonlyMap<string, number>): TargetMove[] {
  const p = promotions[index]!;
  const earlier = promotions.slice(index + 1);
  const covering = (target: string) => earlier.find((e) => e.targets.length === 0 || (target !== REST && e.targets.includes(target)));
  let targets: string[];
  if (p.targets.length > 0) {
    targets = [...p.targets];
  } else {
    // A whole-release deploy moves the rest from the previous whole-release
    // one, and each component a scoped deploy named since from its own release.
    const previousFull = earlier.findIndex((e) => e.targets.length === 0);
    const since = previousFull < 0 ? earlier : earlier.slice(0, previousFull);
    targets = [REST, ...new Set(since.flatMap((e) => e.targets))];
  }
  const now = rank.get(p.releaseVersion);
  return targets.map((target) => {
    const from = covering(target)?.releaseVersion ?? null;
    if (from === null) return { target, from, change: "first" };
    if (from === p.releaseVersion) return { target, from, change: "redeploy" };
    const before = rank.get(from);
    if (now === undefined || before === undefined) return { target, from, change: "unjudged" };
    return { target, from, change: now > before ? "rollback" : "forward" };
  });
}

/**
 * Every promotion in one environment's ledger (newest first), as timeline
 * rows. What a promotion replaced is judged per component: for a scoped
 * deploy, each named component against the newest earlier promotion that
 * moved it; for a whole-release one, the rest against the previous
 * whole-release promotion and each component a scoped deploy moved since
 * against that. A rollback is putting an older release in place than the
 * one a component ran — cut order, `rank` being the release list's index
 * (newest first); a release outside the list is not judged.
 */
export function envPromotionRows(
  promotions: readonly ReleasePromotion[],
  rank: ReadonlyMap<string, number>,
  names: ReadonlyMap<string, string>
): EnvPromotionRow[] {
  return promotions.map((p, index) => {
    const moves = movesOf(promotions, index, rank);
    const froms = new Set(moves.map((move) => move.from));
    const kinds = new Set(moves.map((move) => move.change));
    const only = kinds.size === 1 ? [...kinds][0] : undefined;
    return {
      id: p.id,
      version: p.releaseVersion,
      by: promoterOf(p, names),
      at: p.createdAt,
      targets: [...p.targets],
      moves,
      previousVersion: froms.size === 1 ? [...froms][0]! : null,
      rollback: kinds.has("rollback"),
      rollbackTargets: moves.filter((move) => move.change === "rollback").map((move) => move.target),
      redeploy: only === "redeploy",
      mixed: kinds.size > 1 && (kinds.has("rollback") || kinds.has("redeploy")),
      from: p.fromEnvironmentName,
      note: p.note,
      checks: checksOf(p.gates.filter((gate) => !isHealthGate(gate))),
      health: promotionHealthOf(p.gates),
      bundleDigest: p.bundleDigest,
      artifacts: Object.entries(p.resolvedArtifacts ?? {}).sort(([a], [b]) => a.localeCompare(b)),
    };
  });
}
