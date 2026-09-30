// Copyright (c) 2025 Reliant Labs

/**
 * A domain's acquisition state as one chip.
 *
 * Three axes, matching the console's existing certainty vocabulary
 * (stateVocabulary.ts): hue, icon, and — for the converging states — motion.
 * The point of the third axis is the same as the dashed border there: the
 * difference between "we are working on this" and "you need to do something"
 * must survive a greyscale screenshot in an incident channel.
 *
 * The converging trio (pending-dns, verifying, issuing) are deliberately NOT
 * warnings. Nothing is wrong with a domain waiting for DNS propagation; it is
 * the normal first hour of owning one. Colouring it amber would train tenants
 * to ignore amber. Only `failed` and `conflict` are coloured as problems, and
 * they differ from each other because `conflict` is not retryable by waiting.
 */

import { AlertTriangle, CheckCircle2, CircleDashed, Loader2, ShieldAlert } from "lucide-react";
import type { LucideIcon } from "lucide-react";

import { cn } from "@/lib/utils";
import { DOMAIN_STATE_LABELS, type DomainState } from "@/services/forge/domains";

interface StateStyle {
  container: string;
  icon: LucideIcon;
  /** Whether the icon spins — reserved for states the platform is actively working. */
  spin?: boolean;
}

const STATE_STYLES: Record<DomainState, StateStyle> = {
  // Waiting on the TENANT. Neutral and dashed: not an error, but not progress
  // either — nothing moves until they act.
  "pending-dns": {
    container: "bg-transparent border border-dashed border-border text-muted-foreground",
    icon: CircleDashed,
  },
  // Waiting on US. Same neutral hue, but spinning: work is happening.
  verifying: {
    container: "bg-muted/40 border border-solid border-border text-muted-foreground",
    icon: Loader2,
    spin: true,
  },
  issuing: {
    container: "bg-muted/40 border border-solid border-border text-muted-foreground",
    icon: Loader2,
    spin: true,
  },
  live: {
    container: "bg-success/15 border border-solid border-success/40 text-success",
    icon: CheckCircle2,
  },
  failed: {
    container: "bg-destructive/15 border border-solid border-destructive/40 text-destructive",
    icon: AlertTriangle,
  },
  // A different glyph from `failed` on purpose: the fix is a human dispute,
  // not a retry, and a tenant should not read it as "try again".
  conflict: {
    container: "bg-destructive/15 border border-solid border-destructive/40 text-destructive",
    icon: ShieldAlert,
  },
  unknown: {
    container: "bg-transparent border border-dashed border-border text-muted-foreground",
    icon: CircleDashed,
  },
};

export function DomainStateBadge({ state }: { state: DomainState }) {
  const style = STATE_STYLES[state];
  const Icon = style.icon;
  return (
    <span
      className={cn(
        "inline-flex items-center gap-1.5 rounded-full px-2 py-0.5 text-xs font-medium",
        style.container
      )}
      data-testid={`domain-state-${state}`}
    >
      <Icon className={cn("h-3.5 w-3.5", style.spin && "animate-spin")} aria-hidden="true" />
      {DOMAIN_STATE_LABELS[state]}
    </span>
  );
}
