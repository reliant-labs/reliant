// Copyright (c) 2025 Reliant Labs

/**
 * WHAT HAPPENED TO THIS ENVIRONMENT — one ordered timeline from two kinds of
 * entry, newest first.
 *
 * ── TWO KINDS, ONE LIST, AND THE HIERARCHY IS DELIBERATE ────────────────────
 *
 * PROMOTIONS are intent: somebody decided this environment should run v12.
 * They are what a human (or their CI) did, so they are the substantial rows.
 *
 * CONVERGENCES are observations: the platform watched the cluster and recorded
 * that the release arrived, or that it did not. They are secondary — derived
 * from the cluster's state, rebuildable, never required for correctness — so
 * they are indented, muted and one size down: notes hanging off the decisions
 * rather than peers of them.
 *
 * Interleaving them HERE is what makes the pair legible: "promoted v12 at
 * 14:02, confirmed running at 14:04" is a story. Interleaving them in the
 * release list is what made that list unreadable, which is why the release
 * list (LiveReleases) is promotions only and this is its own tab.
 *
 * ── HONEST LABELS ───────────────────────────────────────────────────────────
 *
 * Each observation says what was SEEN, in the customer's words: confirmed
 * running, couldn't finish rolling out, or an unclear reading. The
 * platform's verbatim reason is shown only when it carries information — a
 * failure, or an unclear reading — because that is the string someone
 * searches for; for a confirmation it only restates the label.
 *
 * An empty observation half is NORMAL and says nothing at all: the timeline
 * simply shows the promotions.
 *
 * Pure props; the page owns the queries.
 */

import { CheckCircle2, CircleAlert, CircleHelp, Upload } from "lucide-react";

import { cn } from "@/lib/utils";
import type { CloudPromotion } from "@/services/forge/cloudEnvs";
import type { LiveConvergence, LiveEnv } from "@/services/forge/live";

import { formatTimestamp } from "../Overview/EnvironmentTable";
import { ReleaseVersion } from "./ReleaseVersion";

/**
 * One timeline entry. A discriminated union rather than a flag, so a renderer
 * cannot read an observation's fields off a promotion or the reverse.
 */
export type ActivityEntry =
  | { kind: "promotion"; at: number; promotion: CloudPromotion }
  | { kind: "observation"; at: number; convergence: LiveConvergence };

/** Milliseconds, or 0 when the entry carries no usable time. */
function millis(iso: string | undefined): number {
  if (!iso) return 0;
  const parsed = Date.parse(iso);
  return Number.isFinite(parsed) ? parsed : 0;
}

/**
 * Merge both kinds into one list, newest first.
 *
 * An entry with no time sorts to the END rather than to the top: an undated
 * row claiming the newest position would displace the one entry a reader
 * actually looks for, and "we do not know when" is not "just now".
 */
export function mergeTimeline(
  promotions: CloudPromotion[],
  convergences: LiveConvergence[]
): ActivityEntry[] {
  const entries: ActivityEntry[] = [
    ...promotions.map((promotion) => ({
      kind: "promotion" as const,
      at: millis(promotion.createdAt),
      promotion,
    })),
    ...convergences.map((convergence) => ({
      kind: "observation" as const,
      at: millis(convergence.observedAt),
      convergence,
    })),
  ];
  return entries.sort((a, b) => {
    if (a.at === b.at) return 0;
    if (a.at === 0) return 1;
    if (b.at === 0) return -1;
    return b.at - a.at;
  });
}

/** What an observation SAW, in the customer's words. */
export function observationLabel(state: LiveConvergence["state"]): string {
  switch (state) {
    case "converged":
      return "Confirmed running";
    case "failed":
      return "Couldn't finish rolling out";
    default:
      return "Unclear reading";
  }
}

export function LiveActivity({
  env,
  promotions,
  convergences,
  isLoading,
  error,
}: {
  env: LiveEnv;
  promotions: CloudPromotion[] | undefined;
  /** The observation half. Undefined while loading, empty when nothing observed. */
  convergences: LiveConvergence[] | undefined;
  isLoading: boolean;
  error: Error | null;
}) {
  if (isLoading && !promotions) {
    return (
      <p data-testid="live-activity-loading" className="px-4 py-3 text-sm text-muted-foreground">
        Loading this environment&apos;s activity…
      </p>
    );
  }
  if (error && !promotions) {
    return (
      <p data-testid="live-activity-error" className="px-4 py-3 text-sm text-muted-foreground">
        Couldn&apos;t load {env.name}&apos;s activity. Try again.{" "}
        <span className="font-mono text-2xs">{error.message}</span>
      </p>
    );
  }

  const rows = mergeTimeline(promotions ?? [], convergences ?? []);
  if (rows.length === 0) {
    return (
      <p data-testid="live-activity-empty" className="px-4 py-3 text-sm text-muted-foreground">
        Nothing has happened here yet — the first promotion is the first entry.
      </p>
    );
  }

  return (
    <ol data-testid="live-activity">
      {rows.map((entry) =>
        entry.kind === "promotion" ? (
          <PromotionEntry key={`promotion-${entry.promotion.id}`} promotion={entry.promotion} />
        ) : (
          <ObservationEntry key={`observation-${entry.convergence.id}`} convergence={entry.convergence} />
        )
      )}
    </ol>
  );
}

/**
 * Shared row geometry: icon · [what happened (grows) · when (never wraps)].
 * The time drops below what happened when the two do not fit side by side,
 * rather than either being squeezed into the other.
 */
const ROW = "grid grid-cols-[auto_minmax(0,1fr)] items-start gap-x-3 border-b border-border/60 pr-4 last:border-0";
const BODY = "flex min-w-0 flex-wrap items-start gap-x-4 gap-y-0.5";

/** INTENT: a decision somebody made. The substantial row. */
function PromotionEntry({ promotion }: { promotion: CloudPromotion }) {
  const by = promotion.promotedByActor || (promotion.promotedByUserId ? "a user" : "");
  return (
    <li
      data-testid={`activity-promotion-${promotion.id}`}
      data-entry="promotion"
      className={cn(ROW, "py-2.5 pl-4 text-sm")}
    >
      <Upload className="mt-0.5 h-3.5 w-3.5 text-muted-foreground" aria-hidden="true" />
      <span className={BODY}>
        <span className="flex min-w-0 grow basis-56 flex-wrap items-center gap-x-1.5 gap-y-0.5 text-foreground">
          <span className="shrink-0">Promoted</span>
          {promotion.releaseVersion ? (
            <ReleaseVersion version={promotion.releaseVersion} copyable={false} />
          ) : (
            <span className="text-muted-foreground">an unnamed release</span>
          )}
          {by && <span className="shrink-0 text-xs text-muted-foreground">by {by}</span>}
        </span>
        <ActivityTime iso={promotion.createdAt} />
      </span>
    </li>
  );
}

/**
 * AN OBSERVATION: what the platform saw. Indented, muted and one size down,
 * so it reads as a note attached to the decisions rather than as one.
 *
 * The cluster name IS shown: a convergence is per target cluster, because one
 * environment can land on two clusters and fail on a third, and it is the
 * customer's own cluster name in the self-managed case.
 */
function ObservationEntry({ convergence }: { convergence: LiveConvergence }) {
  const failed = convergence.state === "failed";
  const confirmed = convergence.state === "converged";
  const Icon = failed ? CircleAlert : confirmed ? CheckCircle2 : CircleHelp;
  return (
    <li
      data-testid={`convergence-${convergence.id}`}
      data-entry="observation"
      data-state={convergence.state}
      className={cn(ROW, "py-2 pl-10 text-xs text-muted-foreground")}
    >
      <Icon className={cn("mt-0.5 h-3 w-3", failed ? "text-destructive-ink" : "text-muted-foreground")} aria-hidden="true" />
      <span className={BODY}>
        <span className="flex min-w-0 grow basis-56 flex-col gap-0.5">
          <span className="flex min-w-0 flex-wrap items-center gap-x-2">
            <span className={failed ? "text-destructive-ink" : undefined}>{observationLabel(convergence.state)}</span>
            {convergence.cluster && (
              <span className="min-w-0 truncate font-mono text-2xs" title={convergence.cluster}>
                {convergence.cluster}
              </span>
            )}
          </span>
          {!confirmed && convergence.reason && (
            <span className="break-words font-mono text-2xs">
              {convergence.reason}
              {convergence.message && ` — ${convergence.message}`}
            </span>
          )}
        </span>
        <ActivityTime iso={convergence.observedAt} />
      </span>
    </li>
  );
}

function ActivityTime({ iso }: { iso: string | undefined }) {
  return (
    <time
      dateTime={iso}
      title={iso}
      className="ml-auto shrink-0 whitespace-nowrap text-right text-xs tabular-nums text-muted-foreground"
    >
      {iso ? formatTimestamp(iso) : "—"}
    </time>
  );
}
