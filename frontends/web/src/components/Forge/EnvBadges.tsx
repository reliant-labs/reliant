// Copyright (c) 2025 Reliant Labs

/**
 * The two badges every environment carries on both forge screens: WHERE it
 * runs, and HOW HEALTHY it is by whichever source can measure it.
 *
 * One component per fact, shared by the Overview rows and the Environment
 * header, so the vocabulary cannot drift between them.
 *
 * WHERE is built on forge-ui's `badge.tsx`, used as installed,
 * and follows the same rules: Reliant cloud is the only destination with a
 * hue (`info`), because it is the one whose operational story differs — no
 * cluster to go and look at — while local/cluster/static are neutral, since
 * none is better than another. Unknown is its OWN treatment (dashed,
 * unfilled), never "Cluster".
 *
 * HEALTH is three shapes, because the three sources measure three different
 * things and one verdict word across all of them would claim they were
 * comparable (see environments.ts EnvHealth). Every shape uses the console's
 * certainty vocabulary (stateVocabulary), so the colours mean the same thing
 * here as in the image cells and the hosted workload list.
 */

import { CheckCircle2, CircleDashed, Loader2 } from "lucide-react";

import Badge from "@/components/forge-ui/badge";
import { Tooltip } from "@/components/ui/Tooltip";
import { cn } from "@/lib/utils";
import {
  whereExplanation,
  whereLabel,
  type EnvHealth,
  type EnvWhere,
} from "@/services/forge/environments";
import { verdictExplanation, verdictLabel } from "@/services/forge/topology";

import { VerdictChip } from "./HostedWorkloads";
import { CERTAINTY_STYLES } from "./stateVocabulary";

export function WhereBadge({ env, where }: { env: string; where: EnvWhere }) {
  return (
    <Tooltip content={whereExplanation(where).replace(/`/g, "")}>
      <span
        data-testid={`where-${env}`}
        data-where={where}
        className="inline-flex whitespace-nowrap font-sans"
      >
        {where === "unknown" ? (
          <span
            className={cn(
              "inline-flex items-center rounded-full px-2 py-0.5 text-2xs font-medium text-foreground",
              CERTAINTY_STYLES.unknown.container
            )}
          >
            {whereLabel(where)}
          </span>
        ) : (
          <Badge label={whereLabel(where)} variant={where === "cloud" ? "info" : "neutral"} size="sm" />
        )}
      </span>
    </Tooltip>
  );
}

/** A neutral, dashed chip: the "not measured" treatment, with the reason as its words. */
function QuietChip({ label, detail, testId }: { label: string; detail: string; testId: string }) {
  const style = CERTAINTY_STYLES.unknown;
  return (
    <Tooltip content={detail}>
      <span
        data-testid={testId}
        data-certainty="unknown"
        className={cn(
          "inline-flex shrink-0 items-center gap-1 rounded px-1.5 text-2xs font-medium leading-5 text-foreground",
          style.container
        )}
      >
        <CircleDashed className={cn("h-3 w-3 shrink-0", style.foreground)} aria-hidden="true" />
        {label}
      </span>
    </Tooltip>
  );
}

export function HealthChip({ env, health }: { env: string; health: EnvHealth }) {
  const testId = `health-${env}`;
  switch (health.kind) {
    case "hosted":
      if (health.loading) {
        return (
          <span
            data-testid={testId}
            data-certainty="unknown"
            className="inline-flex items-center gap-1 text-2xs text-muted-foreground"
          >
            <Loader2 className="h-3 w-3 animate-spin" aria-hidden="true" />
            Asking the control plane…
          </span>
        );
      }
      return <VerdictChip verdict={health.verdict} testId={testId} />;
    case "not-deployed":
      return (
        <QuietChip
          testId={testId}
          label="Not deployed yet"
          detail="The control plane has never been asked to run this environment. The first deploy creates it."
        />
      );
    case "images": {
      const { tally } = health;
      const total = tally["known-good"] + tally["known-bad"] + tally.unknown;
      if (tally["known-bad"] > 0) {
        const style = CERTAINTY_STYLES["known-bad"];
        return (
          <Tooltip content="At least one image in this environment's cluster is not the digest its release froze.">
            <span
              data-testid={testId}
              data-certainty="known-bad"
              className={cn(
                "inline-flex shrink-0 items-center gap-1 rounded px-1.5 text-2xs font-medium leading-5 text-foreground",
                style.container
              )}
            >
              {tally["known-bad"]} of {total} wrong
            </span>
          </Tooltip>
        );
      }
      if (tally.unknown === 0 && total > 0) {
        const style = CERTAINTY_STYLES["known-good"];
        return (
          <Tooltip content="Every image was verified against the cluster and matches the release.">
            <span
              data-testid={testId}
              data-certainty="known-good"
              className={cn(
                "inline-flex shrink-0 items-center gap-1 rounded px-1.5 text-2xs font-medium leading-5 text-foreground",
                style.container
              )}
            >
              <CheckCircle2 className={cn("h-3 w-3 shrink-0", style.foreground)} aria-hidden="true" />
              Verified
            </span>
          </Tooltip>
        );
      }
      // The default for a cluster binding: nobody has looked. Said as such —
      // painting it green is the bug this whole surface exists to prevent.
      return (
        <QuietChip
          testId={testId}
          label="Not verified"
          detail="The release is declared but nobody has checked the cluster. This is not a statement that it is healthy. Verify it on the environment's page."
        />
      );
    }
    default:
      return (
        <span data-testid={testId} data-certainty="none" className="text-2xs text-muted-foreground">
          <span aria-hidden="true">—</span>
          <span className="sr-only">no health reported</span>
        </span>
      );
  }
}

/** The one-sentence form of a hosted verdict, for the Environment header. */
export function verdictSentence(health: EnvHealth): string | null {
  if (health.kind !== "hosted" || health.loading) return null;
  return health.verdict === "unknown"
    ? verdictExplanation("unknown")
    : `${verdictLabel(health.verdict)} — ${verdictExplanation(health.verdict)}`;
}
