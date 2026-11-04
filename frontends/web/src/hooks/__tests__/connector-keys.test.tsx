/**
 * ConnectorService is on forge's generated keys: the Machines page's grant
 * list (`useConnectors`, components/Settings/cloud/machineAccess) and the OAuth
 * consent screen read one `connectorServiceKeys.listConnectors({})` entry, and
 * each write is a generated mutation. These pin that every write that changes
 * the grants refetches the mounted list:
 *
 *   1. CreateConnector (invalidates `connectorServiceKeys.connector`);
 *   2. RevokeConnector (invalidates every ConnectorService read);
 *   3. AuthorizeClient, which can create a grant (every ConnectorService read).
 */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";

import { setTransport } from "@/lib/connect";
import { mockTransport } from "@/lib/test-utils";
import { serviceHandlers } from "@/test/serviceTransport";
import { useConnectors } from "@/components/Settings/cloud/machineAccess";
import { useAuthorizeClient, useCreateConnector, useRevokeConnector } from "../connector-service-hooks_gen";

let grants: { id: string; daemonId: string; name: string; revokedAt?: string }[];
let listCalls: number;

let client: QueryClient;
function wrapper({ children }: { children: ReactNode }) {
  return <QueryClientProvider client={client}>{children}</QueryClientProvider>;
}

const createConnector = vi.fn((req: { name: string; daemonId: string }) => {
  grants = [...grants, { id: `g-${grants.length + 1}`, daemonId: req.daemonId, name: req.name }];
  return { connector: grants[grants.length - 1], credential: "rlc_secret", mcpUrl: "https://mcp.example" };
});

beforeEach(() => {
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  grants = [{ id: "g-1", daemonId: "d-1", name: "ChatGPT" }];
  listCalls = 0;
  createConnector.mockClear();
  setTransport(
    mockTransport(
      serviceHandlers("ConnectorService", {
        listConnectors: () => {
          listCalls++;
          return { connectors: grants };
        },
        createConnector,
        revokeConnector: (req: { id: string }) => {
          grants = grants.map((g) => (g.id === req.id ? { ...g, revokedAt: "2026-10-01T00:00:00Z" } : g));
          return { revoked: true };
        },
        authorizeClient: (req: { newConnector?: { name: string; daemonId: string } }) => {
          if (req.newConnector) createConnector(req.newConnector);
          return {};
        },
      }),
    ),
  );
});

afterEach(() => {
  setTransport(null);
  client.clear();
});

function mountList() {
  return renderHook(() => useConnectors(), { wrapper });
}

const names = (data: { name: string; revokedAt?: string }[] | undefined) =>
  data?.map((g) => `${g.name}${g.revokedAt ? " (revoked)" : ""}`);

describe("ConnectorService keys", () => {
  it("a create refetches the mounted grant list", async () => {
    const list = mountList();
    await waitFor(() => expect(names(list.result.current.data)).toEqual(["ChatGPT"]));
    expect(listCalls).toBe(1);

    const create = renderHook(() => useCreateConnector(), { wrapper });
    await act(() => create.result.current.mutateAsync({ name: "Claude", daemonId: "d-1" }));

    await waitFor(() => expect(names(list.result.current.data)).toEqual(["ChatGPT", "Claude"]));
    expect(listCalls).toBe(2);
  });

  it("a revoke refetches the mounted grant list", async () => {
    const list = mountList();
    await waitFor(() => expect(listCalls).toBe(1));

    const revoke = renderHook(() => useRevokeConnector(), { wrapper });
    await act(() => revoke.result.current.mutateAsync({ id: "g-1" }));

    await waitFor(() => expect(names(list.result.current.data)).toEqual(["ChatGPT (revoked)"]));
  });

  it("an authorization that creates a grant refetches the mounted grant list", async () => {
    const list = mountList();
    await waitFor(() => expect(listCalls).toBe(1));

    const authorize = renderHook(() => useAuthorizeClient(), { wrapper });
    await act(() =>
      authorize.result.current.mutateAsync({
        clientId: "client-1",
        newConnector: { name: "Claude desktop", daemonId: "d-1" },
      }),
    );

    await waitFor(() => expect(names(list.result.current.data)).toEqual(["ChatGPT", "Claude desktop"]));
  });
});
