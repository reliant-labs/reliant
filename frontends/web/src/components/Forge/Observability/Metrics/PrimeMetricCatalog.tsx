// Copyright (c) 2025 Reliant Labs

import { useSearch } from "@tanstack/react-router";

import { useMetricCatalog } from "@/hooks/observability-queries";

import type { ForgeConsole } from "../../Console/consoleContext";
import { ENV_ALL } from "../../Console/envScope";
import { useMetricSearchScope } from "./metricSearchScope";

/** Start the same catalog query the lazy explorer uses, without waiting for its UI. */
export function PrimeMetricCatalog({ orgId, forgeProject, env, roster }: Pick<ForgeConsole, "forgeProject" | "env" | "roster"> & { orgId: string | null }) {
  const search = useSearch({ strict: false }) as Record<string, unknown>;
  const { range, scopeFilters } = useMetricSearchScope(search);

  let envIds: string[] | null = null;
  if (env === ENV_ALL) {
    // All is already scoped to this project, even before GetLiveView answers.
    if (roster.isLoading || roster.envs.some((candidate) => candidate.live)) envIds = [];
  } else {
    // Never substitute a project-wide read for a named environment. The UI may
    // need checkout data to determine the lifecycle of an unknown kind.
    const recorded = roster.envs.find((candidate) => candidate.name === env);
    if (recorded?.lifecycle === "deployed" && recorded.live?.id && recorded.live.kind !== "unknown") envIds = [recorded.live.id];
  }
  useMetricCatalog(orgId && forgeProject && envIds ? { orgId, projectId: forgeProject, envIds, filters: scopeFilters } : null, range);
  return null;
}
