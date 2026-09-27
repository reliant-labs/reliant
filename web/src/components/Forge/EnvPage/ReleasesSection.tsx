// Copyright (c) 2025 Reliant Labs

/**
 * WHAT THIS ENVIRONMENT HAS BEEN ON — its promotion history.
 *
 *   Reliant cloud  the control plane's append-only ledger (ListPromotions):
 *                  every promote and rollback, who did it, when, and the
 *                  digests it froze. No daemon.
 *   everything else forge's ledger lives in the project's checkout, and
 *                  `forge env topology` reports only the CURRENT binding, so
 *                  that is what is shown — with its image digests and their
 *                  verified state. A full history for a checkout ledger is
 *                  `git log .forge/promotions`, and the section says so rather
 *                  than inventing one.
 *   local          never promoted, by construction: `forge env up` runs the
 *                  working tree.
 *
 * Pure props; the page owns the queries.
 */

import { RotateCcw, Upload } from "lucide-react";

import Badge from "@/components/forge-ui/badge";
import type { CloudPromotion } from "@/services/forge/cloudEnvs";
import type { EnvFacts, ForgeEnvSummary } from "@/services/forge/environments";
import { cellFor, shortDigest } from "@/services/forge/topology";

import { TopologyCell } from "../TopologyCell";
import { formatTimestamp } from "../Overview/EnvironmentTable";

export type ReleasesSource =
  | { kind: "cloud"; promotions: CloudPromotion[] | undefined; isLoading: boolean; error: Error | null }
  | { kind: "ledger"; daemonOffline: boolean }
  | { kind: "local" };

const TH = "px-3 py-2 text-left text-2xs font-medium uppercase tracking-wide text-muted-foreground";

export function ReleasesSection({
  summary,
  facts,
  source,
}: {
  summary: ForgeEnvSummary;
  facts: EnvFacts;
  source: ReleasesSource;
}) {
  if (source.kind === "local") {
    return (
      <p data-testid="releases-local" className="text-sm text-muted-foreground">
        A local environment is never promoted — it runs whatever is checked out.
      </p>
    );
  }

  if (source.kind === "ledger") {
    if (source.daemonOffline && !summary.forge) {
      return (
        <p data-testid="releases-daemon-offline" className="text-sm text-muted-foreground">
          This environment&apos;s promotion ledger is in the project&apos;s checkout, which is read by
          your daemon — offline right now.
        </p>
      );
    }
    const images = summary.forge?.images ?? [];
    return (
      <div className="space-y-3" data-testid="releases-ledger">
        {facts.release ? (
          <p className="text-sm text-muted-foreground">
            Bound to <span className="font-mono text-foreground">{facts.release}</span>
            {facts.promotedAt ? ` since ${formatTimestamp(facts.promotedAt)}` : ""}
            {facts.rolledBack ? " (a rollback)" : ""}.{" "}
            {facts.lag && <span>{facts.lag}.</span>}
          </p>
        ) : (
          <p className="text-sm text-muted-foreground">
            Never promoted — no release is bound to this environment.
          </p>
        )}
        {images.length > 0 && (
          <div className="overflow-hidden rounded-lg border border-border bg-card">
            <table className="w-full border-collapse text-xs">
              <caption className="sr-only">The images this binding freezes, and whether each was verified.</caption>
              <thead>
                <tr className="border-b border-border">
                  <th scope="col" className={TH}>
                    Image
                  </th>
                  <th scope="col" className={TH}>
                    Digest
                  </th>
                  <th scope="col" className={TH}>
                    State
                  </th>
                </tr>
              </thead>
              <tbody>
                {images.map((img) => (
                  <tr key={img.image} className="border-b border-border/60 last:border-0">
                    <th scope="row" className="px-3 py-2 text-left font-mono font-normal text-foreground">
                      {img.image}
                    </th>
                    <td className="px-3 py-2 font-mono text-muted-foreground">{shortDigest(img.digest) || "—"}</td>
                    <TopologyCell env={summary.name} cell={cellFor(summary.forge!, img.image)} />
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
        <p className="text-xs text-muted-foreground">
          The ledger lives in the checkout — its history is{" "}
          <code className="font-mono text-foreground">git log .forge/promotions</code>.
        </p>
      </div>
    );
  }

  if (source.isLoading && !source.promotions) {
    return (
      <p data-testid="releases-cloud-loading" className="text-sm text-muted-foreground">
        Reading the promotion ledger…
      </p>
    );
  }
  if (source.error && !source.promotions) {
    return (
      <p data-testid="releases-cloud-error" className="text-sm text-muted-foreground">
        The control plane&apos;s promotion ledger could not be read right now.{" "}
        <span className="font-mono text-2xs">{source.error.message}</span>
      </p>
    );
  }
  const promotions = source.promotions ?? [];
  if (promotions.length === 0) {
    return (
      <p data-testid="releases-cloud-empty" className="text-sm text-muted-foreground">
        Never promoted — the first deploy records the first entry.
      </p>
    );
  }

  return (
    <ol className="overflow-hidden rounded-lg border border-border bg-card" data-testid="releases-cloud">
      {promotions.map((promotion, index) => {
        const Icon = promotion.kind === "rollback" ? RotateCcw : Upload;
        const by = promotion.promotedByActor || (promotion.promotedByUserId ? "a user" : "");
        return (
          <li
            key={promotion.id}
            data-testid={`promotion-${promotion.id}`}
            className="flex flex-wrap items-start gap-x-4 gap-y-1 border-b border-border/60 px-4 py-2.5 last:border-0"
          >
            <span className="inline-flex w-28 shrink-0 items-center gap-1.5">
              <Icon className="h-3.5 w-3.5 text-muted-foreground" aria-hidden="true" />
              <span className="font-mono text-sm text-foreground">{promotion.releaseVersion || "—"}</span>
            </span>
            <span className="flex min-w-0 flex-1 flex-col gap-0.5 text-xs text-muted-foreground">
              <span className="flex flex-wrap items-center gap-2">
                {index === 0 && <Badge label="current" variant="info" size="sm" />}
                {promotion.kind === "rollback" && <Badge label="rollback" variant="warning" size="sm" />}
                {promotion.createdAt && <span>{formatTimestamp(promotion.createdAt)}</span>}
                {by && <span>by {by}</span>}
              </span>
              {promotion.note && <span className="text-foreground">{promotion.note}</span>}
              {promotion.artifacts.length > 0 && (
                <span className="font-mono text-2xs">
                  {promotion.artifacts
                    .map((a) => (a.digest ? `${a.name}@${shortDigest(a.digest)}` : a.name))
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
