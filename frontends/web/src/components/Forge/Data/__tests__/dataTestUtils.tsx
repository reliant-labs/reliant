// Copyright (c) 2025 Reliant Labs

/**
 * Renders the real DataBrowser against the fixture api, served on the wire in
 * a TransportScope (dataFixtureTransport.ts), with the URL state
 * held in a harness: onNavigate replaces it (so a click that changes the view
 * re-renders the browser on the new state, as the router would), and every
 * navigation is recorded. Links render real hrefs (withSearch, as the console
 * builds them), read back with `searchOf`.
 */

import { useState, type ReactNode } from "react";
import { beforeEach } from "vitest";
import { setReaderPrincipal } from "../../AuditedAccess/readerSession";
beforeEach(() => setReaderPrincipal("fixture-reader", true));
import { act, render } from "@testing-library/react";
import { defaultParseSearch } from "@tanstack/react-router";
import { QueryClient } from "@tanstack/react-query";

import { TransportScopeProvider } from "@/lib/transportScope";

import { withSearch } from "../../Console/paths";
import { dataFixtureScope } from "../__fixtures__/dataFixtureTransport";
import type { DataFixtureApi } from "../__fixtures__/fixtureDataApi";
import { DataBrowser } from "../DataBrowser";
import type { DataSearch } from "../dataUrl-model";

export const STAGING = { id: "env-staging", name: "staging" };
export const PROD = { id: "env-prod", name: "prod" };

export function dataHref(next: DataSearch): string {
  return withSearch("/data", next as Record<string, unknown>);
}

/** The DataSearch a link's href carries. */
export function searchOf(href: string | null): DataSearch {
  return defaultParseSearch((href ?? "").slice((href ?? "").indexOf("?"))) as DataSearch;
}

/** A wrapper serving `api` in a TransportScope over a fresh test QueryClient (no retries). */
export function dataFixtureWrapper(api: DataFixtureApi) {
  const scope = dataFixtureScope(api, new QueryClient({ defaultOptions: { queries: { retry: false } } }));
  return ({ children }: { children: ReactNode }) => <TransportScopeProvider scope={scope}>{children}</TransportScopeProvider>;
}

export function renderBrowser(api: DataFixtureApi, initial: DataSearch = {}, env: { id: string; name: string } = STAGING) {
  const navigations: { search: DataSearch; replace: boolean }[] = [];
  let current = initial;
  let set: (next: DataSearch) => void = () => undefined;
  function Harness() {
    const [search, setSearch] = useState<DataSearch>(initial);
    current = search;
    set = setSearch;
    return (
      <DataBrowser
        env={env}
        search={search}
        onNavigate={(next, options) => {
          navigations.push({ search: next, replace: !!options?.replace });
          setSearch(next);
        }}
        href={dataHref}
      />
    );
  }
  const utils = render(<Harness />, { wrapper: dataFixtureWrapper(api) });
  return {
    ...utils,
    navigations,
    search: () => current,
    /** Follow a link, as the router would. */
    go: (next: DataSearch) => act(() => set(next)),
  };
}

/** The browseTable calls made, newest last. */
export function browseCalls(api: DataFixtureApi) {
  return api.calls.filter((call) => call.method === "browseTable").map((call) => call.req as Record<string, unknown>);
}
