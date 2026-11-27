// Copyright (c) 2025 Reliant Labs

/**
 * Renders the real StorageBrowser in a TransportScope over the fixture's
 * StorageBrowserService (its real calls, on a mock transport), with the URL state
 * held in a harness: onNavigate replaces it (so a click that changes the view
 * re-renders the browser on the new state, as the router would), and every
 * navigation is recorded. Links render real hrefs (withSearch, as the console
 * builds them), read back with `searchOf`. Minted download URLs land in
 * `downloads` instead of navigating the test's window.
 */

import { useState } from "react";
import { beforeEach } from "vitest";
import { setReaderPrincipal } from "../../AuditedAccess/readerSession";
beforeEach(() => setReaderPrincipal("fixture-reader", true));
import { act, render } from "@testing-library/react";
import { defaultParseSearch } from "@tanstack/react-router";
import { QueryClient } from "@tanstack/react-query";

import { createTransportScope, TransportScopeProvider } from "@/lib/transportScope";

import { withSearch } from "../../Console/paths";
import { storageFixtureTransport, type StorageFixture } from "../__fixtures__/fixtureStorageService";
import { StorageBrowser } from "../StorageBrowser";
import type { StorageSearch } from "../storageUrl-model";

export const STAGING = { id: "env-staging", name: "staging" };
export const PROD = { id: "env-prod", name: "prod" };

export function storageHref(next: StorageSearch): string {
  return withSearch("/storage", next as Record<string, unknown>);
}

/** The StorageSearch a link's href carries. */
export function searchOf(href: string | null): StorageSearch {
  const value = href ?? "";
  return defaultParseSearch(value.slice(value.indexOf("?"))) as StorageSearch;
}

export function renderBrowser(api: StorageFixture, initial: StorageSearch = {}, env: { id: string; name: string } = STAGING, options: { pageSize?: number } = {}) {
  const navigations: { search: StorageSearch; replace: boolean }[] = [];
  const downloads: string[] = [];
  let current = initial;
  let set: (next: StorageSearch) => void = () => undefined;
  function Harness() {
    const [search, setSearch] = useState<StorageSearch>(initial);
    current = search;
    set = setSearch;
    return (
      <StorageBrowser
        env={env}
        search={search}
        onNavigate={(next, navOptions) => {
          navigations.push({ search: next, replace: !!navOptions?.replace });
          setSearch(next);
        }}
        href={storageHref}
        onDownloadUrl={(url) => downloads.push(url)}
        pageSize={options.pageSize}
      />
    );
  }
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const scope = createTransportScope({ id: "storage-fixture", transport: storageFixtureTransport(api), queryClient: client });
  const utils = render(
    <TransportScopeProvider scope={scope}>
      <Harness />
    </TransportScopeProvider>
  );
  return {
    ...utils,
    client,
    navigations,
    downloads,
    search: () => current,
    /** Follow a link, as the router would. */
    go: (next: StorageSearch) => act(() => set(next)),
  };
}

/** The calls of one method made, newest last. */
export function callsOf(api: StorageFixture, method: StorageFixture["calls"][number]["method"]) {
  return api.calls.filter((call) => call.method === method).map((call) => call.req as Record<string, unknown>);
}
