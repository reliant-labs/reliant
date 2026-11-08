// Copyright (c) 2026 Reliant Labs

/**
 * The hooks behind the empty Sessions list's per-frontend replay state:
 * useFrontendReplay asks ListFrontendReplay for the scope and is not retried
 * on a control plane without it; useReplayEvidence looks for replays over the
 * last 7 days in only the environments it is given, and asks nothing when it
 * is given none. Both keys sit under sessionKeys.root(), so Refresh re-reads them.
 */

import { renderHook, waitFor } from "@testing-library/react";
import { QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { describe, expect, it, vi } from "vitest";

import { createFixtureApi } from "@/components/Forge/Observability/__fixtures__/fixtureApi";
import { ObservabilityApiProvider, type ObservabilityApi } from "@/services/controlPlane/observability/ObservabilityApiContext";
import type { RumScope } from "@/services/controlPlane/observability/rumTypes";
import { createTestQueryClient } from "@/test/renderWithQuery";

import { REPLAY_EVIDENCE_PAGE_SIZE, sessionKeys, useFrontendReplay, useReplayEvidence } from "../observability-session-queries";

const scope: RumScope = { orgId: "org-1", projectId: "shop", envIds: ["env-prod", "env-staging"], services: [] };

function setup(api: ObservabilityApi) {
  const client = createTestQueryClient();
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={client}>
      <ObservabilityApiProvider api={api}>{children}</ObservabilityApiProvider>
    </QueryClientProvider>
  );
  return { client, wrapper };
}

describe("sessionKeys", () => {
  it("put the replay-state reads under the sessions root, keyed by the scope", () => {
    const root = sessionKeys.root();
    for (const key of [sessionKeys.frontendReplay(scope), sessionKeys.replayEvidence(scope, ["env-prod"])]) {
      expect(key.slice(0, root.length)).toEqual([...root]);
    }
    expect(sessionKeys.frontendReplay(scope)).not.toEqual(sessionKeys.frontendReplay({ ...scope, envIds: ["env-prod"] }));
    expect(sessionKeys.replayEvidence(scope, ["env-prod"])).not.toEqual(sessionKeys.replayEvidence(scope, ["env-staging"]));
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

  it("asks nothing without an environment, or while disabled", () => {
    const onCall = vi.fn();
    const { wrapper } = setup(createFixtureApi({ onCall }));
    renderHook(() => useReplayEvidence(scope, []), { wrapper });
    renderHook(() => useReplayEvidence(scope, ["env-prod"], false), { wrapper });
    expect(onCall).not.toHaveBeenCalled();
  });
});
