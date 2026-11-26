// Copyright (c) 2025 Reliant Labs

/**
 * The metrics explorer's hooks: the catalog caps its window at 24 hours, every
 * key carries the whole scope (so Refresh and a scope change behave), and a
 * server refusal is an error the page can show, not a retry loop.
 */

import { renderHook, waitFor } from "@testing-library/react";
import { QueryClientProvider } from "@tanstack/react-query";
import { Code, ConnectError } from "@connectrpc/connect";
import type { ReactNode } from "react";
import { describe, expect, it, vi } from "vitest";

import { createFixtureApi } from "@/components/Forge/Observability/__fixtures__/fixtureApi";
import { type ObservabilityApi } from "@/services/controlPlane/observability/ObservabilityApiContext";
import { ObservabilityFixtures } from "@/test/observabilityFixtures";
import type { ObsScope } from "@/services/controlPlane/observability/types";
import { createTestQueryClient } from "@/test/renderWithQuery";

import { observabilityServiceKeys } from "@/hooks/controlplane/observability-service-hooks_gen";
import { relativeWindow } from "@/services/controlPlane/observability/api";
import { metricCatalogRequest, metricRequest } from "@/services/controlPlane/observability/metricsApi";

import { useMetric, useMetricCatalog, useMetricExemplars } from "./observability-queries";

const scope: ObsScope = { orgId: "org-1", projectId: "p", envIds: ["env-prod"], filters: [] };

function setup(api: ObservabilityApi) {
  const client = createTestQueryClient();
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={client}>
      <ObservabilityFixtures api={api}>{children}</ObservabilityFixtures>
    </QueryClientProvider>
  );
  return { client, wrapper };
}

describe("metric keys", () => {
  it("name the whole scope in the request, so Refresh and an org switch reach them", () => {
    const catalog = observabilityServiceKeys.listMetrics(metricCatalogRequest({ ...scope, window: relativeWindow(3_600_000) }));
    expect(catalog[0]).toBe("controlplane.v1.ObservabilityService");
    expect(catalog[catalog.length - 1]).toMatchObject({ scope: { orgId: "org-1", projectId: "p", envIds: ["env-prod"] } });
  });

  it("differ by scope, range and name, so two views never share an answer", () => {
    const other: ObsScope = { ...scope, envIds: ["env-dev"] };
    const catalog = (s: ObsScope, lastMs: number, name = "") => observabilityServiceKeys.listMetrics(metricCatalogRequest({ ...s, window: relativeWindow(lastMs), name }));
    const detail = (name: string, kind?: "gauge") => observabilityServiceKeys.getMetric(metricRequest({ ...scope, window: relativeWindow(3_600_000), name, kind }));
    expect(catalog(scope, 3_600_000)).not.toEqual(catalog(other, 3_600_000));
    expect(catalog(scope, 3_600_000)).not.toEqual(catalog(scope, 6 * 3_600_000));
    expect(catalog(scope, 3_600_000, "a")).not.toEqual(catalog(scope, 3_600_000, "b"));
    expect(detail("a")).not.toEqual(detail("a", "gauge"));
  });
});

describe("useMetricCatalog", () => {
  it("reads the catalog through the API seam", async () => {
    const { wrapper } = setup(createFixtureApi());
    const { result } = renderHook(() => useMetricCatalog(scope, "1h"), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data!.metrics.map((m) => m.name)).toContain("rpc.server.duration");
  });

  it("caps a 7-day window at 24 hours, the catalog's own limit", async () => {
    const listMetricCatalog = vi.fn(createFixtureApi().listMetricCatalog);
    const { wrapper } = setup({ ...createFixtureApi(), listMetricCatalog });
    const { result } = renderHook(() => useMetricCatalog(scope, "7d"), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    const { window } = listMetricCatalog.mock.calls[0]![0];
    expect(window.endMs - window.startMs).toBe(24 * 60 * 60_000);
  });

  it("keeps an hour as an hour", async () => {
    const listMetricCatalog = vi.fn(createFixtureApi().listMetricCatalog);
    const { wrapper } = setup({ ...createFixtureApi(), listMetricCatalog });
    const { result } = renderHook(() => useMetricCatalog(scope, "1h", "rpc", 50), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    const req = listMetricCatalog.mock.calls[0]![0];
    expect(req.window.endMs - req.window.startMs).toBe(60 * 60_000);
    expect(req).toMatchObject({ name: "rpc", limit: 50, orgId: "org-1" });
  });

  it("does not call until there is a scope", () => {
    const listMetricCatalog = vi.fn();
    const { wrapper } = setup({ ...createFixtureApi(), listMetricCatalog });
    renderHook(() => useMetricCatalog(null, "1h"), { wrapper });
    expect(listMetricCatalog).not.toHaveBeenCalled();
  });

  it("surfaces FailedPrecondition (not set up) once, without retrying", async () => {
    const listMetricCatalog = vi.fn().mockRejectedValue(new ConnectError("observability is not set up for this organization yet", Code.FailedPrecondition));
    const { wrapper } = setup({ ...createFixtureApi(), listMetricCatalog });
    const { result } = renderHook(() => useMetricCatalog(scope, "1h"), { wrapper });
    await waitFor(() => expect(result.current.isError).toBe(true));
    expect(listMetricCatalog).toHaveBeenCalledTimes(1);
  });
});

describe("useMetric", () => {
  it("is idle until a metric is named", () => {
    const getMetric = vi.fn();
    const { wrapper } = setup({ ...createFixtureApi(), getMetric });
    renderHook(() => useMetric(scope, "1h", undefined), { wrapper });
    expect(getMetric).not.toHaveBeenCalled();
  });

  it("reads one metric's detail", async () => {
    const { wrapper } = setup(createFixtureApi());
    const { result } = renderHook(() => useMetric(scope, "1h", "rpc.server.duration"), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data!.attributes.some((a) => a.highCardinality)).toBe(true);
    expect(result.current.data!.suggestedBreakdowns[0]).toBe("status");
  });

  it("surfaces NotFound for a metric with no data", async () => {
    const { wrapper } = setup(createFixtureApi());
    const { result } = renderHook(() => useMetric(scope, "1h", "nope.not.there"), { wrapper });
    await waitFor(() => expect(result.current.isError).toBe(true));
  });
});

describe("useMetricExemplars", () => {
  it("waits for a request, then reads stored traces first", async () => {
    const { wrapper } = setup(createFixtureApi());
    const { result, rerender } = renderHook(({ req }) => useMetricExemplars(scope, "1h", req), { wrapper, initialProps: { req: null as { name: string } | null } });
    expect(result.current.fetchStatus).toBe("idle");
    rerender({ req: { name: "rpc.server.duration" } });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    const stored = result.current.data!.map((e) => e.traceStored);
    expect(stored).toEqual([...stored].sort((a, b) => Number(b) - Number(a)));
    expect(stored[0]).toBe(true);
    expect(stored).toContain(false);
  });

  it("can be switched off", () => {
    const listMetricExemplars = vi.fn();
    const { wrapper } = setup({ ...createFixtureApi(), listMetricExemplars });
    renderHook(() => useMetricExemplars(scope, "1h", { name: "m" }, { enabled: false }), { wrapper });
    expect(listMetricExemplars).not.toHaveBeenCalled();
  });
});
