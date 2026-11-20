// Copyright (c) 2025 Reliant Labs

/**
 * React Query over ChangesApi (services/controlPlane/changes): the
 * Experiments page's reads, and the cache writes its mutations make.
 *
 * A mutation's answer is the change at its new revision, so it is written
 * straight into the change's cache entry (no refetch round trip) and the
 * list and the audit trail are invalidated. A revision conflict refetches
 * the change: the page then shows what moved.
 */

import { useQuery, useQueryClient, type QueryClient } from "@tanstack/react-query";

import { useChangesApi } from "@/services/controlPlane/changes/ChangesApiContext";
import type { Change } from "@/services/controlPlane/changes/changesTypes";

export const changeKeys = {
  all: ["changes"] as const,
  org: () => [...changeKeys.all, "org"] as const,
  list: (orgId: string | null, project: string, environmentId: string | null) => [...changeKeys.all, "list", orgId, project, environmentId] as const,
  lists: () => [...changeKeys.all, "list"] as const,
  change: (changeId: string) => [...changeKeys.all, "change", changeId] as const,
  events: (changeId: string) => [...changeKeys.all, "events", changeId] as const,
  readout: (changeId: string, runId: string) => [...changeKeys.all, "readout", changeId, runId] as const,
};

export function useChangesOrg() {
  const api = useChangesApi();
  const org = useQuery({ queryKey: changeKeys.org(), queryFn: () => api.currentOrgId(), staleTime: 5 * 60_000 });
  return { orgId: org.data ?? null, isLoading: org.isLoading, error: org.error };
}

/** One project's changes, in one environment or all of them (environmentId null). */
export function useChanges(orgId: string | null, project: string, environmentId: string | null) {
  const api = useChangesApi();
  return useQuery({
    queryKey: changeKeys.list(orgId, project, environmentId),
    queryFn: ({ signal }) =>
      api.listChanges({ orgId: orgId as string, project, ...(environmentId ? { environmentId } : {}) }, signal),
    enabled: !!orgId,
    staleTime: 15_000,
  });
}

export function useChange(changeId: string) {
  const api = useChangesApi();
  return useQuery({ queryKey: changeKeys.change(changeId), queryFn: ({ signal }) => api.getChange(changeId, signal), enabled: !!changeId, staleTime: 10_000 });
}

export function useChangeEvents(changeId: string) {
  const api = useChangesApi();
  return useQuery({
    queryKey: changeKeys.events(changeId),
    queryFn: ({ signal }) => api.listChangeEvents(changeId, undefined, signal),
    enabled: !!changeId,
    staleTime: 10_000,
  });
}

/** The readout of the change's open run, or `unavailable` while the control plane has no readout rpc. */
export function useChangeReadout(change: Change | undefined) {
  const api = useChangesApi();
  const read = api.getChangeReadout;
  const runId = change?.currentRun?.id ?? "";
  const query = useQuery({
    queryKey: changeKeys.readout(change?.id ?? "", runId),
    queryFn: ({ signal }) => read!(change!.id, runId, signal),
    enabled: !!read && !!change && !!runId,
    staleTime: 60_000,
  });
  return { unavailable: !read, ...query };
}

/** Record a mutation's answer: the change at its new revision; the list and trail refetch. */
export function rememberChange(queryClient: QueryClient, change: Change): void {
  queryClient.setQueryData(changeKeys.change(change.id), change);
  void queryClient.invalidateQueries({ queryKey: changeKeys.lists() });
  void queryClient.invalidateQueries({ queryKey: changeKeys.events(change.id) });
}

export function useRememberChange() {
  const queryClient = useQueryClient();
  return (change: Change) => rememberChange(queryClient, change);
}
