// Copyright (c) 2025 Reliant Labs

/**
 * The org's hosted static sites, one row per site per environment, with the
 * custom domains bound to each. Built from what the Apps list and the Domains
 * screen already read (useApps: ListEnvironments + GetStatus per env;
 * useForgeDomains: ListDomains with bindings), so the Sites page costs no new
 * RPC and agrees with both.
 */

import type { AppSummary } from "@/services/forge/apps";
import type { DomainState, ForgeDomain } from "@/services/forge/domains";

export interface SiteDomain {
  hostname: string;
  state: DomainState;
}

// A type alias, not an interface: DataTable rows must be indexable records.
export type SiteRow = {
  /** The forge project (app) the site belongs to. */
  app: string;
  env: string;
  envId: string;
  /** The static-site workload's name. */
  site: string;
  /** Where it opens: its live custom domain, else the generated hostname. */
  url: string;
  host: string;
  custom: boolean;
  /** Every custom domain bound to this site, in any state. */
  domains: SiteDomain[];
};

/** Sites by app, then environment (prod first, as the apps list orders them), then name. */
export function sitesOf(apps: readonly AppSummary[], domains: readonly ForgeDomain[]): SiteRow[] {
  const rows: SiteRow[] = [];
  for (const app of apps) {
    for (const env of app.envs) {
      for (const link of env.links) {
        rows.push({
          app: app.name,
          env: env.name,
          envId: env.id,
          site: link.workload,
          url: link.url,
          host: link.host,
          custom: link.custom,
          domains: domains
            .filter((d) => d.binding && d.binding.environmentId === env.id && d.binding.target === link.workload)
            .map((d) => ({ hostname: d.hostname, state: d.state }))
            .sort((a, b) => a.hostname.localeCompare(b.hostname)),
        });
      }
    }
  }
  return rows;
}
