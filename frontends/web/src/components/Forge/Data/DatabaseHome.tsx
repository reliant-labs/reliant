// Copyright (c) 2026 Reliant Labs

/**
 * DATA IS THE DATABASE'S HOME (U6e, owner 2026-11-20: "data and storage got
 * mixed up"; Data = databases, Storage = buckets). Above the tables browser,
 * each managed Postgres in scope: what the platform reports of it, and
 * honestly what it does not report yet (control-plane project-data-map §2).
 *
 *   deploy     GetStatus's verdict for the database deployment (through the
 *              live state): whether the deploy created it and keeps it in step.
 *              This is the deploy's word, not a live health check.
 *   status     the operator's ManagedDatabase phase is not exposed by any RPC:
 *              coming soon.
 *   backups    the operator backs up, but nothing lists backups or the last
 *              one: coming soon.
 *   CPU, memory  the CNPG instances' figures WHEN ListWorkloads reports a
 *              workload named for the database (CNPG names its cluster, and
 *              labels its pods, with the database's name). Otherwise coming
 *              soon: nothing else reports them.
 *
 * /services/<database> redirects to this page, and Services' database rows
 * link here, so the database has one page.
 */

import { useMemo, type ReactNode } from "react";
import { Database } from "lucide-react";

import Link from "@/components/forge-ui/link";
import Status, { type StatusTone } from "@/components/forge-ui/status";
import Tag from "@/components/forge-ui/tag";
import { cn } from "@/lib/utils";
import type { ObsScope, Workload } from "@/services/controlPlane/observability/types";
import { RANGE_LABELS, type RangePreset } from "@/services/controlPlane/observability/window";

import { useForgeConsole } from "../Console/consoleContext";
import { ENV_ALL } from "../Console/envScope";
import { formatBytes, formatCores } from "../Observability/format";
import { Sparkline } from "../LiveState/Sparkline";
import { useAppLiveState, useWorkloadUsage } from "../LiveState/useAppLiveState";
import { containerFigures, databasesOf, verdictStatus } from "../Services/servicesModel";

const RANGE: RangePreset = "24h";

interface DatabaseEntry {
  name: string;
  envName: string;
  envId: string | undefined;
  verdict: { tone: StatusTone; label: string };
  reason: string;
  /** The CNPG instances, when ListWorkloads reports them under the database's name. */
  instances: Workload | null;
}

/** The managed databases in scope, prod first, with the workload that is their instances when one is reported. */
export function databaseEntries(
  hosted: Parameters<typeof databasesOf>[0],
  workloads: readonly Workload[],
  envs: readonly { id: string; name: string }[],
  env: string
): DatabaseEntry[] {
  return databasesOf(hosted)
    .filter(({ envName }) => env === ENV_ALL || envName === env)
    .map(({ envName, deployment }) => {
      const verdict = verdictStatus(deployment);
      return {
        name: deployment.name ?? "",
        envName,
        envId: envs.find((candidate) => candidate.name === envName)?.id,
        verdict: { tone: verdict.tone, label: verdict.label === "Live" ? "Provisioned" : verdict.label },
        reason: deployment.verdict_reason ?? "",
        instances: workloads.find((w) => w.workload === deployment.name && w.envName === envName) ?? null,
      };
    })
    .sort((a, b) => Number(b.envName === "prod") - Number(a.envName === "prod") || a.envName.localeCompare(b.envName) || a.name.localeCompare(b.name));
}

export function DatabaseHome() {
  const { forgeProject, env, href } = useForgeConsole();
  const live = useAppLiveState(forgeProject, env, RANGE);
  const entries = useMemo(() => databaseEntries(live.hosted, live.listed.data?.workloads ?? [], live.envs, env), [live.hosted, live.listed.data, live.envs, env]);
  if (live.envsLoading || live.hostedLoading) {
    return <div className="h-28 animate-pulse rounded-xl border border-border bg-surface motion-reduce:animate-none" aria-label="Loading the databases" />;
  }
  if (entries.length === 0) {
    return (
      <p className="rounded-xl border border-dashed border-border px-4 py-4 text-ui text-ink-muted" data-testid="data-database-none">
        No managed Postgres {env === ENV_ALL ? "in this project" : `in ${env}`}. A <code className="font-mono text-ink">forge.Postgres</code> in the app&apos;s deploy config gets one per environment.
        {env !== ENV_ALL && " Any other database the environment declares is browsable below."}
      </p>
    );
  }
  return (
    <section className="space-y-3" data-testid="data-database-home">
      {entries.map((entry) => (
        <DatabaseCard
          key={`${entry.envName}/${entry.name}`}
          entry={entry}
          orgId={live.orgId}
          app={forgeProject}
          // Every environment in scope: the card opens its environment's tables.
          tablesHref={env === ENV_ALL ? href({ kind: "data" }, { env: entry.envName }) : null}
        />
      ))}
    </section>
  );
}

function DatabaseCard({ entry, orgId, app, tablesHref }: { entry: DatabaseEntry; orgId: string | null; app: string; tablesHref: string | null }) {
  return (
    <div className="rounded-xl border border-border bg-surface p-4" data-testid={`data-database-${entry.envName}-${entry.name}`}>
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
        <Database className="size-4 text-ink-muted" aria-hidden="true" />
        <h2 className="text-section text-ink">{entry.name}</h2>
        <span className="text-meta text-ink-muted">Managed Postgres</span>
        <Tag>{entry.envName}</Tag>
        <span className="flex-1" />
        {tablesHref && (
          <Link href={tablesHref} className="no-color text-ui text-ink-muted hover:text-ink hover:underline">
            Browse tables →
          </Link>
        )}
      </div>
      <dl className="mt-4 grid grid-cols-2 gap-x-6 gap-y-4 lg:grid-cols-5">
        <Fact label="Deploy" title={entry.reason || "The deploy created it and keeps it in step with the release."}>
          <Status tone={entry.verdict.tone} label={entry.verdict.label} />
        </Fact>
        <Fact label="Live status">
          <ComingSoon />
        </Fact>
        <Fact label="Backups">
          <ComingSoon />
        </Fact>
        {entry.instances ? (
          <InstanceFigures workload={entry.instances} envId={entry.envId} orgId={orgId} app={app} />
        ) : (
          <>
            <Fact label="CPU">
              <ComingSoon />
            </Fact>
            <Fact label="Memory">
              <ComingSoon />
            </Fact>
          </>
        )}
      </dl>
      <p className="mt-4 text-meta text-ink-muted">
        {entry.instances
          ? `CPU and memory are the database instances' busiest container, over the ${RANGE_LABELS[RANGE].toLowerCase()}.`
          : "The platform does not report this database's live status, backups, size, connections or CPU and memory yet. They will show here once it does."}
      </p>
    </div>
  );
}

/** CPU and memory of the database's instances: the latest figure and its trend over the window (GetWorkload). */
function InstanceFigures({ workload, envId, orgId, app }: { workload: Workload; envId: string | undefined; orgId: string | null; app: string }) {
  const scope = useMemo<ObsScope | null>(() => (orgId && envId ? { orgId, projectId: app, envIds: [envId], filters: [] } : null), [orgId, app, envId]);
  const usage = useWorkloadUsage(scope, RANGE, workload.workload, envId);
  const figures = containerFigures(workload.containers);
  const sum = (values: (number | null)[]) => (values.some((v) => v !== null) ? values.reduce<number>((acc, v) => acc + (v ?? 0), 0) : null);
  const cpu = sum(figures.map((f) => f.cpu));
  const memory = sum(figures.map((f) => f.memory));
  const windowWords = RANGE_LABELS[RANGE].toLowerCase();
  return (
    <>
      <Fact label="CPU">
        <span className="inline-flex items-center gap-2 tabular-nums">
          {cpu !== null ? formatCores(cpu) : "—"}
          {usage.cpu && <Sparkline values={usage.cpu.values} label={`CPU over the ${windowWords}`} />}
        </span>
      </Fact>
      <Fact label="Memory">
        <span className="inline-flex items-center gap-2 tabular-nums">
          {memory !== null ? formatBytes(memory) : "—"}
          {usage.memory && <Sparkline values={usage.memory.values} label={`Memory over the ${windowWords}`} />}
        </span>
      </Fact>
    </>
  );
}

function Fact({ label, title, children }: { label: string; title?: string; children: ReactNode }) {
  return (
    <div className="min-w-0 space-y-1" title={title}>
      <dt className="text-meta text-ink-muted">{label}</dt>
      <dd className="text-ui text-ink">{children}</dd>
    </div>
  );
}

function ComingSoon({ className }: { className?: string }) {
  return <span className={cn("text-ink-muted", className)}>Coming soon</span>;
}
