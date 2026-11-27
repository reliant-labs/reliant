// Copyright (c) 2025 Reliant Labs

/**
 * IssuesService on its generated keys and client: the inbox names its window
 * RELATIVE to the server's clock on page one (so its request and key stay put
 * as time moves) and pages on the absolute range the server echoed
 * (effective_time_range); a poll of it reaches control-plane with Forge-Poll;
 * a triage refreshes every issue read when it is refused too; and the shop
 * pack's handlers resolve a relative window and echo the range they read.
 */

import type { ReactNode } from "react";
import { QueryClient } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import { Code, ConnectError, createRouterTransport, type HandlerContext } from "@connectrpc/connect";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { IssueResolveKind, IssueStatusChange as WireStatusChange, IssuesService, type ListIssuesRequest } from "@/gen/controlplane/services/issues/v1/issues_pb";
import { issuesHandlers } from "@/fixtures/shop/handlers/issues";
import { msOf } from "@/fixtures/shop/handlers/wire";
import { issuesServiceKeys } from "@/hooks/controlplane/issues-service-hooks_gen";
import { createTransportScope, TransportScopeProvider } from "@/lib/transportScope";
import { relativeWindow } from "@/services/controlPlane/observability/api";
import { listIssuesRequest } from "@/services/controlPlane/observability/issuesApi";
import { obsReadInterceptor } from "@/services/controlPlane/observability/obsInterceptors";
import { ObsQuotaProvider } from "@/services/controlPlane/observability/ObsQuotaContext";
import { resetObsQuotaStore, runAsPoll } from "@/services/controlPlane/observability/quotaStore";
import { resetObsReadScheduler } from "@/services/controlPlane/observability/readScheduler";
import type { IssueScope } from "@/services/controlPlane/observability/issuesTypes";
import { createFixtureApi } from "@/components/Forge/Observability/__fixtures__/fixtureApi";

import { issueKeys, useIssues, useUpdateIssueStatus, type IssueFilters } from "../observability-queries";

const SCOPE: IssueScope = { orgId: "org", projectId: "shop", envIds: ["env-prod"] };
const FILTERS: IssueFilters = { range: "1h", statuses: [], sort: "last", text: "" };
const HOUR = 3_600_000;
const READ_AT = Date.UTC(2026, 10, 10, 15, 0, 0);
const ts = (ms: number) => ({ seconds: BigInt(Math.floor(ms / 1000)), nanos: 0 });

interface Arrival {
  request: ListIssuesRequest;
  poll: boolean;
}

function setup() {
  const arrivals: Arrival[] = [];
  let conflict = false;
  // control-plane behind the app's read stage, as on the app's transport.
  const transport = createRouterTransport(
    (router) =>
      router.service(IssuesService, {
        listIssues: (request: ListIssuesRequest, ctx: HandlerContext) => {
          arrivals.push({ request, poll: ctx.requestHeader.get("Forge-Poll") === "1" });
          // Page one answers with one more page; the server echoes the range it read.
          return {
            issues: [],
            nextPageToken: request.pageToken ? "" : "page-2",
            effectiveTimeRange: { start: ts(READ_AT - HOUR), end: ts(READ_AT) },
          };
        },
        updateIssueStatus: () => {
          if (conflict) throw new ConnectError("the issue changed since it was read", Code.Aborted);
          return { issue: { id: "iss-1" } };
        },
      }),
    { transport: { interceptors: [obsReadInterceptor()] } }
  );
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: Infinity }, mutations: { retry: false } } });
  const scope = createTransportScope({ id: "issues-generated-calls", transport, queryClient: client });
  const wrapper = ({ children }: { children: ReactNode }) => (
    <TransportScopeProvider scope={scope}>
      <ObsQuotaProvider>{children}</ObsQuotaProvider>
    </TransportScopeProvider>
  );
  return { arrivals, client, wrapper, refuse: () => { conflict = true; } };
}

beforeEach(() => {
  resetObsQuotaStore();
  resetObsReadScheduler();
});
afterEach(() => {
  resetObsReadScheduler();
});

describe("the issue inbox on IssuesService's generated key", () => {
  it("sends page one's window relative, capped at what issues keep, and a later page the range page one read", async () => {
    const { arrivals, wrapper } = setup();
    const { result } = renderHook(() => useIssues(SCOPE, { ...FILTERS, range: "30d" }), { wrapper });
    await waitFor(() => expect(result.current.data?.pages).toHaveLength(1));
    const first = arrivals[0]!.request;
    expect(first.scope?.timeRange?.start).toBeUndefined();
    expect(first.scope).toMatchObject({ orgId: "org", projectId: "shop", envIds: ["env-prod"] });
    expect(Number(first.scope?.timeRange?.relative?.last?.seconds)).toBeGreaterThan(0);
    expect(Number(first.scope?.timeRange?.relative?.last?.seconds)).toBeLessThanOrEqual((30 * 24 * HOUR) / 1000);

    await act(async () => { await result.current.fetchNextPage(); });
    await waitFor(() => expect(result.current.data?.pages).toHaveLength(2));
    const second = arrivals[1]!.request;
    expect(second.pageToken).toBe("page-2");
    expect(second.scope?.timeRange?.relative).toBeUndefined();
    expect(msOf(second.scope?.timeRange?.start)).toBe(READ_AT - HOUR);
    expect(msOf(second.scope?.timeRange?.end)).toBe(READ_AT);
    expect(result.current.data?.pages[0]).toEqual({ issues: [], nextPageToken: "page-2" });
  });

  it("asks a filter's probe about the range the list read, not the relative window resolved again", async () => {
    const { arrivals, wrapper } = setup();
    const { result } = renderHook(() => useIssues(SCOPE, { ...FILTERS, userId: "user-7" }), { wrapper });
    await waitFor(() => expect(result.current.data?.pages).toHaveLength(1));
    expect(arrivals).toHaveLength(2);
    const [list, probe] = arrivals.map((a) => a.request);
    expect(list!.userId).toBe("user-7");
    expect(list!.scope?.timeRange?.relative).toBeDefined();
    expect(probe!.userId).toMatch(/^probe-/);
    expect(probe!.pageToken).toBe("");
    expect(probe!.scope?.timeRange?.relative).toBeUndefined();
    expect(msOf(probe!.scope?.timeRange?.start)).toBe(READ_AT - HOUR);
    expect(msOf(probe!.scope?.timeRange?.end)).toBe(READ_AT);
  });

  it("keeps one cache key as time moves, under the service's generated root", () => {
    const at = (now: number) => {
      const real = Date.now;
      Date.now = () => now;
      try {
        return issuesServiceKeys.listIssuesInfinite(listIssuesRequest({ ...SCOPE, window: relativeWindow(HOUR), statuses: [], sort: "last" }));
      } finally {
        Date.now = real;
      }
    };
    expect(at(1_000)).toEqual(at(1_000 + 5 * HOUR));
    expect(at(1_000)[0]).toBe(issuesServiceKeys.all[0]);
  });

  it("marks a read made inside a poll with Forge-Poll, and a person's read without it", async () => {
    const { arrivals, client, wrapper } = setup();
    const { result } = renderHook(() => useIssues(SCOPE, FILTERS), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(arrivals[0]!.poll).toBe(false);
    await act(async () => runAsPoll(() => void client.refetchQueries({ queryKey: issuesServiceKeys.listIssues.all })));
    await waitFor(() => expect(arrivals).toHaveLength(2));
    expect(arrivals[1]!.poll).toBe(true);
  });
});

describe("a triage on IssuesService's generated write", () => {
  it("re-reads every issue read when the server refuses a stale version, not only on success", async () => {
    const { arrivals, refuse, wrapper } = setup();
    const { result } = renderHook(() => ({ issues: useIssues(SCOPE, FILTERS), update: useUpdateIssueStatus() }), { wrapper });
    await waitFor(() => expect(result.current.issues.isSuccess).toBe(true));
    refuse();
    await act(async () => {
      await result.current.update.mutateAsync({ issueId: "iss-1", envId: "env-prod", change: { status: "unresolved" }, expectedVersion: 3 }).catch(() => undefined);
    });
    await waitFor(() => expect(result.current.update.error).toBeInstanceOf(ConnectError));
    await waitFor(() => expect(arrivals).toHaveLength(2));
  });
});

describe("the shop pack's IssuesService handlers", () => {
  it("resolve a relative window against the pack's clock and echo the range they read", async () => {
    const api = createFixtureApi();
    const seen: { startMs: number; endMs: number }[] = [];
    const list = api.listIssues;
    api.listIssues = (req) => { seen.push(req.window); return list(req); };
    const now = Date.UTC(2026, 10, 10, 15, 0, 0);
    const handlers = issuesHandlers(api, { clock: now }) as Record<string, (req: unknown) => Promise<{ effectiveTimeRange?: { start?: { seconds: bigint }; end?: { seconds: bigint } } }>>;
    const answer = await handlers[`${IssuesService.typeName}/ListIssues`]!(
      { scope: { orgId: "org-fixture", envIds: [], services: [], timeRange: { relative: { last: { seconds: BigInt(HOUR / 1000), nanos: 0 } } } }, statuses: [], sort: 0, pageSize: 0, pageToken: "" }
    );
    expect(seen[0]).toEqual({ startMs: now - HOUR, endMs: now });
    expect(msOf(answer.effectiveTimeRange?.start as never)).toBe(now - HOUR);
    expect(msOf(answer.effectiveTimeRange?.end as never)).toBe(now);
  });

  it("decode a triage into the model the fake answers", async () => {
    const api = createFixtureApi();
    const calls: unknown[] = [];
    api.updateIssueStatus = async (req) => { calls.push(req); return (await api.getIssue({ issueId: req.issueId })).issue; };
    const handlers = issuesHandlers(api) as Record<string, (req: unknown) => Promise<unknown>>;
    const { issue: some } = await api.getIssue({ issueId: (await api.listIssues({ orgId: "org-fixture", envIds: [], window: { startMs: 0, endMs: Number.MAX_SAFE_INTEGER }, statuses: [], sort: "last" })).issues[0]!.id });
    await handlers[`${IssuesService.typeName}/UpdateIssueStatus`]!({ issueId: some.id, envId: some.envId, expectedVersion: 7, status: WireStatusChange.RESOLVED, resolveIn: { kind: IssueResolveKind.VERSION, version: "1.2.3" } });
    expect(calls[0]).toEqual({ issueId: some.id, envId: some.envId, expectedVersion: 7, change: { status: "resolved", resolveIn: { kind: "version", version: "1.2.3" } } });
  });
});

it("names each issue read by its generated key", () => {
  expect(issueKeys.detail("iss-1", "1h").slice(0, issuesServiceKeys.getIssue.all.length)).toEqual(issuesServiceKeys.getIssue.all);
  expect(issueKeys.timeline("iss-1").slice(0, issueKeys.timelines().length)).toEqual(issueKeys.timelines());
  expect(issueKeys.root()).toEqual(issuesServiceKeys.all);
});
