// Copyright (c) 2025 Reliant Labs

/**
 * The seam between the Releases pages and the DeployService reads behind
 * them — the same seam ObservabilityApiContext is for telemetry.
 *
 * Every hook in hooks/release-queries.ts calls through this context, so the
 * pages can render against something other than control-plane: component
 * tests and the dev-only preview route (/forge-releases-preview) hand it
 * fixtures. The default is the real client; production never provides
 * anything else.
 */

import { createContext, useContext, type ReactNode } from "react";

import * as deployRuns from "./deployAttempts";
import * as releases from "./releases";

export interface ReleasesApi {
  listReleases: typeof releases.listProjectReleases;
  getRelease: typeof releases.getProjectRelease;
  compareReleases: typeof releases.compareProjectReleases;
  listPromotions: typeof releases.listReleasePromotions;
  getRollout: typeof releases.getPromotionRollout;
  getBundle: typeof releases.getPromotionBundle;
  /** The project's deploys (one per `forge env deploy` run), and one with its log. */
  listDeployRuns: typeof deployRuns.listDeployRuns;
  getDeployRun: typeof deployRuns.getDeployRun;
}

export const controlPlaneReleasesApi: ReleasesApi = {
  listReleases: releases.listProjectReleases,
  getRelease: releases.getProjectRelease,
  compareReleases: releases.compareProjectReleases,
  listPromotions: releases.listReleasePromotions,
  getRollout: releases.getPromotionRollout,
  getBundle: releases.getPromotionBundle,
  listDeployRuns: deployRuns.listDeployRuns,
  getDeployRun: deployRuns.getDeployRun,
};

const ReleasesApiContext = createContext<ReleasesApi>(controlPlaneReleasesApi);

export function ReleasesApiProvider({ api, children }: { api: ReleasesApi; children: ReactNode }) {
  return <ReleasesApiContext.Provider value={api}>{children}</ReleasesApiContext.Provider>;
}

export function useReleasesApi(): ReleasesApi {
  return useContext(ReleasesApiContext);
}
