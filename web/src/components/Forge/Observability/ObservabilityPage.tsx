// Copyright (c) 2025 Reliant Labs

/**
 * Route component for /forge/observability — the project's Observability area.
 *
 * Environment is a filter here, not a place (owner decision 1): the views read
 * every environment of the project the caller can see unless `envs` narrows
 * them, and "group by Environment" lays them side by side. Each env page's
 * Observability tab is the same panel pinned to one environment, a deep link
 * into this area.
 *
 * The environments come from the same roster the sidebar and the Overview
 * read, and only the ones the control plane records count: telemetry is filed
 * under the control plane's environment id, so an environment only the
 * checkout declares has nothing filed under it.
 *
 * Phase 2 adds a Delivery view (releases, DORA) to the panel's tabs; nothing
 * here changes for it.
 */

import { lazy, Suspense, useCallback, useMemo } from "react";
import { useNavigate, useSearch } from "@tanstack/react-router";
import { Activity } from "lucide-react";

import Card from "@/components/forge-ui/card";
import EmptyState from "@/components/forge-ui/empty_state";
import PageHeader from "@/components/forge-ui/page_header";
import SkeletonLoader from "@/components/forge-ui/skeleton_loader";
import { useForgeRoster } from "@/hooks/forge-queries";
import { isChunkLoadError, staleChunkModule } from "@/lib/lazyRoute";
import { useProjectStore } from "@/store/projectStore";

import { forgeScopeOf } from "../forgeScope";
import { CloudNotice } from "../SourceNotices";
import type { ObsNavigate } from "./obsSearch";

// Its own chunk, as on the env page: uPlot and the views load only here.
const ObservabilityPanel = lazy(() =>
  import("./ObservabilityPanel").catch((error: unknown) => {
    if (!isChunkLoadError(error)) throw error;
    return staleChunkModule(error, "default") as unknown as typeof import("./ObservabilityPanel");
  })
);

export function ForgeObservabilityPage() {
  const navigate = useNavigate();
  const { project: projectParam, forgeProject: forgeProjectParam, ...obsSearch } = useSearch({
    from: "/_authenticated/_forge/forge/observability",
  });
  const currentProject = useProjectStore((state) => state.currentProject);
  const scope = forgeScopeOf({ project: projectParam, forgeProject: forgeProjectParam }, currentProject?.id);
  const roster = useForgeRoster(scope.projectId, scope.forgeProject);
  const project = roster.projectName.name;

  const environments = useMemo(
    () => roster.envs.flatMap((env) => (env.live ? [{ id: env.live.id, name: env.name }] : [])),
    [roster.envs]
  );

  // Filter changes replace the history entry; opening a trace or switching view pushes one.
  const navigateObs = useCallback<ObsNavigate>(
    (next, options) => {
      void navigate({
        to: ".",
        search: (prev: Record<string, unknown>) => ({ ...prev, ...next }),
        replace: options?.replace,
      });
    },
    [navigate]
  );

  const header = (
    <PageHeader
      title="Observability"
      subtitle={
        project ? (
          <>
            Logs, traces, dashboards and workloads across every environment of <span className="font-mono">{project}</span>.
            Narrow to some, or group by environment to compare them.
          </>
        ) : (
          "Logs, traces, dashboards and workloads across this project's environments."
        )
      }
    />
  );

  const loading = (
    <Card padding="none" aria-label="Loading" aria-busy="true">
      <SkeletonLoader variant="table-row" count={4} />
    </Card>
  );

  if (roster.isLoading || roster.resolvingName) {
    return (
      <div className="space-y-6" data-testid="forge-observability-loading">
        {header}
        {loading}
      </div>
    );
  }

  return (
    <div className="space-y-6" data-testid="forge-observability">
      {header}
      <CloudNotice availability={roster.live.data?.availability} detail={roster.live.data?.detail} />
      {!project || environments.length === 0 ? (
        <div data-testid="forge-observability-no-envs">
          <EmptyState
            icon={<Activity className="h-6 w-6" aria-hidden="true" />}
            title="No environments to observe yet"
            description="Telemetry is filed under the environments Reliant records. Deploy one through Reliant (forge env deploy <env>) and its logs, traces, metrics and workloads appear here."
          />
        </div>
      ) : (
        <Suspense fallback={loading}>
          <ObservabilityPanel place={{ kind: "project", project, environments }} search={obsSearch} navigate={navigateObs} />
        </Suspense>
      )}
    </div>
  );
}
