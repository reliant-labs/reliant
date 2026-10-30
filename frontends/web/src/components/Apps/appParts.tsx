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
import type { HostedVerdict } from "@/services/forge/topology";

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
        "no-color inline-flex min-w-0 max-w-full items-center gap-1 rounded text-sm text-primary underline-offset-2 hover:underline",
        "focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40",
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
