// Copyright (c) 2025 Reliant Labs

/**
 * DEPLOYS — every `forge env deploy`, from a terminal, CI or the Deploy
 * dialog, as a durable record (services/forge/deployAttempts.ts).
 *
 *   DeployRunsPanel    the Releases page's "Deploys" section: the newest
 *                      runs, each opening its phases, its error and the tail
 *                      of its output.
 *   DeployRunsBanner   the Overview, above the environment strip: a deploy
 *                      in flight or failed, and nothing at all otherwise.
 *
 * A deploy that failed BEFORE it wrote a release or a promotion — the build,
 * the plan, a refused promote — exists only here, which is why the Overview
 * puts a failed one in front of the reader rather than leaving it to be
 * found.
 */

import { useState } from "react";
import { ChevronDown, ChevronRight, ExternalLink } from "lucide-react";

import Link from "@/components/forge-ui/link";
import Status, { type StatusTone } from "@/components/forge-ui/status";
import Tag from "@/components/forge-ui/tag";
import { useDeployRun, useDeployRuns } from "@/hooks/release-queries";
import { deployHeadline, deploysNeedingAttention, phaseWords, type DeployRun, type DeployRunState } from "@/services/forge/deployAttempts";
import { cn } from "@/lib/utils";

import { AskReliantActions, AskReliantMenu } from "../Observability/AskReliant/AskReliant";
import type { AskSubject } from "../Observability/AskReliant/askReliantPrompt";
import { Ago, INK_LINK, Panel } from "../Releases/releaseParts";
import { shortVersion } from "../Releases/releaseModel";

export const DEPLOY_RUN_TONE: Record<DeployRunState, StatusTone> = {
  running: "neutral",
  succeeded: "success",
  failed: "danger",
  abandoned: "warning",
  queued: "warning",
  cancelled: "neutral",
  unknown: "neutral",
};

export const DEPLOY_RUN_LABEL: Record<DeployRunState, string> = {
  running: "Running",
  succeeded: "Succeeded",
  failed: "Failed",
  abandoned: "Stopped reporting",
  queued: "Queued",
  cancelled: "Cancelled",
  unknown: "Unknown",
};

export interface DeployRunHrefs {
  release: (version: string) => string;
}

/** Who started it: the automation label, else the person's name, else nothing. */
function whoOf(run: DeployRun, names: ReadonlyMap<string, string>): string {
  return run.startedByActor || names.get(run.startedByUserId) || "";
}

function durationOf(run: DeployRun): string | null {
  const start = Date.parse(run.startedAt ?? "");
  const end = Date.parse(run.finishedAt ?? "");
  if (!Number.isFinite(start) || !Number.isFinite(end) || end < start) return null;
  const seconds = Math.round((end - start) / 1000);
  return seconds < 60 ? `${seconds}s` : `${Math.floor(seconds / 60)}m ${seconds % 60}s`;
}

// ── Releases › Deploys ──────────────────────────────────────────────────────

export function DeployRunsPanel({
  project,
  env,
  names,
  hrefs,
  openId = null,
}: {
  project: string;
  /** One environment's deploys, or null for every environment. */
  env: string | null;
  names: ReadonlyMap<string, string>;
  hrefs: DeployRunHrefs;
  /** The deploy to open first (a link from the Overview's banner). */
  openId?: string | null;
}) {
  const runs = useDeployRuns(project, env ?? undefined);
  const [open, setOpen] = useState<string | null>(openId);
  return (
    <Panel
      title="Deploys"
      meta="Every forge env deploy — from a terminal, CI or Reliant — with its output, including the ones that failed before they cut a release."
      flush
      testId="releases-deploys"
    >
      {runs.isLoading ? (
        <div className="m-4 h-16 animate-pulse rounded bg-ink/5 motion-reduce:animate-none" aria-label="Loading deploys" />
      ) : runs.error ? (
        <p className="px-4 pb-4 pt-1 text-sm text-ink-muted" data-testid="releases-deploys-error">
          Deploys could not be read right now. Releases and their promotions below are unaffected.
        </p>
      ) : (runs.data ?? []).length === 0 ? (
        <p className="px-4 pb-4 pt-1 text-sm text-ink-muted" data-testid="releases-deploys-empty">
          No deploy has been recorded{env ? ` for ${env}` : ""} yet. Each forge env deploy appears here as it runs.
        </p>
      ) : (
        <ul className="divide-y divide-border/70" aria-label="Deploys">
          {(runs.data ?? []).map((run) => (
            <DeployRunRow
              key={run.id}
              run={run}
              who={whoOf(run, names)}
              showEnv={!env}
              open={open === run.id}
              onToggle={() => setOpen((current) => (current === run.id ? null : run.id))}
              hrefs={hrefs}
            />
          ))}
        </ul>
      )}
    </Panel>
  );
}

function DeployRunRow({
  run,
  who,
  showEnv,
  open,
  onToggle,
  hrefs,
}: {
  run: DeployRun;
  who: string;
  showEnv: boolean;
  open: boolean;
  onToggle: () => void;
  hrefs: DeployRunHrefs;
}) {
  const duration = durationOf(run);
  return (
    <li data-testid={`deploy-run-${run.id}`} data-state={run.state}>
      <button
        type="button"
        onClick={onToggle}
        aria-expanded={open}
        className="flex w-full items-center gap-3 px-4 py-2.5 text-left text-sm hover:bg-ink/5 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring"
      >
        {open ? <ChevronDown className="h-3.5 w-3.5 shrink-0 text-ink-muted" aria-hidden="true" /> : <ChevronRight className="h-3.5 w-3.5 shrink-0 text-ink-muted" aria-hidden="true" />}
        <Status tone={DEPLOY_RUN_TONE[run.state]} label={DEPLOY_RUN_LABEL[run.state]} size="sm" />
        <span className="min-w-0 flex-1 truncate text-ink">{deployHeadline(run, shortVersion)}</span>
        {showEnv && <Tag>{run.env}</Tag>}
        {run.source && <span className="hidden text-xs text-ink-muted md:inline">{run.source}</span>}
        <span className="shrink-0 text-xs text-ink-muted">
          {who ? `${who} · ` : ""}
          <Ago at={run.startedAt} />
          {duration ? ` · ${duration}` : ""}
        </span>
      </button>
      {open && <DeployRunDetail run={run} hrefs={hrefs} />}
    </li>
  );
}

function DeployRunDetail({ run, hrefs }: { run: DeployRun; hrefs: DeployRunHrefs }) {
  const detail = useDeployRun(run.hasLog || run.state === "running" ? run.id : null);
  const log = detail.data?.log ?? "";
  return (
    <div className="space-y-3 border-t border-border/70 bg-surface-sunken px-4 py-3 text-sm" data-testid={`deploy-run-detail-${run.id}`}>
      <ol className="flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-ink-muted" aria-label="Phases">
        {run.phases.map((phase, index) => (
          <li key={`${phase.name}-${index}`} className={cn(phase.name === run.failedPhase && run.state !== "succeeded" && "font-medium text-danger-ink")}>
            {index > 0 && <span aria-hidden="true">→ </span>}
            {phaseWords(phase.name)}
          </li>
        ))}
      </ol>
      {run.error && <p className="whitespace-pre-wrap text-danger-ink">{run.error}</p>}
      <div className="flex flex-wrap items-center gap-x-4 gap-y-1 text-xs">
        {run.release && (
          <Link href={hrefs.release(run.release)} className={INK_LINK}>
            Release {shortVersion(run.release)}
          </Link>
        )}
        {run.runUrl && (
          <a href={run.runUrl} target="_blank" rel="noreferrer" className={cn(INK_LINK, "inline-flex items-center gap-1")}>
            CI run <ExternalLink className="h-3 w-3" aria-hidden="true" />
            <span className="sr-only">(opens in a new tab)</span>
          </a>
        )}
        {run.exitCode !== null && <span className="text-ink-muted">exit {run.exitCode}</span>}
      </div>
      {run.state === "failed" && (
        <div data-testid={`deploy-run-ask-${run.id}`}>
          <AskReliantActions subject={failedDeploySubject(run)} />
        </div>
      )}
      {run.hasLog || run.state === "running" ? (
        detail.isLoading ? (
          <div className="h-24 animate-pulse rounded bg-ink/5 motion-reduce:animate-none" aria-label="Loading output" />
        ) : detail.error ? (
          <p className="text-xs text-ink-muted">The output could not be read right now.</p>
        ) : log ? (
          <>
            {detail.data?.run.logTruncated && <p className="text-xs text-ink-muted">Showing the end of the output; the start was dropped to keep it short.</p>}
            <pre className="max-h-80 overflow-auto rounded-md border border-border/60 bg-surface p-3 font-mono text-xs leading-5 text-ink" data-testid={`deploy-run-log-${run.id}`}>
              {log}
            </pre>
          </>
        ) : (
          <p className="text-xs text-ink-muted">No output yet.</p>
        )
      ) : (
        <p className="text-xs text-ink-muted">
          {run.source === "terminal" ? "Run from a terminal, so its output stayed there." : "No output was recorded for this deploy."}
        </p>
      )}
    </div>
  );
}

/** A failed deploy as Ask Reliant's subject: references only, never its output (askReliantPrompt). */
function failedDeploySubject(run: DeployRun): AskSubject {
  return { kind: "deploy-failed", env: run.env || null, runId: run.id, version: run.release || undefined };
}

// ── Overview: above the environment strip ───────────────────────────────────

export function DeployRunsBanner({ project, deployHref }: { project: string; deployHref: (run: DeployRun) => string }) {
  const runs = useDeployRuns(project);
  const shown = deploysNeedingAttention(runs.data ?? []);
  if (shown.length === 0) return null;
  return (
    <ul className="space-y-2" aria-label="Deploys in progress or failed" data-testid="overview-deploys">
      {shown.map((run) => (
        <li
          key={run.id}
          data-testid={`overview-deploy-${run.id}`}
          data-state={run.state}
          className={cn(
            "flex flex-wrap items-center gap-x-3 gap-y-1 rounded-lg border px-4 py-2.5 text-sm",
            run.state === "running" ? "border-border bg-surface" : "border-danger-border bg-danger-surface"
          )}
        >
          <Status tone={DEPLOY_RUN_TONE[run.state]} label={DEPLOY_RUN_LABEL[run.state]} size="sm" />
          <span className="min-w-0 flex-1 truncate text-ink">
            {deployHeadline(run, shortVersion)}
            {run.error && run.state !== "running" && <span className="text-ink-muted"> — {run.error}</span>}
          </span>
          <span className="text-xs text-ink-muted">
            <Ago at={run.startedAt} />
          </span>
          <Link href={deployHref(run)} className={cn(INK_LINK, "text-xs")}>
            {run.state === "running" ? "Watch" : "See the output"}
          </Link>
          {run.state === "failed" && <AskReliantMenu subject={failedDeploySubject(run)} appearance="button" label="Ask Reliant" testId={`overview-deploy-ask-${run.id}`} />}
        </li>
      ))}
    </ul>
  );
}
