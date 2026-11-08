// Copyright (c) 2025 Reliant Labs
import { describe, it, expect, vi } from "vitest";
import { renderHook } from "@testing-library/react";
import { optionalViews, useOptionalViews } from "../useOptionalViews";
const probes = vi.hoisted(() => ({ profiles: {} as Record<string, unknown> }));
vi.mock("@/hooks/observability-queries", () => ({ useServiceSignals: () => probes.profiles }));
const empty = { services: [], truncated: false };
describe("optional signal navigation", () => {
  it("distinguishes unknown, truncated and definitely empty reads", () => {
    expect(optionalViews(undefined)).toEqual({ profiles: true });
    expect(optionalViews(empty)).toEqual({ profiles: false });
    expect(optionalViews({ ...empty, truncated: true })).toEqual({ profiles: true });
    expect(optionalViews({ services: [{ service: "api", emitting: ["profiles"], declared: [], declarationKnown: true }], truncated: false }).profiles).toBe(true);
  });
  it("fails open on loading, errors and background failures", () => {
    const scope = { orgId: "org", projectId: "shop", envIds: [], filters: [] };
    for (const state of [{ isSuccess: false }, { isSuccess: true, isFetching: true }, { isSuccess: false, isError: true }]) {
      probes.profiles = { ...state, data: empty };
      expect(renderHook(() => useOptionalViews(scope)).result.current).toEqual({ profiles: true });
    }
    probes.profiles = { isSuccess: true, data: empty };
    expect(renderHook(() => useOptionalViews(scope)).result.current).toEqual({ profiles: false });
    expect(renderHook(() => useOptionalViews(null)).result.current).toEqual({ profiles: true });
  });
});
