// Copyright (c) 2026 Reliant Labs

/**
 * The run comparison's view model: one summary row per run with deltas
 * against the baseline, the config keys whose values differ, and each run's
 * per-attempt lines merged into one line (a later attempt's points replace
 * an earlier one's from where it resumed).
 */

import { create } from "@bufbuild/protobuf";

import { MlCollection, MlSeriesSchema, type GetMlRunResponse, type MlSeries } from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";
import { ML_STEP_KEY } from "@/services/controlPlane/ml/telemetry";

import { onStepAxis } from "../../kit/telemetry";

export interface CompareRow {
  id: string;
  name: string;
  objectiveKey: string;
  objective: number;
  bestValLoss: number;
  bestValStep: number;
  evals: Record<string, number>;
  tokens: number;
  gpuHours: number;
  usd: number;
  goodput: number;
  mfu: number;
  restarts: number;
}

const finite = (v: number | undefined): number => (typeof v === "number" && Number.isFinite(v) ? v : NaN);

/** The tasks shown as columns (the release gate's headline tasks). */
export const COMPARE_TASKS = ["mmlu_pro", "gsm8k", "safety_refusals"] as const;

export function compareRow(d: GetMlRunResponse): CompareRow {
  const run = d.run!;
  let bestValLoss = finite(run.summary["val/loss"]);
  let bestValStep = 0;
  for (const c of d.checkpoints) {
    const v = c.metrics["val/loss"];
    if (Number.isFinite(v) && !(v >= bestValLoss)) {
      bestValLoss = v;
      bestValStep = Number(c.step);
    }
  }
  // The latest evaluated checkpoint's scores, else the run's eval/* summary.
  const evaluated = [...d.checkpoints].filter((c) => Object.keys(c.evalScores).length > 0).sort((a, b) => Number(b.step) - Number(a.step))[0];
  const evals: Record<string, number> = {};
  for (const t of COMPARE_TASKS) evals[t] = finite(evaluated?.evalScores[t] ?? run.summary[`eval/${t}`]);
  return {
    id: run.id,
    name: run.name,
    objectiveKey: run.objectiveKey,
    objective: finite(run.summary[run.objectiveKey]),
    bestValLoss,
    bestValStep,
    evals,
    tokens: Number(run.tokensSeen),
    gpuHours: finite(run.cost?.gpuHours),
    usd: run.cost?.priced ? finite(run.cost.usd) : NaN,
    goodput: finite(run.goodput),
    mfu: finite(run.summary["mfu"] ?? run.summary["throughput/mfu"]),
    restarts: run.restarts,
  };
}

/** a − baseline, NaN when either is unknown. */
export const delta = (a: number, base: number) => (Number.isFinite(a) && Number.isFinite(base) ? a - base : NaN);

/** Whether a delta is good news for a metric (lower loss, cost and restarts are better). */
export function deltaTone(metric: "loss" | "higher" | "cost", d: number): "success" | "danger" | undefined {
  if (!Number.isFinite(d) || Math.abs(d) < 1e-9) return undefined;
  const better = metric === "higher" ? d > 0 : d < 0;
  return better ? "success" : "danger";
}

export interface ConfigDiffRow {
  key: string;
  values: string[];
}

/** The config keys whose values are not all the same across runs ("—" where a run has none). */
export function configDiff(runs: readonly GetMlRunResponse[]): ConfigDiffRow[] {
  const maps = runs.map((d) => {
    const m = new Map<string, string>(Object.entries(d.run?.params ?? {}));
    for (const p of d.config?.params ?? []) m.set(p.phaseKey ? `${p.phaseKey}.${p.key}` : p.key, p.value);
    const par = d.config?.parallelism;
    if (par && par.worldSize) m.set("parallelism", `dp ${par.data} · tp ${par.tensor} · pp ${par.pipeline || 1} (${par.worldSize} GPUs)`);
    if (d.config?.code?.commit) m.set("code.commit", d.config.code.commit.slice(0, 12));
    for (const ds of d.config?.datasets ?? []) m.set(`data.${ds.name}`, `${ds.version}${ds.mixtureWeight ? ` × ${ds.mixtureWeight}` : ""}`);
    return m;
  });
  const keys = [...new Set(maps.flatMap((m) => [...m.keys()]))].sort();
  return keys.map((key) => ({ key, values: maps.map((m) => m.get(key) ?? "—") })).filter((r) => new Set(r.values).size > 1);
}

/** One line per run from its per-attempt lines of `key` (empty when nothing is collected). */
export function mergeAttempts(series: readonly MlSeries[], key: string, runName: string): MlSeries | null {
  const parts = series.filter((s) => s.key === key && s.collection === MlCollection.COLLECTED && s.x.length > 0 && (!s.stat || s.stat === "p50")).sort((a, b) => a.attempt - b.attempt);
  if (parts.length === 0) {
    const missing = series.find((s) => s.key === key);
    return missing ? create(MlSeriesSchema, { ...missing, group: runName }) : null;
  }
  const x: number[] = [];
  const y: number[] = [];
  for (const p of parts) {
    const first = p.x[0];
    while (x.length && x[x.length - 1] >= first) {
      x.pop();
      y.pop();
    }
    x.push(...p.x);
    y.push(...p.y);
  }
  return create(MlSeriesSchema, { ...parts[0], x, y, group: runName, attempt: 0, stat: "" });
}

/** Series with x shifted to seconds since the earliest point of any of them (the step series dropped). */
function sinceStart(series: readonly MlSeries[]): MlSeries[] {
  const curves = series.filter((s) => s.key !== ML_STEP_KEY);
  const t0 = Math.min(...curves.flatMap((s) => (s.x.length ? [s.x[0]] : [])));
  if (!Number.isFinite(t0)) return curves;
  return curves.map((s) => create(MlSeriesSchema, { ...s, x: s.x.map((x) => x - t0) }));
}

/**
 * Every run's curves on one axis: by step when every run with curves reports
 * its step (runs from different weeks line up at the same step), else by time
 * since each run started. A run with no curves collected has no say: its
 * not-collected series stay as they are on either axis.
 */
export function alignRuns(byRun: ReadonlyMap<string, readonly MlSeries[]>): { byStep: boolean; series: Map<string, MlSeries[]> } {
  const collected = (s: readonly MlSeries[]) => s.some((x) => x.key !== ML_STEP_KEY && x.collection === MlCollection.COLLECTED && x.x.length > 0);
  const stepped = new Map([...byRun].map(([id, s]) => [id, onStepAxis(s)] as const));
  const voters = [...byRun].filter(([, s]) => collected(s));
  const byStep = voters.length > 0 && voters.every(([id]) => stepped.get(id) !== null);
  const aligned = (id: string, s: readonly MlSeries[]) => (byStep ? (stepped.get(id) ?? s.filter((x) => x.key !== ML_STEP_KEY)) : sinceStart(s));
  return { byStep, series: new Map([...byRun].map(([id, s]) => [id, aligned(id, s)])) };
}
