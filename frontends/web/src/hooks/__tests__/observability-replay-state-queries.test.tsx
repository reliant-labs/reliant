// Copyright (c) 2026 Reliant Labs

/**
 * The hooks behind the empty Sessions list's per-frontend replay state:
 * useFrontendReplay asks ListFrontendReplay for the scope and is not retried
 * on a control plane without it; useReplayEvidence looks for replays over the
 * last 7 days in only the environments it is given, following the page token
 * up to 10 pages, and asks nothing when it is given none. The evidence sits under RumService's listSessions
 * key and the frontends' read is a generated one (listFrontendReplay), so Refresh re-reads both.
 */

import { renderHook, waitFor } from "@testing-library/react";
import { QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { describe, expect, it, vi } from "vitest";

import { createFixtureApi } from "@/components/Forge/Observability/__fixtures__/fixtureApi";
import { type ObservabilityApi } from "@/services/controlPlane/observability/ObservabilityApiContext";
import { ObservabilityFixtures } from "@/test/observabilityFixtures";
import type { RumScope } from "@/services/controlPlane/observability/rumTypes";
import { createTestQueryClient } from "@/test/renderWithQuery";

import { rumServiceKeys } from "@/hooks/controlplane/rum-service-hooks_gen";
import { keyNamesOrg } from "@/lib/orgScope";

import { REPLAY_EVIDENCE_MAX_PAGES, REPLAY_EVIDENCE_PAGE_SIZE, useFrontendReplay, useReplayEvidence } from "../observability-session-queries";

const scope: RumScope = { orgId: "org-1", projectId: "shop", envIds: ["env-prod", "env-staging"], services: [] };

function setup(api: ObservabilityApi) {
  const client = createTestQueryClient();
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={client}>
      <ObservabilityFixtures api={api}>{children}</ObservabilityFixtures>
    </QueryClientProvider>
  );
  return { client, wrapper };
}

describe("the replay evidence's key", () => {
  it("sits under RumService's listSessions, names the org, and is one per environment set", async () => {
    const { client, wrapper } = setup(createFixtureApi());
    const prod = renderHook(() => useReplayEvidence(scope, ["env-prod"]), { wrapper });
    const staging = renderHook(() => useReplayEvidence(scope, ["env-staging"]), { wrapper });
    await waitFor(() => expect(prod.result.current.isSuccess && staging.result.current.isSuccess).toBe(true));
    const keys = client.getQueryCache().findAll({ queryKey: rumServiceKeys.listSessions.all }).map((q) => q.queryKey);
    expect(keys).toHaveLength(2);
    expect(keys.every((key) => keyNamesOrg(key, "org-1"))).toBe(true);
  });
});

describe("useFrontendReplay", () => {
  it("reads the scope's frontends: org, project and environments", async () => {
    const onCall = vi.fn();
    const { wrapper } = setup(createFixtureApi({ onCall }));
    const { result } = renderHook(() => useFrontendReplay(scope), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(onCall).toHaveBeenCalledWith("ListFrontendReplay", { orgId: "org-1", projectId: "shop", envIds: ["env-prod", "env-staging"] });
    expect(result.current.data!.frontends.map((f) => `${f.envName}/${f.frontend}:${f.state}`)).toEqual(["prod/web:on", "staging/web:on"]);
  });

  it("asks nothing until it is enabled", () => {
    const onCall = vi.fn();
    const { wrapper } = setup(createFixtureApi({ onCall }));
    renderHook(() => useFrontendReplay(scope, false), { wrapper });
    renderHook(() => useFrontendReplay(null), { wrapper });
    expect(onCall).not.toHaveBeenCalled();
  });

  it("does not retry a control plane without the rpc", async () => {
    const onCall = vi.fn();
    const { wrapper } = setup(createFixtureApi({ onCall, frontendReplay: "not-configured" }));
    const { result } = renderHook(() => useFrontendReplay(scope), { wrapper });
    await waitFor(() => expect(result.current.isError).toBe(true));
    expect(onCall.mock.calls.filter(([rpc]) => rpc === "ListFrontendReplay")).toHaveLength(1);
  });
});

describe("useReplayEvidence", () => {
  it("asks for the newest replays of the last 7 days in the given environments", async () => {
    const onCall = vi.fn();
    const { wrapper } = setup(createFixtureApi({ onCall }));
    const now = Date.now();
    const { result } = renderHook(() => useReplayEvidence(scope, ["env-staging"]), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    const [, req] = onCall.mock.calls.find(([rpc]) => rpc === "ListSessions")!;
    expect(req).toMatchObject({ orgId: "org-1", projectId: "shop", envIds: ["env-staging"], services: [], hasReplay: true, hasError: false, pageSize: REPLAY_EVIDENCE_PAGE_SIZE });
    const span = req.window.endMs - req.window.startMs;
    expect(span).toBe(7 * 24 * 60 * 60_000);
    expect(req.window.endMs).toBeGreaterThanOrEqual(now);
  });

  it("follows the page token with one window, and says when it stopped before the end", async () => {
    const api = createFixtureApi();
    const calls: { pageToken?: string; window: { startMs: number; endMs: number } }[] = [];
    const listSessions: ObservabilityApi["listSessions"] = async (req) => {
      calls.push({ pageToken: req.pageToken, window: req.window });
      return { sessions: [], nextPageToken: `t:${calls.length}` };
    };
    const { wrapper } = setup({ ...api, listSessions });
    const { result } = renderHook(() => useReplayEvidence(scope, ["env-prod"]), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(calls.map((c) => c.pageToken)).toEqual([undefined, ...Array.from({ length: REPLAY_EVIDENCE_MAX_PAGES - 1 }, (_, i) => `t:${i + 1}`)]);
    expect(new Set(calls.map((c) => c.window.startMs)).size).toBe(1);
    expect(result.current.data!.complete).toBe(false);
  });

  it("is complete when the last page has no token", async () => {
    const { wrapper } = setup(createFixtureApi());
    const { result } = renderHook(() => useReplayEvidence(scope, ["env-prod"]), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data!.complete).toBe(true);
    expect(result.current.data!.sessions.every((s) => s.hasReplay && s.envId === "env-prod")).toBe(true);
  });

  it("asks nothing without an environment, or while disabled", () => {
    const onCall = vi.fn();
    const { wrapper } = setup(createFixtureApi({ onCall }));
    renderHook(() => useReplayEvidence(scope, []), { wrapper });
    renderHook(() => useReplayEvidence(scope, ["env-prod"], false), { wrapper });
    expect(onCall).not.toHaveBeenCalled();
  });
});
