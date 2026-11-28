// Copyright (c) 2025 Reliant Labs

/**
 * React Query hooks for Sessions (RumService): the session list, one
 * session's summary and its timeline. The replay itself is not a query — it is
 * paged, buffered ahead of the playhead and, for a session still being
 * recorded, polled; Sessions/replayFeed.ts owns that.
 *
 * The list, a session and its timeline are RumService's generated reads,
 * keyed by their request (rumServiceKeys). The list sends its preset as a
 * window relative to the server's clock (RumTimeRange.relative), so its
 * request and key stay put while "the last hour" moves, and a Refresh re-reads
 * the hour as of now.
 *
 * The list pages by the server's token, and the token lives in the URL
 * (Sessions/SessionsView.tsx), so a page is a link. The token is bound to the
 * filters and NOT to the window, so "the last hour" re-resolving between page
 * one and page two cannot invalidate it: page two continues after the last
 * session page one showed. That is why a page of the list sends the preset,
 * not page one's effective_time_range: a linked page two has no page one.
 */

import { useQuery, type InfiniteData, type QueryFunctionContext } from "@tanstack/react-query";
import type { MessageInitShape } from "@bufbuild/protobuf";
import { requestKey } from "@reliantlabs/forge-web-runtime/service-hooks";
import { scopedClient } from "@reliantlabs/forge-web-runtime/transport-scope";

import { ListSessionsRequestSchema, RumService, type GetSessionTimelineResponse, type ListSessionsResponse } from "@/gen/controlplane/services/rum/v1/rum_pb";
import { useListFrontendReplay } from "@/hooks/controlplane/observability-service-hooks_gen";
import { rumServiceKeys, rumServiceQueries, useGetSession, useGetSessionTimelineInfinite } from "@/hooks/controlplane/rum-service-hooks_gen";
import { connectClient } from "@/lib/connect";
import { keepPreviousInOrg } from "@/lib/orgScope";
import { relativeWindow } from "@/services/controlPlane/observability/api";
import { FilterUnsupportedError, obsRetryDelay, rumRetry } from "@/services/controlPlane/observability/errors";
import { frontendReplayRequest, toFrontendReplayList } from "@/services/controlPlane/observability/frontendReplayApi";
import { obsCall } from "@/services/controlPlane/observability/obsInterceptors";
import {
  listSessionsRequest,
  mapSessionSummary,
  sessionRequest,
  timelineRequest,
  toSession,
  toSessionList,
  toTimeline,
  type SessionsQuery,
} from "@/services/controlPlane/observability/rumApi";
import type { RumScope, SessionList, SessionSummary, Timeline } from "@/services/controlPlane/observability/rumTypes";
import { rangeMs, type RangePreset } from "@/services/controlPlane/observability/window";

export interface SessionFilters {
  range: RangePreset;
  hasError: boolean;
  userId: string;
  pathContains: string;
  /** Only sessions of this browser, OS and frontend build (exact). */
  browser?: string;
  os?: string;
  frontendVersion?: string;
  service?: string;
  /** The page shown: a next_page_token from the URL. Absent: the first page. */
  pageToken?: string;
}

export const SESSION_PAGE_SIZE = 25;
export const TIMELINE_PAGE_SIZE = 500;

/** The request of a read that is disabled because there is no scope yet: it is never sent. */
const NO_SCOPE: RumScope = { orgId: "", envIds: [] };

/**
 * The call options of every session read: the read marked, at the moment it
 * starts, as a poll, a prefetch or a person's read, for the control-plane
 * transport's read stage (obsInterceptors.ts).
 */
const obsRead = () => obsCall();

/** The client a hand-written read calls: its QueryClient's TransportScope's, else the app's (as a generated read's). */
const rumClientOf = scopedClient(RumService, connectClient(RumService));

/**
 * Do not let sessions returned without a requested filter appear narrowed.
 * Each row in a user- or client-filtered list must name the requested value.
 */
function assertSessionFiltersApplied(filters: SessionFilters, result: SessionList) {
  for (const s of result.sessions) {
    if (filters.userId && s.userId !== filters.userId) throw new FilterUnsupportedError("user", "sessions");
    if (filters.browser && s.client.browserName !== filters.browser) throw new FilterUnsupportedError("browser", "sessions");
    if (filters.os && s.client.osName !== filters.os) throw new FilterUnsupportedError("OS", "sessions");
    if (filters.frontendVersion && s.serviceVersion !== filters.frontendVersion) throw new FilterUnsupportedError("frontend version", "sessions");
  }
}

/** One page of the scope's sessions. The previous page stays on screen while the next one loads. */
export function useSessions(scope: RumScope | null, filters: SessionFilters) {
  const generated = rumServiceQueries.listSessions(
    listSessionsRequest({
      ...(scope ?? NO_SCOPE),
      services: filters.service ? [filters.service] : [],
      window: relativeWindow(rangeMs(filters.range)),
      hasError: filters.hasError,
      hasReplay: true,
      userId: filters.userId,
      pathContains: filters.pathContains,
      browser: filters.browser,
      os: filters.os,
      frontendVersion: filters.frontendVersion,
      pageSize: SESSION_PAGE_SIZE,
      pageToken: filters.pageToken,
    }),
    {
      select: toSessionList,
      enabled: !!scope,
      // The previous page, but never another org's (lib/orgScope).
      placeholderData: keepPreviousInOrg(scope?.orgId ?? null),
      staleTime: 30_000,
      retry: rumRetry, retryDelay: obsRetryDelay,
      callOptions: obsRead,
    }
  );
  // The filter check fails the FETCH, as a server refusing the filter would:
  // in `select` its error would leave the previous filters' rows on screen.
  const fetchPage = generated.queryFn as unknown as (context: QueryFunctionContext) => Promise<ListSessionsResponse>;
  return useQuery({
    ...generated,
    queryFn: async (context) => {
      const res = await fetchPage(context);
      assertSessionFiltersApplied(filters, toSessionList(res));
      return res;
    },
  });
}

/** One session's summary. */
export function useSession(scope: RumScope | null, sessionId: string | undefined) {
  return useGetSession(sessionRequest({ ...(scope ?? NO_SCOPE), sessionId: sessionId ?? "" }), {
    select: toSession,
    enabled: !!scope && !!sessionId,
    staleTime: 30_000,
    retry: rumRetry, retryDelay: obsRetryDelay,
    callOptions: obsRead,
  });
}

/** Every page of a timeline, as the console's model. */
function toTimelinePages(data: InfiniteData<GetSessionTimelineResponse, string>): InfiniteData<Timeline, string> {
  return { pages: data.pages.map(toTimeline), pageParams: data.pageParams };
}

/** One session's timeline, oldest first, a page at a time. */
export function useSessionTimeline(scope: RumScope | null, sessionId: string | undefined) {
  return useGetSessionTimelineInfinite(timelineRequest({ ...(scope ?? NO_SCOPE), sessionId: sessionId ?? "", pageSize: TIMELINE_PAGE_SIZE }), {
    select: toTimelinePages,
    enabled: !!scope && !!sessionId,
    staleTime: 30_000,
    retry: rumRetry, retryDelay: obsRetryDelay,
    callOptions: obsRead,
  });
}

/**
 * Each frontend's session replay switch in the scope's environments
 * (ObservabilityService.ListFrontendReplay), for the empty list to say which
 * frontend is off and why. It reads Postgres and the hosting hub, not
 * ClickHouse, so it goes around the quota guard: a quota pause must not hide
 * the reason the list is empty. A generated read; the panel's Refresh re-reads
 * every one (listFrontendReplay.all) with the session reads.
 */
export function useFrontendReplay(scope: RumScope | null, enabled = true) {
  return useListFrontendReplay(frontendReplayRequest({ orgId: scope?.orgId ?? "", projectId: scope?.projectId, envIds: scope?.envIds ?? [] }), {
    select: toFrontendReplayList,
    enabled: !!scope?.orgId && enabled,
    staleTime: 60_000,
    retry: rumRetry, retryDelay: obsRetryDelay,
    callOptions: () => obsCall(),
  });
}

/** How far back the console looks for replays a frontend's config says it should not be recording. */
export const REPLAY_EVIDENCE_RANGE: RangePreset = "7d";
/** The server's page maximum. */
export const REPLAY_EVIDENCE_PAGE_SIZE = 100;
/** The most pages the evidence read follows: 1,000 sessions, then it stops and says so (`complete: false`). */
export const REPLAY_EVIDENCE_MAX_PAGES = 10;

/** The replays the evidence read saw, and whether it saw every one in the window. */
export interface ReplayEvidencePages {
  sessions: SessionSummary[];
  /** False: it stopped at REPLAY_EVIDENCE_MAX_PAGES with more to read, so a frontend it found nothing for is unknown. */
  complete: boolean;
}

/**
 * The sessions with a replay in `envIds` over REPLAY_EVIDENCE_RANGE, newest
 * first, following the server's page token up to REPLAY_EVIDENCE_MAX_PAGES:
 * what reconciles a frontend whose runtime config reads "off" but that still
 * records through the build flag (ListFrontendReplay cannot see a flag baked
 * into the bundle). One window for every page, as the token requires: page
 * one sends the preset (relative), and every later page sends the absolute
 * range page one read (effective_time_range) with its token.
 *
 * Hand-written on the scope's client (rumClientOf), because one query follows
 * many pages; keyed under rumServiceKeys.listSessions by its first page's
 * request, so a Refresh and the org switch's purge reach it with the list.
 */
export function useReplayEvidence(scope: RumScope | null, envIds: readonly string[], enabled = true) {
  const query: SessionsQuery = {
    ...(scope ?? NO_SCOPE),
    envIds: [...envIds],
    services: [],
    window: relativeWindow(rangeMs(REPLAY_EVIDENCE_RANGE)),
    hasError: false,
    hasReplay: true,
    userId: "",
    pathContains: "",
    pageSize: REPLAY_EVIDENCE_PAGE_SIZE,
  };
  const first = listSessionsRequest(query);
  return useQuery({
    queryKey: [...rumServiceKeys.listSessions.all, requestKey(ListSessionsRequestSchema, first), "replay-evidence"],
    queryFn: async (context): Promise<ReplayEvidencePages> => {
      const client = rumClientOf(context);
      const sessions: SessionSummary[] = [];
      let request: MessageInitShape<typeof ListSessionsRequestSchema> = first;
      for (let page = 0; page < REPLAY_EVIDENCE_MAX_PAGES; page++) {
        const res = await client.listSessions(request, obsCall({ signal: context.signal }));
        sessions.push(...res.sessions.map(mapSessionSummary));
        if (!res.nextPageToken) return { sessions, complete: true };
        request = listSessionsRequest({ ...query, pageToken: res.nextPageToken }, res.effectiveTimeRange);
      }
      return { sessions, complete: false };
    },
    enabled: !!scope && enabled && envIds.length > 0,
    staleTime: 60_000,
    retry: rumRetry, retryDelay: obsRetryDelay,
  });
}
