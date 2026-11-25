// Copyright (c) 2026 Reliant Labs

/**
 * ML › Runs (MlEarlyAccessService.ListMlRuns), early access: training runs,
 * newest first — state, the declared template and its orchestrator, the
 * accelerators it held, restarts and preemptions, when it started and how
 * long it ran, and its loss curve when it logged one. `?app=` narrows to one
 * app.
 */

import Card from "@/components/forge-ui/card";
import DataTable, { type DataTableColumn } from "@/components/forge-ui/data_table";
import Status from "@/components/forge-ui/status";
import { useMlRuns } from "@/hooks/ml-queries";
import type { MlRun } from "@/services/controlPlane/ml/types";

import { UPlotChart } from "../Observability/charts/UPlotChart";
import { formatAgo, formatDuration, formatWhen } from "../Observability/format";
import { lossSeries, RUN_STATE, runDurationMs } from "./mlModel";
import { AppFilter, MlFrame, MlRead, useMlGate } from "./MlParts";

export interface MlAppPageProps {
  app: string;
  onApp: (app: string | undefined) => void;
  now?: number;
}

export function MlRunsPage({ app, onApp, now }: MlAppPageProps) {
  const { orgId } = useMlGate();
  const query = useMlRuns(orgId, app);
  const apps = query.data?.kind === "ok" ? query.data.data.map((run) => run.project) : [];
  return (
    <MlFrame view="runs" controls={<AppFilter app={app} apps={apps} onApp={onApp} testId="ml-runs-filters" />}>
      <MlRead query={query} what="training runs">
        {(runs) => <RunsTable runs={runs} now={now ?? Date.now()} />}
      </MlRead>
    </MlFrame>
  );
}

function LossSparkline({ run }: { run: MlRun }) {
  const loss = lossSeries(run);
  const points = loss?.points.filter((p) => p.atMs !== null) ?? [];
  if (!loss || points.length < 2) return <span className="text-ink-muted">—</span>;
  return (
    <div className="w-28">
      <UPlotChart compact height={28} timestamps={points.map((p) => p.atMs! / 1000)} series={[{ label: loss.name, values: points.map((p) => p.value), color: "--chart-1" }]} label={`${loss.name} of ${run.name}`} />
    </div>
  );
}

function RunsTable({ runs, now }: { runs: MlRun[]; now: number }) {
  if (runs.length === 0) return <p className="text-ui text-ink-muted" data-testid="ml-runs-none">No training runs recorded.</p>;
  const columns: DataTableColumn<{ run: MlRun }>[] = [
    {
      key: "name",
      header: "Run",
      className: "w-full max-w-0",
      render: (_, { run }) => (
        <span className="block min-w-0">
          <span className="block truncate text-ink" title={run.name}>
            {run.name || run.id}
          </span>
          <span className="block truncate text-meta text-ink-muted">{[run.project, run.launchedBy].filter(Boolean).join(" · ")}</span>
        </span>
      ),
    },
    { key: "state", header: "State", render: (_, { run }) => <Status tone={RUN_STATE[run.state].tone} label={RUN_STATE[run.state].label} busy={RUN_STATE[run.state].busy} /> },
    {
      key: "template",
      header: "Template",
      render: (_, { run }) => (
        <span className="block">
          <span className="block font-mono text-code text-ink">{run.template || "—"}</span>
          <span className="block text-meta text-ink-muted">{run.orchestrator}</span>
        </span>
      ),
    },
    {
      key: "accelerators",
      header: "Accelerators",
      render: (_, { run }) => (run.acceleratorCount > 0 ? <span className="whitespace-nowrap text-ink">{`${run.acceleratorCount} × ${run.acceleratorProduct || "accelerator"}`}</span> : <span className="text-ink-muted">—</span>),
    },
    {
      key: "interruptions",
      header: "Restarts / preemptions",
      numeric: true,
      render: (_, { run }) => (
        <span className={run.restarts + run.preemptions > 0 ? "text-warning-ink" : "text-ink-muted"}>
          {run.restarts} / {run.preemptions}
        </span>
      ),
    },
    {
      key: "started",
      header: "Started",
      numeric: true,
      render: (_, { run }) => {
        const took = runDurationMs(run, now);
        return (
          <span className="block whitespace-nowrap">
            <span className="block text-ink" title={run.startedAtMs !== null ? formatWhen(run.startedAtMs, now) : undefined}>
              {run.startedAtMs !== null ? formatAgo(run.startedAtMs, now) : "Not started"}
            </span>
            {took !== null && <span className="block text-meta text-ink-muted">{formatDuration(took)}</span>}
          </span>
        );
      },
    },
    { key: "loss", header: "Loss", render: (_, { run }) => <LossSparkline run={run} /> },
  ];
  return (
    <Card padding="none" className="overflow-hidden" data-testid="ml-runs">
      <DataTable<{ run: MlRun }>
        flush
        stacked
        ariaLabel="Training runs"
        columns={columns}
        data={runs.map((run) => ({ run }))}
        getRowKey={({ run }) => run.id}
        getRowProps={({ run }) => ({ className: "align-top", "data-testid": `ml-run-${run.id}`, "data-state": run.state })}
      />
    </Card>
  );
}
