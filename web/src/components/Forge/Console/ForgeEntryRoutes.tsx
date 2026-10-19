// Copyright (c) 2025 Reliant Labs

/**
 * The route components for every /forge URL that is not a console page: the
 * bare `/forge` and the retired URLs. Each finds the forge project
 * (ForgeResolve) and replaces the URL with the console page it means now
 * (paths.ts). Their search schemas keep the old params, so a notification,
 * bookmark or pasted link from before the move lands where it meant —
 * environment scope included.
 *
 *   /forge                                → /forge/p/:project
 *   /forge/env/:env?tab=releases          → /forge/p/:project/releases?env=:env
 *   /forge/env/:env?tab=activity          → /forge/p/:project/activity?env=:env
 *   /forge/env/:env?tab=secrets&secret=s  → /forge/p/:project/secrets?env=:env&secret=s
 *   /forge/env/:env?tab=observability&…   → the page hosting the view, ?env=:env&…
 *   /forge/env/:env[?tab=…]               → /forge/p/:project/environments/:env?env=:env
 *   /forge/observability?view=…&envs=…    → the page hosting the view, ?env=<one env, or all>
 *   /forge/domains                        → /forge/p/:project/domains
 *   /forge/topology|environments|status|secrets[?env=…]
 *                                         → that environment's page (its secrets, with
 *                                           `secret`), or the Overview
 */

import { useParams, useSearch } from "@tanstack/react-router";

import type { ObsSearch } from "../Observability/obsSearch";
import { ForgeResolve } from "./ForgeResolve";
import {
  consolePath,
  legacyEnvTarget,
  legacyObservabilityTarget,
  withSearch,
  type LegacyEnvSearch,
} from "./paths";

type ResolveSearch = { project?: string; forgeProject?: string };

/** `/forge`: the console's front door (the app sidebar's entry). */
export function ForgeLandingRoute() {
  const search = useSearch({ strict: false }) as ResolveSearch;
  return <ForgeResolve {...pick(search)} target={(name) => consolePath(name, { kind: "overview" })} />;
}

export function LegacyEnvRoute() {
  const { env = "" } = useParams({ strict: false }) as { env?: string };
  const search = useSearch({ strict: false }) as ResolveSearch & LegacyEnvSearch;
  return <ForgeResolve {...pick(search)} target={(name) => legacyEnvTarget(name, env, search)} />;
}

export function LegacyObservabilityRoute() {
  const search = useSearch({ strict: false }) as ResolveSearch & ObsSearch;
  return <ForgeResolve {...pick(search)} target={(name) => legacyObservabilityTarget(name, search)} />;
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
