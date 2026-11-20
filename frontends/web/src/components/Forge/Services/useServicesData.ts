// Copyright (c) 2026 Reliant Labs

/** The reads the Services list and a service's page share (prototype). */

import { useMemo } from "react";

import { useObservabilityOrg, useServiceOverview, useWorkloads } from "@/hooks/observability-queries";
import type { ObsScope } from "@/services/controlPlane/observability/types";
import type { RangePreset } from "@/services/controlPlane/observability/window";

import { useForgeConsole } from "../Console/consoleContext";
import { ENV_ALL } from "../Console/envScope";
import { ENV_KEY } from "../Observability/attributes";
import { useHostedByEnv, useStripEnvs } from "../Workloads/WorkloadPages";
import { servicesRows } from "./servicesModel";

export interface ServicesSearch {
  range?: RangePreset;
  metric?: string;
}

/** The reads both pages share: the environments, GetStatus, the workloads and the request side, cut to the scope. */
export function useServicesData(range: RangePreset) {
  const { forgeProject, env } = useForgeConsole();
  const strip = useStripEnvs();
  const { hosted, loading: hostedLoading } = useHostedByEnv(strip.envs);
  const org = useObservabilityOrg();
  const orgId = org.orgId;
  const scopedEnv = env === ENV_ALL ? null : (strip.envs.find((candidate) => candidate.name === env) ?? null);
  const projectScope = useMemo<ObsScope | null>(() => (orgId ? { orgId, projectId: forgeProject, envIds: [], filters: [] } : null), [orgId, forgeProject]);
  const redScope = useMemo<ObsScope | null>(
    () => (orgId ? { orgId, projectId: forgeProject, envIds: scopedEnv ? [scopedEnv.id] : [], filters: [] } : null),
    [orgId, forgeProject, scopedEnv]
  );
  const listed = useWorkloads(projectScope, range);
  const redGroupBy = useMemo(() => (scopedEnv ? [] : [ENV_KEY]), [scopedEnv]);
  const red = useServiceOverview(redScope, range, [], redGroupBy);
  const rows = useMemo(
    () =>
      servicesRows({
        workloads: listed.data?.workloads ?? [],
        jobs: listed.data?.jobs ?? [],
        telemetry: red.data?.services ?? [],
        hosted,
        envs: strip.envs,
        env,
      }),
    [listed.data, red.data, hosted, strip.envs, env]
  );
  return { env, envs: strip.envs, envsLoading: strip.loading, hosted, hostedLoading, listed, red, rows, orgId };
}

