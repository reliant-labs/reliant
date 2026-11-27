// Copyright (c) 2025 Reliant Labs

/**
 * The Experiments page's ChangesService reads, and the cache writes its
 * mutations make, over the generated hooks
 * (hooks/controlplane/changes-service-hooks_gen.ts). Reads map the answer
 * into the console's types in `select` (services/controlPlane/changes/
 * changesWire.ts); the cache holds the messages.
 *
 * A mutation's answer is the change at its new revision, so it is written
 * straight into the change's cache entry (no refetch round trip) and the
 * list and the audit trail are invalidated: every write passes NO_REFETCH
 * and hands its answer to rememberChange. A revision conflict refetches the
 * change: the page then shows what moved.
 *
 * Three reads stay hand-written over the generated keys: the list, which
 * reads every page in one query; the readout, keyed by the change's open run
 * too (its request names no run: the server picks one); and the delivery
 * status, keyed by the change's revision too (useChangesDelivery).
 */

import { useMemo } from "react";
import { create } from "@bufbuild/protobuf";
import { useQuery, useQueryClient, type QueryClient } from "@tanstack/react-query";

import {
  ChangesService,
  GetChangeResponseSchema,
  ListChangesResponseSchema,
  type Change as ChangeMessage,
  type GetChangeResponse,
  type ListChangesResponse,
} from "@/gen/controlplane/services/changes/v1/changes_pb";
import { changesServiceKeys, changesServiceMutations, changesServiceQueries, useGetChange, useListChangeEvents } from "@/hooks/controlplane/changes-service-hooks_gen";
import { connectClient } from "@/lib/connect";
import { useRouteOrgIdOverride } from "@/lib/orgScope";
import { currentOrgId } from "@/services/controlPlane/orgs";
import { listChangeEventsRequest, listChangesRequest, type ListChangesInit } from "@/services/controlPlane/changes/changesRequests";
import type { Change } from "@/services/controlPlane/changes/changesTypes";
import { changeEventPageFrom, changeFrom, changeListFrom, deliveryStatusFrom, readoutFrom } from "@/services/controlPlane/changes/changesWire";
import { changeErrorKind, readoutErrorKind } from "@/services/controlPlane/changes/errors";

/**
 * The one key not on ChangesService: the org the page is scoped by, when the
 * route names none. Org switches reset its `changes` root
 * (ORG_SCOPED_QUERY_ROOTS), as they do ChangesService's.
 */
export const changesOrgKey = ["changes", "org"] as const;

/**
 * The org every Changes request is scoped by. On an org page it is the
 * route's org (lib/orgScope.ts), at the same render as the route, never a
 * cached answer for the org the tab was in before; the query only outside the
 * org tree. A switch of the tab's org resets this answer and every list
 * (ORG_SCOPED_QUERY_ROOTS).
 */
export function useChangesOrg() {
  const routeOrg = useRouteOrgIdOverride();
  const org = useQuery({ queryKey: changesOrgKey, queryFn: () => currentOrgId(), staleTime: 5 * 60_000, enabled: routeOrg === undefined });
  if (routeOrg !== undefined) return { orgId: routeOrg, isLoading: routeOrg === null, error: null };
  return { orgId: org.data ?? null, isLoading: org.isLoading, error: org.error };
}

/**
 * The most pages of ListChanges one list reads (200 changes each). A project
 * past it shows the first MAX_CHANGE_PAGES * 200 and says the list is cut.
 */
export const MAX_CHANGE_PAGES = 25;

/**
 * Every page of one listing, as one ListChangesResponse: the changes of all
 * of them, and the next page's token when MAX_CHANGE_PAGES stopped it first
 * (empty when it read them all). It is cached under the first page's
 * generated key, the shape the generated read caches there.
 */
async function readEveryPage(req: ListChangesInit, signal: AbortSignal): Promise<ListChangesResponse> {
  const client = connectClient(ChangesService);
  const changes: ChangeMessage[] = [];
  let pageToken = "";
  for (let page = 0; page < MAX_CHANGE_PAGES; page++) {
    const resp = await client.listChanges({ ...req, ...(pageToken ? { pageToken } : {}) }, { signal });
    changes.push(...resp.changes);
    pageToken = resp.nextPageToken;
    if (!pageToken) break;
  }
  return create(ListChangesResponseSchema, { changes, nextPageToken: pageToken });
}

const everyChange = (resp: ListChangesResponse) => ({ changes: changeListFrom(resp).changes, truncated: resp.nextPageToken !== "" });

/**
 * One project's changes, in one environment or all of them (environmentId
 * null): every page the server returns, so a project with more than a page
 * of changes never silently loses the later ones. `truncated` says the page
 * cap stopped it first.
 */
export function useChanges(orgId: string | null, project: string, environmentId: string | null) {
  const req = listChangesRequest({ orgId: orgId ?? "", project, ...(environmentId ? { environmentId } : {}) });
  return useQuery({
    queryKey: changesServiceKeys.listChanges(req),
    queryFn: ({ signal }) => readEveryPage(req, signal),
    enabled: !!orgId,
    staleTime: 15_000,
    select: everyChange,
  });
}

const changeOfAnswer = (resp: GetChangeResponse): Change => changeFrom(resp.change);

export function useChange(changeId: string) {
  return useGetChange({ changeId }, { enabled: !!changeId, staleTime: 10_000, select: changeOfAnswer });
}

export function useChangeEvents(changeId: string) {
  return useListChangeEvents(listChangeEventsRequest(changeId), { enabled: !!changeId, staleTime: 10_000, select: changeEventPageFrom });
}

/**
 * The readout of the change's experiment run (the open one, else the latest:
 * the server picks). Every call is a ClickHouse read, so it is never polled
 * and a known refusal (no experiment, no metric, not deployed) is not
 * retried. Keyed by the open run as well, so a new run reads afresh.
 */
export function useChangeReadout(change: Change | undefined) {
  const read = changesServiceQueries.getChangeReadout(
    { changeId: change?.id ?? "" },
    {
      enabled: !!change,
      staleTime: 60_000,
      refetchOnWindowFocus: false,
      retry: (failures, error) => readoutErrorKind(error) === "other" && failures < 2,
      select: readoutFrom,
    }
  );
  return useQuery({ ...read, queryKey: [...read.queryKey, change?.currentRun?.id ?? ""] });
}

/** How often the delivery status is read again while a target is still behind. */
export const DELIVERY_POLL_MS = 15_000;

/**
 * Whether the environment's apps have been handed the snapshot that covers
 * the change's revision (GetChangesDeliveryStatus). Keyed by that revision
 * too, so every answer was read after the page saw it: the snapshot revision
 * it names then covers it (changeModel.ts deliveryStateOf). Read again every
 * DELIVERY_POLL_MS only while a target is behind and the tab is visible
 * (React Query pauses an interval in the background), and on focus. A control
 * plane without the rpc answers Unimplemented once, never retried.
 */
export function useChangesDelivery(change: Pick<Change, "environmentId" | "revision"> | undefined) {
  const read = changesServiceQueries.getChangesDeliveryStatus(
    { environmentId: change?.environmentId ?? "" },
    {
      enabled: !!change?.environmentId,
      staleTime: 10_000,
      retry: (failures, error) => changeErrorKind(error) !== "not-available" && failures < 2,
      // Behind: a target holds an older snapshot than the environment renders (changeModel.ts "waiting").
      refetchInterval: (query) => {
        const status = query.state.data;
        return status && status.targets.some((target) => target.revision < status.revision) ? DELIVERY_POLL_MS : false;
      },
      select: deliveryStatusFrom,
    }
  );
  return useQuery({ ...read, queryKey: [...read.queryKey, change?.revision ?? 0] });
}

/** What every change write answers: the change at its new revision. */
export interface ChangeReply {
  change?: ChangeMessage;
}

/** A write's own refresh is off: its answer goes to rememberChange instead. */
export const NO_REFETCH = { invalidate: false } as const;

/** ChangesService's writes on this tree's QueryClient. Pass NO_REFETCH, then rememberChange the answer. */
export function useChangeWrites() {
  const queryClient = useQueryClient();
  return useMemo(() => changesServiceMutations(queryClient), [queryClient]);
}

/** Record a mutation's answer: the change at its new revision; the list and trail refetch. */
export function rememberChange(queryClient: QueryClient, change: ChangeMessage | undefined): void {
  if (!change) return;
  queryClient.setQueryData(changesServiceQueries.getChange({ changeId: change.id }).queryKey, create(GetChangeResponseSchema, { change }));
  void queryClient.invalidateQueries({ queryKey: changesServiceKeys.listChanges.all });
  void queryClient.invalidateQueries({ queryKey: changesServiceKeys.listChangeEvents(listChangeEventsRequest(change.id)) });
  void queryClient.invalidateQueries({ queryKey: changesServiceKeys.getChangesDeliveryStatus({ environmentId: change.environmentId }) });
}

export function useRememberChange() {
  const queryClient = useQueryClient();
  return (change: ChangeMessage | undefined) => rememberChange(queryClient, change);
}
