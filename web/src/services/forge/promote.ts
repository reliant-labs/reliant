// Copyright (c) 2025 Reliant Labs

/**
 * Forge promote data layer.
 *
 * This module owns the shape of forge's promote-plan document, the two
 * classifications a reviewer must not misread, and — the part that makes the
 * write safe — the derivation of the CONFIRMATION TOKEN from a rendered plan.
 * No React, no styling; presentation lives in components/Forge/Promote.
 *
 * THREE THINGS HERE ARE LOAD-BEARING.
 *
 * 1. DIRECTION HAS FIVE VALUES, NOT TWO. `behind` is a ROLLBACK: it moves an
 *    environment backwards, which is legitimate and is the single most
 *    consequential fact on the screen to misread. `same` (already bound to this
 *    release), `initial` (never promoted, so there is no "from") and `unknown`
 *    (the ordering could not place both releases) are each a different thing.
 *    Forge's own decoder REFUSES an unrecognised direction rather than
 *    defaulting it, because a default of `same` would render a rollback as a
 *    no-op; the fallback here is `unknown` for the same reason.
 *
 * 2. ADDED AND REMOVED ARE NOT DIGEST CHANGES. A promote that `removed` an
 *    image changes the SHAPE of the binding, not a version inside it. Read
 *    `change` rather than diffing the two digest fields — a digest compare
 *    silently turns added/removed into "one side is empty", which is exactly
 *    how a structural change gets rendered as a routine version bump.
 *
 *    Note what `removed` does and does not mean, because it is easy to
 *    overstate: per forge's own source the env's binding stops DECLARING the
 *    image. Promote deletes nothing from any cluster — it writes a pointer —
 *    and the next deploy simply no longer pins that image. Copy in this feature
 *    says that and no more.
 *
 * 3. AN UNCOMPUTED COMMIT RANGE IS NOT AN EMPTY ONE. `commits.count` is
 *    meaningful only when the state says so. "0 commits" and "we could not
 *    tell" are different claims, and the states that mean the latter are
 *    ordinary situations a real project reaches — a release cut from a dirty
 *    tree, a ledger from another branch, no git. commitRangeKind collapses the
 *    nine states into the only three distinctions a renderer needs, so no call
 *    site has to enumerate them and accidentally let a new one fall into
 *    "measured".
 */

import type { ForgeReleaseGit } from "./topology";

// ── Direction ───────────────────────────────────────────────────────────────

/** Where the target release sits relative to what the env runs now. */
export type PromoteDirection = "unknown" | "initial" | "ahead" | "behind" | "same";

const DIRECTIONS: readonly PromoteDirection[] = ["unknown", "initial", "ahead", "behind", "same"];

/**
 * promoteDirectionOf reads the direction, falling back to `unknown`.
 *
 * The fallback direction is the whole point: an unrecognised value from a newer
 * forge must not land on `same` or `ahead`, either of which would describe a
 * rollback as harmless.
 */
export function promoteDirectionOf(value: string | undefined): PromoteDirection {
  if (!value) return "unknown";
  const lowered = value.toLowerCase() as PromoteDirection;
  return DIRECTIONS.includes(lowered) ? lowered : "unknown";
}

/** True when this promote moves the environment BACKWARDS. */
export function isRollback(plan: ForgePromotePlan | null | undefined): boolean {
  return promoteDirectionOf(plan?.direction) === "behind";
}

// ── Image change ────────────────────────────────────────────────────────────

/** What a promote does to ONE image. */
export type PromoteImageChange = "unknown" | "unchanged" | "changed" | "added" | "removed";

const IMAGE_CHANGES: readonly PromoteImageChange[] = [
  "unknown",
  "unchanged",
  "changed",
  "added",
  "removed",
];

/**
 * imageChangeOf reads an image's classification, falling back to `unknown`.
 *
 * Never to `unchanged`. That is the one wrong answer, because it reports a real
 * change as "nothing happens to this image" — forge's decoder rejects the value
 * outright for the same reason.
 */
export function imageChangeOf(value: string | undefined): PromoteImageChange {
  if (!value) return "unknown";
  const lowered = value.toLowerCase() as PromoteImageChange;
  return IMAGE_CHANGES.includes(lowered) ? lowered : "unknown";
}

/**
 * How a change should READ, which is a coarser question than what it is.
 *
 * `structural` is the distinction the screen exists to make: added and removed
 * alter which images the binding declares at all, and a reviewer who skims them
 * as a third shade of "changed" has missed the only irreversible-feeling part
 * of the diff. `version` is a digest moving underneath an image that stays.
 */
export type ChangeShape = "structural" | "version" | "none" | "unknown";

export function changeShapeOf(change: PromoteImageChange): ChangeShape {
  switch (change) {
    case "added":
    case "removed":
      return "structural";
    case "changed":
      return "version";
    case "unchanged":
      return "none";
    default:
      return "unknown";
  }
}

// ── Commit range ────────────────────────────────────────────────────────────

/** Forge's commit-range states. See commitRangeKind before branching on these. */
export type PromoteCommitRangeState =
  | "unknown"
  | "computed"
  | "first_promote"
  | "same_release"
  | "ledger_missing"
  | "no_commit"
  | "commit_not_found"
  | "dirty_release"
  | "git_unavailable";

/**
 * The three things a commit range can BE, from a renderer's point of view.
 *
 *   measured          the count and the list are real; show them
 *   empty-by-definition  there is no range to compute (first promote, same
 *                     release). Zero is the true answer, and it was not
 *                     measured — but it is also not unavailable.
 *   unavailable       a count would be a guess. Show the state's own reason
 *                     and NO number.
 *
 * Anything unrecognised is `unavailable`, so a state added by a newer forge
 * cannot start rendering as a measured zero.
 */
export type CommitRangeKind = "measured" | "empty-by-definition" | "unavailable";

export function commitRangeKind(state: string | undefined): CommitRangeKind {
  switch ((state ?? "").toLowerCase()) {
    case "computed":
      return "measured";
    case "first_promote":
    case "same_release":
      return "empty-by-definition";
    default:
      return "unavailable";
  }
}

/**
 * True when a commit COUNT may be put on screen. The guard exists because
 * `count` is present and zero in every unavailable state, so the tempting
 * `commits.count > 0` test renders "no changes" for "we could not tell".
 */
export function hasCommitCount(commits: ForgePromoteCommitRange | undefined): boolean {
  return commitRangeKind(commits?.state) === "measured";
}

// ── The plan document ───────────────────────────────────────────────────────
//
// Typed against forge's promote `--json` contract (internal/cli/promote_plan.go),
// which is explicitly ADDITIVE: fields are added, never renamed or repurposed.
// Every `omitempty` field is optional here and nothing is assumed non-null — a
// document from a newer or older forge must render, not throw.

export interface ForgePromoteImageChange {
  image: string;
  /** Read this, not the digests. See the module comment. */
  change?: string;
  /** What the env's existing binding declares. Absent for an added image. */
  current_digest?: string;
  /** What the target release declares. Absent for a removed image. */
  target_digest?: string;
}

export interface ForgePromoteTally {
  unchanged?: number;
  changed?: number;
  added?: number;
  removed?: number;
}

export interface ForgePromoteCommitRange {
  /** Check this FIRST. A zero count with a non-computed state means unknown. */
  state?: string;
  detail?: string;
  from_commit?: string;
  to_commit?: string;
  /**
   * True when the target is BEHIND the current release, which INVERTS the
   * meaning of `commits`: they are the commits being taken AWAY, not landed. A
   * renderer showing them as incoming changes has flipped the most
   * consequential fact on the screen.
   */
  reverts?: boolean;
  count?: number;
  commits?: string[];
  truncated?: boolean;
}

/** The env's state BEFORE this promote. */
export interface ForgePromoteCurrent {
  /** False for an env never promoted — separate from an empty release. */
  bound?: boolean;
  release?: string;
  /** RFC3339 PROMOTE time, not a deploy time. */
  promoted_at?: string;
  /** False is not an error: the release was cut on a branch this checkout lacks. */
  release_known?: boolean;
  git?: ForgeReleaseGit;
  release_created_at?: string;
  note?: string;
}

/** The release being promoted TO. */
export interface ForgePromoteTarget {
  release?: string;
  created_at?: string;
  git?: ForgeReleaseGit;
  images?: number;
}

export interface ForgePromotePlan {
  env?: string;
  release?: string;
  /** Where the binding is recorded. Opaque, for DISPLAY only. */
  ledger?: string;
  generated_at?: string;
  dry_run?: boolean;
  /** Whether the binding was actually WRITTEN. The preview/applied discriminator. */
  applied?: boolean;
  current?: ForgePromoteCurrent;
  target?: ForgePromoteTarget;
  direction?: string;
  direction_detail?: string;
  releases_between?: number;
  images?: ForgePromoteImageChange[];
  tally?: ForgePromoteTally;
  commits?: ForgePromoteCommitRange;
  /** False only when this promote would alter nothing at all. */
  changed?: boolean;
  /** Always true. Promote moves a pointer; nothing reaches a cluster. */
  ships_nothing?: boolean;
  /** The command that actually ships these digests. */
  next_step?: string;
  note?: string;
  ok?: boolean;
}

// ── The confirmation token ──────────────────────────────────────────────────

/**
 * The claim about current state that authorises a write.
 *
 * Exactly one of the two is set, and the type makes the third possibility —
 * neither — unrepresentable. That mirrors the server, which rejects a request
 * with neither field as InvalidArgument and one with both as contradictory.
 */
export type PromoteConfirmationToken =
  /** "I saw this environment bound to this release." */
  | { expectedCurrentRelease: string; expectUnbound?: false }
  /** "I saw no binding at all" — a first promote. */
  | { expectUnbound: true; expectedCurrentRelease?: undefined };

/**
 * confirmationTokenFor derives the token FROM THE PLAN THE USER SAW.
 *
 * THIS FUNCTION IS THE SAFETY PROPERTY, and its argument is the reason. The
 * token must describe the binding the reviewer actually read off the screen, so
 * it is computed from the plan document itself rather than assembled from
 * component state, props threaded through a dialog, or a remembered env name.
 * There is no other way to build a token in this feature: the apply path takes
 * a plan and calls this, so "the token matches what was rendered" holds by
 * construction rather than by discipline.
 *
 * Why a forge EnvBinding makes this necessary: it is {release, resolved,
 * promoted_at} with NO history, so a promote OVERWRITES the previous release
 * outright. Nothing can be read back and undone — recovery means digging the
 * old value out of git, if the ledger was even committed. The token is
 * optimistic concurrency over that single destructive write.
 *
 * Returns null when the plan cannot support a claim. That is not a failure to
 * paper over with a default: `bound` absent means forge did not say whether the
 * env was bound, and both possible guesses are dangerous — `expectUnbound` on a
 * bound env asks to blind-overwrite it, and a fabricated release name asks the
 * server to match something nobody saw. A null here must disable the write.
 */
export function confirmationTokenFor(
  plan: ForgePromotePlan | null | undefined
): PromoteConfirmationToken | null {
  const current = plan?.current;
  if (!current) return null;

  // Unbound is an explicit claim, and only `bound === false` licenses it.
  if (current.bound === false) return { expectUnbound: true };

  if (current.bound === true) {
    const release = (current.release ?? "").trim();
    // Bound with no release name: forge reported a binding this client cannot
    // name, so no claim can be made about it.
    if (release === "") return null;
    return { expectedCurrentRelease: release };
  }

  return null;
}

/**
 * describeToken renders the claim in the words the confirm step shows.
 *
 * The user is told what they are asserting, not just what they are doing —
 * that assertion is what the server checks, and a refusal only makes sense to
 * someone who was shown the claim it refers to.
 */
export function describeToken(token: PromoteConfirmationToken): string {
  return token.expectUnbound
    ? "this environment has never been promoted"
    : `this environment is currently bound to ${token.expectedCurrentRelease}`;
}

// ── Refusal ─────────────────────────────────────────────────────────────────

/**
 * The guard's finding when it refused. Nothing was written.
 *
 * A plain shape rather than the generated proto message, because this travels
 * as a connect error DETAIL and the UI only ever reads it. Keeping the field
 * names in the UI's own idiom also means a component never imports the
 * generated enum to ask the one question it cares about.
 */
export interface PromoteRefusal {
  /** Branch on this, never on `detail`. */
  reason: "stale-current-release" | "unknown";
  /** What the caller claimed, echoed back. */
  expectedCurrentRelease: string;
  expectedUnbound: boolean;
  /** What the ledger actually holds NOW. */
  actualBound: boolean;
  actualCurrentRelease: string;
  /** RFC3339 for when the binding that was found was written. */
  actualPromotedAt: string;
  /** One-sentence human phrasing from the server. */
  detail: string;
}

/**
 * describeActualBinding renders what the guard found, for the refusal notice.
 *
 * `actualBound === false` is rendered as its own sentence rather than as an
 * empty release, because "never promoted" and "bound to something blank" are
 * different findings and the operator's next move differs.
 */
export function describeActualBinding(refusal: PromoteRefusal): string {
  if (!refusal.actualBound) return "no binding at all — the environment is not promoted";
  if (!refusal.actualCurrentRelease) return "a binding with no release recorded";
  return refusal.actualCurrentRelease;
}
