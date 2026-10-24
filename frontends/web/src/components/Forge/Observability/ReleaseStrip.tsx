// Copyright (c) 2025 Reliant Labs

/**
 * The promotions behind the release markers, in words: one line saying how
 * many there are in the window, which opens into a list naming each one's
 * environment, version, commit and who promoted it — and the way into a
 * before/after comparison of any of them.
 *
 * The canvas has room for a version and nothing else; this is where the rest
 * of a marker is said, and where a screen-reader user meets the markers at all.
 */

import { useState } from "react";
import { ChevronDown, ChevronRight, GitCommitHorizontal, RefreshCw, Rocket, Undo2 } from "lucide-react";

import Button from "@/components/forge-ui/button";
import type { Promotion } from "@/services/controlPlane/observability/deliveryTypes";
import { deliveryErrorKind, obsErrorMessage } from "@/services/controlPlane/observability/errors";
import { cn } from "@/lib/utils";

import { shortCommit } from "./deliveryScope";
import { formatAgo, formatWhen } from "./format";
import type { ReleaseMarkers } from "./releaseMarkers";

export interface ReleaseStripProps {
  releases: ReleaseMarkers;
  /** "last hour", lower case, for the sentence. */
  rangeLabel: string;
  /** Start open (the Releases dashboard, where the list is the point). */
  defaultOpen?: boolean;
  onCompare: (promotion: Promotion) => void;
}

export function ReleaseStrip({ releases, rangeLabel, defaultOpen = false, onCompare }: ReleaseStripProps) {
  const [open, setOpen] = useState(defaultOpen);
  const line = "flex flex-wrap items-center gap-2 rounded-lg border border-border/60 bg-background px-3 py-2 text-xs text-muted-foreground";

  if (releases.status === "off") return null;
  if (releases.status === "loading") {
    return (
      <p className={line} data-testid="obs-releases" data-status="loading" role="status">
        <Rocket className="h-3.5 w-3.5" aria-hidden="true" />
        Loading the releases promoted in the {rangeLabel}…
      </p>
    );
  }
  if (releases.status === "error") {
    const kind = deliveryErrorKind(releases.error);
    const why =
      kind === "not-member"
        ? "you're not a member of this organization, so its release ledger can't be read."
        : kind === "env-unavailable"
          ? "an environment in view isn't available to this organization (it may have been deleted)."
          : kind === "denied"
            ? "you don't have access to this organization's release ledger."
            : `the release ledger couldn't be read${obsErrorMessage(releases.error) ? ` (${obsErrorMessage(releases.error)})` : ""}.`;
    return (
      <p className={line} data-testid="obs-releases" data-status="error" data-kind={kind} role="status">
        <Rocket className="h-3.5 w-3.5" aria-hidden="true" />
        <span>Release markers aren&apos;t shown: {why}</span>
        {kind === "unavailable" && (
          <button
            type="button"
            onClick={releases.refetch}
            className="inline-flex items-center gap-1 rounded-sm text-primary underline-offset-2 hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            <RefreshCw className="h-3 w-3" aria-hidden="true" />
            Try again
          </button>
        )}
      </p>
    );
  }
  if (releases.excluded) {
    return (
      <p className={line} data-testid="obs-releases" data-status="excluded" role="status">
        <Rocket className="h-3.5 w-3.5" aria-hidden="true" />
        No release markers: the filters exclude every environment.
      </p>
    );
  }
  const count = releases.promotions.length;
  if (count === 0) {
    return (
      <p className={line} data-testid="obs-releases" data-status="none" role="status">
        <Rocket className="h-3.5 w-3.5" aria-hidden="true" />
        No release was promoted in the {rangeLabel}.
      </p>
    );
  }
  const rollbacks = releases.promotions.filter((p) => p.kind === "rollback").length;

  return (
    <div className="rounded-lg border border-border/60 bg-background text-xs" data-testid="obs-releases" data-status="ready" data-count={count}>
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        aria-expanded={open}
        aria-controls="obs-releases-list"
        className="flex w-full flex-wrap items-center gap-2 rounded-lg px-3 py-2 text-left text-muted-foreground hover:text-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        data-testid="obs-releases-toggle"
      >
        {open ? <ChevronDown className="h-3.5 w-3.5" aria-hidden="true" /> : <ChevronRight className="h-3.5 w-3.5" aria-hidden="true" />}
        <span className="inline-block h-3 w-0 border-l border-dashed border-primary" aria-hidden="true" />
        <span>
          <span className="font-medium text-foreground">
            {releases.truncated ? `The newest ${count}` : count} release{count === 1 ? "" : "s"}
          </span>{" "}
          promoted in the {rangeLabel}
          {rollbacks > 0 ? `, ${rollbacks} of them rollback${rollbacks === 1 ? "" : "s"}` : ""} · marked on the charts
        </span>
        <span className="ml-auto">{open ? "Hide" : "Show"}</span>
      </button>
      {open && (
        <div id="obs-releases-list" className="border-t border-border/60">
          {releases.truncated && (
            <p className="px-3 pt-2 text-warning" role="status" data-testid="obs-releases-truncated">
              The window holds more promotions than one request returns, so only the newest {count} are listed and marked. Narrow the
              window or the environments to see the rest.
            </p>
          )}
          <ul className="max-h-64 divide-y divide-border/50 overflow-y-auto" aria-label="Releases in this window, newest first">
            {releases.promotions.map((p) => (
              <PromotionRow key={p.promotionId} promotion={p} onCompare={() => onCompare(p)} />
            ))}
          </ul>
        </div>
      )}
    </div>
  );
}

export function PromotionRow({ promotion: p, onCompare }: { promotion: Promotion; onCompare: () => void }) {
  const rollback = p.kind === "rollback";
  return (
    <li className="flex flex-wrap items-center gap-x-3 gap-y-1 px-3 py-2" data-testid={`obs-release-${p.promotionId}`} data-kind={p.kind}>
      <span className="w-24 shrink-0 tabular-nums text-muted-foreground" title={formatWhen(p.promotedAtMs)}>
        {formatAgo(p.promotedAtMs)}
      </span>
      <span className="w-16 shrink-0 truncate font-mono text-foreground" title={p.envName}>
        {p.envName}
      </span>
      <span className="inline-flex min-w-[6rem] items-center gap-1.5">
        {rollback && <Undo2 className="h-3.5 w-3.5 text-warning" aria-label="Rollback" />}
        <span className={cn("font-mono font-medium", rollback ? "text-warning" : "text-foreground")}>{p.version || p.releaseId.slice(0, 12)}</span>
        {rollback && <span className="rounded px-1 text-2xs uppercase tracking-wide text-warning ring-1 ring-inset ring-warning/40">rollback</span>}
      </span>
      <span className="inline-flex items-center gap-1 font-mono text-muted-foreground" title={p.gitCommit || "No commit recorded"}>
        <GitCommitHorizontal className="h-3.5 w-3.5" aria-hidden="true" />
        {p.gitCommit ? shortCommit(p.gitCommit) : "no commit"}
        {p.gitTag && p.gitTag !== p.version ? <span className="ml-1 text-foreground">{p.gitTag}</span> : null}
      </span>
      <span className="min-w-0 flex-1 truncate text-muted-foreground" title={p.promotedBy}>
        {p.promotedBy ? `by ${p.promotedBy}` : ""}
      </span>
      <Button variant="ghost" size="xs" onClick={onCompare} data-testid={`obs-release-compare-${p.promotionId}`}>
        Compare before / after
      </Button>
    </li>
  );
}
