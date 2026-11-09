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

import { FilterUnsupportedError, obsRetryDelay, rumRetry } from "@/services/controlPlane/observability/errors";
import type { FrontendReplayList } from "@/services/controlPlane/observability/frontendReplayApi";
import { useObservabilityApi } from "@/services/controlPlane/observability/ObservabilityApiContext";
import { useObsReadApi } from "./observability-queries";
import type { RumScope, SessionList, SessionSummary, Timeline } from "@/services/controlPlane/observability/rumTypes";
import { resolveWindow, type RangePreset } from "@/services/controlPlane/observability/window";

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

function scopeKey(scope: RumScope | null) {
  return [scope?.orgId ?? "", scope?.projectId ?? "", scope?.envIds ?? []] as const;
}

/** One root for every session read, so the panel's Refresh re-reads all of them. */
export const sessionKeys = {
  root: () => ["observability", "sessions"] as const,
  list: (scope: RumScope | null, filters: SessionFilters) => [...sessionKeys.root(), "list", ...scopeKey(scope), filters] as const,
  detail: (scope: RumScope | null, sessionId: string) => [...sessionKeys.root(), "detail", ...scopeKey(scope), sessionId] as const,
  timeline: (scope: RumScope | null, sessionId: string) => [...sessionKeys.root(), "timeline", ...scopeKey(scope), sessionId] as const,
  frontendReplay: (scope: RumScope | null) => [...sessionKeys.root(), "frontend-replay", ...scopeKey(scope)] as const,
  replayEvidence: (scope: RumScope | null, envIds: readonly string[]) => [...sessionKeys.root(), "replay-evidence", ...scopeKey(scope), envIds] as const,
};

/**
 * Refuse rows a control plane older than the client filters answered without
 * them (it ignores unknown fields). A server that applies a browser or OS
 * filter returns only sessions whose client names that value; an older one
 * returns sessions whose client is unknown or different.
 */
function assertSessionFiltersApplied(filters: SessionFilters, result: SessionList) {
  for (const s of result.sessions) {
    if (filters.browser && s.client.browserName !== filters.browser) throw new FilterUnsupportedError("browser", "sessions");
    if (filters.os && s.client.osName !== filters.os) throw new FilterUnsupportedError("OS", "sessions");
    if (filters.frontendVersion && s.serviceVersion !== filters.frontendVersion) throw new FilterUnsupportedError("frontend version", "sessions");
  }
}

/** One page of the scope's sessions. The previous page stays on screen while the next one loads. */
export function useSessions(scope: RumScope | null, filters: SessionFilters) {
  const api = useObsReadApi();
  return useQuery({
    queryKey: sessionKeys.list(scope, filters),
    queryFn: async ({ signal }): Promise<SessionList> => {
      const result = await api.listSessions({
        ...(scope as RumScope),
        services: filters.service ? [filters.service] : [],
        window: resolveWindow(filters.range),
        hasError: filters.hasError,
        // Only sessions with a replay exist as sessions: one with nothing to
        // watch (a hidden window's background requests) is never listed. The
        // control plane enforces it too; asking for it keeps an older one honest.
        hasReplay: true,
        userId: filters.userId,
        pathContains: filters.pathContains,
        browser: filters.browser,
        os: filters.os,
        frontendVersion: filters.frontendVersion,
        pageSize: SESSION_PAGE_SIZE,
        pageToken: filters.pageToken,
      }, { signal });
      assertSessionFiltersApplied(filters, result);
      return result;
    },
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

/**
 * Each frontend's session replay switch in the scope's environments
 * (ObservabilityService.ListFrontendReplay), for the empty list to say which
 * frontend is off and why. It reads Postgres and the hosting hub, not
 * ClickHouse, so it goes around the quota guard: a quota pause must not hide
 * the reason the list is empty. Under sessionKeys.root(), so Refresh re-reads it.
 */
export function useFrontendReplay(scope: RumScope | null, enabled = true) {
  const api = useObservabilityApi();
  return useQuery({
    queryKey: sessionKeys.frontendReplay(scope),
    queryFn: ({ signal }): Promise<FrontendReplayList> =>
      api.listFrontendReplay({ orgId: (scope as RumScope).orgId, projectId: scope?.projectId, envIds: scope?.envIds ?? [] }, { signal }),
    enabled: !!scope && enabled,
    staleTime: 60_000,
    retry: rumRetry, retryDelay: obsRetryDelay,
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
 * into the bundle). One window for every page, as the token requires.
 */
export function useReplayEvidence(scope: RumScope | null, envIds: readonly string[], enabled = true) {
  const api = useObsReadApi();
  return useQuery({
    queryKey: sessionKeys.replayEvidence(scope, envIds),
    queryFn: async ({ signal }): Promise<ReplayEvidencePages> => {
      const window = resolveWindow(REPLAY_EVIDENCE_RANGE);
      const sessions: SessionSummary[] = [];
      let pageToken: string | undefined;
      for (let page = 0; page < REPLAY_EVIDENCE_MAX_PAGES; page++) {
        const result = await api.listSessions({
          ...(scope as RumScope),
          envIds: [...envIds],
          services: [],
          window,
          hasError: false,
          hasReplay: true,
          userId: "",
          pathContains: "",
          pageSize: REPLAY_EVIDENCE_PAGE_SIZE,
          pageToken,
        }, { signal });
        sessions.push(...result.sessions);
        if (!result.nextPageToken) return { sessions, complete: true };
        pageToken = result.nextPageToken;
      }
      return { sessions, complete: false };
    },
    enabled: !!scope && enabled && envIds.length > 0,
    staleTime: 60_000,
    retry: rumRetry, retryDelay: obsRetryDelay,
  });
}
