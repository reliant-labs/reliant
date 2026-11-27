// Copyright (c) 2025 Reliant Labs

/**
 * The project catalog: one org's projects with their apps, and the org's
 * apps no project holds (`ProjectService.ListProjectsWithApps`), as the pure
 * model services/projects/catalog.ts reads it. Every project surface — the
 * selector, the Projects page, a project page's chips — reads this one entry.
 *
 * The request names its org, so the generated key does too: one org's
 * catalog is never served as another's. No query goes out before the org is
 * known (a request naming none would be the caller's default org, not the
 * tab's). Project writes invalidate `projectServiceKeys.all`, which holds
 * this entry, so a new or removed project shows here without wiring.
 */

import { useCallback } from "react";
import { useQuery } from "@tanstack/react-query";

import type { ListProjectsWithAppsResponse } from "@/gen/reliant/v1/project_pb";
import { orgQueryEnabled, orgRequest, useOrgScope, withOrgFailure } from "@/lib/orgScope";
import { catalogFromResponse, type ProjectCatalog } from "@/services/projects/catalog";

import { projectServiceQueries } from "./project-service-hooks_gen";
import { useHasAuthSession } from "./useHasAuthSession";

/** How long a catalog is fresh: an app deployed elsewhere shows within this. */
export const PROJECT_CATALOG_STALE_TIME_MS = 30_000;

/** The request for `orgId`'s catalog (null: no org, the caller's default). */
export function projectCatalogRequest(orgId: string | null) {
  return orgRequest({}, orgId);
}

// One catalog per response, so every reader of one entry shares its rows.
const catalogs = new WeakMap<ListProjectsWithAppsResponse, ProjectCatalog>();
function catalogOf(response: ListProjectsWithAppsResponse): ProjectCatalog {
  let catalog = catalogs.get(response);
  if (!catalog) {
    catalog = catalogFromResponse(response);
    catalogs.set(response, catalog);
  }
  return catalog;
}

export interface ProjectCatalogView {
  /** Undefined until the first answer. */
  catalog: ProjectCatalog | undefined;
  /** True until the first answer (never while signed out). */
  isLoading: boolean;
  error: Error | null;
  refetch: () => void;
}

/**
 * The catalog of `orgId`. Omitted (undefined), the tab's org (lib/orgScope:
 * the committed `/o/<org>` route's, else the tab's), and nothing is asked
 * until that org is known. `null` names no org (a server with no control
 * plane).
 */
export function useProjectCatalog(orgId?: string | null): ProjectCatalogView {
  const hasSession = useHasAuthSession();
  const scope = useOrgScope();
  const explicit = orgId !== undefined;
  const org = explicit ? orgId : scope.orgId;
  // On both brands, like useProjects: the forge host mounts the one route
  // tree (U6), projects included. The public demo's fixed visitor asks its
  // transport (useHasAuthSession.demo.ts), which answers the sample projects.
  const wanted = hasSession;
  const enabled = wanted && (explicit || orgQueryEnabled(scope));
  const options = projectServiceQueries.listProjectsWithApps(projectCatalogRequest(org), {
    enabled,
    staleTime: PROJECT_CATALOG_STALE_TIME_MS,
    select: catalogOf,
  });
  const query = useQuery(explicit ? options : withOrgFailure(options, scope));
  const { refetch } = query;
  const refetchCatalog = useCallback(() => {
    void refetch();
  }, [refetch]);
  return {
    catalog: query.data,
    isLoading: wanted && query.isPending,
    error: query.error,
    refetch: refetchCatalog,
  };
}
