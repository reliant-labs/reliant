// Copyright (c) 2025 Reliant Labs

/**
 * The Experiments page's words and arithmetic, pure: what a change's mode is
 * called, who gets it in a phrase, percentages in basis points, what
 * moved between two revisions (the conflict notice), and whether a saved
 * revision reached the environment's apps (deliveryStateOf).
 */

import type { Change, ChangeDeliveryTarget, ChangeMode, ChangeSplitArm, ChangesDeliveryStatus, ChangeUnit } from "@/services/controlPlane/changes/changesTypes";

/** The three answers to "Who gets it?", in the page's words. */
export type WhoGetsIt = "everyone" | "some" | "compare";

export const WHO_GETS_IT: { value: WhoGetsIt; label: string; hint: string }[] = [
  { value: "everyone", label: "Everyone", hint: "One version for everybody: remote config you can change without a deploy." },
  { value: "some", label: "Some people", hint: "A stable share of your users gets the new version, and you raise it as you gain confidence." },
  { value: "compare", label: "Compare versions", hint: "A random split, measured against one success metric, so you can tell which version works better." },
];

export const UNIT_LABELS: Record<ChangeUnit, { label: string; hint: string }> = {
  user: { label: "User", hint: "Each signed-in user keeps the same version on every device." },
  account: { label: "Account", hint: "Everyone in one account (organization) gets the same version." },
  visitor: { label: "Visitor", hint: "An anonymous visitor keeps the same version in their browser." },
};

export const MODE_LABELS: Record<ChangeMode, string> = {
  off: "Off",
  preview: "Preview",
  everyone: "Everyone",
  rollout: "Rolling out",
  experiment: "Experiment",
};

/** Which "Who gets it?" answer a change's live mode is. */
export function whoGetsItOf(change: Pick<Change, "mode" | "currentRun">): WhoGetsIt {
  if (change.mode === "everyone") return "everyone";
  if (change.mode === "rollout") return "some";
  if (change.mode === "experiment") return "compare";
  return change.currentRun?.kind === "rollout" ? "some" : "compare";
}

/** Basis points as a percentage string: 2500 → "25", 50 → "0.5". */
export function formatBp(bp: number): string {
  return String(Math.round(bp) / 100);
}

/** A percentage the reader typed, as basis points; null when it is not a number from 0 to 100. */
export function parsePercent(text: string): number | null {
  const trimmed = text.trim().replace(/%$/, "");
  if (trimmed === "") return null;
  const value = Number(trimmed);
  if (!Number.isFinite(value) || value < 0 || value > 100) return null;
  return Math.round(value * 100);
}

/** Who gets the change right now, in a phrase. */
export function deliveryPhrase(change: Change): string {
  const units = `${change.unit}s`;
  switch (change.mode) {
    case "off":
      return "Everyone gets the default";
    case "preview":
      return change.preview.length === 1 ? "1 teammate previews it" : `${change.preview.length} teammates preview it`;
    case "everyone":
      return `Everyone gets ${change.everyoneVariant || "the new version"}`;
    case "rollout":
      return `${formatBp(change.percentBp)}% of ${units} get ${change.rolloutVariant || "the new version"}`;
    case "experiment":
      return `${formatBp(change.percentBp)}% of ${units} are in the comparison`;
  }
}

/** How long ago `since` was, coarsely: "12m", "5h", "3d". */
export function ageOf(since: Date | null, now: number = Date.now()): string {
  if (!since) return "";
  const minutes = Math.max(0, Math.floor((now - since.getTime()) / 60_000));
  if (minutes < 60) return `${minutes}m`;
  const hours = Math.floor(minutes / 60);
  if (hours < 48) return `${hours}h`;
  return `${Math.floor(hours / 24)}d`;
}

/** `since` as "12m ago", or "just now" inside the first minute. */
export function agoOf(since: Date | null, now: number = Date.now()): string {
  if (!since) return "";
  const age = ageOf(since, now);
  return age === "0m" ? "just now" : `${age} ago`;
}

/**
 * Whether a change's saved revision reached the environment's apps, from
 * GetChangesDeliveryStatus read AFTER the page saw that revision (its query
 * is keyed by the change's revision, hooks/changes-queries.ts), so the
 * snapshot revision it names covers it.
 *
 *   saved       the revision is saved; nothing yet says where it went (the
 *               status is loading, failed, or the control plane can't say)
 *   no-targets  the environment has no hosted apps the platform writes to
 *   waiting     some target still has an older snapshot than the one the
 *               environment renders now
 *   delivered   every target has the environment's current snapshot written
 *               to it (the CDN origin, the ConfigMap): `at` is the latest
 *               write. A page applies it on its next load; a pod after the
 *               ConfigMap sync plus the SDK's re-read. Not "seen".
 *
 * The server's `current` flag compares each target's ETag with the
 * ENVIRONMENT's current snapshot, never the change's own revision.
 * A later edit to another change in the environment moves it
 * on, so "waiting" can outlast this change's own delivery: it says only that
 * the environment's latest changes are not everywhere yet.
 */
export type ChangeDeliveryState =
  | { kind: "saved" }
  | { kind: "no-targets" }
  | { kind: "waiting"; delivered: number; targets: number }
  | { kind: "delivered"; targets: number; at: Date | null };

/** Whether one target has the environment's current snapshot. */
export function targetDelivered(target: ChangeDeliveryTarget): boolean {
  return target.current;
}

export function deliveryStateOf(status: ChangesDeliveryStatus | undefined): ChangeDeliveryState {
  if (!status) return { kind: "saved" };
  const targets = status.targets.length;
  if (targets === 0) return { kind: "no-targets" };
  const reached = status.targets.filter((target) => targetDelivered(target));
  if (reached.length < targets) return { kind: "waiting", delivered: reached.length, targets };
  const at = reached.reduce<Date | null>((latest, target) => (target.deliveredAt && (!latest || target.deliveredAt > latest) ? target.deliveredAt : latest), null);
  return { kind: "delivered", targets, at };
}

/** An even split across the declared variants (the remainder goes to the first), in basis points. */
export function evenSplit(variants: string[]): ChangeSplitArm[] {
  if (variants.length === 0) return [];
  const share = Math.floor(10000 / variants.length);
  return variants.map((variant, i) => ({ variant, weightBp: i === 0 ? 10000 - share * (variants.length - 1) : share }));
}

export function splitTotal(split: ChangeSplitArm[]): number {
  return split.reduce((sum, arm) => sum + arm.weightBp, 0);
}

/** A change key the server accepts: lowercase, starts with a letter, at most 64. */
export const CHANGE_KEY = /^[a-z][a-z0-9_.-]{0,63}$/;

/**
 * What moved between the revision the reader had and the one the server
 * holds now, as plain lines for the conflict notice. Fields the reader
 * cannot act on (timestamps, who) are left out.
 */
export function changedFields(before: Change, after: Change): string[] {
  const lines: string[] = [];
  if (before.mode !== after.mode) lines.push(`Mode: ${MODE_LABELS[before.mode]} → ${MODE_LABELS[after.mode]}`);
  if (before.percentBp !== after.percentBp) lines.push(`Share: ${formatBp(before.percentBp)}% → ${formatBp(after.percentBp)}%`);
  if (before.unit !== after.unit) lines.push(`What stays together: ${UNIT_LABELS[before.unit].label} → ${UNIT_LABELS[after.unit].label}`);
  const audience = (c: Change) => (c.audience ? `${c.audience.attribute} is ${c.audience.in.join(", ")}` : "everyone");
  if (audience(before) !== audience(after)) lines.push(`Eligible: ${audience(before)} → ${audience(after)}`);
  const split = (c: Change) => c.split.map((a) => `${a.variant} ${formatBp(a.weightBp)}%`).join(" / ") || "none";
  if (split(before) !== split(after)) lines.push(`Split: ${split(before)} → ${split(after)}`);
  if ((before.metric?.event ?? "") !== (after.metric?.event ?? "")) lines.push(`Success metric: ${before.metric?.event || "none"} → ${after.metric?.event || "none"}`);
  if (before.preview.length !== after.preview.length) lines.push(`Preview list: ${before.preview.length} → ${after.preview.length} people`);
  if ((before.currentRun?.id ?? "") !== (after.currentRun?.id ?? "")) lines.push(after.currentRun ? `A new run started (${after.currentRun.id})` : "The run ended");
  if (before.description !== after.description) lines.push("Description edited");
  if (lines.length === 0) lines.push(`Revision ${before.revision} → ${after.revision}`);
  return lines;
}
