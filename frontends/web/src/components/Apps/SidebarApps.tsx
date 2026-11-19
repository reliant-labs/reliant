// Copyright (c) 2025 Reliant Labs

/**
 * The live apps, under the sidebar's Apps entry: a name that opens the app's
 * console and the link to the live site itself, one click from anywhere in
 * the app. Absent until something is live, so a user who has deployed
 * nothing sees the plain Apps entry and no empty section.
 */

import { ExternalLink } from "lucide-react";
import { useNavigate } from "@tanstack/react-router";

import Status from "@/components/forge-ui/status";
import Tooltip from "@/components/forge-ui/tooltip";
import { useApps } from "@/hooks/apps-queries";
import { cn } from "@/lib/utils";
import { isLive, primaryLink } from "@/services/forge/apps";

import { consolePath } from "../Forge/Console/paths";
import { verdictBusy, verdictTone } from "./appParts";

/** Enough to be a shortcut; the Apps page holds the rest. */
const MAX_LISTED = 3;

export function SidebarApps() {
  const navigate = useNavigate();
  const { apps } = useApps();
  const live = apps.filter(isLive);
  if (live.length === 0) return null;
  const listed = live.slice(0, MAX_LISTED);
  const more = live.length - listed.length;

  return (
    <ul className="space-y-0.5" aria-label="Live apps" data-testid="sidebar-apps">
      {listed.map((app) => {
        const link = primaryLink(app)!;
        // The dot is the health of the environment the link opens, not of the app at large.
        const env = app.envs.find((candidate) => candidate.links.includes(link));
        const tone = env ? verdictTone(env.verdict) : "neutral";
        // Mid-rollout the dot is a spinner, so a deploy never reads as a fault.
        const busy = env ? verdictBusy(env.verdict) : false;
        return (
          // A sub-row under Apps, shaped like a chat row (restyle §11.3): the
          // status dot in the 16px slot, the name, the site's link on hover.
          <li
            key={app.name}
            className="group/app flex h-[var(--row-h)] items-center rounded-md pr-1 text-ui transition-colors hover:bg-foreground/5"
          >
            <button
              type="button"
              onClick={() => void navigate({ href: consolePath(app.name, { kind: "overview" }) })}
              className="flex h-full min-w-0 flex-1 items-center gap-2 rounded-md pl-8 text-left text-foreground/80 hover:text-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring"
              data-testid={`sidebar-app-${app.name}`}
            >
              <span className="flex w-4 shrink-0 items-center justify-center">
                <Status tone={tone} size="md" busy={busy} />
              </span>
              <span className="min-w-0 truncate">{app.name}</span>
            </button>
            <Tooltip
              content={`Open ${link.host}`}
              side="right"
              delay={300}
              triggerClassName="hidden group-hover/app:inline-flex group-focus-within/app:inline-flex"
            >
              <a
                href={link.url}
                target="_blank"
                rel="noreferrer"
                aria-label={`Open ${app.name} at ${link.host}`}
                className={cn(
                  "inline-flex size-6 shrink-0 items-center justify-center rounded-md text-muted-foreground hover:bg-foreground/5 hover:text-foreground",
                  "focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
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
            className="flex h-6 items-center rounded-md px-2 text-xs text-muted-foreground hover:text-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            {more} more
          </button>
        </li>
      )}
    </ul>
  );
}
