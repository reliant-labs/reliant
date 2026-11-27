// Copyright (c) 2026 Reliant Labs

/**
 * The Evals pages' view-models, as pure functions of the contract's
 * messages: the matrix (rows × tasks, cells coloured only by significance),
 * the header numbers, the score-vs-step series of eval-periodic, an eval
 * run's results against its baseline, and the samples drill's filters.
 */

import { create } from "@bufbuild/protobuf";

import {
  MlAxis,
  MlCollection,
  MlSeriesSchema,
  MlSubjectKind,
  type GetMlEvalMatrixResponse,
  type MlEvalCell,
  type MlEvalRow,
  type MlEvalRun,
  type MlEvalSample,
  type MlEvalSuite,
  type MlEvalTask,
  type MlSeries,
} from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";

import { mlHref } from "../kit/links";
import type { MlTone } from "../kit/labels";
import { tsMs } from "../kit/format";

// ── Numbers ───────────────────────────────────────────────────────────────────

const MINUS = "−";

/** A fraction score as points: 0.7005 → "70.1". */
export const fmtScore = (v: number | undefined | null): string => (typeof v === "number" && Number.isFinite(v) ? (v * 100).toFixed(1) : "—");

/** A delta in points with its sign: −0.0115 → "−1.1", 0.008 → "+0.8". */
export function fmtDeltaPt(d: number | undefined | null): string {
  if (typeof d !== "number" || !Number.isFinite(d)) return "—";
  const pt = d * 100;
  if (Math.abs(pt) < 0.05) return "±0.0";
  return `${pt > 0 ? "+" : MINUS}${Math.abs(pt).toFixed(1)}`;
}

/** The 95% half-width of a score in points: "±2.5". */
export const fmtCi = (stderr: number): string => (Number.isFinite(stderr) ? `±${(1.96 * stderr * 100).toFixed(1)}` : "");

/** A p-value as people read it. */
export function fmtP(p: number | undefined | null): string {
  if (typeof p !== "number" || !Number.isFinite(p)) return "—";
  if (p < 0.001) return "p < 0.001";
  return `p = ${p < 0.01 ? p.toFixed(3) : p.toFixed(2)}`;
}

/**
 * A cell's tone: by significance, never by the raw delta. A significant
 * regression is danger, a significant improvement success, anything else
 * plain (02-views §4.1).
 */
export function cellTone(cell: Pick<MlEvalCell, "significant" | "regression">): MlTone {
  if (!cell.significant) return "neutral";
  return cell.regression ? "danger" : "success";
}

// ── Matrix ────────────────────────────────────────────────────────────────────

export interface MatrixRowVm {
  row: MlEvalRow;
  isBaseline: boolean;
  cells: ReadonlyMap<string, MlEvalCell>;
}

export interface MatrixVm {
  suite: MlEvalSuite | undefined;
  columns: MlEvalTask[];
  rows: MatrixRowVm[];
  baseline: MlEvalRow | undefined;
  categories: string[];
}

/** The matrix, its task columns filtered to `category` (unset: every task). */
export function buildMatrix(m: GetMlEvalMatrixResponse, category?: string): MatrixVm {
  const tasks = m.suite?.tasks ?? [];
  const categories = [...new Set(tasks.map((t) => t.category))];
  const columns = category ? tasks.filter((t) => t.category === category) : tasks;
  const byRow = new Map<string, Map<string, MlEvalCell>>();
  for (const c of m.cells) {
    const row = byRow.get(c.rowId) ?? new Map<string, MlEvalCell>();
    row.set(c.taskKey, c);
    byRow.set(c.rowId, row);
  }
  return {
    suite: m.suite,
    columns,
    categories,
    baseline: m.rows.find((r) => r.id === m.baselineRowId),
    rows: m.rows.map((row) => ({ row, isBaseline: row.id === m.baselineRowId, cells: byRow.get(row.id) ?? new Map() })),
  };
}

/** Where a cell opens: its eval run's task drill, against the baseline's eval run. */
export const cellHref = (row: MlEvalRow, task: string, baselineEvalRunId: string | undefined) =>
  mlHref("evals", ["runs", row.evalRunId], { task, ...(baselineEvalRunId && baselineEvalRunId !== row.evalRunId ? { baseline: baselineEvalRunId } : {}) });

/** The row a person reads first as "the candidate": the newest evaluated row aliased candidate. */
export function newestCandidate(rows: readonly MlEvalRow[]): MlEvalRow | undefined {
  return [...rows].filter((r) => r.alias === "candidate").sort((a, b) => (tsMs(b.evaluatedAt) ?? 0) - (tsMs(a.evaluatedAt) ?? 0))[0];
}

/** Significant regressions against the baseline, across every non-baseline row. */
export function significantRegressions(m: GetMlEvalMatrixResponse): MlEvalCell[] {
  return m.cells.filter((c) => c.rowId !== m.baselineRowId && c.significant && c.regression);
}

/** The subjects compared to the baseline that are ours (external API baselines excluded). */
export const candidatesEvaluated = (m: GetMlEvalMatrixResponse) => m.rows.filter((r) => r.id !== m.baselineRowId && r.subject?.kind !== MlSubjectKind.UNSPECIFIED).length;

/** The runs that ended within `days` of `now`. */
export const runsWithin = (runs: readonly MlEvalRun[], now: number, days: number) => runs.filter((r) => (tsMs(r.endedAt) ?? 0) >= now - days * 86_400_000);

export const spendOf = (runs: readonly MlEvalRun[]) => runs.reduce((sum, r) => sum + (r.cost?.usd ?? 0), 0);
export const samplesOf = (runs: readonly MlEvalRun[]) => runs.reduce((sum, r) => sum + r.results.reduce((s, x) => s + x.n, 0), 0);

/** Newest first. */
export const byEnded = (runs: readonly MlEvalRun[]) => [...runs].sort((a, b) => (tsMs(b.endedAt) ?? 0) - (tsMs(a.endedAt) ?? 0));

// ── Score vs step (eval-periodic) ─────────────────────────────────────────────

/** The training step of a checkpoint subject id ("<run>/<phase>/<step>"), or null. */
export function stepOfCheckpoint(id: string): number | null {
  const step = Number(id.split("/").at(-1));
  return Number.isFinite(step) && id.split("/").length === 3 ? step : null;
}

export interface StepPanel {
  task: MlEvalTask;
  series: MlSeries[];
  latest: number | null;
  reference: number | null;
}

/**
 * eval-periodic's runs (checkpoint subjects of one run phase) as one panel
 * per task: the score at each step, and the reference (production) as a
 * dashed flat line over the same steps.
 */
export function scoreVsStep(runs: readonly MlEvalRun[], suite: MlEvalSuite | undefined, phaseId: string, reference: MlEvalRun | undefined): StepPanel[] {
  if (!suite) return [];
  const periodic = runs
    .filter((r) => r.phase?.id === phaseId && r.suiteId === suite.id && r.subject?.kind === MlSubjectKind.CHECKPOINT)
    .map((r) => ({ run: r, step: stepOfCheckpoint(r.subject!.id) }))
    .filter((x): x is { run: MlEvalRun; step: number } => x.step !== null)
    .sort((a, b) => a.step - b.step);
  return suite.tasks.map((task) => {
    const x = periodic.map((p) => p.step);
    const y = periodic.map((p) => p.run.results.find((r) => r.taskKey === task.key)?.value ?? Number.NaN);
    const ref = reference?.results.find((r) => r.taskKey === task.key)?.value ?? null;
    const series = [
      create(MlSeriesSchema, { key: `eval/${task.key}`, label: task.name, group: "candidate", unit: "ratio", axis: MlAxis.STEP, x, y, collection: MlCollection.COLLECTED, subject: periodic[0]?.run.phase }),
      ...(ref !== null && x.length
        ? [create(MlSeriesSchema, { key: `eval/${task.key}`, label: task.name, group: "production", stat: "v2", unit: "ratio", axis: MlAxis.STEP, x, y: x.map(() => ref), collection: MlCollection.COLLECTED })]
        : []),
    ];
    const finite = y.filter(Number.isFinite);
    return { task, series, latest: finite.length ? finite[finite.length - 1] : null, reference: ref };
  });
}

// ── An eval run ───────────────────────────────────────────────────────────────

export interface ResultVm {
  task: MlEvalTask | undefined;
  key: string;
  metric: string;
  value: number;
  stderr: number;
  n: number;
  baseline: number | null;
  delta: number | null;
  cell: MlEvalCell | undefined;
}

/** An eval run's per-task results beside its baseline's, with the matrix cell when the run is a matrix row. */
export function resultsVm(run: MlEvalRun, suite: MlEvalSuite | undefined, baseline: MlEvalRun | undefined, cells: readonly MlEvalCell[]): ResultVm[] {
  return run.results.map((r) => {
    const b = baseline && baseline.id !== run.id ? baseline.results.find((x) => x.taskKey === r.taskKey) : undefined;
    return {
      task: suite?.tasks.find((t) => t.key === r.taskKey),
      key: r.taskKey,
      metric: r.metric,
      value: r.value,
      stderr: r.stderr,
      n: r.n,
      baseline: b ? b.value : null,
      delta: b ? r.value - b.value : null,
      cell: cells.find((c) => c.taskKey === r.taskKey),
    };
  });
}

export interface SliceVm {
  task: string;
  slice: string;
  dimension: string;
  value: string;
  score: number;
  n: number;
  baseline: number | null;
  delta: number | null;
}

/** Every slice of `run` (or of one task), beside the baseline's same slice. */
export function slicesVm(run: MlEvalRun, baseline: MlEvalRun | undefined, task?: string): SliceVm[] {
  return run.results
    .filter((r) => !task || r.taskKey === task)
    .flatMap((r) =>
      r.slices.map((s) => {
        const b = baseline && baseline.id !== run.id ? baseline.results.find((x) => x.taskKey === r.taskKey)?.slices.find((x) => x.slice === s.slice) : undefined;
        const [dimension, value] = s.slice.includes("=") ? s.slice.split("=") : ["", s.slice];
        return { task: r.taskKey, slice: s.slice, dimension, value, score: s.value, n: s.n, baseline: b ? b.value : null, delta: b ? s.value - b.value : null };
      }),
    );
}

// ── Samples ───────────────────────────────────────────────────────────────────

export const SAMPLE_FILTERS = ["all", "regressed", "improved", "same"] as const;
export type SampleFilter = (typeof SAMPLE_FILTERS)[number];

export const sampleFilterOf = (value: string | undefined): SampleFilter => ((SAMPLE_FILTERS as readonly string[]).includes(value ?? "") ? (value as SampleFilter) : "all");

/** A judged task scores 1–5; a graded one 0 or 1. */
export const isJudged = (samples: readonly MlEvalSample[]) => samples.some((s) => s.score > 1 || s.baselineScore > 1);

/** A sample's score as written: "✓" / "✕", or "4 / 5" for a judged task. */
export const fmtSampleScore = (score: number, judged: boolean) => (judged ? `${score} / 5` : score >= 1 ? "✓" : "✕");

export const flipTone = (flip: string): MlTone => (flip === "regressed" ? "danger" : flip === "improved" ? "success" : "neutral");

/** The slices a samples drill can filter on: the task result's slice keys. */
export function sliceOptions(run: MlEvalRun | undefined, task: string): string[] {
  return run?.results.find((r) => r.taskKey === task)?.slices.map((s) => s.slice) ?? [];
}

/** The "matrix" suite to show: the URL's, else the first. */
export const pickSuite = (suites: readonly MlEvalSuite[], id: string | undefined) => suites.find((s) => s.id === id) ?? suites[0];

/** A task's heading: "GSM8K · 8-shot CoT". */
export const taskTitle = (task: Pick<MlEvalTask, "name" | "setting"> | undefined, key: string) => (task ? `${task.name}${task.setting ? ` · ${task.setting}` : ""}` : key);
