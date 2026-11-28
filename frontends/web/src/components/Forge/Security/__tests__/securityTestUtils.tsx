// Copyright (c) 2025 Reliant Labs

/**
 * Renders a Security view against a fixture VulnerabilityService
 * (fixtures.ts), with the URL's filters held in a harness so a filter click
 * re-renders on the new search, as the router would.
 *
 * The fixture is served on the wire (vulnService.ts) under a TransportScope,
 * so the pages' generated reads, wire.ts's requests and mapping included, run
 * as they do in production. `null` renders with no scope: the app's transport
 * (a test's setTransport), as the demo and production render.
 */

import { useState, type ReactNode } from "react";
import { act, render } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

import { transport as appTransport } from "@/lib/connect";
import { createTransportScope, TransportScopeProvider } from "@/lib/transportScope";

import { FIXTURE_PROJECT } from "../fixtures";
import { VulnerabilitiesView, type VulnerabilitiesViewProps } from "../VulnerabilitiesPage";
import type { VulnSearch } from "../vulnModel";
import { vulnerabilityTransport, type VulnerabilityReads } from "../vulnService";

const testClient = () => new QueryClient({ defaultOptions: { queries: { retry: false } } });

/**
 * A TransportScope whose VulnerabilityService answers from `reads`, on a
 * QueryClient of its own (or `queryClient`). Every other service stays on the
 * app's transport, as it was before the scope.
 */
export function vulnerabilityScope(reads: VulnerabilityReads, queryClient: QueryClient = testClient()) {
  return createTransportScope({ id: "security-fixture", transport: vulnerabilityTransport(reads, appTransport), queryClient });
}

export function renderWith(reads: VulnerabilityReads | null, children: ReactNode) {
  if (reads === null) return render(<QueryClientProvider client={testClient()}>{children}</QueryClientProvider>);
  return render(<TransportScopeProvider scope={vulnerabilityScope(reads)}>{children}</TransportScopeProvider>);
}

export function renderVulnerabilities(
  reads: VulnerabilityReads | null,
  { environmentId, search: initial = {}, patch }: { environmentId?: string; search?: VulnSearch; patch?: VulnerabilitiesViewProps["patch"] } = {}
) {
  let current = initial;
  let set: (next: VulnSearch) => void = () => undefined;
  function Harness() {
    const [search, setSearch] = useState<VulnSearch>(initial);
    current = search;
    set = setSearch;
    return (
      <VulnerabilitiesView
        scope={{ project: FIXTURE_PROJECT, environmentId }}
        search={search}
        onSearch={setSearch}
        advisoryHref={(id) => `/forge/p/shop/security/vulnerabilities/${id}`}
        sbomsHref={(release) => `/forge/p/shop/security/sboms${release ? `?release=${release}` : ""}`}
        patch={patch}
      />
    );
  }
  const utils = renderWith(reads, <Harness />);
  return { ...utils, search: () => current, go: (next: VulnSearch) => act(() => set(next)) };
}
