// Copyright (c) 2026 Reliant Labs

/**
 * useAppLiveState(app, env, range): THE read behind every live-state view
 * (U6c, owner 2026-11-20) — the Services tab (list and detail), the
 * Overview's live-state strip, and the built-in Live state dashboard. It
 * combines the live view (environments and their releases), ListWorkloads,
 * GetServiceOverview and GetStatus into one model (liveStateModel.ts), so the
 * three places always show the same numbers.
 *
 * It takes the app by name rather than from the console context, so a page
 * of several projects (Services across projects) reads each one the same way.
 * Every read is a shared react-query entry: three views of one app cost one
 * set of requests.
 */

import { useMemo } from "react";

import { useCloudEnvStatuses, useLiveView } from "@/hooks/forge-queries";
import { useObservabilityOrg, useServiceOverview, useWorkload, useWorkloads } from "@/hooks/observability-queries";
import type { ObsScope } from "@/services/controlPlane/observability/types";
import type { RangePreset } from "@/services/controlPlane/observability/window";
import type { ForgeHostedWorkload } from "@/services/forge/topology";

import { ENV_ALL } from "../Console/envScope";
import { ENV_KEY } from "../Observability/attributes";
import { sumSeries } from "../Services/servicesModel";
import { liveStateEnvs, mergeLiveState, type LiveStateEnv, type LiveStateModel } from "./liveStateModel";

export type HostedByEnvName = ReadonlyMap<string, ReadonlyMap<string, ForgeHostedWorkload>>;

export interface AppLiveState extends LiveStateModel {
  app: string;
  env: string;
  range: RangePreset;
  envs: LiveStateEnv[];
  envsLoading: boolean;
  hosted: HostedByEnvName;
  hostedLoading: boolean;
  /** ListWorkloads and GetServiceOverview, for their loading, error and as-of states. */
  listed: ReturnType<typeof useWorkloads>;
  red: ReturnType<typeof useServiceOverview>;
  orgId: string | null;
  /** The scope the request side was read for: the reads a row's own charts make start from it. */
  scope: ObsScope | null;
}

export function useAppLiveState(app: string | null, env: string, range: RangePreset): AppLiveState {
  const org = useObservabilityOrg();
  const orgId = org.orgId ?? null;
  const live = useLiveView(app);
  const envs = useMemo(() => liveStateEnvs(live.data?.envs ?? []), [live.data]);
  const statuses = useCloudEnvStatuses(envs.map((candidate) => candidate.id));
  const statusKey = envs.map((candidate) => `${candidate.id}:${statuses.get(candidate.id)?.data ? 1 : 0}`).join(",");
  const hostedLoading = envs.some((candidate) => statuses.get(candidate.id)?.isLoading);
  const hosted = useMemo<HostedByEnvName>(() => {
    const out = new Map<string, Map<string, ForgeHostedWorkload>>();
    for (const candidate of envs) {
      const workloads = statuses.get(candidate.id)?.data?.workloads ?? [];
      out.set(candidate.name, new Map(workloads.filter((w) => w.name).map((w) => [w.name!, w])));
    }
    return out;
    // `statuses` is a fresh Map per render; `statusKey` changes when any status lands.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [envs, statusKey]);

  const scopedEnv = env === ENV_ALL ? null : (envs.find((candidate) => candidate.name === env) ?? null);
  const projectScope = useMemo<ObsScope | null>(() => (orgId && app ? { orgId, projectId: app, envIds: [], filters: [] } : null), [orgId, app]);
  const scope = useMemo<ObsScope | null>(
    () => (orgId && app ? { orgId, projectId: app, envIds: scopedEnv ? [scopedEnv.id] : [], filters: [] } : null),
    [orgId, app, scopedEnv]
  );
  const listed = useWorkloads(projectScope, range);
  const redGroupBy = useMemo(() => (scopedEnv ? [] : [ENV_KEY]), [scopedEnv]);
  const red = useServiceOverview(scope, range, [], redGroupBy);

  const model = useMemo(
    () =>
      mergeLiveState({
        envs,
        env,
        workloads: listed.data?.workloads ?? [],
        jobs: listed.data?.jobs ?? [],
        telemetry: red.data?.services ?? [],
        hosted,
      }),
    [envs, env, listed.data, red.data, hosted]
  );

  return { ...model, app: app ?? "", env, range, envs, envsLoading: live.isLoading, hosted, hostedLoading, listed, red, orgId, scope };
}

/**
 * One workload's CPU and memory over the window, summed over its containers
 * (GetWorkload): the live-state strip's sparklines and the dashboard's charts.
 * Shares its cache entry with the service's page.
 */
export function useWorkloadUsage(scope: ObsScope | null, range: RangePreset, name: string, envId: string | undefined) {
  const detail = useWorkload(scope, range, name);
  return useMemo(() => {
    const series = (detail.data?.series ?? []).filter((s) => !envId || s.envId === envId);
    return {
      cpu: detail.data ? sumSeries(series.filter((s) => s.metric === "cpu_usage")) : null,
      memory: detail.data ? sumSeries(series.filter((s) => s.metric === "memory_working_set")) : null,
      pods: detail.data?.workloads.find((w) => !envId || w.envId === envId)?.pods ?? [],
      loading: detail.isLoading,
    };
  }, [detail.data, detail.isLoading, envId]);
}
