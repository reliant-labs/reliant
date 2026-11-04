// Copyright (c) 2025 Reliant Labs

/**
 * The Apps list as a query: every hosted environment in the org (one
 * ListEnvironments), each one's status (the same per-environment GetStatus
 * entries the console's Overview reads, so the two share a cache), and the
 * Reliant projects that are forge projects. services/forge/apps.ts turns them
 * into one list.
 *
 * A failure of the environment read resolves to an `availability` rather than
 * throwing, for useCloudEnvironments' reason: a build with no control plane,
 * or a role that may not read deploy state, is a state to describe, not an
 * error — those users simply have no deployed apps to list.
 */

import { useMemo } from "react";
import { useQuery } from "@tanstack/react-query";

import {
  buildApps,
  isHostedEnv,
  type AppEnvStatus,
  type AppSummary,
} from "@/services/forge/apps";
import {
  cloudAvailabilityFromError,
  cloudErrorDetail,
  hasCloudControlPlane,
  listOrgEnvironments,
  type CloudAvailability,
  type CloudEnv,
} from "@/services/forge/cloudEnvs";

import { forgeCloudKeys, useCloudEnvStatuses } from "./forge-queries";
import { useProjects } from "./project-queries";

interface OrgEnvsResult {
  availability: CloudAvailability;
  envs: CloudEnv[];
  detail: string;
}

export interface AppsState {
  apps: AppSummary[];
  /** The environment list has not answered yet. Per-environment statuses load inside each AppEnv. */
  loading: boolean;
  /** "no-control-plane" for a build with none: nothing hosted to list, and nothing to say about it. */
  availability: CloudAvailability;
  detail: string;
}

export function useOrgEnvironments() {
  return useQuery<OrgEnvsResult>({
    queryKey: forgeCloudKeys.orgEnvs(),
    queryFn: async () => {
      try {
        return { availability: "available" as const, envs: await listOrgEnvironments(), detail: "" };
      } catch (err) {
        return { availability: cloudAvailabilityFromError(err), envs: [], detail: cloudErrorDetail(err) };
      }
    },
    enabled: hasCloudControlPlane(),
    staleTime: 30_000,
    retry: false,
  });
}

/** Every app the user can see, newest deploy first. */
export function useApps(): AppsState {
  const orgEnvs = useOrgEnvironments();
  const { projects } = useProjects();
  const envs = useMemo(() => (orgEnvs.data?.envs ?? []).filter(isHostedEnv), [orgEnvs.data]);
  const statuses = useCloudEnvStatuses(envs.map((env) => env.id));

  // Rebuilt per render, like useCloudEnvStatuses' map: a handful of apps, and
  // memoising on that freshly-allocated map would never hit.
  const byId = new Map<string, AppEnvStatus>();
  for (const env of envs) {
    const entry = statuses.get(env.id);
    // Loading is `undefined`; a failed (or disabled) read is `null` — nothing to show, nothing pending.
    byId.set(env.id, entry?.data ?? (entry?.isLoading && !entry.error ? undefined : null));
  }
  const apps = buildApps(envs, byId, projects);

  const available = hasCloudControlPlane();
  return {
    apps,
    loading: available && orgEnvs.isLoading,
    availability: available ? (orgEnvs.data?.availability ?? "available") : "no-control-plane",
    detail: orgEnvs.data?.detail ?? "",
  };
}
