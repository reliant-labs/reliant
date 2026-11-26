// Copyright (c) 2026 Reliant Labs

/**
 * Training (`/forge/ml/runs`): every run, clickable (02-views §3.1). The
 * header strip answers "what is running and what is it costing"; the table
 * answers how far each run is, which is unhealthy and which is best on its
 * experiment's objective; the leaderboard ranks experiments. Rows open the
 * run; a mini-Gantt segment opens that phase attempt; checked rows compare.
 */

import { useMemo, useState } from "react";

import Link from "@/components/forge-ui/link";
import { FilterBar, FilterChip, FilterMenu, FilterSearch } from "@/components/forge-ui/filter_bar";
import { MlSubjectKind, type MlExperiment, type MlRun } from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";
import { cn } from "@/lib/utils";

import { MlFrame, MlRead } from "../MlParts";
import {
  fmtAgo,
  fmtCompact,
  fmtDuration,
  fmtGpuHours,
  fmtPct,
  fmtStep,
  fmtUsd,
  has,
  KpiStrip,
  MlLink,
  MlTable,
  mlHref,
  num,
  Panel,
  Sparkline,
  SubjectLink,
  tsMs,
  useMl,
  useMlSearch,
  type MlColumn,
} from "../kit";
import { CostPair, RunState } from "./parts";
import { filterRuns, groupRuns, leaderboard, listKpis, miniGantt } from "./trainingModel";

/** A run's URL segment: its name. */
export const runHref = (run: Pick<MlRun, "name" | "id">, search?: Parameters<typeof mlHref>[2]) => mlHref("runs", [run.name || run.id], search);

export function RunsList() {
  const runs = useMl("listMlRuns", {});
  const experiments = useMl("listMlExperiments", {});
  const exps = experiments.data?.kind === "ok" ? experiments.data.data.experiments : [];
  return (
    <MlFrame view="runs" title="Training">
      <MlRead query={runs} what="training runs">
        {(data) => <RunsListBody runs={data.runs} experiments={exps} />}
      </MlRead>
    </MlFrame>
  );
}

function RunsListBody({ runs, experiments }: { runs: readonly MlRun[]; experiments: readonly MlExperiment[] }) {
  const [now] = useState(() => Date.now());
  const [search, setSearch] = useMlSearch();
  const [picked, setPicked] = useState<string[]>([]);
  const kpis = useMemo(() => listKpis(runs, now), [runs, now]);
  const shown = useMemo(() => filterRuns(runs, search.filter, search.q), [runs, search.filter, search.q]);
  const groups = useMemo(() => groupRuns(shown, experiments, search.group), [shown, experiments, search.group]);
  const toggle = (id: string) => setPicked((p) => (p.includes(id) ? p.filter((x) => x !== id) : [...p, id]));
  const columns = useRunColumns(picked, toggle, now);
  const board = leaderboard(experiments, runs);

  return (
    <div className="space-y-4">
      <KpiStrip
        testId="ml-runs-kpis"
        items={[
          { label: "Active runs", value: kpis.activeRuns, sub: kpis.queuedRuns ? `${kpis.queuedRuns} queued` : "none queued", hero: true },
          { label: "GPUs in use", value: kpis.gpusInUse, sub: "held by running attempts" },
          { label: "Goodput (active)", value: fmtPct(kpis.goodput), sub: "productive share of GPU time" },
          { label: "Last 24h", value: fmtGpuHours(kpis.gpuHours24h), sub: fmtUsd(kpis.usd24h) },
          { label: "Failures · 7d", value: kpis.failures7d, tone: kpis.failures7d ? "warning" : undefined, sub: `${kpis.restarts7d} restarts` },
        ]}
      />

      <FilterBar
        label="Filter training runs"
        active={(search.filter ? 1 : 0) + (search.q ? 1 : 0)}
        onClear={() => setSearch({ filter: undefined, q: undefined })}
        testId="ml-runs-filters"
        trailing={
          <>
            <FilterChip on={search.group === "experiment"} onClick={() => setSearch({ group: search.group === "experiment" ? undefined : "experiment" })} testId="ml-runs-group">
              Group by experiment
            </FilterChip>
            {picked.length > 0 && (
              <MlLink href={mlHref("runs", ["compare"], { compare: picked.join(",") })} testId="ml-runs-compare" className="text-primary-ink">
                Compare ({picked.length})
              </MlLink>
            )}
          </>
        }
      >
        <FilterMenu
          label="Experiment"
          value={search.filter}
          options={experiments.map((e) => ({ value: e.id, label: e.name, count: runs.filter((r) => r.experimentId === e.id).length }))}
          onChange={(v) => setSearch({ filter: v })}
          testId="ml-runs-experiment"
        />
        <FilterSearch value={search.q ?? ""} onChange={(v) => setSearch({ q: v || undefined })} placeholder="Search name, owner, tag, lr=2e-5" testId="ml-runs-search" />
      </FilterBar>

      {groups.map((g) => (
        <Panel key={g.key} title={g.label || "Runs"} note={`${g.runs.length} run${g.runs.length === 1 ? "" : "s"}`} flush testId={`ml-runs-group-${g.key}`}>
          <MlTable columns={columns} rows={g.runs} rowKey={(r) => r.id} rowHref={(r) => runHref(r)} testId="ml-runs" empty={<p className="py-3 text-meta text-ink-muted">No run matches.</p>} />
        </Panel>
      ))}

      <Panel title="Experiments" note="ranked by each experiment's objective" flush testId="ml-experiments">
        <MlTable
          rows={board}
          rowKey={(r) => r.experiment.id}
          rowHref={(r) => mlHref("runs", [], { filter: r.experiment.id, group: "experiment" })}
          columns={[
            { key: "name", header: "Experiment", render: (r) => <SubjectLink subject={{ kind: MlSubjectKind.EXPERIMENT, id: r.experiment.id, label: r.experiment.name }} /> },
            { key: "owner", header: "Owner", render: (r) => <span className="text-ink-muted">{r.experiment.owner}</span> },
            { key: "runs", header: "Runs", numeric: true, render: (r) => `${r.runs}${r.experiment.activeRuns ? ` (${r.experiment.activeRuns} active)` : ""}` },
            {
              key: "best",
              header: "Best run",
              render: (r) => (r.best ? <MlLink href={runHref(r.best)}>{r.best.name}</MlLink> : <span className="text-ink-muted">—</span>),
            },
            {
              key: "objective",
              header: "Objective",
              numeric: true,
              render: (r) =>
                has(r.experiment.bestObjective) ? (
                  <span>
                    <span className="text-ink-muted">{r.experiment.objectiveKey} </span>
                    {r.experiment.bestObjective.toFixed(3)}
                  </span>
                ) : (
                  <span className="text-ink-muted">not tracked</span>
                ),
            },
            { key: "cost", header: "GPU-h · $", numeric: true, render: (r) => <CostPair cost={r.experiment.cost} /> },
            { key: "last", header: "Last activity", numeric: true, render: (r) => <span className="text-ink-muted">{num(r.experiment.activeRuns) ? "now" : fmtAgo(tsMs(r.experiment.lastActivityAt), now)}</span> },
          ]}
        />
      </Panel>
    </div>
  );
}

function useRunColumns(picked: readonly string[], toggle: (id: string) => void, now: number): MlColumn<MlRun>[] {
  return [
    {
      key: "pick",
      header: <span className="sr-only">Select to compare</span>,
      width: "32px",
      render: (r) => (
        <input type="checkbox" aria-label={`Compare ${r.name}`} checked={picked.includes(r.id)} onChange={() => toggle(r.id)} className="h-3.5 w-3.5 accent-primary" data-testid={`ml-runs-pick-${r.name}`} />
      ),
    },
    {
      key: "run",
      header: "Run",
      sort: (r) => r.name,
      render: (r) => (
        <div className="flex flex-col py-0.5 leading-tight">
          <MlLink href={runHref(r)} className="font-medium">
            {r.name}
          </MlLink>
          <span className="text-meta text-ink-muted">
            {r.owner} · {fmtAgo(tsMs(r.createdAt), now).replace(" ago", "")}
            {r.sweepId ? ` · ${r.sweepId} t${r.trial}` : ""}
            {r.launchedVia === "observed" ? " · observed" : ""}
          </span>
        </div>
      ),
    },
    { key: "state", header: "State", sort: (r) => r.state, render: (r) => <RunState run={r} compact /> },
    { key: "phases", header: "Phases", render: (r) => <MiniGantt run={r} now={now} /> },
    {
      key: "step",
      header: "Step",
      numeric: true,
      sort: (r) => num(r.step) / Math.max(1, num(r.totalSteps)),
      render: (r) =>
        num(r.totalSteps) > 0 ? (
          <div className="flex flex-col leading-tight">
            <span>
              {fmtStep(r.step)}
              <span className="text-ink-muted">/{fmtStep(r.totalSteps)}</span>
            </span>
            <span className="text-meta text-ink-muted">
              {r.currentPhaseKey} · {fmtCompact(r.tokensSeen)} tok
            </span>
          </div>
        ) : (
          <span className="text-ink-muted" title="Observed job: no step reported">
            —
          </span>
        ),
    },
    { key: "eta", header: "ETA", numeric: true, sort: (r) => r.etaSeconds ?? Infinity, render: (r) => (r.etaSeconds !== undefined ? fmtDuration(r.etaSeconds) : <span className="text-ink-muted">—</span>) },
    { key: "gpus", header: "GPUs", numeric: true, sort: (r) => r.accelerators, render: (r) => <span title={r.acceleratorProduct}>{r.accelerators}×{shortProduct(r.acceleratorProduct)}</span> },
    { key: "goodput", header: "Goodput", numeric: true, sort: (r) => (has(r.goodput) ? r.goodput : -1), render: (r) => fmtPct(r.goodput) },
    { key: "cost", header: "GPU-h · $", numeric: true, sort: (r) => r.cost?.usd ?? 0, render: (r) => <CostPair cost={r.cost} /> },
    {
      key: "restarts",
      header: "Restarts",
      numeric: true,
      sort: (r) => r.restarts,
      render: (r) => (
        <span className={cn(r.restarts ? "text-ink" : "text-ink-muted")} title={`${r.restarts} restarts, ${r.preemptions} preemptions`}>
          {r.restarts}
          {r.preemptions ? <span className="text-ink-muted"> · {r.preemptions}p</span> : null}
        </span>
      ),
    },
    {
      key: "objective",
      header: "Objective",
      numeric: true,
      sort: (r) => r.summary[r.objectiveKey] ?? Infinity,
      render: (r) => {
        const v = r.summary[r.objectiveKey];
        return (
          <span className="inline-flex items-center justify-end gap-2">
            <Sparkline values={r.objective?.y ?? []} width={64} height={18} />
            <span className="w-12 text-right">{has(v) ? v.toFixed(3) : "—"}</span>
          </span>
        );
      },
    },
  ];
}

const shortProduct = (p: string) => (p.includes("H100") ? "H100" : p.includes("A100") ? "A100" : p.includes("L4") ? "L4" : p);

/** Phases as a strip over the run's lifetime, one segment per attempt (each a link to it), a ✕ where one failed. */
function MiniGantt({ run, now }: { run: MlRun; now: number }) {
  const segs = miniGantt(run, now);
  if (!segs.length) return <span className="text-meta text-ink-muted">not started</span>;
  return (
    <div className="relative h-4 w-48" data-testid={`ml-mini-gantt-${run.name}`}>
      <div className="absolute inset-x-0 top-1/2 h-px bg-border-subtle" aria-hidden="true" />
      {segs.map((s) => (
        <Link
          key={`${s.phaseKey}-${s.n}`}
          href={mlHref("runs", [run.name, "phases", s.phaseKey], { attempt: s.n })}
          className="no-color absolute inset-y-0.5 rounded-sm hover:outline hover:outline-1 hover:outline-ink"
          style={{
            left: `${s.left * 100}%`,
            width: `max(3px, ${s.width * 100}%)`,
            background: s.queued
              ? "repeating-linear-gradient(135deg, transparent 0 3px, hsl(var(--card) / 0.6) 3px 5px), hsl(var(--muted-foreground) / 0.35)"
              : `hsl(var(--chart-${(s.phaseIndex % 6) + 1}) / ${s.running ? 1 : 0.7})`,
          }}
          title={`${s.phaseKey} attempt ${s.n}: ${s.queued ? "queued" : s.endLabel}`}
          onClick={(e) => e.stopPropagation()}
        >
          {s.failed && (
            <span className="absolute -right-1 -top-1.5 text-meta font-bold leading-none text-danger-ink" aria-hidden="true">
              ✕
            </span>
          )}
        </Link>
      ))}
    </div>
  );
}
