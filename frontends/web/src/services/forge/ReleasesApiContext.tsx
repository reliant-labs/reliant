// Copyright (c) 2025 Reliant Labs

/**
 * The seam between the Releases pages and the DeployService reads behind
 * them — the same seam ObservabilityApiContext is for telemetry.
 *
 * Every hook in hooks/release-queries.ts calls through this context, so the
 * pages can render against something other than control-plane: component
 * tests and the dev-only preview route (/forge-releases-preview) hand it
 * fixtures. The default is the real client, BOUND TO AN ORG: every read it
 * makes names the org the hook's key names (lib/orgScope), so a cached answer
 * is always that org's. Production never provides anything else.
 */

import { createContext, useContext, useMemo, type ReactNode } from "react";

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

/** The real client's reads in `orgId` (null: none named): each request names it. */
export function controlPlaneReleasesApi(orgId: string | null): ReleasesApi {
  return {
    listReleases: (project, limit) => releases.listProjectReleases(project, limit, orgId),
    getRelease: (project, version) => releases.getProjectRelease(project, version, orgId),
    compareReleases: (req) => releases.compareProjectReleases(req, orgId),
    listPromotions: (environmentId, limit) => releases.listReleasePromotions(environmentId, limit, orgId),
    getRollout: (environmentId, promotionId) => releases.getPromotionRollout(environmentId, promotionId, orgId),
    getBundle: (environmentId, bundleId, digest) => releases.getPromotionBundle(environmentId, bundleId, digest, orgId),
    listDeployRuns: (project, env, limit) => deployRuns.listDeployRuns(project, env, limit, orgId),
    getDeployRun: (id) => deployRuns.getDeployRun(id, orgId),
  };
}

const ReleasesApiContext = createContext<ReleasesApi | null>(null);

export function ReleasesApiProvider({ api, children }: { api: ReleasesApi; children: ReactNode }) {
  return <ReleasesApiContext.Provider value={api}>{children}</ReleasesApiContext.Provider>;
}

/** The Releases reads in `orgId`: a provided api (fixtures) as it is, else the real client bound to the org. */
export function useReleasesApi(orgId: string | null): ReleasesApi {
  const provided = useContext(ReleasesApiContext);
  return useMemo(() => provided ?? controlPlaneReleasesApi(orgId), [provided, orgId]);
}
