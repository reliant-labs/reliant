// Copyright (c) 2025 Reliant Labs

/**
 * The live apps, under the sidebar's Apps entry: a name that opens the app's
 * console and the link to the live site itself, one click from anywhere in
 * the app. Absent until something is live, so a user who has deployed
 * nothing sees the plain Apps entry and no empty section.
 */

import { ExternalLink } from "lucide-react";
import { useNavigate } from "@tanstack/react-router";

import type { StatusTone } from "@/components/forge-ui/status";
import Tooltip from "@/components/forge-ui/tooltip";
import { useApps } from "@/hooks/apps-queries";
import { cn } from "@/lib/utils";
import { isLive, primaryLink } from "@/services/forge/apps";

import { consolePath } from "../Forge/Console/paths";
import { verdictTone } from "./appParts";

/** Enough to be a shortcut; the Apps page holds the rest. */
const MAX_LISTED = 3;

const DOT: Record<StatusTone, string> = {
  success: "bg-success",
  warning: "bg-warning",
  danger: "bg-destructive",
  neutral: "bg-muted-foreground/50",
};

export function SidebarApps() {
  const navigate = useNavigate();
  const { apps } = useApps();
  const live = apps.filter(isLive);
  if (live.length === 0) return null;
  const listed = live.slice(0, MAX_LISTED);
  const more = live.length - listed.length;

  return (
    <ul className="space-y-0.5 pb-1 pl-6" aria-label="Live apps" data-testid="sidebar-apps">
      {listed.map((app) => {
        const link = primaryLink(app)!;
        // The dot is the health of the environment the link opens, not of the app at large.
        const env = app.envs.find((candidate) => candidate.links.includes(link));
        const tone = env ? verdictTone(env.verdict) : "neutral";
        return (
          <li key={app.name} className="group flex h-7 items-center gap-1 rounded-md pr-1 hover:bg-muted/50">
            <button
              type="button"
              onClick={() => void navigate({ href: consolePath(app.name, { kind: "overview" }) })}
              className="flex min-w-0 flex-1 items-center gap-2 rounded-md px-2 text-left text-sm text-foreground/85 hover:text-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
              data-testid={`sidebar-app-${app.name}`}
            >
              <span className={cn("h-1.5 w-1.5 shrink-0 rounded-full", DOT[tone])} aria-hidden="true" />
              <span className="min-w-0 truncate">{app.name}</span>
            </button>
            <Tooltip content={`Open ${link.host}`} side="right" delay={300} triggerClassName="inline-flex">
              <a
                href={link.url}
                target="_blank"
                rel="noreferrer"
                aria-label={`Open ${app.name} at ${link.host}`}
                className={cn(
                  "inline-flex h-6 w-6 shrink-0 items-center justify-center rounded text-muted-foreground hover:bg-muted hover:text-foreground",
                  "focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
                )}
                data-testid={`sidebar-app-link-${app.name}`}
              >
                <ExternalLink className="h-3.5 w-3.5" aria-hidden="true" />
              </a>
            </Tooltip>
          </li>
        );
      })}
      {more > 0 && (
        <li>
          <button
            type="button"
            onClick={() => void navigate({ to: "/apps" })}
            className="flex h-6 items-center rounded-md px-2 text-xs text-muted-foreground hover:text-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
          >
            {more} more
          </button>
        </li>
      )}
    </ul>
  );
}
