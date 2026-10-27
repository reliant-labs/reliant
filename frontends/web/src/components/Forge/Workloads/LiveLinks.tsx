// Copyright (c) 2025 Reliant Labs

/**
 * The Overview's "where is the API" row: each hosted environment's backends
 * that have a public URL. Frontends are not repeated here — they have their
 * own panel (FrontendsPanel in OverviewHome) with copy, release and domains.
 *
 * Read from the same GetStatus observations the Overview already holds
 * (`hosted`), so it costs no request. Absent when no backend has a URL.
 */

import { ExternalLink, Globe } from "lucide-react";

import { appLinksOf, compareEnvNames } from "@/services/forge/apps";

import type { HostedByEnv } from "./OverviewHome";
import type { StripEnv } from "./workloadModel";

export function LiveLinks({ envs, hosted }: { envs: StripEnv[]; hosted: HostedByEnv }) {
  const rows = envs
    .filter((env) => !env.preview && env.placed)
    .map((env) => ({ env: env.name, links: appLinksOf([...(hosted.get(env.name)?.values() ?? [])]).filter((link) => link.kind === "api") }))
    .filter((row) => row.links.length > 0)
    .sort((a, b) => compareEnvNames(a.env, b.env));
  if (rows.length === 0) return null;

  return (
    <section
      className="flex flex-wrap items-center gap-x-6 gap-y-2 rounded-lg border border-border bg-surface px-4 py-3"
      aria-label="API URLs"
      data-testid="overview-live-links"
    >
      <span className="inline-flex items-center gap-1.5 text-xs font-medium uppercase tracking-wide text-ink-muted">
        <Globe className="h-3.5 w-3.5" aria-hidden="true" />
        APIs
      </span>
      {rows.map((row) => (
        <div key={row.env} className="flex min-w-0 items-center gap-2 text-sm" data-testid={`overview-live-${row.env}`}>
          <span className="text-ink-muted">{row.env}</span>
          {row.links.map((link) => (
            <a
              key={link.url}
              href={link.url}
              target="_blank"
              rel="noreferrer"
              title={`Open ${link.workload || link.host} in ${row.env}`}
              className="inline-flex min-w-0 items-center gap-1 rounded font-medium text-ink underline-offset-2 hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent"
            >
              <span className="truncate">{link.host}</span>
              <ExternalLink className="h-3 w-3 shrink-0 text-ink-muted" aria-hidden="true" />
            </a>
          ))}
        </div>
      ))}
    </section>
  );
}
