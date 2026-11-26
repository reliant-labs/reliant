// Copyright (c) 2026 Reliant Labs

/**
 * The seam between the coverage file view (components/Forge/Tests) and
 * wherever a file's source comes from (coverageSource.ts).
 *
 * Component tests and the preview hand it a fixture fetcher
 * (components/Forge/Tests/coverageFixtures.ts), and production never
 * provides anything but the real one, which today answers
 * `not-yet-available` (reliant's GetCoverageSource is W2b). The default is
 * that fetch on the nearest TransportScope's transport, else on the app's.
 */

import { createContext, useContext, type ReactNode } from "react";
import type { Transport } from "@connectrpc/connect";

import { transport as appTransport } from "@/lib/connect";
import { useTransport } from "@/lib/transportScope";

import { fetchCoverageSource, type FetchCoverageSource } from "./coverageSource";

const scopedFetches = new WeakMap<Transport, FetchCoverageSource>();

/**
 * The real fetch on `transport` (a TransportScope's, lib/transportScope.tsx),
 * memoized per transport. The app's transport is fetchCoverageSource itself.
 */
export function coverageSourceFetchOf(transport: Transport): FetchCoverageSource {
  if (transport === appTransport) return fetchCoverageSource;
  let fetch = scopedFetches.get(transport);
  if (!fetch) {
    fetch = (project, blobId, opts) => fetchCoverageSource(project, blobId, opts, transport);
    scopedFetches.set(transport, fetch);
  }
  return fetch;
}

const CoverageSourceContext = createContext<FetchCoverageSource | null>(null);

export function CoverageSourceProvider({ fetch, children }: { fetch: FetchCoverageSource; children: ReactNode }) {
  return <CoverageSourceContext.Provider value={fetch}>{children}</CoverageSourceContext.Provider>;
}

/** The provided fetch, else the real one on the nearest TransportScope's transport, else on the app's. */
export function useCoverageSourceFetch(): FetchCoverageSource {
  const provided = useContext(CoverageSourceContext);
  const transport = useTransport(appTransport);
  return provided ?? coverageSourceFetchOf(transport);
}
