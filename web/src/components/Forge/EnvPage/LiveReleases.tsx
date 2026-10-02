// Copyright (c) 2025 Reliant Labs

/**
 * WHAT THIS ENVIRONMENT HAS BEEN ON — the control plane's promotion ledger.
 *
 * ONE SOURCE, and that is the change. This section used to have two: the
 * control plane's append-only ledger for hosted envs, and — for everything
 * else — the CURRENT binding from the daemon plus a line telling the reader to
 * go and run `git log .forge/promotions` themselves. A daemon-sourced row
 * sitting beside a hosted one is exactly why an offline daemon degraded the
 * whole page, and "run this git command" is not a history: it is an admission
 * that the screen does not have one.
 *
 * Now every environment that has a control-plane row has its history here,
 * because every env that declares a control plane keeps its ledger there
 * whatever its kind — including self-managed, which forge applies but the
 * platform still records. An env with NO row is not rendered by this
 * component at all; Live says "not built yet" above it.
 *
 * Pure props; the page owns the queries.
 */

import { Upload } from "lucide-react";

import Badge from "@/components/forge-ui/badge";
import type { CloudPromotion } from "@/services/forge/cloudEnvs";
import { declaredNotBuilt, type LiveEnv } from "@/services/forge/live";
import { shortDigest } from "@/services/forge/topology";

import { formatTimestamp } from "../Overview/EnvironmentTable";

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

  const rows = promotions ?? [];
  if (rows.length === 0) {
    // Declared-but-not-built and never-promoted read differently: the first
    // has a declaration to point at, the second does not. Neither is an error.
    return declaredNotBuilt(env) ? (
      <p data-testid="live-releases-declared" className="text-sm text-muted-foreground">
        Nothing promoted yet. <code className="font-mono text-foreground">forge env build {env.name}</code>{" "}
        records the first release.
      </p>
    ) : (
      <p data-testid="live-releases-empty" className="text-sm text-muted-foreground">
        Never promoted — the first deploy records the first entry.
      </p>
    );
  }

  return (
    <ol className="overflow-hidden rounded-lg border border-border bg-card" data-testid="live-releases">
      {rows.map((promotion, index) => {
        const by = promotion.promotedByActor || (promotion.promotedByUserId ? "a user" : "");
        return (
          <li
            key={promotion.id}
            data-testid={`promotion-${promotion.id}`}
            className="flex flex-wrap items-start gap-x-4 gap-y-1 border-b border-border/60 px-4 py-2.5 last:border-0"
          >
            <span className="inline-flex w-28 shrink-0 items-center gap-1.5">
              <Upload className="h-3.5 w-3.5 text-muted-foreground" aria-hidden="true" />
              <span className="font-mono text-sm text-foreground">{promotion.releaseVersion || "—"}</span>
            </span>
            <span className="flex min-w-0 flex-1 flex-col gap-0.5 text-xs text-muted-foreground">
              <span className="flex flex-wrap items-center gap-2">
                {index === 0 && <Badge label="current" variant="info" size="sm" />}
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
      })}
    </ol>
  );
}
