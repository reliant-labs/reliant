// Copyright (c) 2025 Reliant Labs

/**
 * The home view's apps panel: under the new-chat controls, what this project
 * has shipped and where it is live — or, before anything is, the way to
 * build an app.
 *
 * Scoped to the current project's app (its forge project name), because the
 * home view is a project's: the other apps are a click away on the Apps page,
 * whose link sits beside this one.
 */

import { ArrowRight, Rocket } from "lucide-react";
import { useNavigate } from "@tanstack/react-router";

import { useApps } from "@/hooks/apps-queries";
import { cn } from "@/lib/utils";
import { isLive } from "@/services/forge/apps";
import { useProjectStore } from "@/store/projectStore";

import { consolePath } from "../Forge/Console/paths";
import { AppEnvRow } from "./appParts";
import { BUILD_APP_PROMPT, deployAppPrompt } from "./appStart";

/** `onPrefill` puts the text in the composer; the user sends it. */
export function HomeApps({ onPrefill }: { onPrefill: (text: string) => void }) {
  const navigate = useNavigate();
  const forgeName = useProjectStore((state) => (state.currentProject?.forge_project_name ?? "").trim());
  const { apps, availability } = useApps();
  const app = forgeName ? apps.find((candidate) => candidate.name === forgeName) : undefined;
  const otherLive = apps.filter((candidate) => candidate.name !== forgeName && isLive(candidate)).length;

  if (app && isLive(app)) {
    return (
      <section
        className="w-full rounded-lg border border-border bg-card px-4 py-3 text-left"
        aria-label={`${app.name} is live`}
        data-testid="home-apps"
        data-state="live"
      >
        <div className="mb-2 flex items-center justify-between gap-2">
          <div className="flex min-w-0 items-center gap-2">
            <span className="rounded bg-success/15 px-1.5 py-0.5 text-2xs font-medium uppercase tracking-wider text-success-ink">Live</span>
            <span className="truncate text-sm font-semibold text-foreground">{app.name}</span>
          </div>
          <div className="flex shrink-0 items-center gap-3 text-xs">
            {otherLive > 0 && (
              <button type="button" onClick={() => void navigate({ to: "/apps" })} className={LINK}>
                {otherLive} more live
              </button>
            )}
            <button
              type="button"
              onClick={() => void navigate({ href: consolePath(app.name, { kind: "overview" }) })}
              className={cn(LINK, "inline-flex items-center gap-1")}
              data-testid="home-apps-console"
            >
              Console <ArrowRight className="h-3 w-3" aria-hidden="true" />
            </button>
          </div>
        </div>
        <div className="space-y-1.5">
          {app.envs
            .filter((env) => env.links.length > 0)
            .map((env) => (
              <AppEnvRow key={env.id} env={env} compact />
            ))}
        </div>
      </section>
    );
  }

  // Its deployments could not be read: whether it is live is unknown, so offer nothing rather than a deploy it may not need.
  if (app && availability !== "available") return null;
  // A forge project that is not live yet is one deploy away; anything else starts from an idea.
  const deploy = !!app;
  return (
    <button
      type="button"
      onClick={() => onPrefill(deploy ? deployAppPrompt(app.name) : BUILD_APP_PROMPT)}
      className={cn(
        "group flex w-full items-center gap-3 rounded-lg border border-dashed border-border bg-card/60 px-4 py-3 text-left transition-colors",
        "hover:border-primary/50 hover:bg-card focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
      )}
      data-testid="home-apps"
      data-state={deploy ? "deploy" : "build"}
    >
      <span className="flex h-8 w-8 shrink-0 items-center justify-center rounded-md bg-primary/10 text-primary">
        <Rocket className="h-4 w-4" aria-hidden="true" />
      </span>
      <span className="min-w-0 flex-1">
        <span className="block text-sm font-medium text-foreground">{deploy ? `Deploy ${app.name}` : "Build an app"}</span>
        <span className="block text-xs text-muted-foreground">
          {deploy
            ? "Put it on Reliant hosting and get a live URL for each environment."
            : "Describe it. Reliant builds it with forge and deploys it to a live URL."}
        </span>
      </span>
      <ArrowRight className="h-4 w-4 shrink-0 text-muted-foreground transition-transform group-hover:translate-x-0.5" aria-hidden="true" />
    </button>
  );
}

const LINK = "rounded text-muted-foreground hover:text-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40";
