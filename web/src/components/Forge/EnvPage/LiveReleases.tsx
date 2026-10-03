// Copyright (c) 2025 Reliant Labs

/**
 * WHAT THIS ENVIRONMENT HAS BEEN ASKED TO RUN, AND WHAT WAS SEEN RUNNING —
 * one ordered timeline from two kinds of entry.
 *
 * ── TWO KINDS, ONE LIST, AND THE HIERARCHY IS DELIBERATE ────────────────────
 *
 * PROMOTIONS are intent: somebody decided this environment should run v12.
 * They are the primary record, they are what a human did, and they are
 * rendered as the substantial rows — the release version, who promoted it, the
 * artifact digests.
 *
 * CONVERGENCES are observations: the platform watched the cluster and recorded
 * that it arrived, or that it failed. They are secondary — derived from the
 * cluster's current state, rebuildable, and never required for correctness —
 * so they are rendered as indented, muted, smaller entries hanging off the
 * timeline rather than as peers of the decisions.
 *
 * Interleaving them in one list is what makes the pair legible: "asked for v12
 * at 14:02, confirmed at 14:04" is a story, and the same facts in two separate
 * panels is a puzzle. Styling them identically would be the opposite mistake —
 * it would make an observation look like a decision somebody made.
 *
 * ── WHAT THIS REPLACES ──────────────────────────────────────────────────────
 *
 * The observation half used to be an APPLY: a forge process announcing it was
 * applying something, then reporting how that went. Every row had to say
 * "reported by forge · alice" to stay honest, because the claim came from the
 * client. forge does not apply any more — a promotion declares intent, the
 * cluster is converged to it, and the platform observes the result itself — so
 * there is no reporter, no actor to attribute, and no label to apply.
 *
 * An empty observation half is NORMAL and says nothing at all: the timeline
 * simply shows the promotions. Nothing observes most environments yet, and a
 * row saying "no observation" beside every promotion would be noise that
 * crowds out the entries that mean something.
 *
 * Pure props; the page owns the queries.
 */

import { CheckCircle2, CircleAlert, Upload } from "lucide-react";

import Badge from "@/components/forge-ui/badge";
import type { CloudPromotion } from "@/services/forge/cloudEnvs";
import { declaredNotBuilt, type LiveConvergence, type LiveEnv } from "@/services/forge/live";
import { shortDigest } from "@/services/forge/topology";

import { formatTimestamp } from "../Overview/EnvironmentTable";

/**
 * One timeline entry. A discriminated union rather than a flag, so a renderer
 * cannot read an observation's fields off a promotion or the reverse.
 */
type Entry =
  | { kind: "promotion"; at: number; promotion: CloudPromotion; current: boolean }
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
): Entry[] {
  const entries: Entry[] = [
    ...promotions.map((promotion, index) => ({
      kind: "promotion" as const,
      at: millis(promotion.createdAt),
      promotion,
      // The list arrives newest first, so the first promotion is the binding
      // one. Derived from the INPUT order rather than from the merged order,
      // because an observation could otherwise shift which row looks current.
      current: index === 0,
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

export function LiveReleases({
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
      <p data-testid="live-releases-loading" className="text-sm text-muted-foreground">
        Loading this environment&apos;s history…
      </p>
    );
  }

  // Plain, and it says what to do. The server's own words stay as detail for a
  // support conversation (#366) — they are the one technical string worth more
  // than a friendly one.
  if (error && !promotions) {
    return (
      <p data-testid="live-releases-error" className="text-sm text-muted-foreground">
        Couldn&apos;t load {env.name}&apos;s history. Try again.{" "}
        <span className="font-mono text-2xs">{error.message}</span>
      </p>
    );
  }

  const rows = mergeTimeline(promotions ?? [], convergences ?? []);
  if (rows.length === 0) {
    // Declared-but-not-built and never-promoted read differently: the first
    // has a declaration to point at, the second does not. Neither is an error.
    return declaredNotBuilt(env) ? (
      <p data-testid="live-releases-declared" className="text-sm text-muted-foreground">
        Nothing promoted yet.{" "}
        <code className="font-mono text-foreground">forge env build {env.name}</code> records the
        first release.
      </p>
    ) : (
      <p data-testid="live-releases-empty" className="text-sm text-muted-foreground">
        Never promoted — the first deploy records the first entry.
      </p>
    );
  }

  return (
    <ol
      className="overflow-hidden rounded-lg border border-border bg-card"
      data-testid="live-releases"
    >
      {rows.map((entry) =>
        entry.kind === "promotion" ? (
          <PromotionRow
            key={`promotion-${entry.promotion.id}`}
            promotion={entry.promotion}
            current={entry.current}
          />
        ) : (
          <ObservationRow key={`observation-${entry.convergence.id}`} convergence={entry.convergence} />
        )
      )}
    </ol>
  );
}

/** INTENT: a decision somebody made. The substantial row. */
function PromotionRow({
  promotion,
  current,
}: {
  promotion: CloudPromotion;
  current: boolean;
}) {
  const by = promotion.promotedByActor || (promotion.promotedByUserId ? "a user" : "");
  return (
    <li
      data-testid={`promotion-${promotion.id}`}
      data-entry="promotion"
      className="flex flex-wrap items-start gap-x-4 gap-y-1 border-b border-border/60 px-4 py-2.5 last:border-0"
    >
      <span className="inline-flex w-28 shrink-0 items-center gap-1.5">
        <Upload className="h-3.5 w-3.5 text-muted-foreground" aria-hidden="true" />
        <span className="font-mono text-sm text-foreground">
          {promotion.releaseVersion || "—"}
        </span>
      </span>
      <span className="flex min-w-0 flex-1 flex-col gap-0.5 text-xs text-muted-foreground">
        <span className="flex flex-wrap items-center gap-2">
          {current && <Badge label="current" variant="info" size="sm" />}
          {promotion.createdAt && <span>{formatTimestamp(promotion.createdAt)}</span>}
          {by && <span>by {by}</span>}
        </span>
        {promotion.note && <span className="text-foreground">{promotion.note}</span>}
        {promotion.artifacts.length > 0 && (
          <span className="font-mono text-2xs">
            {promotion.artifacts
              .map((artifact) =>
                artifact.digest ? `${artifact.name}@${shortDigest(artifact.digest)}` : artifact.name
              )
              .join(" · ")}
          </span>
        )}
      </span>
    </li>
  );
}

/**
 * AN OBSERVATION: what the platform saw. Indented, muted and one size down,
 * so it reads as a note attached to the decisions above rather than as one.
 *
 * The cluster name IS shown, and that is not an internal noun leaking: a
 * convergence is per target cluster precisely because one environment can
 * land on two clusters and fail on a third, and it is the customer's own
 * cluster name in the self-managed case. Without it a two-cluster environment
 * shows two entries a reader cannot tell apart.
 *
 * The failure REASON is the platform's verbatim string. It is the one place a
 * technical word beats a friendly one — it is what somebody searches for — and
 * it is clearly subordinate to the plain sentence beside it.
 */
function ObservationRow({ convergence }: { convergence: LiveConvergence }) {
  const failed = convergence.state === "failed";
  return (
    <li
      data-testid={`convergence-${convergence.id}`}
      data-entry="observation"
      data-state={convergence.state}
      className="flex flex-wrap items-start gap-x-4 gap-y-1 border-b border-border/60 py-2 pl-10 pr-4 last:border-0"
    >
      <span className="inline-flex w-20 shrink-0 items-center gap-1.5">
        {failed ? (
          <CircleAlert className="h-3 w-3 text-destructive" aria-hidden="true" />
        ) : (
          <CheckCircle2 className="h-3 w-3 text-muted-foreground" aria-hidden="true" />
        )}
        <span className="text-2xs text-muted-foreground">
          {failed ? "failed" : convergence.state === "converged" ? "confirmed" : "unclear"}
        </span>
      </span>
      <span className="flex min-w-0 flex-1 flex-col gap-0.5 text-2xs text-muted-foreground">
        <span className="flex flex-wrap items-center gap-2">
          {convergence.observedAt && <span>{formatTimestamp(convergence.observedAt)}</span>}
          {convergence.cluster && <span className="font-mono">{convergence.cluster}</span>}
        </span>
        {convergence.reason && (
          <span className={failed ? "font-mono text-destructive" : "font-mono"}>
            {convergence.reason}
            {convergence.message && ` — ${convergence.message}`}
          </span>
        )}
      </span>
    </li>
  );
}
