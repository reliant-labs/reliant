// Copyright (c) 2025 Reliant Labs

/**
 * The seam between the Experiments page and ChangesService, as
 * ObservabilityApiContext is for the observability views: component tests
 * hand it an in-memory fake, and the default is the live control plane.
 * Production never provides anything else.
 */

import { createContext, useContext, type ReactNode } from "react";

import { liveChangesApi, type ChangesApi } from "./changesApi";

const ChangesApiContext = createContext<ChangesApi>(liveChangesApi);

export function ChangesApiProvider({ api, children }: { api: ChangesApi; children: ReactNode }) {
  return <ChangesApiContext.Provider value={api}>{children}</ChangesApiContext.Provider>;
}

export function useChangesApi(): ChangesApi {
  return useContext(ChangesApiContext);
}
