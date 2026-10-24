/**
 * ConnectionService reads and writes share the generated key scope: saving a
 * connection refetches the mounted connection list (so a picker never shows a
 * stale one), and the OAuth path's invalidateConnectionQueries — the flow
 * itself calls the client directly — reaches every connection read and every
 * catalog read, whose `connected` flags the new connection changes.
 */

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import type { ReactNode } from "react";

import { ConnectionAuthKind, IntegrationAuthMethodSchema, type IntegrationAuthMethod } from "@/gen/reliant/v1/connection_pb";
import { create } from "@bufbuild/protobuf";
import { setTransport } from "@/lib/connect";
import { mockTransport } from "@/lib/test-utils";
import { useCatalogEntry } from "../catalog-queries";
import {
  apiKeyConnectionRequest,
  createdConnection,
  invalidateConnectionQueries,
  useConnections,
  useCreateApiKeyConnection,
  useUnavailableIntegrations,
} from "../connection-queries";

const queryClient = new QueryClient({
  defaultOptions: { queries: { retry: false, gcTime: 0 }, mutations: { retry: false } },
});

function wrapper({ children }: { children: ReactNode }) {
  return <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;
}

afterEach(() => {
  cleanup();
  queryClient.clear();
  setTransport(null);
});

function connectionTransport() {
  const saved: Array<{ id: string; integrationId: string; name: string }> = [];
  const calls: Record<string, number> = {};
  const count = (name: string) => (calls[name] = (calls[name] ?? 0) + 1);
  const oauthOff: IntegrationAuthMethod = create(IntegrationAuthMethodSchema, { kind: ConnectionAuthKind.OAUTH2, available: false });
  const transport = mockTransport({
    "ConnectionService.ListConnections": () => {
      count("ListConnections");
      return { connections: [...saved] };
    },
    "ConnectionService.ListIntegrations": () => {
      count("ListIntegrations");
      return { integrations: [{ id: "gmail", methods: [oauthOff] }] };
    },
    "ConnectionService.CreateApiKeyConnection": (req) => {
      const { integrationId, name } = req as { integrationId: string; name: string };
      const connection = { id: `c${saved.length + 1}`, integrationId, name };
      saved.push(connection);
      return { connection };
    },
    "CatalogService.GetCatalogEntry": () => {
      count("GetCatalogEntry");
      return { entry: { summary: { ref: "github/issue.create@1", connected: saved.length > 0 } } };
    },
  });
  return { transport, calls: (name: string) => calls[name] ?? 0 };
}

describe("ConnectionService keys", () => {
  it("saving a pasted credential refetches the mounted connection list and the catalog entry", async () => {
    const { transport, calls } = connectionTransport();
    setTransport(transport);

    const list = renderHook(() => useConnections("github"), { wrapper });
    const entry = renderHook(() => useCatalogEntry("github/issue.create@1"), { wrapper });
    await waitFor(() => {
      expect(list.result.current.data).toEqual([]);
      expect(entry.result.current.data?.summary.connected).toBe(false);
    });

    const create = renderHook(() => useCreateApiKeyConnection(), { wrapper });
    const response = await create.result.current.mutateAsync(
      apiKeyConnectionRequest({ integrationId: "github", name: "work", kind: "api_key", fields: { api_key: "k" }, params: {} }),
    );
    expect(createdConnection(response)).toMatchObject({ id: "c1", integrationId: "github", name: "work" });

    await waitFor(() => {
      expect(list.result.current.data?.map((c) => c.id)).toEqual(["c1"]);
      expect(entry.result.current.data?.summary.connected).toBe(true);
    });
    expect(calls("ListConnections")).toBe(2);
    expect(calls("GetCatalogEntry")).toBe(2);
  });

  it("invalidateConnectionQueries (the OAuth path) reaches every connection and catalog read", async () => {
    const { transport, calls } = connectionTransport();
    setTransport(transport);

    const list = renderHook(() => useConnections("github"), { wrapper });
    const unavailable = renderHook(() => useUnavailableIntegrations(), { wrapper });
    const entry = renderHook(() => useCatalogEntry("github/issue.create@1"), { wrapper });
    await waitFor(() => {
      expect(list.result.current.isSuccess).toBe(true);
      expect(unavailable.result.current.has("gmail")).toBe(true);
      expect(entry.result.current.isSuccess).toBe(true);
    });

    invalidateConnectionQueries(queryClient);

    await waitFor(() => {
      expect(calls("ListConnections")).toBe(2);
      expect(calls("ListIntegrations")).toBe(2);
      expect(calls("GetCatalogEntry")).toBe(2);
    });
  });
});
