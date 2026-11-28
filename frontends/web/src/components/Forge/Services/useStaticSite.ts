// Copyright (c) 2026 Reliant Labs

/**
 * The reads behind a static site's hosting sections (Site insights R3):
 *
 *   useStaticSite(target)              GetStaticSite: hostnames, release, usage
 *                                      against the free allowance, configuration
 *   useStaticSiteTraffic(target, range) GetStaticSiteTraffic: the CDN series and
 *                                      totals over 24h / 7d / 30d
 *
 * Both act in the console's org (lib/orgScope, as GetStatus does: the same
 * authority) and go through the generated StaticSiteService hooks, so the
 * demo's and the sample tour's transport scope answers them like any other.
 */

import { Code, ConnectError } from "@connectrpc/connect";

import { useGetStaticSite, useGetStaticSiteTraffic } from "@/hooks/controlplane/static-site-service-hooks_gen";
import { orgRequest, useOrgScope } from "@/lib/orgScope";

import { TRAFFIC_RANGE_WIRE, trafficView, type TrafficRange } from "./staticSiteModel";

/** One hosted static site: the environment serving it and the frontend's name. */
export interface StaticSiteTarget {
  envId: string;
  frontend: string;
}

/** Codes on which asking again cannot change the answer. */
const FINAL: ReadonlySet<Code> = new Set([Code.PermissionDenied, Code.Unauthenticated, Code.Unimplemented, Code.NotFound, Code.FailedPrecondition, Code.InvalidArgument]);

function retry(failureCount: number, error: unknown): boolean {
  if (error instanceof ConnectError && FINAL.has(error.code)) return false;
  return failureCount < 1;
}

/** GetStaticSite for `target` (null: nothing to read, e.g. an environment the console does not list). */
export function useStaticSite(target: StaticSiteTarget | null) {
  const org = useOrgScope();
  return useGetStaticSite(orgRequest({ environmentId: target?.envId ?? "", frontend: target?.frontend ?? "" }, org.orgId), {
    enabled: !!target?.envId && org.ready,
    staleTime: 30_000,
    retry,
  });
}

/** GetStaticSiteTraffic for `target` over `range`, as the charts read it (staticSiteModel.trafficView). */
export function useStaticSiteTraffic(target: StaticSiteTarget | null, range: TrafficRange) {
  const org = useOrgScope();
  return useGetStaticSiteTraffic(orgRequest({ environmentId: target?.envId ?? "", frontend: target?.frontend ?? "", range: TRAFFIC_RANGE_WIRE[range] }, org.orgId), {
    enabled: !!target?.envId && org.ready,
    select: trafficView,
    staleTime: 60_000,
    retry,
  });
}

/** Why a hosting read has no answer, in words the section shows; null when the error says nothing a person can act on. */
export function hostingReadProblem(error: unknown, what: "site" | "traffic"): string | null {
  if (!(error instanceof ConnectError)) return null;
  switch (error.code) {
    case Code.Unimplemented:
      return what === "traffic" ? "Traffic metrics arrive with the next control-plane release." : "Hosting details arrive with the next control-plane release.";
    case Code.PermissionDenied:
    case Code.Unauthenticated:
      return "Reading this needs deploy access in this organization.";
    case Code.FailedPrecondition:
      return "Claim this site to see its hosting details.";
    case Code.NotFound:
      return "The control plane does not know this site in this environment.";
    default:
      return null;
  }
}
