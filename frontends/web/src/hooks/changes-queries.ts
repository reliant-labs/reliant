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

import { useRouteOrgIdOverride } from "@/lib/orgScope";
import { useChangesApi } from "@/services/controlPlane/changes/ChangesApiContext";
import type { Change } from "@/services/controlPlane/changes/changesTypes";
import { readoutErrorKind } from "@/services/controlPlane/changes/errors";

export const changeKeys = {
  all: ["changes"] as const,
  org: () => [...changeKeys.all, "org"] as const,
  list: (orgId: string | null, project: string, environmentId: string | null) => [...changeKeys.all, "list", orgId, project, environmentId] as const,
  lists: () => [...changeKeys.all, "list"] as const,
  change: (changeId: string) => [...changeKeys.all, "change", changeId] as const,
  events: (changeId: string) => [...changeKeys.all, "events", changeId] as const,
  readout: (changeId: string, runId: string) => [...changeKeys.all, "readout", changeId, runId] as const,
};

/**
 * The org every Changes request is scoped by. On an org page it is the
 * route's org (lib/orgScope.ts), at the same render as the route, never a
 * cached answer for the org the tab was in before; the query only outside the
 * org tree. A switch of the tab's org resets the whole `changes` root
 * (ORG_SCOPED_QUERY_ROOTS), this answer and every list with it.
 */
export function useChangesOrg() {
  const api = useChangesApi();
  const routeOrg = useRouteOrgIdOverride();
  const org = useQuery({ queryKey: changeKeys.org(), queryFn: () => api.currentOrgId(), staleTime: 5 * 60_000, enabled: routeOrg === undefined });
  if (routeOrg !== undefined) return { orgId: routeOrg, isLoading: routeOrg === null, error: null };
  return { orgId: org.data ?? null, isLoading: org.isLoading, error: org.error };
}

/**
 * The most pages of ListChanges one list reads (200 changes each). A project
 * past it shows the first MAX_CHANGE_PAGES * 200 and says the list is cut.
 */
export const MAX_CHANGE_PAGES = 25;

/**
 * One project's changes, in one environment or all of them (environmentId
 * null): every page the server returns, so a project with more than a page
 * of changes never silently loses the later ones. `truncated` says the page
 * cap stopped it first.
 */
export function useChanges(orgId: string | null, project: string, environmentId: string | null) {
  const api = useChangesApi();
  return useQuery({
    queryKey: changeKeys.list(orgId, project, environmentId),
    queryFn: async ({ signal }) => {
      const input = { orgId: orgId as string, project, ...(environmentId ? { environmentId } : {}) };
      const changes: Change[] = [];
      let pageToken = "";
      for (let page = 0; page < MAX_CHANGE_PAGES; page++) {
        const resp = await api.listChanges({ ...input, ...(pageToken ? { pageToken } : {}) }, signal);
        changes.push(...resp.changes);
        pageToken = resp.nextPageToken;
        if (!pageToken) return { changes, truncated: false };
      }
      return { changes, truncated: true };
    },
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

/**
 * The readout of the change's experiment run (the open one, else the latest:
 * the server picks). Every call is a ClickHouse read, so it is never polled
 * and a known refusal (no experiment, no metric, not deployed) is not
 * retried.
 */
export function useChangeReadout(change: Change | undefined) {
  const api = useChangesApi();
  return useQuery({
    queryKey: changeKeys.readout(change?.id ?? "", change?.currentRun?.id ?? ""),
    queryFn: ({ signal }) => api.getChangeReadout(change!.id, undefined, signal),
    enabled: !!change,
    staleTime: 60_000,
    refetchOnWindowFocus: false,
    retry: (failures, error) => readoutErrorKind(error) === "other" && failures < 2,
  });
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
