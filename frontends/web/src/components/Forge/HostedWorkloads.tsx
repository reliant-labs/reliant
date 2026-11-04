// Copyright (c) 2025 Reliant Labs

/**
 * A hosted environment's health verdict, as a chip in the console's
 * certainty vocabulary (stateVocabulary), with an icon so it survives
 * greyscale and colour-vision deficiency. The environment rows use it; the
 * workloads themselves are the Overview's (../Workloads).
 */

import { AlertTriangle, CheckCircle2, CircleDashed, Loader2 } from "lucide-react";
import type { LucideIcon } from "lucide-react";

import Tooltip from "@/components/forge-ui/tooltip";
import { cn } from "@/lib/utils";
import { verdictCertainty, verdictExplanation, verdictLabel, type HostedVerdict } from "@/services/forge/topology";

import { CERTAINTY_STYLES } from "./stateVocabulary";

/** The icon per verdict: the same certainty glyph family the topology cells use. */
const VERDICT_ICON: Record<HostedVerdict, LucideIcon> = {
  converged: CheckCircle2,
  converging: Loader2,
  diverged: AlertTriangle,
  degraded: AlertTriangle,
  unknown: CircleDashed,
};

/**
 * The verdict in the console's three-level certainty vocabulary
 * (stateVocabulary's CERTAINTY_STYLES — the same fill/border treatment the
 * topology cells use, so there is no second colour
 * language). Converged is the only known-good; converging is NOT — a workload
 * that has not held its shape past the stability window has not yet earned it,
 * so it keeps the dashed, unfilled `unknown` treatment.
 *
 * The TEXT is always `text-ink`; the hue lives on the icon, the fill
 * and the border. Hued text at 11px over its own 15% tint measured 2.8:1
 * (success, light) and 3.5:1 (destructive, dark) — below AA — while the
 * certainty still reads from three independent axes without it.
 */
export function VerdictChip({
  verdict,
  reason,
  testId,
}: {
  verdict: HostedVerdict;
  reason?: string;
  testId?: string;
}) {
  const certainty = verdictCertainty(verdict);
  const style = CERTAINTY_STYLES[certainty];
  const label = verdictLabel(verdict);
  const Icon = VERDICT_ICON[verdict];
  const detail = reason ? `${verdictExplanation(verdict)} Control plane: ${reason}.` : verdictExplanation(verdict);
  return (
    <Tooltip content={detail}>
      <span
        className={cn(
          "inline-flex shrink-0 items-center gap-1 rounded px-1.5 text-2xs font-medium leading-5 text-ink",
          style.container
        )}
        data-verdict={verdict}
        data-certainty={certainty}
        data-testid={testId}
      >
        <Icon className={cn("h-3 w-3 shrink-0", style.foreground)} aria-hidden="true" />
        {label}
      </span>
    </Tooltip>
  );
}
