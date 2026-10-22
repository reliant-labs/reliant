// Copyright (c) 2025 Reliant Labs

/**
 * WHAT THIS ENVIRONMENT HAS BEEN ASKED TO RUN — the release list.
 *
 * One row per promotion, newest first: the version, whether it is current,
 * when, who, and the image digests it pinned. What the platform OBSERVED after
 * each promotion is not here; that is the Activity tab (LiveActivity.tsx).
 * A release list is something a reader scans and compares, and observation
 * rows interleaved between the releases pushed them apart until the history
 * was the hardest thing on the page to read.
 *
 * ── THE ROW NEVER OVERLAPS ITSELF ───────────────────────────────────────────
 *
 * The version used to sit in a fixed `w-28` column as plain text. forge's
 * versions are long (`20261005.202711-1f9bf6721d7d`), so it wrapped onto two
 * lines, the "current" pill beside it was drawn over the wrap, and the next
 * row's date collided with it. The row now WRAPS WHOLE PIECES instead of
 * squeezing them:
 *
 *   [ version (one line, middle-truncated) · pill ]           [ date ]
 *   [ by · note · image refs (each truncated)                         ]
 *
 * The head cell asks for a 14rem basis and grows; when the row cannot fit it
 * and the date side by side, the date drops to its own line (right-aligned)
 * rather than overlapping. Inside the head, the pill likewise drops below the
 * version before either is squeezed, and the version clips its own overflow,
 * so its pinned tail can never paint under a neighbour. Each image ref is its
 * own truncating chip, with the full ref in its title.
 *
 * Pure props; the page owns the queries.
 */

import Badge from "@/components/forge-ui/badge";
import type { CloudPromotion } from "@/services/forge/cloudEnvs";
import { declaredNotBuilt, type LiveEnv } from "@/services/forge/live";
import { shortDigest } from "@/services/forge/topology";

import { formatTimestamp } from "../Overview/EnvironmentTable";
import { ReleaseVersion } from "./ReleaseVersion";

export function LiveReleases({
  env,
  promotions,
  isLoading,
  error,
}: {
  env: LiveEnv;
  promotions: CloudPromotion[] | undefined;
  isLoading: boolean;
  error: Error | null;
}) {
  if (isLoading && !promotions) {
    return (
      <p data-testid="live-releases-loading" className="px-4 py-3 text-sm text-muted-foreground">
        Loading this environment&apos;s releases…
      </p>
    );
  }

  // Plain, and it says what to do. The server's own words stay as detail for a
  // support conversation (#366) — they are the one technical string worth more
  // than a friendly one.
  if (error && !promotions) {
    return (
      <p data-testid="live-releases-error" className="px-4 py-3 text-sm text-muted-foreground">
        Couldn&apos;t load {env.name}&apos;s releases. Try again.{" "}
        <span className="font-mono text-2xs">{error.message}</span>
      </p>
    );
  }

  const rows = promotions ?? [];
  if (rows.length === 0) {
    // Declared-but-not-built and never-promoted read differently: the first
    // has a declaration to point at, the second does not. Neither is an error.
    return declaredNotBuilt(env) ? (
      <p data-testid="live-releases-declared" className="px-4 py-3 text-sm text-muted-foreground">
        Nothing promoted yet.{" "}
        <code className="font-mono text-foreground">forge env build {env.name}</code> records the
        first release.
      </p>
    ) : (
      <p data-testid="live-releases-empty" className="px-4 py-3 text-sm text-muted-foreground">
        Never promoted — the first deploy records the first release.
      </p>
    );
  }

  return (
    <ol data-testid="live-releases">
      {rows.map((promotion, index) => (
        // The list arrives newest first, so the first promotion is the binding one.
        <ReleaseRow key={promotion.id} promotion={promotion} current={index === 0} />
      ))}
    </ol>
  );
}

function ReleaseRow({ promotion, current }: { promotion: CloudPromotion; current: boolean }) {
  const by = promotion.promotedByActor || (promotion.promotedByUserId ? "a user" : "");
  return (
    <li
      data-testid={`promotion-${promotion.id}`}
      data-entry="promotion"
      data-current={current || undefined}
      className="flex flex-wrap items-center gap-x-4 gap-y-1.5 border-b border-border/60 px-4 py-3 last:border-0"
    >
      <span
        className="flex min-w-0 grow basis-56 flex-wrap items-center gap-x-2 gap-y-1"
        data-testid={`release-head-${promotion.id}`}
      >
        {promotion.releaseVersion ? (
          <ReleaseVersion version={promotion.releaseVersion} className="text-sm" />
        ) : (
          <span className="font-mono text-sm text-muted-foreground">—</span>
        )}
        {current && (
          <span className="shrink-0" data-testid={`release-current-${promotion.id}`}>
            <Badge label="current" variant="info" size="sm" />
          </span>
        )}
      </span>

      <time
        dateTime={promotion.createdAt}
        title={promotion.createdAt}
        data-testid={`release-date-${promotion.id}`}
        className="ml-auto shrink-0 whitespace-nowrap text-right text-xs tabular-nums text-muted-foreground"
      >
        {promotion.createdAt ? formatTimestamp(promotion.createdAt) : "—"}
      </time>

      {(by || promotion.note || promotion.artifacts.length > 0) && (
        <span className="flex w-full min-w-0 flex-col gap-1.5 text-xs text-muted-foreground">
          {(by || promotion.note) && (
            <span className="min-w-0 break-words">
              {by && <>Promoted by {by}</>}
              {by && promotion.note && " · "}
              {promotion.note && <span className="text-foreground">{promotion.note}</span>}
            </span>
          )}
          {promotion.artifacts.length > 0 && (
            <ul className="flex min-w-0 flex-wrap gap-1.5" aria-label="Images">
              {promotion.artifacts.map((artifact) => {
                const short = artifact.digest ? `${artifact.name}@${shortDigest(artifact.digest)}` : artifact.name;
                const full = artifact.digest ? `${artifact.name}@${artifact.digest}` : artifact.name;
                return (
                  <li
                    key={`${artifact.name}@${artifact.digest}`}
                    title={full}
                    className="min-w-0 max-w-full truncate rounded border border-border/60 bg-background px-1.5 py-0.5 font-mono text-2xs"
                  >
                    {short}
                  </li>
                );
              })}
            </ul>
          )}
        </span>
      )}
    </li>
  );
}
