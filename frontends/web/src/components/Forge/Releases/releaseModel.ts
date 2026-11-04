// Copyright (c) 2025 Reliant Labs

/**
 * THE RULES OF THE RELEASES PAGES, as pure functions over the ledger — what
 * is live where, what a scoped deploy means for "live", what counts as
 * rolled back, how far an environment is behind — so they are tested
 * without rendering and the list and the detail page cannot disagree.
 *
 * ── LIVE, PARTLY LIVE, RAN BEFORE, NEVER ─────────────────────────────────────
 *
 * Per environment, a release is one of four (the release × environment column, plus scoped deploys):
 *
 *   live     the newest WHOLE-release promotion there is this release.
 *   partial  a scoped deploy moved some components to it after that, so the
 *            environment runs it for those components only. Never shown as
 *            plain live: that would overstate what is running.
 *   ran      it was promoted there once, and something else is live now.
 *   never    no promotion of it there.
 *
 * The promotion ledger decides. When an environment's ledger could not be
 * read, the control plane's live view (its current release) is the
 * fallback, and nothing is claimed about what ran before.
 *
 * ── ROLLED BACK ──────────────────────────────────────────────────────────────
 *
 * There is no rollback kind in the ledger any more: recovery is a promote,
 * and promoting an OLDER release is how a rollback is spelled. So a release
 * was rolled back in an environment when the whole-release promotion after
 * its own put an older release there. Order is cut order (the release list
 * is newest first); a release outside the loaded list is not judged.
 */

import type { ReleaseGate, ReleasePromotion, ReleaseRecord } from "@/services/forge/releases";

// ── Inputs ──────────────────────────────────────────────────────────────────

/** An environment as the Releases pages need it. */
export interface ReleaseEnv {
  id: string;
  name: string;
  /** Long-lived environments get a column; previews share one. */
  kind: "long" | "preview";
  /** The release the control plane's live view says is bound now ("" when none). */
  current: string;
  promotedAt?: string;
  promotedByActor: string;
  promotedByUserId: string;
}

export interface PromotionsByEnv {
  /** env id → its ledger, newest first; undefined while loading or when it failed. */
  get(envId: string): ReleasePromotion[] | undefined;
}

export type CellState = "live" | "partial" | "ran" | "never";

export interface EnvCell {
  state: CellState;
  /** For a partial cell: the components the scoped deploy moved. */
  targets: string[];
}

export interface ChecksSummary {
  passed: number;
  /** Gates that count (skipped ones do not). */
  total: number;
  failed: number;
  gates: ReleaseGate[];
}

export interface ReleaseRow {
  release: ReleaseRecord;
  /** Long-lived env name → state. */
  cells: Record<string, EnvCell>;
  /** Preview environments running this release now. */
  previewsLive: string[];
  /** Components of the newest scoped deploy of this release, when its newest promotion was scoped. */
  scoped: string[];
  /** Environments it was rolled back in. */
  rolledBack: string[];
  checks: ChecksSummary | null;
}

export interface ScopedFollowUp {
  version: string;
  targets: string[];
  at?: string;
  by: string;
}

export type PipelineRelation =
  | { kind: "current" }
  | { kind: "newer"; count: number }
  | { kind: "behind"; count: number; of: string }
  | { kind: "ahead"; count: number; of: string }
  | { kind: "unknown" };

export interface PipelineCell {
  env: ReleaseEnv;
  /** The release live for the whole environment, or null before the first deploy. */
  base: string | null;
  /** Scoped deploys on top of `base`, newest first. */
  followUps: ScopedFollowUp[];
  by: string;
  at?: string;
  relation: PipelineRelation;
}

// ── Helpers ─────────────────────────────────────────────────────────────────

const AUTO_VERSION = /^(\d{8}\.\d{6})-[0-9a-f]{6,}$/i;

/** A date-named auto version reads by its date part ("20261018.155157"); anything else is shown whole. */
export function shortVersion(version: string): string {
  const match = AUTO_VERSION.exec(version);
  return match ? match[1]! : version;
}

/** Who promoted: the automation actor, else the user's name, else nothing. */
export function promoterOf(p: { promotedByActor: string; promotedByUserId: string }, names: ReadonlyMap<string, string>): string {
  return p.promotedByActor || names.get(p.promotedByUserId) || "";
}

/** The long-lived env every relation is measured against: prod, else the first. */
export function referenceEnv(envs: readonly ReleaseEnv[]): ReleaseEnv | null {
  const long = envs.filter((env) => env.kind === "long");
  return long.find((env) => env.name === "prod") ?? long[0] ?? null;
}

interface EnvState {
  base: string | null;
  basePromotion: ReleasePromotion | null;
  followUps: ReleasePromotion[];
  ran: Set<string>;
  /** Whether the ledger answered; false means `base` came from the live view. */
  fromLedger: boolean;
}

function envState(env: ReleaseEnv, promotions: ReleasePromotion[] | undefined): EnvState {
  if (!promotions || promotions.length === 0) {
    return { base: env.current || null, basePromotion: null, followUps: [], ran: new Set(env.current ? [env.current] : []), fromLedger: false };
  }
  // Scoped deploys since the last whole-release promotion, newest first. A
  // component runs what its NEWEST scoped deploy put there, so a newer one
  // masks the components it moved from every older one: an older
  // follow-up keeps only its unmasked components, and one left with none runs
  // nowhere any more.
  const followUps: ReleasePromotion[] = [];
  const masked = new Set<string>();
  let basePromotion: ReleasePromotion | null = null;
  for (const promotion of promotions) {
    if (promotion.targets.length === 0) {
      basePromotion = promotion;
      break;
    }
    const live = promotion.targets.filter((target) => !masked.has(target));
    promotion.targets.forEach((target) => masked.add(target));
    if (live.length > 0) followUps.push({ ...promotion, targets: live });
  }
  return {
    base: basePromotion?.releaseVersion ?? null,
    basePromotion,
    followUps,
    ran: new Set(promotions.map((p) => p.releaseVersion)),
    fromLedger: true,
  };
}

/**
 * The versions that stand rolled back in one environment: walked oldest →
 * newest over the whole-release promotions, the classification
 * control-plane's dora.Classify makes (internal/dora/dora.go):
 *
 *   - a promotion of a release OLDER than the one running rolls the running
 *     one back. Older is cut order when both releases are in the list, else
 *     which one this environment ran FIRST (a release outside the loaded
 *     window is judged by the env's own history, as dora's rule 3 does);
 *   - a promotion of a release that stood rolled back puts it back: that is a
 *     roll-forward, and the tag goes. So v1 → v2 → v1 → v2 leaves v2 live and
 *     untagged, as dora calls the last step a deployment.
 */
function rolledBackVersions(promotions: readonly ReleasePromotion[], rank: ReadonlyMap<string, number>): Set<string> {
  const out = new Set<string>();
  const firstRun = new Map<string, number>();
  let previous: string | null = null;
  let step = 0;
  for (const promotion of [...promotions].reverse()) {
    if (promotion.targets.length > 0) continue;
    const version = promotion.releaseVersion;
    if (!firstRun.has(version)) firstRun.set(version, step++);
    if (previous !== null && previous !== version) {
      const before = rank.get(previous);
      const now = rank.get(version);
      // Larger rank = older (the list is newest first).
      const older =
        before !== undefined && now !== undefined ? now > before : firstRun.get(version)! < firstRun.get(previous)!;
      if (older) out.add(previous);
    }
    out.delete(version);
    previous = version;
  }
  return out;
}

function checksOf(gates: ReleaseGate[]): ChecksSummary | null {
  if (gates.length === 0) return null;
  // One verdict per gate name: the latest recorded wins.
  const byName = new Map<string, ReleaseGate>();
  for (const gate of gates) {
    const seen = byName.get(gate.name);
    if (!seen || (gate.finishedAt ?? "") >= (seen.finishedAt ?? "")) byName.set(gate.name, gate);
  }
  const unique = [...byName.values()].sort((a, b) => a.name.localeCompare(b.name));
  const counted = unique.filter((gate) => gate.status !== "skipped");
  return {
    passed: counted.filter((gate) => gate.status === "passed").length,
    total: counted.length,
    failed: counted.filter((gate) => gate.status === "failed" || gate.status === "error").length,
    gates: unique,
  };
}

// ── The list ────────────────────────────────────────────────────────────────

export interface ReleaseModel {
  rows: ReleaseRow[];
  pipeline: PipelineCell[];
  longEnvs: ReleaseEnv[];
  previews: ReleaseEnv[];
}

export function buildReleaseModel(
  releases: readonly ReleaseRecord[],
  envs: readonly ReleaseEnv[],
  promotions: PromotionsByEnv,
  names: ReadonlyMap<string, string> = new Map()
): ReleaseModel {
  const longEnvs = envs.filter((env) => env.kind === "long");
  const previews = envs.filter((env) => env.kind === "preview");
  const rank = new Map(releases.map((release, index) => [release.version, index]));
  const states = new Map(longEnvs.map((env) => [env.name, envState(env, promotions.get(env.id))]));
  const rolledBack = new Map(
    longEnvs.map((env) => [env.name, rolledBackVersions(promotions.get(env.id) ?? [], rank)])
  );

  const rows = releases.map((release): ReleaseRow => {
    const cells: Record<string, EnvCell> = {};
    const gates: ReleaseGate[] = [];
    let newest: ReleasePromotion | null = null;
    for (const env of longEnvs) {
      const state = states.get(env.name)!;
      const followUp = state.followUps.find((p) => p.releaseVersion === release.version);
      cells[env.name] =
        state.base === release.version
          ? { state: "live", targets: [] }
          : followUp
            ? { state: "partial", targets: followUp.targets }
            : state.ran.has(release.version)
              ? { state: "ran", targets: [] }
              : { state: "never", targets: [] };
      for (const promotion of promotions.get(env.id) ?? []) {
        if (promotion.releaseVersion !== release.version) continue;
        gates.push(...promotion.gates);
        if (!newest || (promotion.createdAt ?? "") > (newest.createdAt ?? "")) newest = promotion;
      }
    }
    return {
      release,
      cells,
      previewsLive: previews.filter((env) => env.current === release.version).map((env) => env.name),
      scoped: newest?.targets ?? [],
      rolledBack: longEnvs.filter((env) => rolledBack.get(env.name)!.has(release.version)).map((env) => env.name),
      checks: checksOf(gates),
    };
  });

  const reference = referenceEnv(envs);
  const pipeline = longEnvs.map((env): PipelineCell => {
    const state = states.get(env.name)!;
    const promotion = state.basePromotion;
    return {
      env,
      base: state.base,
      followUps: state.followUps.map((p) => ({ version: p.releaseVersion, targets: p.targets, at: p.createdAt, by: promoterOf(p, names) })),
      by: promotion ? promoterOf(promotion, names) : promoterOf(env, names),
      at: promotion ? promotion.createdAt : env.promotedAt,
      relation: relationOf(env, state.base, reference, reference ? states.get(reference.name)?.base ?? null : null, rank),
    };
  });

  return { rows, pipeline, longEnvs, previews };
}

function relationOf(
  env: ReleaseEnv,
  base: string | null,
  reference: ReleaseEnv | null,
  referenceBase: string | null,
  rank: ReadonlyMap<string, number>
): PipelineRelation {
  if (!base) return { kind: "unknown" };
  const mine = rank.get(base);
  if (mine === undefined) return { kind: "unknown" };
  if (!reference || reference.name === env.name || !referenceBase) {
    return mine === 0 ? { kind: "current" } : { kind: "newer", count: mine };
  }
  const theirs = rank.get(referenceBase);
  if (theirs === undefined) return { kind: "unknown" };
  if (mine === theirs) return { kind: "current" };
  return mine > theirs
    ? { kind: "behind", count: mine - theirs, of: reference.name }
    : { kind: "ahead", count: theirs - mine, of: reference.name };
}

// ── Filtering ───────────────────────────────────────────────────────────────

export const STATUS_FILTERS = ["live", "rolled-back", "scoped", "undeployed"] as const;
export type StatusFilter = (typeof STATUS_FILTERS)[number];

export const STATUS_FILTER_LABELS: Record<StatusFilter, string> = {
  live: "Live",
  "rolled-back": "Rolled back",
  scoped: "Scoped deploy",
  undeployed: "Never deployed",
};

/** The release detail page's tabs, in order (its URL's `tab`). */
export const RELEASE_TABS = ["changes", "rollout", "checks", "artifacts", "health"] as const;
export type ReleaseTab = (typeof RELEASE_TABS)[number];

export interface RowFilter {
  /** The top-bar scope: an env name or "all". */
  env: string;
  /** Show every release, not only those that ran in the scoped env. */
  everyRelease: boolean;
  query: string;
  author?: string;
  status?: StatusFilter;
}

/** Whether `row` ran in `env` at all (a preview: whether it runs there now). */
export function ranIn(row: ReleaseRow, env: string): boolean {
  const cell = row.cells[env];
  if (cell) return cell.state !== "never";
  return row.previewsLive.includes(env);
}

function isLiveIn(row: ReleaseRow, env: string): boolean {
  if (env === "all") return Object.values(row.cells).some((cell) => cell.state === "live" || cell.state === "partial") || row.previewsLive.length > 0;
  const cell = row.cells[env];
  return cell ? cell.state === "live" || cell.state === "partial" : row.previewsLive.includes(env);
}

export function filterRows(rows: readonly ReleaseRow[], filter: RowFilter): ReleaseRow[] {
  const query = filter.query.trim().toLowerCase();
  return rows.filter((row) => {
    if (!filter.everyRelease && filter.env !== "all" && !ranIn(row, filter.env)) return false;
    if (filter.author && row.release.author !== filter.author) return false;
    if (filter.status === "live" && !isLiveIn(row, filter.env)) return false;
    if (filter.status === "rolled-back" && row.rolledBack.length === 0) return false;
    if (filter.status === "scoped" && row.scoped.length === 0) return false;
    if (filter.status === "undeployed" && (Object.values(row.cells).some((cell) => cell.state !== "never") || row.previewsLive.length > 0)) return false;
    if (!query) return true;
    const { version, gitCommit, subject, author } = row.release;
    return [version, gitCommit, subject, author].some((field) => field.toLowerCase().includes(query));
  });
}

/** Every commit author in the list, for the Author filter. */
export function authorsOf(rows: readonly ReleaseRow[]): string[] {
  return [...new Set(rows.map((row) => row.release.author).filter(Boolean))].sort((a, b) => a.localeCompare(b));
}

// ── One release ─────────────────────────────────────────────────────────────

/**
 * "Live on prod, dev", "Live on prod, dev and 2 previews", "Live on prod for
 * admin-web", "Ran before on prod", "Not deployed". Previews are counted, not
 * named: there can be dozens, and the matrix collapses them into one.
 */
export function releaseStatus(row: ReleaseRow): { tone: "success" | "neutral"; label: string; previews: string[] } {
  const live = Object.entries(row.cells).filter(([, cell]) => cell.state === "live").map(([env]) => env);
  const partial = Object.entries(row.cells).filter(([, cell]) => cell.state === "partial");
  const previews = row.previewsLive;
  const previewWords = previews.length === 1 ? "1 preview" : `${previews.length} previews`;
  if (live.length > 0) {
    return { tone: "success", label: `Live on ${live.join(", ")}${previews.length ? ` and ${previewWords}` : ""}`, previews };
  }
  if (partial.length > 0) {
    const [env, cell] = partial[0]!;
    return { tone: "success", label: `Live on ${env} for ${cell.targets.join(", ")}`, previews };
  }
  if (previews.length > 0) return { tone: "success", label: `Live on ${previewWords}`, previews };
  const ran = Object.entries(row.cells).filter(([, cell]) => cell.state === "ran").map(([env]) => env);
  if (ran.length > 0) return { tone: "neutral", label: `Ran before on ${ran.join(", ")}`, previews };
  return { tone: "neutral", label: "Not deployed", previews };
}

/**
 * The environment the detail page reads its rollout, objects and health in:
 * the scoped one when the release was promoted there, else the one it was
 * promoted to most recently, else none.
 */
export function detailEnvOf(
  version: string,
  scopeEnv: string,
  envs: readonly ReleaseEnv[],
  promotions: PromotionsByEnv
): { env: ReleaseEnv; promotion: ReleasePromotion } | null {
  const long = envs.filter((env) => env.kind === "long");
  // The whole-release promotion is the one that rolled the release out; a
  // scoped follow-up re-shipped some of it. Prefer the former.
  const newestIn = (env: ReleaseEnv) => {
    const mine = promotions.get(env.id)?.filter((p) => p.releaseVersion === version) ?? [];
    return mine.find((p) => p.targets.length === 0) ?? mine[0] ?? null;
  };
  const scoped = long.find((env) => env.name === scopeEnv);
  if (scoped) {
    const promotion = newestIn(scoped);
    if (promotion) return { env: scoped, promotion };
  }
  let best: { env: ReleaseEnv; promotion: ReleasePromotion } | null = null;
  for (const env of long) {
    const promotion = newestIn(env);
    if (promotion && (!best || (promotion.createdAt ?? "") > (best.promotion.createdAt ?? ""))) best = { env, promotion };
  }
  return best;
}

/**
 * What to compare a release with by default: what `env` ran just before this
 * release was first promoted there, else the release cut before it.
 */
export function defaultBaseOf(
  version: string,
  releases: readonly ReleaseRecord[],
  envPromotions: readonly ReleasePromotion[] | undefined
): string | null {
  if (envPromotions) {
    const chronological = [...envPromotions].reverse();
    const first = chronological.findIndex((p) => p.releaseVersion === version);
    if (first > 0) {
      for (let i = first - 1; i >= 0; i--) {
        const candidate = chronological[i]!;
        if (candidate.targets.length === 0 && candidate.releaseVersion !== version) return candidate.releaseVersion;
      }
    }
  }
  const index = releases.findIndex((release) => release.version === version);
  return index >= 0 ? (releases[index + 1]?.version ?? null) : null;
}

export type TimelineTone = "ok" | "bad" | "neutral";

export interface TimelineEntry {
  key: string;
  title: string;
  detail: string;
  at?: string;
  tone: TimelineTone;
}

/**
 * The promotion timeline of one release across the long-lived environments:
 * cut, each promotion (with its checks), and what replaced it — "rolled back"
 * when an older release took its place, "superseded" when a newer one did.
 */
export function releaseTimeline(
  release: ReleaseRecord,
  envs: readonly ReleaseEnv[],
  promotions: PromotionsByEnv,
  releases: readonly ReleaseRecord[],
  names: ReadonlyMap<string, string>
): TimelineEntry[] {
  const rank = new Map(releases.map((r, index) => [r.version, index]));
  const entries: TimelineEntry[] = [];
  const cutBits = [release.branch && `from ${release.branch}`, release.author && `by ${release.author}`].filter(Boolean);
  entries.push({ key: "cut", title: "Cut", detail: cutBits.join(" "), at: release.createdAt, tone: "ok" });
  for (const env of envs.filter((candidate) => candidate.kind === "long")) {
    const ledger = promotions.get(env.id) ?? [];
    const chronological = [...ledger].reverse();
    chronological.forEach((promotion, index) => {
      if (promotion.releaseVersion !== release.version) return;
      const by = promoterOf(promotion, names);
      const checks = checksOf(promotion.gates);
      const bits = [
        by && `by ${by}`,
        promotion.targets.length > 0 && `scoped to ${promotion.targets.join(", ")}`,
        checks && `checks ${checks.passed}/${checks.total}`,
      ].filter(Boolean);
      entries.push({
        key: `promote-${promotion.id}`,
        title: `Promoted to ${env.name}`,
        detail: bits.join(" · "),
        at: promotion.createdAt,
        tone: checks && checks.failed > 0 ? "bad" : "ok",
      });
      if (promotion.targets.length > 0) return;
      const next = chronological.slice(index + 1).find((p) => p.targets.length === 0);
      if (!next || next.releaseVersion === release.version) return;
      const older = (rank.get(next.releaseVersion) ?? -1) > (rank.get(release.version) ?? Infinity);
      const nextBy = promoterOf(next, names);
      entries.push({
        key: `replaced-${next.id}`,
        title: older ? `Rolled back in ${env.name}` : `Superseded in ${env.name}`,
        // "Rolled back in prod · to v1.4 by sean": the version is where it
        // went, the name is who sent it there.
        detail: [`${older ? "to" : "by"} ${shortVersion(next.releaseVersion)}`, nextBy && `by ${nextBy}`].filter(Boolean).join(" "),
        at: next.createdAt,
        tone: older ? "bad" : "neutral",
      });
    });
  }
  return [entries[0]!, ...entries.slice(1).sort((a, b) => (a.at ?? "").localeCompare(b.at ?? ""))];
}

/** Commit to the first promotion into `env`; null when the commit time was not recorded. */
export function leadTimeMs(release: ReleaseRecord, envPromotions: readonly ReleasePromotion[] | undefined): number | null {
  if (!release.commitTime || !envPromotions) return null;
  const first = [...envPromotions].reverse().find((p) => p.releaseVersion === release.version && p.targets.length === 0);
  if (!first?.createdAt) return null;
  const ms = Date.parse(first.createdAt) - Date.parse(release.commitTime);
  return Number.isFinite(ms) && ms >= 0 ? ms : null;
}
