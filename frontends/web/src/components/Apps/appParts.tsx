// Copyright (c) 2025 Reliant Labs

/**
 * The pieces every apps surface shares — the Apps page, the sidebar, the home
 * view and a chat's deploy card — so a live link, an environment's health and
 * its last deploy read the same wherever an app appears.
 */

import { ExternalLink } from "lucide-react";

import Status, { type StatusTone } from "@/components/forge-ui/status";
import { formatAbsoluteTime, formatRelativeTime } from "@/lib/relativeTime";
import { cn } from "@/lib/utils";
import type { AppEnv, AppLink } from "@/services/forge/apps";
import { verdictLabel, type HostedVerdict } from "@/services/forge/topology";

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

export function EnvStatus({ env, size = "sm" }: { env: AppEnv; size?: "sm" | "md" }) {
  if (env.loading) {
    return <span className="inline-block h-3 w-14 animate-pulse rounded bg-muted motion-reduce:animate-none" aria-label="Loading status" />;
  }
  return <Status tone={verdictTone(env.verdict)} label={verdictLabel(env.verdict)} size={size} />;
}

/**
 * A live URL, opened in a new tab. The host is the text: the scheme is noise,
 * and the platform's word-pair hostnames are how a user recognises their app.
 */
export function LiveLink({
  link,
  className,
  showKind = false,
}: {
  link: AppLink;
  className?: string;
  /** Prefix "API" for a backend URL, where a frontend sits beside it. */
  showKind?: boolean;
}) {
  return (
    <a
      href={link.url}
      target="_blank"
      rel="noreferrer"
      title={`Open ${link.workload || link.host} (${link.url})`}
      onClick={(event) => event.stopPropagation()}
      className={cn(
        "inline-flex min-w-0 max-w-full items-center gap-1 rounded text-sm text-primary underline-offset-2 hover:underline",
        "focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40",
        link.kind === "api" && "text-muted-foreground hover:text-foreground",
        className
      )}
      data-testid="app-live-link"
      data-link-kind={link.kind}
    >
      {showKind && link.kind === "api" && <span className="shrink-0 text-xs font-medium uppercase tracking-wide">API</span>}
      <span className="min-w-0 truncate">{link.host}</span>
      <ExternalLink className="h-3 w-3 shrink-0" aria-hidden="true" />
    </a>
  );
}

/** "v1.4.0 · 12 minutes ago", or what stands in for it before the first deploy. */
export function LastDeploy({ env }: { env: AppEnv }) {
  if (env.loading) return null;
  if (!env.release) return <span className="text-xs text-muted-foreground">Not deployed yet</span>;
  return (
    <span className="text-xs text-muted-foreground" title={env.deployedAt ? `Deployed ${formatAbsoluteTime(env.deployedAt)}` : undefined}>
      <span className="font-medium text-foreground/80">{env.release}</span>
      {env.deployedAt && <> · deployed {formatRelativeTime(env.deployedAt)}</>}
    </span>
  );
}

/**
 * One environment as a row: its name and health, its live links, and its last
 * deploy. The links are the point of the row, so they take the width.
 */
export function AppEnvRow({ env, compact = false }: { env: AppEnv; compact?: boolean }) {
  return (
    <div
      className={cn("grid items-center gap-x-4 gap-y-1", compact ? "grid-cols-[4.5rem_minmax(0,1fr)]" : "grid-cols-[6rem_minmax(0,1fr)_auto] max-sm:grid-cols-[5rem_minmax(0,1fr)]")}
      data-testid={`app-env-${env.name}`}
    >
      <div className="flex min-w-0 flex-col gap-0.5">
        <span className="truncate text-sm font-medium text-foreground">{env.name}</span>
        {!compact && <EnvStatus env={env} />}
      </div>
      <div className="flex min-w-0 flex-wrap items-center gap-x-4 gap-y-1">
        {env.links.length > 0 ? (
          env.links.map((link) => <LiveLink key={link.url} link={link} showKind={env.links.length > 1} />)
        ) : env.loading ? (
          <span className="inline-block h-3 w-40 animate-pulse rounded bg-muted motion-reduce:animate-none" aria-hidden="true" />
        ) : (
          <span className="text-sm text-muted-foreground">No public URL</span>
        )}
      </div>
      {!compact && (
        <div className="justify-self-end max-sm:col-span-2 max-sm:justify-self-start">
          <LastDeploy env={env} />
        </div>
      )}
    </div>
  );
}
