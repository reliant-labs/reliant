// Copyright (c) 2025 Reliant Labs

/**
 * The route components for the /forge URLs that are not an app's console page:
 * the org pages' entries and the retired URLs. The org pages (Apps, org-wide
 * Observability) render in place; a URL that names ONE app — `?project=` (a
 * Reliant project id) or `?forgeProject=` (the forge name) — finds the forge
 * project (ForgeResolve) and replaces the URL with the console page it means
 * (paths.ts). The retired URLs' search schemas keep the old params, so a
 * notification, bookmark or pasted link from before the move lands where it
 * meant — environment scope included.
 *
 *   /forge                                → /apps, every app (components/Apps)
 *   /forge?project=…|forgeProject=…       → /forge/p/:project
 *   /forge/observability                  → org-wide Observability
 *   /forge/observability?project=…|…      → that app's Observability page
 *   /forge/observability?view=logs&…      → /forge/observability/logs (org-wide, filters kept)
 *   /forge/env/:env?tab=releases          → /forge/p/:project/releases?env=:env
 *   /forge/env/:env?tab=activity          → /forge/p/:project/activity?env=:env
 *   /forge/env/:env?tab=secrets&secret=s  → /forge/p/:project/secrets?env=:env&secret=s
 *   /forge/env/:env?tab=observability&…   → the page hosting the view, ?env=:env&…
 *   /forge/env/:env[?tab=…]               → /forge/p/:project/environments/:env?env=:env
 *   /forge/domains                        → /forge/p/:project/domains
 *   /forge/topology|environments|status|secrets[?env=…]
 *                                         → that environment's page (its secrets, with
 *                                           `secret`), or the Overview
 */

import { useEffect } from "react";
import { Navigate, useLocation, useNavigate, useParams, useSearch } from "@tanstack/react-router";

import { OrgObservabilityPage } from "../Org";
import { ForgeResolve } from "./ForgeResolve";
import {
  carriedOrgSearch,
  consolePath,
  isObsNavView,
  legacyEnvTarget,
  legacyObservabilityTarget,
  type LegacyObsSearch,
  orgPath,
  orgPageOf,
  withSearch,
  type LegacyEnvSearch,
} from "./paths";

type ResolveSearch = { project?: string; forgeProject?: string };

/** `/forge`: the Apps list (/apps) — or, naming one app, that app's console. */
export function ForgeLandingRoute() {
  const search = useSearch({ strict: false }) as ResolveSearch;
  // Naming nothing, /forge means "my apps": the list, not a guess at one of them.
  if (!search.project && !search.forgeProject) return <Navigate to="/apps" replace />;
  return <ForgeResolve {...pick(search)} target={(name) => consolePath(name, { kind: "overview" })} />;
}

export function LegacyEnvRoute() {
  const { env = "" } = useParams({ strict: false }) as { env?: string };
  const search = useSearch({ strict: false }) as ResolveSearch & LegacyEnvSearch;
  return <ForgeResolve {...pick(search)} target={(name) => legacyEnvTarget(name, env, search)} />;
}

/**
 * `/forge/observability[/:view]`: org-wide Observability. Naming one app
 * redirects to its console; a bare `?view=` (a link from before the org pages)
 * redirects to the org page hosting that view.
 */
export function OrgObservabilityRoute() {
  const search = useSearch({ strict: false }) as ResolveSearch & LegacyObsSearch;
  const { pathname } = useLocation();
  if (search.project || search.forgeProject) {
    return <ForgeResolve {...pick(search)} target={(name) => legacyObservabilityTarget(name, search)} />;
  }
  if (search.view && orgPageOf(pathname)?.kind === "observability") {
    return <OrgViewRedirect search={search} />;
  }
  return <OrgObservabilityPage />;
}

function OrgViewRedirect({ search }: { search: LegacyObsSearch }) {
  const navigate = useNavigate();
  const view = search.view;
  const href = withSearch(
    isObsNavView(view) ? orgPath({ kind: "obs-view", view }) : orgPath({ kind: "observability" }),
    carriedOrgSearch(search as Record<string, unknown>)
  );
  useEffect(() => {
    void navigate({ href, replace: true });
  }, [href, navigate]);
  return null;
}

export function LegacyDomainsRoute() {
  const search = useSearch({ strict: false }) as ResolveSearch;
  return <ForgeResolve {...pick(search)} target={(name) => consolePath(name, { kind: "domains" })} />;
}

/** The per-command screens retired before the console (topology, environments, status, secrets). */
export function LegacyCommandRoute() {
  const search = useSearch({ strict: false }) as ResolveSearch & { env?: string; secret?: string };
  return (
    <ForgeResolve
      {...pick(search)}
      target={(name) =>
        search.env
          ? legacyEnvTarget(name, search.env, { tab: search.secret ? "secrets" : undefined, secret: search.secret })
          : withSearch(consolePath(name, { kind: "overview" }))
      }
    />
  );
}

function pick(search: ResolveSearch): ResolveSearch {
  return { project: search.project, forgeProject: search.forgeProject };
}
