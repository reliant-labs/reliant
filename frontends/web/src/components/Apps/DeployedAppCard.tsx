// Copyright (c) 2025 Reliant Labs

/**
 * What a deploy in a chat produced: under the shell call that ran
 * `forge env deploy <env>`, the app's live links in that environment and the
 * way into the app. Shown whether or not the call is expanded — the URL is the
 * outcome the user asked for, not a detail of the tool call.
 *
 * The environment comes from the command (deployedEnvOf); the app is the
 * chat's project's forge project; the URLs come from the control plane's
 * status of that environment. Nothing is parsed out of forge's output.
 */

import { ArrowRight, Clock, Rocket } from "lucide-react";
import { useNavigate } from "@tanstack/react-router";

import { useCloudEnvironments, useCloudEnvStatus } from "@/hooks/forge-queries";
import { cn } from "@/lib/utils";
import { appLinksOf, deployedEnvOf } from "@/services/forge/apps";
import { useProjectStore } from "@/store/projectStore";

import { consolePath } from "../Forge/Console/paths";
import { LiveLink } from "./appParts";

/** forge's exit status for a deploy queued on billing: it goes out once billing is set up. */
const EXIT_QUEUED = 7;

export interface ShellDeploy {
  env: string;
  queued: boolean;
}

/**
 * The deploy a finished shell call made, or null. Only a call that succeeded
 * (or was queued on billing) made one: a failed deploy produced nothing to
 * link to, and a backgrounded one has not finished.
 */
export function deployOfShellCall(
  input: Record<string, unknown> | string | undefined,
  result: { content: string; is_error?: boolean } | undefined
): ShellDeploy | null {
  if (!result || result.is_error) return null;
  const command = commandOf(input);
  const env = command ? deployedEnvOf(command) : null;
  if (!env) return null;
  const exit = exitCodeOf(result.content);
  if (exit === "background") return null;
  if (exit === EXIT_QUEUED) return { env, queued: true };
  if (exit !== 0 && exit !== null) return null;
  return { env, queued: false };
}

/** The command line, from an input object or its JSON — nested once, as older calls recorded it. */
function commandOf(input: Record<string, unknown> | string | undefined): string {
  if (!input) return "";
  if (typeof input === "object") return typeof input.command === "string" ? commandOf(input.command) : "";
  if (!input.trim().startsWith("{")) return input;
  try {
    const parsed = JSON.parse(input) as { command?: unknown };
    return typeof parsed.command === "string" ? parsed.command : input;
  } catch {
    return input;
  }
}

/** The structured shell result's exit code; null for a plain-text result, which carries none. */
function exitCodeOf(content: string): number | null | "background" {
  if (!content.startsWith("{")) return null;
  try {
    const parsed = JSON.parse(content) as { exit_code?: unknown; backgrounded?: unknown };
    if (parsed.backgrounded) return "background";
    return typeof parsed.exit_code === "number" ? parsed.exit_code : null;
  } catch {
    return null;
  }
}

export function DeployedAppCard({ deploy }: { deploy: ShellDeploy }) {
  const navigate = useNavigate();
  const app = useProjectStore((state) => (state.currentProject?.forge_project_name ?? "").trim());
  const envs = useCloudEnvironments(app || null);
  const env = envs.data?.envs.find((candidate) => candidate.name === deploy.env) ?? null;
  const status = useCloudEnvStatus(env?.id ?? null);
  const links = status.data ? appLinksOf(status.data.workloads) : [];
  const loading = !!app && (envs.isLoading || (!!env && status.isLoading));

  const target = app ? consolePath(app, { kind: "overview" }) : "/apps";
  const Icon = deploy.queued ? Clock : Rocket;

  return (
    <div
      className="flex flex-wrap items-center gap-x-3 gap-y-1.5 border-t border-border/50 bg-background px-2.5 py-2"
      data-testid="deployed-app-card"
      data-env={deploy.env}
      data-state={deploy.queued ? "queued" : "deployed"}
    >
      <span className="flex min-w-0 items-center gap-1.5 text-xs font-medium text-foreground">
        <Icon className={cn("h-3.5 w-3.5 shrink-0", deploy.queued ? "text-warning-ink" : "text-success-ink")} aria-hidden="true" />
        <span className="truncate">
          {deploy.queued
            ? `Deploy to ${deploy.env} queued — it goes live once billing is set up`
            : app
              ? `${app} is deployed to ${deploy.env}`
              : `Deployed to ${deploy.env}`}
        </span>
      </span>
      {!deploy.queued && (
        <span className="flex min-w-0 flex-wrap items-center gap-x-3 gap-y-1">
          {links.map((link) => (
            <LiveLink key={link.url} link={link} className="text-xs" />
          ))}
          {loading && <span className="inline-block h-3 w-36 animate-pulse rounded bg-muted motion-reduce:animate-none" aria-hidden="true" />}
        </span>
      )}
      <button
        type="button"
        onClick={() => void navigate({ href: target })}
        className="ml-auto inline-flex shrink-0 items-center gap-1 rounded text-xs text-muted-foreground hover:text-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
        data-testid="deployed-app-open"
      >
        {app ? "View app" : "Apps"} <ArrowRight className="h-3 w-3" aria-hidden="true" />
      </button>
    </div>
  );
}
