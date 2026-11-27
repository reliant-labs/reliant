// Copyright (c) 2025 Reliant Labs

import { createClient } from "@connectrpc/connect";
import { QueryClient, QueryClientProvider, useQuery } from "@tanstack/react-query";
import { renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { expect, it } from "vitest";

import { AppDatabaseService } from "@/gen/controlplane/services/app_database/v1/app_database_pb";
import { scopedClient, TransportScopeProvider } from "@/lib/transportScope";

import { dataFixtureScope, dataFixtureTransport } from "./dataFixtureTransport";
import { createDataFixtureApi } from "./fixtureDataApi";

it("routes scoped queries to the fixture and caches them on the scope's QueryClient", async () => {
  const outerClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const scopeClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const fixture = createDataFixtureApi();
  const fallback = createDataFixtureApi({ noDatabases: true });
  const scope = dataFixtureScope(fixture, scopeClient);
  const clientOf = scopedClient(AppDatabaseService, createClient(AppDatabaseService, dataFixtureTransport(fallback)));
  const queryKey = ["data-fixture-scope-test"];
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={outerClient}>
      <TransportScopeProvider scope={scope}>{children}</TransportScopeProvider>
    </QueryClientProvider>
  );
  const { result, unmount } = renderHook(() => useQuery({
    queryKey,
    queryFn: (context) => clientOf(context).listBrowsableDatabases({ environmentId: "env-dev" }),
  }), { wrapper });

  try {
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data?.databases.map((db) => db.name)).toContain("shop");
    expect(fixture.calls).toHaveLength(1);
    expect(fixture.calls[0].method).toBe("listBrowsableDatabases");
    expect(fallback.calls).toHaveLength(0);
    expect(scopeClient.getQueryData(queryKey)).toEqual(result.current.data);
    expect(outerClient.getQueryCache().getAll()).toHaveLength(0);
  } finally {
    unmount();
    scopeClient.clear();
    outerClient.clear();
  }
});
