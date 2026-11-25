// Copyright (c) 2025 Reliant Labs

/**
 * The seam between the Releases pages and the DeployService reads behind
 * them — the same seam ObservabilityApiContext is for telemetry.
 *
 * Every hook in hooks/release-queries.ts calls through this context, so the
 * pages can render against something other than control-plane: component
 * tests hand it fixtures. The default is the real client, BOUND TO AN ORG:
 * every read it makes names the org the hook's key names (lib/orgScope), so a
 * cached answer is always that org's. Production never provides anything
 * else, and neither does the dev-only preview (/forge-releases-preview),
 * which runs the real client over the shop fixture pack's transport.
 *
 * The default reads over the nearest TransportScope's transport
 * (lib/transportScope.tsx), the app's outside every scope: a scoped subtree
 * (the forge tour's sample app) gets its scope's answers with no provider.
 */

import { createContext, useContext, useMemo, type ReactNode } from "react";
import type { Transport } from "@connectrpc/connect";

import { transport as appTransport } from "@/lib/connect";
import { useTransport } from "@/lib/transportScope";

import * as deployRuns from "./deployAttempts";
import * as releases from "./releases";

/** A service read with its transport already bound: the seam's methods name no transport. */
type Bound<F> = F extends (transport: Transport, ...args: infer A) => infer R ? (...args: A) => R : never;

export interface ReleasesApi {
  listReleases: Bound<typeof releases.listProjectReleases>;
  getRelease: Bound<typeof releases.getProjectRelease>;
  compareReleases: Bound<typeof releases.compareProjectReleases>;
  listPromotions: Bound<typeof releases.listReleasePromotions>;
  getRollout: Bound<typeof releases.getPromotionRollout>;
  getBundle: Bound<typeof releases.getPromotionBundle>;
  /** The project's deploys (one per `forge env deploy` run), and one with its log. */
  listDeployRuns: Bound<typeof deployRuns.listDeployRuns>;
  getDeployRun: Bound<typeof deployRuns.getDeployRun>;
}

/** The real client's reads over `transport`, in `orgId` (null: none named): each request names it. */
export function releasesApiOf(transport: Transport, orgId: string | null): ReleasesApi {
  return {
    listReleases: (project, limit) => releases.listProjectReleases(transport, project, limit, orgId),
    getRelease: (project, version) => releases.getProjectRelease(transport, project, version, orgId),
    compareReleases: (req) => releases.compareProjectReleases(transport, req, orgId),
    listPromotions: (environmentId, limit) => releases.listReleasePromotions(transport, environmentId, limit, orgId),
    getRollout: (environmentId, promotionId) => releases.getPromotionRollout(transport, environmentId, promotionId, orgId),
    getBundle: (environmentId, bundleId, digest) => releases.getPromotionBundle(transport, environmentId, bundleId, digest, orgId),
    listDeployRuns: (project, env, limit) => deployRuns.listDeployRuns(transport, project, env, limit, orgId),
    getDeployRun: (id) => deployRuns.getDeployRun(transport, id, orgId),
  };
}

/**
 * The real client's reads in `orgId` over the app's transport.
 *
 * @deprecated Use `releasesApiOf(transport, orgId)`, with the transport the caller's scope gives (`useTransport`).
 */
export function controlPlaneReleasesApi(orgId: string | null): ReleasesApi {
  return releasesApiOf(appTransport, orgId);
}

const ReleasesApiContext = createContext<ReleasesApi | null>(null);

export function ReleasesApiProvider({ api, children }: { api: ReleasesApi; children: ReactNode }) {
  return <ReleasesApiContext.Provider value={api}>{children}</ReleasesApiContext.Provider>;
}

/** The Releases reads in `orgId`: a provided api (fixtures) as it is, else the real client bound to the org, over the nearest scope's transport. */
export function useReleasesApi(orgId: string | null): ReleasesApi {
  const provided = useContext(ReleasesApiContext);
  const transport = useTransport(appTransport);
  return useMemo(() => provided ?? releasesApiOf(transport, orgId), [provided, transport, orgId]);
}
