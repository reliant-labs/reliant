// Copyright (c) 2025 Reliant Labs

/**
 * Renders a Security view against the fixture api (fixtures.ts), with the
 * URL's filters held in a harness so a filter click re-renders on the new
 * search, as the router would.
 */

import { useState, type ReactNode } from "react";
import { act, render } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

import { VulnerabilityApiProvider, type VulnerabilityApi } from "@/services/controlPlane/vulnerabilities/VulnerabilityApiContext";

import { FIXTURE_PROJECT } from "../fixtures";
import { VulnerabilitiesView, type VulnerabilitiesViewProps } from "../VulnerabilitiesPage";
import type { VulnSearch } from "../vulnModel";

export function renderWith(api: VulnerabilityApi, children: ReactNode) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <VulnerabilityApiProvider api={api}>{children}</VulnerabilityApiProvider>
    </QueryClientProvider>
  );
}

export function renderVulnerabilities(
  api: VulnerabilityApi,
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
  const utils = renderWith(api, <Harness />);
  return { ...utils, search: () => current, go: (next: VulnSearch) => act(() => set(next)) };
}
