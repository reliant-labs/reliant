// Copyright (c) 2026 Reliant Labs

/**
 * The seam between the coverage file view (components/Forge/Tests) and
 * wherever a file's source comes from (coverageSource.ts).
 *
 * The same shape as TestApiContext: component tests and the preview hand it
 * a fixture fetcher (components/Forge/Tests/coverageFixtures.ts), and
 * production never provides anything but the real one, which today answers
 * `not-yet-available` (reliant's GetCoverageSource is W2b).
 */

import { createContext, useContext, type ReactNode } from "react";

import { fetchCoverageSource, type FetchCoverageSource } from "./coverageSource";

const CoverageSourceContext = createContext<FetchCoverageSource>(fetchCoverageSource);

export function CoverageSourceProvider({ fetch, children }: { fetch: FetchCoverageSource; children: ReactNode }) {
  return <CoverageSourceContext.Provider value={fetch}>{children}</CoverageSourceContext.Provider>;
}

export function useCoverageSourceFetch(): FetchCoverageSource {
  return useContext(CoverageSourceContext);
}
