// Copyright (c) 2025 Reliant Labs
import { describe, it, expect, vi } from "vitest";
import { renderHook } from "@testing-library/react";
import { optionalViews, useOptionalViews } from "../useOptionalViews";
const probes = vi.hoisted(() => ({ profiles: {} as Record<string, unknown>, sessions: {} as Record<string, unknown> }));
vi.mock("@/hooks/observability-queries", () => ({ useServiceSignals: () => probes.profiles }));
vi.mock("@/hooks/observability-session-queries", () => ({ useSessions: () => probes.sessions }));
const empty = { services: [], truncated: false };
const noSessions = { sessions: [], nextPageToken: "" };
describe("optional signal navigation", () => {
  it("distinguishes unknown, truncated and definitely empty reads", () => {
    expect(optionalViews(undefined, undefined)).toEqual({ profiles: true, sessions: true });
    expect(optionalViews(empty, noSessions)).toEqual({ profiles: false, sessions: false });
    expect(optionalViews({ ...empty, truncated: true }, { ...noSessions, nextPageToken: "next" })).toEqual({ profiles: true, sessions: true });
    expect(optionalViews({ services: [{ service: "api", emitting: ["profiles"], declared: [], declarationKnown: true }], truncated: false }, undefined).profiles).toBe(true);
  });
  it("fails open on loading, errors, placeholder data and background failures", () => {
    const scope = { orgId: "org", projectId: "shop", envIds: [], filters: [] };
    for (const state of [{ isSuccess: false }, { isSuccess: true, isFetching: true }, { isSuccess: false, isError: true }]) {
      probes.profiles = { ...state, data: empty };
      probes.sessions = { ...state, data: noSessions };
      expect(renderHook(() => useOptionalViews(scope)).result.current).toEqual({ profiles: true, sessions: true });
    }
    probes.profiles = { isSuccess: true, data: empty };
    probes.sessions = { isSuccess: true, data: noSessions, isPlaceholderData: true };
    expect(renderHook(() => useOptionalViews(scope)).result.current).toEqual({ profiles: false, sessions: true });
    expect(renderHook(() => useOptionalViews(null)).result.current).toEqual({ profiles: true, sessions: true });
  });
});
