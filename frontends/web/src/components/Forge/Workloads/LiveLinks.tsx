// Copyright (c) 2025 Reliant Labs

/**
 * The Overview's "where is it live" row: each hosted environment's public
 * URLs, frontends first, so opening the app is the first thing the console
 * offers rather than a field on a workload's page.
 *
 * Read from the same GetStatus observations the Overview already holds
 * (`hosted`), so it costs no request. Absent when nothing has a URL.
 */

import { ExternalLink, Globe } from "lucide-react";

import { appLinksOf, compareEnvNames } from "@/services/forge/apps";

import type { HostedByEnv } from "./OverviewHome";
import type { StripEnv } from "./workloadModel";

export function LiveLinks({ envs, hosted }: { envs: StripEnv[]; hosted: HostedByEnv }) {
  const rows = envs
    .filter((env) => !env.preview && env.placed)
    .map((env) => ({ env: env.name, links: appLinksOf([...(hosted.get(env.name)?.values() ?? [])]) }))
    .filter((row) => row.links.length > 0)
    .sort((a, b) => compareEnvNames(a.env, b.env));
  if (rows.length === 0) return null;

  return (
    <section
      className="flex flex-wrap items-center gap-x-6 gap-y-2 rounded-lg border border-border bg-surface px-4 py-3"
      aria-label="Live URLs"
      data-testid="overview-live-links"
    >
      <span className="inline-flex items-center gap-1.5 text-xs font-medium uppercase tracking-wide text-ink-muted">
        <Globe className="h-3.5 w-3.5" aria-hidden="true" />
        Live
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
              {link.kind === "api" && row.links.length > 1 && <span className="text-xs font-normal text-ink-muted">API</span>}
              <span className="truncate">{link.host}</span>
              <ExternalLink className="h-3 w-3 shrink-0 text-ink-muted" aria-hidden="true" />
            </a>
          ))}
        </div>
      ))}
    </section>
  );
}
