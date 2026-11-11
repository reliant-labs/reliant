// Copyright (c) 2025 Reliant Labs

/**
 * The pieces every apps surface shares — the Apps page, the sidebar and a
 * chat's deploy card — so a live link and an environment's health read the
 * same wherever an app appears.
 */

import { ExternalLink } from "lucide-react";

import type { StatusTone } from "@/components/forge-ui/status";
import { cn } from "@/lib/utils";
import type { AppLink } from "@/services/forge/apps";
import { verdictLabel, type HostedVerdict } from "@/services/forge/topology";

/**
 * An environment's health on the Apps list: the control plane's rollout
 * verdict, AND its firing alerts. Converged pods with alerts firing are not
 * "Healthy" — that word is kept for converged with nothing firing; otherwise
 * the alerts lead ("Alerts firing (2)"), in warning, never green. A verdict
 * already worse than that keeps its own word and tone, with the count after.
 * `busy` is the spinner instead of the dot: settling with nothing firing.
 * Firing alerts outrank a rollout in flight, so they keep their warning dot.
 */
export function envHealth(
  verdict: HostedVerdict,
  firing: number
): { label: string; tone: StatusTone; alerting: boolean; busy: boolean } {
  if (firing <= 0) return { label: verdictLabel(verdict), tone: verdictTone(verdict), alerting: false, busy: verdictBusy(verdict) };
  const alerts = `Alerts firing (${firing})`;
  const tone = verdictTone(verdict);
  if (tone === "danger") return { label: `${verdictLabel(verdict)} · ${alerts}`, tone, alerting: true, busy: false };
  return { label: alerts, tone: "warning", alerting: true, busy: false };
}

/**
 * Whether a verdict is a change in flight — shown as a spinner, not a dot.
 * Converging ("Settling") is a rollout that has not finished or not yet held
 * steady: neither good nor bad, so it gets no colour, only motion.
 */
export function verdictBusy(verdict: HostedVerdict): boolean {
  return verdict === "converging";
}

/**
 * The spinner rule with a second signal: a deploy run the surface knows is in
 * progress for this environment. A deploy in flight spins whatever the
 * verdict, except one already known-bad — red stays for genuinely diverged or
 * not serving, so a deploy cannot hide a broken environment behind motion.
 */
export function inFlight(verdict: HostedVerdict, deploying: boolean): boolean {
  return verdictBusy(verdict) || (deploying && verdictTone(verdict) !== "danger");
}

export function verdictTone(verdict: HostedVerdict): StatusTone {
  switch (verdict) {
    case "converged":
      return "success";
    case "converging":
      // In flight: the spinner (verdictBusy) carries it, in no colour.
      return "neutral";
    case "diverged":
    case "degraded":
      return "danger";
    default:
      return "neutral";
  }
}

/**
 * A live site, opened in a new tab. The host is the text: the scheme is noise,
 * and the host — the user's own domain, or the platform's word-pair hostname
 * until that is live — is how a user recognises their app.
 */
export function LiveLink({ link, className }: { link: AppLink; className?: string }) {
  return (
    <a
      href={link.url}
      target="_blank"
      rel="noreferrer"
      title={`Open ${link.workload || link.host} (${link.url})`}
      onClick={(event) => event.stopPropagation()}
      className={cn(
        "no-color inline-flex min-w-0 max-w-full items-center gap-1 rounded text-sm text-primary-ink underline-offset-2 hover:underline",
        "focus:outline-none focus-visible:ring-2 focus-visible:ring-ring",
        className
      )}
      data-testid="app-live-link"
      data-custom-domain={link.custom ? "true" : undefined}
    >
      <span className="min-w-0 truncate">{link.host}</span>
      <ExternalLink className="h-3 w-3 shrink-0" aria-hidden="true" />
    </a>
  );
}
