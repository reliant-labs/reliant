/**
 * CatalogService reads share the generated key scope: every catalog read —
 * an entry, the trigger-type search, the palette's paged search and the
 * input-option lists — sits under `catalogServiceKeys.all`, so a connection
 * made through the connect dialog (which flips `connected` on all of them)
 * refetches every mounted one, and nothing keeps a stale "Not connected".
 */

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import type { ReactNode } from "react";

import { CatalogEntryKind } from "@/gen/reliant/v1/catalog_pb";
import { ApiKeyConnectionKind } from "@/gen/reliant/v1/connection_pb";
import { setTransport } from "@/lib/connect";
import { mockTransport } from "@/lib/test-utils";
import { useCatalogSearch } from "@/components/workflow/palette/useCatalogSearch";
import { catalogServiceKeys } from "../catalog-service-hooks_gen";
import { useCatalogEntry, useIntegrationEventNaming } from "../catalog-queries";
import { useCreateApiKeyConnection } from "../connection-queries";
import { useInputOptions } from "../input-options-queries";

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

/** A catalog whose one GitHub entry becomes connected once a connection is saved. */
function catalogTransport() {
  let connected = false;
  const calls: Record<string, number> = {};
  const count = (name: string) => (calls[name] = (calls[name] ?? 0) + 1);
  const summary = () => ({
    ref: "github/issue.create@1",
    kind: CatalogEntryKind.ACTION,
    id: "issue.create",
    displayName: "Create issue",
    integration: { id: "github", displayName: "GitHub" },
    connectionRequired: true,
    connected,
  });
  const transport = mockTransport({
    "CatalogService.GetCatalogEntry": () => {
      count("GetCatalogEntry");
      return { entry: { summary: summary() } };
    },
    "CatalogService.SearchCatalog": () => {
      count("SearchCatalog");
      return { entries: [summary()], totalSize: 1 };
    },
    "CatalogService.ListInputOptions": () => {
      count("ListInputOptions");
      return { options: connected ? [{ value: "acme/api" }] : [] };
    },
    "ConnectionService.CreateApiKeyConnection": () => {
      connected = true;
      return { connection: { id: "c1", integrationId: "github" } };
    },
  });
  return { transport, calls: (name: string) => calls[name] ?? 0 };
}

describe("CatalogService keys", () => {
  it("saving a connection refetches the mounted catalog entry, so it reads connected", async () => {
    const { transport, calls } = catalogTransport();
    setTransport(transport);

    const entry = renderHook(() => useCatalogEntry("github/issue.create@1"), { wrapper });
    await waitFor(() => expect(entry.result.current.data?.summary.connected).toBe(false));

    const create = renderHook(() => useCreateApiKeyConnection(), { wrapper });
    await create.result.current.mutateAsync({ integrationId: "github", name: "", kind: ApiKeyConnectionKind.API_KEY, fields: {}, params: {} });

    await waitFor(() => expect(entry.result.current.data?.summary.connected).toBe(true));
    expect(calls("GetCatalogEntry")).toBe(2);
  });

  it("invalidating catalogServiceKeys.all reaches the entry, the trigger types, the paged search and the input options", async () => {
    const { transport, calls } = catalogTransport();
    setTransport(transport);

    const entry = renderHook(() => useCatalogEntry("github/issue.create@1"), { wrapper });
    const naming = renderHook(() => useIntegrationEventNaming(), { wrapper });
    const search = renderHook(() => useCatalogSearch({ query: "issue", kind: "action" }), { wrapper });
    const options = renderHook(() => useInputOptions({ type: "github.repository" }), { wrapper });
    await waitFor(() => {
      expect(entry.result.current.isSuccess).toBe(true);
      expect(search.result.current.entries).toHaveLength(1);
      expect(options.result.current.isSuccess).toBe(true);
    });
    expect(naming.result.current).toBeDefined();
    const before = { search: calls("SearchCatalog"), options: calls("ListInputOptions") };

    await queryClient.invalidateQueries({ queryKey: catalogServiceKeys.all });

    await waitFor(() => {
      expect(calls("GetCatalogEntry")).toBe(2);
      // The trigger-type search and the palette search are both SearchCatalog.
      expect(calls("SearchCatalog")).toBe(before.search + 2);
      expect(calls("ListInputOptions")).toBe(before.options + 1);
    });
  });
});
