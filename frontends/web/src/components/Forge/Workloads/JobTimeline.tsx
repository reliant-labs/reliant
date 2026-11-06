// Copyright (c) 2025 Reliant Labs

/**
 * The Overview's JOBS as a run timeline: one row per job, every run of it in
 * the window placed on a shared time track, coloured by result.
 *
 *   control-plane-migrate  prod   ●────●─────────✕──●        Succeeded · 41s · 5h ago
 *                                 13:00            now
 *
 * A job's story is a sequence of runs, and a table of latest runs hid it: a
 * migration that failed yesterday and passed today read as green. Here the
 * failure stays on the track for the whole window.
 *
 * The runs come from ListWorkloads' jobs (newest first, at most 200), the one
 * read the page already holds; nothing is fetched here. A run with no start
 * time cannot be placed and is counted, not drawn. Each marker is a link to
 * the job's page; hovering it names the run, its result, reason, duration and
 * release.
 */

import type { ReactNode } from "react";

import Link from "@/components/forge-ui/link";
import Status from "@/components/forge-ui/status";
import Tag from "@/components/forge-ui/tag";
import type { WorkloadJob } from "@/services/controlPlane/observability/types";
import { cn } from "@/lib/utils";

import { formatAgo, formatTimestamp } from "../Observability/format";
import { shortVersion } from "../Releases/releaseModel";
import { INK_LINK } from "../Releases/releaseParts";
import type { OverviewHrefs } from "./OverviewHome";
import { formatSeconds, JOB_STATUS, type JobRow } from "./workloadModel";
import { jobFailurePhrase } from "./plainHealth";

const MARK: Record<WorkloadJob["result"], string> = {
  succeeded: "bg-success border-success",
  failed: "bg-danger border-danger",
  running: "bg-surface border-info animate-pulse motion-reduce:animate-none",
  unknown: "bg-surface border-ink-subtle",
};

export function JobTimeline({
  rows,
  runs,
  window,
  loading,
  showEnv,
  hrefs,
  empty,
}: {
  /** The latest run of each job (jobRows), newest first. */
  rows: JobRow[];
  /** Every run in scope, newest first: the markers. */
  runs: readonly WorkloadJob[];
  /** The track's span. */
  window: { startMs: number; endMs: number };
  loading: boolean;
  showEnv: boolean;
  hrefs: OverviewHrefs;
  empty: ReactNode;
}) {
  if (loading) {
    return (
      <div className="space-y-2 px-4 pb-3" aria-busy="true">
        {Array.from({ length: 2 }).map((_, i) => (
          <div key={i} className="h-8 animate-pulse rounded bg-ink/5 motion-reduce:animate-none">
            <span className="sr-only">Loading…</span>
          </div>
        ))}
      </div>
    );
  }
  if (rows.length === 0) {
    return (
      <p className="px-4 pb-4 pt-1 text-sm text-ink-muted" data-testid="overview-jobs-empty">
        {empty}
      </p>
    );
  }
  const span = Math.max(1, window.endMs - window.startMs);
  const at = (ms: number) => Math.min(100, Math.max(0, ((ms - window.startMs) / span) * 100));
  return (
    <div className="px-4 pb-3">
      <ul className="divide-y divide-border/70" aria-label="Jobs">
        {rows.map((row) => {
          const own = runs.filter((run) => run.envName === row.envName && run.workload === row.workload);
          const placed = own.filter((run) => run.startedAtMs !== null);
          const latest = row.latest;
          const status = JOB_STATUS[latest.result];
          const href = hrefs.workload(row.workload, row.envName);
          return (
            <li
              key={row.key}
              className="grid grid-cols-1 items-center gap-x-4 gap-y-1 py-2.5 md:grid-cols-[minmax(10rem,14rem)_minmax(0,1fr)_auto]"
              data-testid={`overview-job-${row.workload}${showEnv ? `-${row.envName}` : ""}`}
              data-result={latest.result}
            >
              <div className="min-w-0">
                <Link href={href} className="no-color block truncate text-sm font-medium text-ink hover:underline" title={latest.name}>
                  {row.workload}
                </Link>
                <span className="flex flex-wrap items-center gap-1.5 text-xs text-ink-muted">
                  {showEnv && <Tag>{row.envName}</Tag>}
                  {row.runs > 1 ? `${row.runs} runs${row.failedRuns ? `, ${row.failedRuns} failed` : ""}` : "1 run"}
                </span>
              </div>

              <div className="relative h-6" aria-hidden="true" data-testid="overview-job-track">
                <span className="absolute inset-x-0 top-1/2 h-px -translate-y-1/2 bg-border" />
                {placed.map((run) => {
                  const start = at(run.startedAtMs!);
                  const end = run.finishedAtMs ? at(run.finishedAtMs) : run.result === "running" ? 100 : start;
                  const tip = `${run.name}: ${JOB_STATUS[run.result].label}${run.result === "failed" && run.reason ? ` (${jobFailurePhrase(run.reason)})` : ""}, started ${formatTimestamp(run.startedAtMs!)}${run.durationSeconds ? `, ${formatSeconds(run.durationSeconds)}` : ""}${run.releaseVersion ? `, release ${shortVersion(run.releaseVersion)}` : ""}`;
                  return (
                    <Link
                      key={run.name}
                      href={href}
                      title={tip}
                      tabIndex={-1}
                      className="no-color absolute top-1/2 flex h-3 -translate-y-1/2 items-center"
                      // A run is at least a dot wide, and never runs off the track's end.
                      style={{ left: `min(${start}%, calc(100% - 0.75rem))`, width: `max(0.75rem, ${end - start}%)` }}
                      data-result={run.result}
                    >
                      <span className={cn("block h-3 w-full rounded-full border-2", MARK[run.result])} />
                    </Link>
                  );
                })}
                <span className="absolute -bottom-3 left-0 text-2xs text-ink-subtle">{formatAgo(window.startMs)}</span>
                <span className="absolute -bottom-3 right-0 text-2xs text-ink-subtle">now</span>
              </div>

              <div className="flex min-w-0 flex-wrap items-center justify-end gap-x-2 gap-y-0.5 text-xs text-ink-muted md:flex-nowrap">
                <span className="inline-flex items-center gap-1.5">
                  <Status tone={status.tone} label={status.label} />
                  {latest.result === "failed" && latest.reason && <span className="text-danger-ink">· {jobFailurePhrase(latest.reason)}</span>}
                </span>
                <span className="tabular-nums">{latest.durationSeconds ? formatSeconds(latest.durationSeconds) : latest.result === "running" ? "running" : "—"}</span>
                <span>
                  {latest.finishedAtMs ? (
                    <time dateTime={new Date(latest.finishedAtMs).toISOString()} title={new Date(latest.finishedAtMs).toLocaleString()}>
                      {formatAgo(latest.finishedAtMs)}
                    </time>
                  ) : latest.result === "running" ? (
                    "not yet"
                  ) : (
                    "not observed"
                  )}
                </span>
                {latest.releaseVersion && (
                  <Link href={hrefs.release(latest.releaseVersion)} className={INK_LINK} title={latest.releaseVersion}>
                    {shortVersion(latest.releaseVersion)}
                  </Link>
                )}
              </div>
            </li>
          );
        })}
      </ul>
    </div>
  );
}
