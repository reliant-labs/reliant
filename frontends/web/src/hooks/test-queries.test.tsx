// Copyright (c) 2026 Reliant Labs
// @vitest-environment jsdom

import { create } from "@bufbuild/protobuf";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { expect, it, vi } from "vitest";

import { GetCoverageResponseSchema } from "@/gen/controlplane/services/tests/v1/tests_pb";
import { testsServiceQueries } from "@/hooks/controlplane/tests-service-hooks_gen";
import { useCoverage, type CoverageQuery } from "@/hooks/test-queries";
import { coverageRequest } from "@/services/controlPlane/tests/wire";

vi.mock("@/lib/connect", () => ({ connectClient: () => ({}) }));
vi.mock("@/lib/orgScope", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/orgScope")>()),
  // Hold network reads while exercising the observer's cache/placeholder behavior.
  useOrgScope: () => ({ orgId: null, ready: false, error: null, retry: () => {} }),
}));

it("keeps null coverage separate from cached default requests and clears previous data across null", async () => {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const project = "project";
  const defaultQuery: CoverageQuery = { target: { gitCommit: "" }, path: "" };
  const nextQuery: CoverageQuery = { target: { gitCommit: "next" }, path: "" };
  const key = (query: CoverageQuery) => testsServiceQueries.getCoverage(coverageRequest({ orgId: null, project, ...query })).queryKey;
  client.setQueryData(key(defaultQuery), create(GetCoverageResponseSchema, { gitCommit: "cached-default" }));
  const wrapper = ({ children }: { children: ReactNode }) => <QueryClientProvider client={client}>{children}</QueryClientProvider>;
  const { result, rerender, unmount } = renderHook(({ query }: { query: CoverageQuery | null }) => useCoverage(project, query), {
    wrapper,
    initialProps: { query: null as CoverageQuery | null },
  });
  try {
    expect(result.current.data).toBeUndefined();
    rerender({ query: defaultQuery });
    expect(result.current.data?.gitCommit).toBe("cached-default");
    rerender({ query: null });
    expect(result.current.data).toBeUndefined();
    rerender({ query: nextQuery });
    expect(result.current.data).toBeUndefined();
    act(() => {
      client.setQueryData(key(nextQuery), create(GetCoverageResponseSchema, { gitCommit: "next" }));
    });
    await waitFor(() => expect(result.current.data?.gitCommit).toBe("next"));
  } finally {
    unmount();
    client.clear();
  }
});
