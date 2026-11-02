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
 */
export function envHealth(verdict: HostedVerdict, firing: number): { label: string; tone: StatusTone; alerting: boolean } {
  if (firing <= 0) return { label: verdictLabel(verdict), tone: verdictTone(verdict), alerting: false };
  const alerts = `Alerts firing (${firing})`;
  const tone = verdictTone(verdict);
  if (tone === "danger") return { label: `${verdictLabel(verdict)} · ${alerts}`, tone, alerting: true };
  return { label: alerts, tone: "warning", alerting: true };
}

export function verdictTone(verdict: HostedVerdict): StatusTone {
  switch (verdict) {
    case "converged":
      return "success";
    case "converging":
      return "warning";
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
