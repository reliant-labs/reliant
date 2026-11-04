// Copyright (c) 2025 Reliant Labs

/**
 * The Environments page's list: ONE CARD PER ENVIRONMENT, answering what a
 * reader brings to a project before opening any of them. What is it on and
 * where did that come from; is it healthy and are its workloads up; how did
 * the last deploy go, and did the release prove good (the obs check); does
 * what runs match what was declared; and where to look next (its
 * dashboards, errors and alerts).
 *
 * A card, not a table row: eight facts, several with a line of detail (where
 * a deploy failed, what drifted), did not fit one row, and stacking them in
 * one cell made rows that lined up with nothing. Inside a card the facts are
 * one label/value grid, so they line up with each other.
 *
 * Every fact is read from the control plane, so the list renders with the
 * daemon offline. The facts beyond the roster's own (environmentSummary.ts)
 * are optional: a card without a summary shows what the roster knows.
 *
 * PURE PROPS, like every forge presentation component: rows and callbacks in,
 * no fetching, so the contract tests hand it object literals.
 */

import type { ReactNode } from "react";
import { ChevronRight, Clock } from "lucide-react";

import Badge from "@/components/forge-ui/badge";
import Button from "@/components/forge-ui/button";
import Card from "@/components/forge-ui/card";
import Link from "@/components/forge-ui/link";
import Status from "@/components/forge-ui/status";
import Tooltip from "@/components/forge-ui/tooltip";
import type { CloudEnvStatus } from "@/services/forge/cloudEnvs";
import {
  declaredNotBuilt,
  isPlacedKind,
  isQueued,
  liveKindLabel,
  queuedOnLabel,
  type LiveEnv,
} from "@/services/forge/live";

import { DEPLOY_RUN_LABEL, DEPLOY_RUN_TONE } from "../Deploy/DeployRuns";
import { HealthChip } from "../EnvBadges";
import { Ago, INK_LINK } from "../Releases/releaseParts";
import type { EnvSummary, Fact } from "./environmentSummary";

/** Where an environment's own telemetry pages are, each already scoped to it. */
export interface EnvLinks {
  dashboards: string;
  errors: string;
  alerts: string;
}

export interface EnvironmentRow {
  env: LiveEnv;
  /** The platform's observation. Only ever present for a placed env. */
  status: CloudEnvStatus | undefined;
  statusLoading: boolean;
  /** The facts beyond the roster's (environmentSummary.ts). Absent: not shown. */
  summary?: EnvSummary;
  links?: EnvLinks;
}

export interface EnvironmentListProps {
  rows: EnvironmentRow[];
  onOpen: (env: string) => void;
}

export function EnvironmentList({ rows, onOpen }: EnvironmentListProps) {
  return (
    <ul className="grid grid-cols-1 gap-4 xl:grid-cols-2" data-testid="forge-env-list" aria-label="Environments in this project">
      {rows.map((row) => (
        <li key={row.env.name} className="min-w-0">
          <EnvironmentCard row={row} onOpen={onOpen} />
        </li>
      ))}
    </ul>
  );
}

function EnvironmentCard({ row, onOpen }: { row: EnvironmentRow; onOpen: (env: string) => void }) {
  const { env, status, statusLoading, summary, links } = row;
  return (
    <Card padding="none" className="h-full" data-testid={`env-row-${env.name}`} data-kind={env.kind}>
      <div className="flex items-center gap-2 border-b border-border px-4 py-3">
        <button
          type="button"
          onClick={() => onOpen(env.name)}
          className="no-color min-w-0 truncate rounded font-mono text-sm font-semibold text-ink hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        >
          {env.name}
        </button>
        <Badge label={liveKindLabel(env.kind)} variant="neutral" size="sm" />
        <span className="ml-auto">
          <Button
            variant="ghost"
            size="sm"
            onClick={() => onOpen(env.name)}
            data-testid={`env-row-open-${env.name}`}
            aria-label={`Open ${env.name}`}
            rightIcon={<ChevronRight className="h-3.5 w-3.5" />}
          >
            Open
          </Button>
        </span>
      </div>

      <dl className="grid grid-cols-[7.5rem_minmax(0,1fr)] gap-x-4 gap-y-3 px-4 py-3 text-sm">
        <Term>Release</Term>
        <dd className="min-w-0">
          <ReleaseCell env={env} />
          {env.promotedAt && (
            <Tooltip content="When this environment was PROMOTED to the release — not when it was deployed. Promotion writes a pointer; deployment moves bytes.">
              <span data-testid={`promoted-${env.name}`} className="mt-0.5 inline-flex items-center gap-1 text-xs text-ink-muted">
                <Clock className="h-3 w-3 shrink-0" aria-hidden="true" />
                promoted {formatTimestamp(env.promotedAt)}
              </span>
            </Tooltip>
          )}
        </dd>

        <Term>Health</Term>
        <dd className="min-w-0">
          {/* Health is an OBSERVATION. A placed env's comes from the platform;
              anything else's only from its own telemetry, and without that
              the honest cell is "not observed from here". */}
          {isPlacedKind(env.kind) ? (
            <HealthChip env={env.name} health={{ kind: "hosted", verdict: status?.verdict ?? "unknown", loading: statusLoading && !status }} />
          ) : summary ? (
            <FactValue fact={summary.health} testId={`telemetry-health-${env.name}`} />
          ) : (
            <Dash label="not observed from here" />
          )}
        </dd>

        {summary && (
          <>
            <Term>Workloads</Term>
            <dd className="min-w-0" data-testid={`workloads-${env.name}`}>
              {summary.workloads ? (
                <span className="tabular-nums text-ink">
                  {summary.workloads.ready}/{summary.workloads.total} ready
                  {summary.workloads.source === "platform" && <span className="ml-1 text-xs text-ink-muted">· as the platform observed</span>}
                </span>
              ) : (
                <Dash label="nothing reports from it" />
              )}
            </dd>

            <Term>Last deploy</Term>
            <dd className="min-w-0" data-testid={`last-deploy-${env.name}`}>
              <LastDeploy summary={summary} />
            </dd>

            <Term>Obs check</Term>
            <dd className="min-w-0">
              {summary.check ? <FactValue fact={summary.check} testId={`obs-check-${env.name}`} /> : <Dash label="nothing promoted to check" />}
            </dd>

            <Term>Drift</Term>
            <dd className="min-w-0">
              <FactValue fact={summary.drift} testId={`drift-${env.name}`} />
            </dd>
          </>
        )}
      </dl>

      {links && (
        <nav className="flex flex-wrap gap-x-4 gap-y-1 border-t border-border px-4 py-2.5 text-xs" aria-label={`${env.name} telemetry`}>
          <Link href={links.dashboards} className={INK_LINK} data-testid={`env-link-dashboards-${env.name}`}>
            Dashboards
          </Link>
          <Link href={links.errors} className={INK_LINK} data-testid={`env-link-errors-${env.name}`}>
            Errors
          </Link>
          <Link href={links.alerts} className={INK_LINK} data-testid={`env-link-alerts-${env.name}`}>
            Alerts
          </Link>
        </nav>
      )}
    </Card>
  );
}

function Term({ children }: { children: ReactNode }) {
  return <dt className="pt-0.5 text-xs text-ink-muted">{children}</dt>;
}

function FactValue({ fact, testId }: { fact: Fact; testId: string }) {
  return (
    <span className="flex min-w-0 flex-col gap-0.5" data-testid={testId} data-tone={fact.tone}>
      <Status tone={fact.tone} label={fact.label} size="sm" />
      {fact.detail !== "" && (
        <span className="truncate text-xs text-ink-muted" title={fact.detail}>
          {fact.detail}
        </span>
      )}
    </span>
  );
}

function LastDeploy({ summary }: { summary: EnvSummary }) {
  const run = summary.lastDeploy;
  if (!run) return <Dash label="no deploy recorded" />;
  const failed = run.state === "failed" || run.state === "abandoned";
  return (
    <span className="flex min-w-0 flex-col gap-0.5" data-state={run.state}>
      <span className="flex flex-wrap items-center gap-x-2 gap-y-0.5">
        <Status tone={DEPLOY_RUN_TONE[run.state]} label={DEPLOY_RUN_LABEL[run.state]} size="sm" />
        {run.release !== "" && <span className="font-mono text-xs text-ink">{run.release}</span>}
        <Ago at={run.finishedAt ?? run.startedAt} className="text-xs text-ink-muted" />
      </span>
      {failed && (run.failedPhase !== "" || run.error !== "") && (
        <span className="truncate text-xs text-danger-ink" title={run.error || undefined}>
          {run.failedPhase !== "" ? `stopped at ${run.failedPhase}` : ""}
          {run.failedPhase !== "" && run.error !== "" ? ": " : ""}
          {run.error}
        </span>
      )}
    </span>
  );
}

function ReleaseCell({ env }: { env: LiveEnv }) {
  if (env.kind === "local") {
    return <span className="text-xs text-ink-muted">working tree</span>;
  }
  if (env.release === "") {
    return <Badge label={declaredNotBuilt(env) ? "declared, not built" : "never promoted"} variant="neutral" size="sm" />;
  }
  return (
    <div className="flex min-w-0 flex-col gap-0.5">
      <span className="flex items-center gap-2">
        <span className="font-mono text-sm text-ink">{env.release}</span>
        {/* The release is RECORDED but not rolling out: it waits on a person.
            Said here, on the release it is about, so a reader does not take
            the version for what is running. */}
        {isQueued(env) && (
          <Tooltip content="Queued: this release is recorded and goes out on its own once that is resolved. Open the environment for what to do.">
            <span data-testid={`queued-${env.name}`}>
              <Badge label={`Waiting on ${queuedOnLabel(env.holds)}`} variant="warning" size="sm" dot />
            </span>
          </Tooltip>
        )}
      </span>
      {/* The provenance line (§2.1): images from the release's source, config
          from the render's. Truncated here, in full on the environment's page. */}
      {env.provenance !== "" && (
        <span data-testid={`provenance-${env.name}`} className="truncate font-mono text-2xs text-ink-muted" title={env.provenance}>
          {env.provenance}
        </span>
      )}
    </div>
  );
}

function Dash({ label }: { label: string }) {
  return (
    <span className="text-xs text-ink-muted">
      <span aria-hidden="true">—</span>
      <span className="sr-only">{label}</span>
    </span>
  );
}

/** RFC3339 → local; the raw string rather than "Invalid Date" when unparseable. */
export function formatTimestamp(value: string): string {
  const parsed = new Date(value);
  if (Number.isNaN(parsed.getTime())) return value;
  return parsed.toLocaleString(undefined, {
    year: "numeric",
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  });
}
