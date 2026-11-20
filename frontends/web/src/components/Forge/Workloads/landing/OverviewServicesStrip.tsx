// Copyright (c) 2026 Reliant Labs

/**
 * The Overview's SERVICES strip (U6e, owner: "one live view: Services"): one
 * compact chip per thing the app runs — its status, ready of desired, and
 * its requests per second — each opening its page under Services (the
 * database's opens Data). The full live view (CPU, memory, instances, charts)
 * is the Services page's; this is the glance that leads there.
 *
 * Read from useAppLiveState, the hook the Services page reads, so the two
 * never disagree.
 */

import { ArrowRight } from "lucide-react";

import Link from "@/components/forge-ui/link";
import Status from "@/components/forge-ui/status";
import { cn } from "@/lib/utils";
import type { RangePreset } from "@/services/controlPlane/observability/window";

import { useAppLiveState } from "../../LiveState/useAppLiveState";
import { formatRate } from "../../Observability/format";

export interface ServicesStripLinks {
  services: string;
  service: (name: string, env: string) => string;
  /** The database's home (Data), scoped to its environment. Absent: the database opens under Services like the rest. */
  data?: (env: string) => string;
}

export function OverviewServicesStrip({ app, env, range, links }: { app: string; env: string; range: RangePreset; links: ServicesStripLinks }) {
  const live = useAppLiveState(app, env, range);
  const loading = live.envsLoading || (live.listed.isLoading && !live.listed.data);
  const showEnv = live.scopedEnv === null;
  const rows = live.rows;
  return (
    <section className="min-w-0 space-y-3" data-testid="overview-services-strip">
      <div className="flex min-h-7 items-center justify-between gap-3">
        <h2 className="min-w-0 text-section text-ink">
          Services
          {!loading && (
            <span className="ml-2 text-meta font-normal text-ink-muted">
              {rows.length}
              {live.attention > 0 ? ` · ${live.attention} need${live.attention === 1 ? "s" : ""} attention` : ""}
            </span>
          )}
        </h2>
        <Link href={links.services} className="no-color -mr-2 inline-flex h-7 items-center gap-1 rounded-md px-2 text-meta text-ink-muted hover:bg-ink/5 hover:text-ink">
          All services
          <ArrowRight className="h-3 w-3" aria-hidden="true" />
        </Link>
      </div>
      {loading ? (
        <div className="h-12 animate-pulse rounded-lg bg-ink/5 motion-reduce:animate-none" aria-label="Loading services" />
      ) : rows.length === 0 ? (
        <p className="text-ui text-ink-muted">Nothing has reported yet. Services appear once Reliant sees their pods.</p>
      ) : (
        <ul className="flex flex-wrap gap-2">
          {rows.map((row) => {
            const href = row.type === "database" && links.data ? links.data(row.envName) : links.service(row.name, row.envName);
            const health = row.type === "workload" ? row.health : undefined;
            return (
              <li key={row.key}>
                <Link
                  href={href}
                  className={cn(
                    "no-color inline-flex h-9 items-center gap-2 rounded-md border bg-surface px-3 text-ui text-ink transition-colors hover:bg-ink/5",
                    row.tone === "danger" ? "border-destructive/40" : row.tone === "warning" ? "border-warning/40" : "border-border"
                  )}
                  title={`${row.name} (${row.kindLabel}): ${row.statusNote ?? row.status}`}
                  data-testid={`overview-strip-${row.key}`}
                >
                  {row.statusNote ? <span className="size-2 rounded-full border border-ink-muted" aria-label={row.statusNote} role="img" /> : <Status tone={row.tone} label={row.status} hideLabel />}
                  <span className="font-medium">{row.name}</span>
                  {showEnv && <span className="text-meta text-ink-muted">{row.envName}</span>}
                  {row.type === "workload" && <span className={cn("text-meta tabular-nums", row.short ? "text-warning-ink" : "text-ink-muted")}>{row.ready}</span>}
                  {health && health.requestRate > 0 && <span className="text-meta tabular-nums text-ink-muted">{formatRate(health.requestRate)}</span>}
                </Link>
              </li>
            );
          })}
        </ul>
      )}
    </section>
  );
}
