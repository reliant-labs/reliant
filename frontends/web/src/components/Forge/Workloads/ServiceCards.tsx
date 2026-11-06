// Copyright (c) 2025 Reliant Labs

/**
 * The Overview's SERVICES as health cards: one card per long-running workload,
 * shaped unlike the lists around it (Processes, compact lines; Jobs, a run
 * timeline), so the three never read as the same table again.
 *
 *   ▌platform-api  Deployment          ● Healthy
 *   ▌2/2 ready · 20261018.155157
 *   ▌37.1/s      0.8%      86ms
 *   ▌req/s       errors    p95
 *   ▌CPU ▬▬▭ 36%           Memory ▬▬▭ 41%
 *   ▌Restarts 24h  0
 *
 * The left rule is the health tone: a wall of cards is scanned by colour
 * first. The RED figures are the workload's own requests, from the same
 * GetServiceOverview answer Process health reads (the caller passes it in, by
 * name and environment); a workload that serves no requests says so once.
 *
 * The whole card opens the workload (its name is the link, stretched over the
 * card); the release link inside stays its own target. A declared-only
 * (unobserved) workload has no page to open: its card is dashed and links
 * nowhere.
 */

import { useState, type ReactNode } from "react";

import Link from "@/components/forge-ui/link";
import Status, { type StatusTone } from "@/components/forge-ui/status";
import Tag from "@/components/forge-ui/tag";
import type { ServiceHealth } from "@/services/controlPlane/observability/types";
import { cn } from "@/lib/utils";

import { formatBytes, formatCores, formatDuration, formatPercent, formatRate } from "../Observability/format";
import { ERROR_RATE_ALERT } from "../Observability/OverviewView";
import type { HostedByEnv, OverviewHrefs } from "./OverviewHome";
import { HEALTH_STATUS, KIND_LABEL, lastExitOf, replicasShort, replicasText, worstUsage, type ServiceRow } from "./workloadModel";
import { exitPhrase } from "./plainHealth";
import { HealthStatus, UsageMeter, WorkloadRelease } from "./workloadParts";

/** Cards shown before "Show all": two rows at the widest layout. */
export const CARDS_SHOWN = 6;

const RULE: Record<StatusTone, string> = {
  success: "bg-success",
  warning: "bg-warning",
  danger: "bg-danger",
  neutral: "bg-ink-subtle",
};

/** The request side of a card: found by workload name and environment, or not instrumented. */
export interface RedLookup {
  /** The process's figures; undefined when it sent no request telemetry. */
  of: (workload: string, env: string) => ServiceHealth | undefined;
  /** The read has not answered yet. */
  loading: boolean;
}

export function ServiceCards({
  rows,
  loading,
  showEnv,
  windowShort,
  hosted,
  hrefs,
  red,
  empty,
}: {
  rows: ServiceRow[];
  loading: boolean;
  showEnv: boolean;
  windowShort: string;
  hosted: HostedByEnv;
  hrefs: OverviewHrefs;
  red: RedLookup;
  empty: ReactNode;
}) {
  const [all, setAll] = useState(false);
  if (loading) {
    return (
      <ul className="grid grid-cols-1 gap-3 md:grid-cols-2 xl:grid-cols-3" aria-label="Services" aria-busy="true">
        {Array.from({ length: 3 }).map((_, i) => (
          <li key={i} className="h-40 animate-pulse rounded-lg border border-border bg-surface motion-reduce:animate-none">
            <span className="sr-only">Loading…</span>
          </li>
        ))}
      </ul>
    );
  }
  if (rows.length === 0) {
    return (
      <p className="rounded-lg border border-dashed border-border bg-surface px-4 py-5 text-sm text-ink-muted" data-testid="overview-services-empty">
        {empty}
      </p>
    );
  }
  const shown = all ? rows : rows.slice(0, CARDS_SHOWN);
  return (
    <div className="space-y-2">
      <ul className="grid grid-cols-1 gap-3 md:grid-cols-2 xl:grid-cols-3" aria-label="Services">
        {shown.map((w) => (
          <ServiceCard
            key={`${w.envName}/${w.workload}`}
            w={w}
            showEnv={showEnv}
            windowShort={windowShort}
            hosted={hosted}
            hrefs={hrefs}
            health={red.of(w.workload, w.envName)}
            redLoading={red.loading}
          />
        ))}
      </ul>
      {rows.length > CARDS_SHOWN && (
        <button
          type="button"
          onClick={() => setAll((prev) => !prev)}
          className="inline-flex h-7 items-center rounded-md px-2 text-xs font-medium text-ink-muted hover:bg-ink/5 hover:text-ink"
          data-testid="overview-services-more"
        >
          {all ? "Show fewer" : `Show all ${rows.length} services`}
        </button>
      )}
    </div>
  );
}

function ServiceCard({
  w,
  showEnv,
  windowShort,
  hosted,
  hrefs,
  health,
  redLoading,
}: {
  w: ServiceRow;
  showEnv: boolean;
  windowShort: string;
  hosted: HostedByEnv;
  hrefs: OverviewHrefs;
  health: ServiceHealth | undefined;
  redLoading: boolean;
}) {
  const tone: StatusTone = w.unobserved ? "neutral" : HEALTH_STATUS[w.health].tone;
  const exit = w.unobserved ? null : lastExitOf(w);
  return (
    <li
      className={cn(
        "relative flex min-w-0 flex-col gap-2.5 overflow-hidden rounded-lg border bg-surface py-3 pl-4 pr-3 transition-colors",
        w.unobserved ? "border-dashed border-border" : "border-border hover:border-ink-subtle"
      )}
      data-testid={`overview-service-${w.workload}${showEnv ? `-${w.envName}` : ""}`}
      data-health={w.unobserved ? "unobserved" : w.health}
    >
      <span aria-hidden="true" className={cn("absolute inset-y-0 left-0 w-1", RULE[tone])} />
      <div className="flex items-start justify-between gap-2">
        <div className="min-w-0">
          <div className="flex min-w-0 flex-wrap items-baseline gap-x-2">
            {w.unobserved ? (
              <span className="truncate font-medium text-ink">{w.workload}</span>
            ) : (
              <Link
                href={hrefs.workload(w.workload, w.envName)}
                className="no-color truncate font-medium text-ink after:absolute after:inset-0 after:content-[''] focus-visible:outline-none focus-visible:after:rounded-lg focus-visible:after:ring-2 focus-visible:after:ring-ring"
              >
                {w.workload}
              </Link>
            )}
            {!w.unobserved && KIND_LABEL[w.kind] && <span className="text-xs text-ink-muted">{KIND_LABEL[w.kind]}</span>}
          </div>
          <div className="mt-0.5 flex min-w-0 flex-wrap items-center gap-x-1.5 text-xs text-ink-muted">
            {showEnv && <Tag>{w.envName}</Tag>}
            {w.unobserved ? (
              <span>Declared by {w.envName}&apos;s last recorded render; nothing reports from it.</span>
            ) : (
              <>
                <span className={cn("tabular-nums", replicasShort(w) && "text-warning-ink")}>{replicasText(w)} ready</span>
                <span aria-hidden="true">·</span>
                <span className="relative z-10">
                  <WorkloadRelease workload={w} releaseHref={hrefs.release} hosted={hosted.get(w.envName)?.get(w.workload) ?? null} />
                </span>
              </>
            )}
          </div>
        </div>
        <span className="shrink-0">
          {w.unobserved ? (
            <span title={w.healthReason}>
              <Status tone="neutral" label="Unobserved" />
            </span>
          ) : (
            <HealthStatus workload={w} />
          )}
        </span>
      </div>

      {!w.unobserved && (
        <>
          <Red health={health} loading={redLoading} />
          <div className="grid grid-cols-2 gap-3 text-xs">
            <span className="flex min-w-0 items-center gap-2">
              <span className="text-ink-muted">CPU</span>
              <UsageMeter usage={worstUsage(w, "cpu")} format={formatCores} what="CPU" />
            </span>
            <span className="flex min-w-0 items-center gap-2">
              <span className="text-ink-muted">Memory</span>
              <UsageMeter usage={worstUsage(w, "memory")} format={formatBytes} what="Memory" />
            </span>
          </div>
          <div className="text-xs text-ink-muted">
            <span>Restarts {windowShort}</span>{" "}
            {w.restartsInWindow ? (
              <>
                <span className="tabular-nums text-ink">{w.restartsInWindow}</span>
                {exit && (
                  <span
                    className={cn("ml-1", tone === "danger" ? "text-danger-ink" : tone === "warning" ? "text-warning-ink" : "text-ink-muted")}
                    title={exit.atMs ? `Last exited ${new Date(exit.atMs).toLocaleString()}` : undefined}
                  >
                    · {exitPhrase(exit.reason)}
                  </span>
                )}
              </>
            ) : (
              <span className="tabular-nums">0</span>
            )}
          </div>
        </>
      )}
    </li>
  );
}

/** Rate, errors and p95, big enough to read across a row of cards. */
function Red({ health, loading }: { health: ServiceHealth | undefined; loading: boolean }) {
  if (loading && !health) return <div className="h-9 animate-pulse rounded bg-ink/5 motion-reduce:animate-none" aria-hidden="true" />;
  if (!health || health.requestCount === 0) {
    return <p className="text-xs text-ink-muted" data-testid="service-card-no-requests">No requests traced in this window.</p>;
  }
  const erroring = health.errorRate >= ERROR_RATE_ALERT;
  return (
    <dl className="grid grid-cols-3 gap-2" data-testid="service-card-red">
      <Figure name="req/s" value={formatRate(health.requestRate)} />
      <Figure name="errors" value={formatPercent(health.errorRate)} tone={erroring ? "text-danger-ink" : undefined} />
      <Figure name="p95" value={formatDuration(health.p95Ms)} />
    </dl>
  );
}

function Figure({ name, value, tone }: { name: string; value: string; tone?: string }) {
  return (
    // The term is first in the markup, as a description list requires; the figure is drawn above it.
    <div className="flex min-w-0 flex-col-reverse">
      <dt className="text-2xs uppercase tracking-wide text-ink-muted">{name}</dt>
      <dd className={cn("truncate text-base font-semibold tabular-nums text-ink", tone)}>{value}</dd>
    </div>
  );
}
