// Copyright (c) 2025 Reliant Labs

/**
 * React Query hooks for Sessions (RumService): the session list, one
 * session's summary and its timeline. The replay itself is not a query — it is
 * paged, buffered ahead of the playhead and, for a session still being
 * recorded, polled; Sessions/replayFeed.ts owns that.
 *
 * The list pages by the server's token, and the token lives in the URL
 * (Sessions/SessionsView.tsx), so a page is a link. The token is bound to the
 * filters and NOT to the window, so "the last hour" re-resolving between page
 * one and page two cannot invalidate it: page two continues after the last
 * session page one showed.
 */

import { keepPreviousData, useInfiniteQuery, useQuery } from "@tanstack/react-query";

import { obsRetryDelay, rumRetry } from "@/services/controlPlane/observability/errors";
import { useObsReadApi } from "./observability-queries";
import type { RumScope, SessionList, Timeline } from "@/services/controlPlane/observability/rumTypes";
import { resolveWindow, type RangePreset } from "@/services/controlPlane/observability/window";

export interface SessionFilters {
  range: RangePreset;
  hasError: boolean;
  hasReplay: boolean;
  userId: string;
  pathContains: string;
  service?: string;
  /** The page shown: a next_page_token from the URL. Absent: the first page. */
  pageToken?: string;
}

export const SESSION_PAGE_SIZE = 25;
export const TIMELINE_PAGE_SIZE = 500;

function scopeKey(scope: RumScope | null) {
  return [scope?.orgId ?? "", scope?.projectId ?? "", scope?.envIds ?? []] as const;
}

/** One root for every session read, so the panel's Refresh re-reads all of them. */
export const sessionKeys = {
  root: () => ["observability", "sessions"] as const,
  list: (scope: RumScope | null, filters: SessionFilters) => [...sessionKeys.root(), "list", ...scopeKey(scope), filters] as const,
  detail: (scope: RumScope | null, sessionId: string) => [...sessionKeys.root(), "detail", ...scopeKey(scope), sessionId] as const,
  timeline: (scope: RumScope | null, sessionId: string) => [...sessionKeys.root(), "timeline", ...scopeKey(scope), sessionId] as const,
};

/** One page of the scope's sessions. The previous page stays on screen while the next one loads. */
export function useSessions(scope: RumScope | null, filters: SessionFilters) {
  const api = useObsReadApi();
  return useQuery({
    queryKey: sessionKeys.list(scope, filters),
    queryFn: ({ signal }): Promise<SessionList> =>
      api.listSessions({
        ...(scope as RumScope),
        services: filters.service ? [filters.service] : [],
        window: resolveWindow(filters.range),
        hasError: filters.hasError,
        hasReplay: filters.hasReplay,
        userId: filters.userId,
        pathContains: filters.pathContains,
        pageSize: SESSION_PAGE_SIZE,
        pageToken: filters.pageToken,
      }, { signal }),
    enabled: !!scope,
    placeholderData: keepPreviousData,
    staleTime: 30_000,
    retry: rumRetry, retryDelay: obsRetryDelay,
  });
}

/** One session's summary. */
export function useSession(scope: RumScope | null, sessionId: string | undefined) {
  const api = useObsReadApi();
  return useQuery({
    queryKey: sessionKeys.detail(scope, sessionId ?? ""),
    queryFn: ({ signal }) => api.getSession({ ...(scope as RumScope), sessionId: sessionId as string }, { signal }),
    enabled: !!scope && !!sessionId,
    staleTime: 30_000,
    retry: rumRetry, retryDelay: obsRetryDelay,
  });
}

/** One session's timeline, oldest first, a page at a time. */
export function useSessionTimeline(scope: RumScope | null, sessionId: string | undefined) {
  const api = useObsReadApi();
  return useInfiniteQuery({
    queryKey: sessionKeys.timeline(scope, sessionId ?? ""),
    queryFn: ({ pageParam, signal }): Promise<Timeline> =>
      api.getSessionTimeline({
        ...(scope as RumScope),
        sessionId: sessionId as string,
        pageSize: TIMELINE_PAGE_SIZE,
        pageToken: pageParam ?? undefined,
      }, { signal }),
    initialPageParam: null as string | null,
    getNextPageParam: (last) => last.nextPageToken || undefined,
    enabled: !!scope && !!sessionId,
    staleTime: 30_000,
    retry: rumRetry, retryDelay: obsRetryDelay,
  });
}
