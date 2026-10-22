// Copyright (c) 2025 Reliant Labs

/**
 * One Observability view, hosted as a console page (decision D9).
 *
 * The views (ObservabilityPanel) still speak their own URL vocabulary
 * (obsSearch.ts) — "open Logs for this service", "narrow to these
 * environments" — and this translates it to the console's:
 *
 *   view   is the PAGE now. A view switch from inside a view (a log line's
 *          "open trace", a dashboard panel's "logs") navigates to the page
 *          hosting that view (paths.ts pageOfObsView), carrying the window,
 *          filters and group-by.
 *   envs   is the top-bar scope now: `?env=`, one environment or all. The
 *          panel is told the scope and draws no environment picker of its own,
 *          so there is one environment control on the screen, not two.
 *
 * Every other key is the panel's own and passes through unchanged, so a link
 * from before the move (redirected by paths.ts) opens the same view.
 */

import { lazy, Suspense, useCallback, useMemo } from "react";
import { useNavigate, useSearch } from "@tanstack/react-router";
import { Activity } from "lucide-react";

import Card from "@/components/forge-ui/card";
import EmptyState from "@/components/forge-ui/empty_state";
import SkeletonLoader from "@/components/forge-ui/skeleton_loader";
import { isChunkLoadError, staleChunkModule } from "@/lib/lazyRoute";

import type { ObsNavigate, ObsSearch, ObsView } from "../Observability/obsSearch";
import { CloudNotice } from "../SourceNotices";
import { useForgeConsole } from "./consoleContext";
import { ENV_ALL } from "./envScope";
import { carriedObsSearch, consolePath, envScopeOfEnvs, pageOfObsView, withSearch } from "./paths";

// Its own chunk: uPlot and the views load only on a page that shows them.
const ObservabilityPanel = lazy(() =>
  import("../Observability/ObservabilityPanel").catch((error: unknown) => {
    if (!isChunkLoadError(error)) throw error;
    return staleChunkModule(error, "default") as unknown as typeof import("../Observability/ObservabilityPanel");
  })
);

export interface ObservabilityHostProps {
  view: ObsView;
  /**
   * Whether the view needs a recorded environment to mean anything. Usage is
   * the organization's allowance, readable before anything is deployed.
   */
  needsEnvironments?: boolean;
}

export function ObservabilityHost({ view, needsEnvironments = true }: ObservabilityHostProps) {
  const { forgeProject, env, roster, page } = useForgeConsole();
  const navigate = useNavigate();
  const search = useSearch({ strict: false }) as Record<string, unknown>;

  const environments = useMemo(
    () =>
      roster.envs.flatMap((candidate) =>
        candidate.live ? [{ id: candidate.live.id, name: candidate.name, declared: candidate.live.declaredShape }] : []
      ),
    [roster.envs]
  );

  const obsSearch = useMemo<ObsSearch>(
    () => ({ ...(carriedObsSearch(search) as ObsSearch), view, envs: env === ENV_ALL ? undefined : [env] }),
    [search, view, env]
  );

  const navigateObs = useCallback<ObsNavigate>(
    (next, options) => {
      const { view: nextView, envs: nextEnvs, ...rest } = next;
      const scoped = "envs" in next ? { env: envScopeOfEnvs(nextEnvs) } : {};
      const target = "view" in next ? pageOfObsView(nextView) : page;
      if (target.kind !== page.kind || (target.kind === "obs-view" && page.kind === "obs-view" && target.view !== page.view)) {
        void navigate({
          href: withSearch(consolePath(forgeProject, target), { ...carriedObsSearch(search), ...rest, env, ...scoped }),
          replace: options?.replace,
        });
        return;
      }
      void navigate({
        to: ".",
        search: (prev: Record<string, unknown>) => ({ ...prev, ...rest, ...scoped }),
        replace: options?.replace,
      });
    },
    [navigate, forgeProject, page, search, env]
  );

  const loading = (
    <Card padding="none" aria-label="Loading" aria-busy="true">
      <SkeletonLoader variant="table-row" count={4} />
    </Card>
  );

  if (roster.isLoading || roster.resolvingName) return <div data-testid="obs-host-loading">{loading}</div>;

  // A scope naming an environment with no telemetry filed under it must not
  // fall through to "every environment" (an empty id list means all of them).
  const unrecorded = needsEnvironments && env !== ENV_ALL && !environments.some((candidate) => candidate.name === env);

  return (
    <div className="space-y-4" data-testid="obs-host" data-view={view}>
      <CloudNotice availability={roster.live.data?.availability} detail={roster.live.data?.detail} />
      {unrecorded && environments.length > 0 ? (
        <p
          data-testid="obs-unrecorded-env"
          className="rounded-lg border border-dashed border-border bg-surface px-4 py-5 text-sm text-ink-muted"
        >
          Reliant has no record of <span className="font-medium text-ink">{env}</span>, so no telemetry is filed under
          it. Once it is deployed through Reliant, its logs, traces and metrics appear here.
        </p>
      ) : needsEnvironments && environments.length === 0 ? (
        <div data-testid="forge-observability-no-envs">
          <EmptyState
            icon={<Activity className="h-6 w-6" aria-hidden="true" />}
            title="No environments to observe yet"
            description="Telemetry is filed under the environments Reliant records. Deploy one through Reliant (forge env deploy <env>) and its logs, traces, metrics and workloads appear here."
          />
        </div>
      ) : (
        <Suspense fallback={loading}>
          <ObservabilityPanel
            place={{ kind: "project", project: forgeProject, environments }}
            search={obsSearch}
            navigate={navigateObs}
            chrome="console"
          />
        </Suspense>
      )}
    </div>
  );
}
