// Copyright (c) 2026 Reliant Labs

/**
 * Run comparison (`/forge/ml/runs/compare?compare=a,b,c`, 02-views §3.14),
 * the body only (MlTrainingView frames it): a summary row per run with
 * deltas against the baseline (`?baseline`, else the first), the config keys
 * that differ, the runs' loss, eval, grad-norm and tokens/s curves overlaid
 * on one synced cursor (each run one line, its attempts merged), and the
 * system diff (MFU and where each run's step goes).
 */

import { useMemo } from "react";
import { useQueries } from "@tanstack/react-query";

import { FilterBar, FilterChip } from "@/components/forge-ui/filter_bar";
import { mlKeys } from "@/hooks/ml-queries";
import { MlAxis, MlSubjectKind, type GetMlRunResponse, type MlSeries, type QueryMlSeriesResponse } from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";
import type { MlMethod, MlRequest, MlResponse } from "@/services/controlPlane/ml/mlApi";
import { useMlApi } from "@/services/controlPlane/ml/MlApiContext";
import type { MlOutcome } from "@/services/controlPlane/ml/types";

import { MlNoData, useMlGate } from "../../MlParts";
import { EmptyNote, fmtCompact, fmtGpuHours, fmtPct, fmtStep, fmtUsd, Grid, Meter, MlTable, Panel, SubjectLink, useMlSearch, type MlColumn } from "../../kit";
import { MetricTile } from "../obs/MetricTile";
import { COMPARE_TASKS, compareRow, configDiff, delta, deltaTone, mergeAttempts, type CompareRow } from "./compareModel";

const SYNC = "ml-run-compare";
const PALETTE = ["--chart-1", "--chart-2", "--chart-3", "--chart-4", "--chart-5", "--chart-6"] as const;
const CURVES = [
  { title: "Train loss", key: "train/loss" },
  { title: "Val loss", key: "val/loss" },
  { title: "Grad norm", key: "optim/grad_norm" },
  { title: "Tokens/s", key: "throughput/tokens_per_s" },
  { title: "MMLU-Pro (eval-periodic)", key: "eval/mmlu_pro" },
  { title: "Step time", key: "timers/step_ms" },
] as const;

/** Several reads of one rpc, one per request, cached as useMl caches each. */
function useMlMany<M extends MlMethod>(method: M, reqs: readonly MlRequest<M>[]) {
  const { orgId } = useMlGate();
  const api = useMlApi();
  return useQueries({
    queries: reqs.map((req) => ({
      queryKey: mlKeys.read(orgId, method, req),
      queryFn: ({ signal }: { signal: AbortSignal }) => api.read(orgId, method, req, { signal }) as Promise<MlOutcome<MlResponse<M>>>,
      enabled: !!orgId,
      staleTime: 30_000,
      retry: false,
    })),
  });
}

const okOf = <T,>(d: MlOutcome<T> | undefined): T | undefined => (d?.kind === "ok" ? d.data : undefined);

export function RunCompare({ ids }: { ids: string[] }) {
  const [search, setSearch] = useMlSearch();
  const runReqs = useMemo(() => ids.map((run) => ({ run })), [ids]);
  const runs = useMlMany("getMlRun", runReqs);
  const seriesReqs = useMemo(() => ids.map((id) => ({ subject: { kind: MlSubjectKind.RUN, id, label: id }, keys: CURVES.map((c) => c.key), axis: MlAxis.STEP, maxPoints: 200 })), [ids]);
  const series = useMlMany("queryMlSeries", seriesReqs);

  if (ids.length < 2) {
    return (
      <Panel title="Compare runs" testId="ml-run-compare">
        <EmptyNote>Pick two or more runs to compare: select them in the Training list, or add `?compare=a,b`.</EmptyNote>
      </Panel>
    );
  }
  if (runs.some((q) => q.isLoading)) return <Panel title="Compare runs" testId="ml-run-compare"><EmptyNote>Reading {ids.length} runs…</EmptyNote></Panel>;
  if (runs.every((q) => q.data?.kind === "no-data")) return <MlNoData what="runs" />;
  const details = runs.map((q) => okOf(q.data as MlOutcome<GetMlRunResponse> | undefined)).filter((d): d is GetMlRunResponse => !!d?.run);
  const missing = ids.filter((id) => !details.some((d) => d.run!.id === id || d.run!.name === id));
  const baselineId = details.find((d) => d.run!.id === search.baseline || d.run!.name === search.baseline)?.run!.id ?? details[0]?.run!.id;
  const seriesById = new Map<string, readonly MlSeries[]>(ids.map((id, i) => [id, okOf(series[i]?.data as MlOutcome<QueryMlSeriesResponse> | undefined)?.series ?? []]));
  return (
    <div className="space-y-4" data-testid="ml-run-compare">
      <FilterBar label="Baseline" testId="ml-compare-baseline" trailing={missing.length ? <span className="text-meta text-warning-ink">Not found: {missing.join(", ")}</span> : undefined}>
        <span className="text-meta text-ink-muted">Baseline</span>
        {details.map((d) => (
          <FilterChip key={d.run!.id} on={d.run!.id === baselineId} onClick={() => setSearch({ baseline: d.run!.id === details[0].run!.id ? undefined : d.run!.id })} testId={`ml-compare-baseline-${d.run!.name}`}>
            {d.run!.name}
          </FilterChip>
        ))}
      </FilterBar>
      <SummaryTable details={details} baselineId={baselineId} />
      <Grid cols={2}>
        <ConfigDiff details={details} />
        <SystemDiff details={details} />
      </Grid>
      <Curves details={details} seriesById={seriesById} ids={ids} />
    </div>
  );
}

function Delta({ value, base, kind, format }: { value: number; base: number; kind: "loss" | "higher" | "cost"; format: (v: number) => string }) {
  const d = delta(value, base);
  const tone = deltaTone(kind, d);
  if (!tone) return null;
  return <span className={tone === "danger" ? "ml-1.5 text-meta text-danger-ink" : "ml-1.5 text-meta text-ink-muted"}>{`${d > 0 ? "+" : "−"}${format(Math.abs(d))}`}</span>;
}

function SummaryTable({ details, baselineId }: { details: readonly GetMlRunResponse[]; baselineId?: string }) {
  const rows = details.map(compareRow);
  const base = rows.find((r) => r.id === baselineId) ?? rows[0];
  const isBase = (r: CompareRow) => r.id === base?.id;
  const num3 = (v: number) => (Number.isFinite(v) ? v.toFixed(3) : "—");
  const pts = (v: number) => `${(v * 100).toFixed(1)}`;
  const columns: MlColumn<CompareRow>[] = [
    {
      key: "run",
      header: "Run",
      render: (r) => (
        <span className="inline-flex items-center gap-2">
          <span className="h-2 w-2 rounded-full" style={{ backgroundColor: `hsl(var(${PALETTE[rows.indexOf(r) % PALETTE.length]}))` }} aria-hidden="true" />
          <SubjectLink subject={{ kind: MlSubjectKind.RUN, id: r.id, label: r.name }}>{r.name}</SubjectLink>
          {isBase(r) && <span className="text-meta text-ink-muted">baseline</span>}
        </span>
      ),
    },
    { key: "objective", header: "Objective", numeric: true, render: (r) => <>{num3(r.objective)}{!isBase(r) && <Delta value={r.objective} base={base.objective} kind="loss" format={num3} />}</>, sort: (r) => r.objective },
    { key: "best", header: "Best val loss", numeric: true, render: (r) => <span title={r.bestValStep ? `at step ${fmtStep(r.bestValStep)}` : undefined}>{num3(r.bestValLoss)}{!isBase(r) && <Delta value={r.bestValLoss} base={base.bestValLoss} kind="loss" format={num3} />}</span>, sort: (r) => r.bestValLoss },
    ...COMPARE_TASKS.map<MlColumn<CompareRow>>((t) => ({
      key: t,
      header: t,
      numeric: true,
      render: (r) => <>{Number.isFinite(r.evals[t]) ? fmtPct(r.evals[t], 1) : "—"}{!isBase(r) && <Delta value={r.evals[t]} base={base.evals[t]} kind="higher" format={pts} />}</>,
      sort: (r) => r.evals[t],
    })),
    { key: "tokens", header: "Tokens", numeric: true, render: (r) => fmtCompact(r.tokens), sort: (r) => r.tokens },
    { key: "gpuh", header: "GPU-h", numeric: true, render: (r) => fmtGpuHours(r.gpuHours), sort: (r) => r.gpuHours },
    { key: "usd", header: "Cost", numeric: true, render: (r) => fmtUsd(r.usd), sort: (r) => r.usd },
    { key: "goodput", header: "Goodput", numeric: true, render: (r) => <>{fmtPct(r.goodput)}{!isBase(r) && <Delta value={r.goodput} base={base.goodput} kind="higher" format={(v) => `${(v * 100).toFixed(0)} pts`} />}</>, sort: (r) => r.goodput },
    { key: "mfu", header: "MFU", numeric: true, render: (r) => fmtPct(r.mfu), sort: (r) => r.mfu },
    { key: "restarts", header: "Restarts", numeric: true, render: (r) => r.restarts, sort: (r) => r.restarts },
  ];
  return (
    <Panel title="Summary" note="deltas against the baseline; red where it is worse" flush testId="ml-compare-summary">
      <MlTable columns={columns} rows={rows} rowKey={(r) => r.id} testId="ml-compare-summary-table" />
    </Panel>
  );
}

function ConfigDiff({ details }: { details: readonly GetMlRunResponse[] }) {
  const rows = configDiff(details);
  const columns: MlColumn<(typeof rows)[number]>[] = [
    { key: "key", header: "Key", render: (r) => <span className="font-mono text-code text-ink">{r.key}</span> },
    ...details.map((d, i) => ({
      key: d.run!.id,
      header: d.run!.name.replace(/^llama-8b-support-/, ""),
      render: (r: (typeof rows)[number]) => <span className={r.values[i] !== r.values[0] ? "font-mono text-code text-ink" : "font-mono text-code text-ink-muted"}>{r.values[i]}</span>,
    })),
  ];
  return (
    <Panel title="Config diff" note={`${rows.length} keys differ`} flush testId="ml-compare-config">
      <MlTable columns={columns} rows={rows} rowKey={(r) => r.key} maxRows={14} empty={<EmptyNote>Every config key is the same.</EmptyNote>} testId="ml-compare-config-table" />
    </Panel>
  );
}

const STAGE_COLORS: Record<string, string> = { data_wait: "--chart-4", forward: "--chart-1", backward: "--chart-2", optimizer: "--chart-5", comm_exposed: "--chart-3", checkpoint: "--chart-6", rollout: "--chart-6" };

function SystemDiff({ details }: { details: readonly GetMlRunResponse[] }) {
  return (
    <Panel title="System diff" note="MFU and where each run's step goes (current phase, p50)" testId="ml-compare-system">
      <div className="space-y-3">
        {details.map((d) => {
          const row = compareRow(d);
          const stages = d.stepBreakdown;
          const total = stages.reduce((s, x) => s + x.msP50, 0);
          return (
            <div key={d.run!.id} className="space-y-1">
              <div className="flex items-baseline justify-between gap-3 text-ui">
                <span className="truncate text-ink">{d.run!.name}</span>
                <span className="flex items-center gap-3 text-meta text-ink-muted">
                  MFU <Meter ratio={row.mfu} />
                  <span className="tabular-nums">{total ? `${(total / 1000).toFixed(1)} s/step` : "—"}</span>
                </span>
              </div>
              {total > 0 ? (
                <div className="flex h-2.5 overflow-hidden rounded-full bg-surface-sunken">
                  {stages.map((s) => (
                    <span key={s.stage} className="h-full border-l border-surface first:border-0" style={{ width: `${(s.msP50 / total) * 100}%`, backgroundColor: `hsl(var(${STAGE_COLORS[s.stage] ?? "--chart-6"}))` }} title={`${s.stage}: ${Math.round(s.msP50)} ms`} />
                  ))}
                </div>
              ) : (
                <p className="text-meta text-ink-muted">No step breakdown reported.</p>
              )}
            </div>
          );
        })}
        <ul className="flex flex-wrap gap-x-4 gap-y-1 text-meta text-ink-muted">
          {Object.entries(STAGE_COLORS)
            .filter(([k]) => k !== "rollout")
            .map(([k, c]) => (
              <li key={k} className="inline-flex items-center gap-1.5">
                <span className="h-2 w-2 rounded-sm" style={{ backgroundColor: `hsl(var(${c}))` }} aria-hidden="true" />
                {k.replace("_", " ")}
              </li>
            ))}
        </ul>
      </div>
    </Panel>
  );
}

function Curves({ details, seriesById, ids }: { details: readonly GetMlRunResponse[]; seriesById: ReadonlyMap<string, readonly MlSeries[]>; ids: readonly string[] }) {
  const colors = useMemo(() => new Map(details.map((d, i) => [d.run!.name, PALETTE[i % PALETTE.length] as string])), [details]);
  return (
    <section className="space-y-2" data-testid="ml-compare-curves">
      <div className="flex flex-wrap items-baseline gap-3">
        <h2 className="text-section text-ink">Curves</h2>
        <span className="text-meta text-ink-muted">step axis, each run's current phase; attempts merged</span>
        <ul className="flex flex-wrap gap-x-4 text-meta text-ink-muted">
          {[...colors.entries()].map(([name, c]) => (
            <li key={name} className="inline-flex items-center gap-1.5">
              <span className="h-0.5 w-4 rounded-full" style={{ backgroundColor: `hsl(var(${c}))` }} aria-hidden="true" />
              {name}
            </li>
          ))}
        </ul>
      </div>
      <div className="grid min-w-0 gap-3 sm:grid-cols-2 xl:grid-cols-3">
        {CURVES.map((c) => {
          const lines = details.flatMap((d) => {
            const id = ids.find((x) => x === d.run!.id || x === d.run!.name) ?? d.run!.id;
            const m = mergeAttempts(seriesById.get(id) ?? [], c.key, d.run!.name);
            return m ? [m] : [];
          });
          return <MetricTile key={c.key} title={c.title} series={lines} syncKey={SYNC} colors={colors} testId={`ml-compare-${c.key}`} />;
        })}
      </div>
    </section>
  );
}
