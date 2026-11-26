// Copyright (c) 2026 Reliant Labs

/**
 * ML › Evals › <eval run> (02-views §4.2): what was evaluated, how (harness,
 * suite version, dataset digests, seed, judge), what it cost, every task's
 * score with its interval against the baseline, and the slices. `?task=`
 * opens that task's samples (§4.3) above the results; `?baseline=` names
 * the eval run to compare with (default: the suite's production row).
 */

import { useMemo } from "react";
import type { UseQueryResult } from "@tanstack/react-query";

import { MlSubjectKind, type MlEvalRun } from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";
import type { MlOutcome } from "@/services/controlPlane/ml/types";
import { cn } from "@/lib/utils";

import { MlFrame, MlRead } from "../MlParts";
import {
  EmptyNote,
  FactList,
  fmtCompact,
  fmtDuration,
  fmtGpuHours,
  fmtInt,
  fmtMs,
  fmtTime,
  fmtUsd,
  Grid,
  KpiStrip,
  MlLink,
  mlHref,
  MlTable,
  Panel,
  StatusDot,
  SubjectLink,
  TONE_DOT,
  tsMs,
  useMl,
  useMlSearch,
} from "../kit";
import { SamplesDrill } from "./SamplesDrill";
import { cellTone, fmtCi, fmtDeltaPt, fmtScore, resultsVm, slicesVm, taskTitle, type ResultVm } from "./evalsModel";

function dataOf<T>(q: UseQueryResult<MlOutcome<T>, unknown>): T | undefined {
  return q.data?.kind === "ok" ? q.data.data : undefined;
}

export const shortDigest = (d: string) => d.replace(/^sha256:/, "").slice(0, 12);

function Subject({ run }: { run: MlEvalRun }) {
  if (!run.subject) return <>—</>;
  return run.subject.kind === MlSubjectKind.UNSPECIFIED ? <>{run.subject.label}</> : <SubjectLink subject={run.subject} />;
}

function Delta({ r }: { r: ResultVm }) {
  if (r.delta === null) return <span className="text-ink-muted">—</span>;
  const tone = r.cell ? cellTone(r.cell) : "neutral";
  return (
    <span className="inline-flex items-center justify-end gap-1.5" title={r.cell ? (r.cell.significant ? "significant at 95% (paired)" : "not significant") : undefined}>
      {tone !== "neutral" && <span className={cn("h-1.5 w-1.5 rounded-full", TONE_DOT[tone])} aria-hidden="true" />}
      <span className={tone === "danger" ? "text-danger-ink" : undefined}>{fmtDeltaPt(r.delta)}</span>
    </span>
  );
}

export function EvalRunPage({ id }: { id: string }) {
  const [search, setSearch] = useMlSearch();
  const runQ = useMl("getMlEvalRun", { evalRunId: id });
  const run = dataOf(runQ)?.evalRun;
  const suites = useMl("listMlEvalSuites", {});
  const suite = dataOf(suites)?.suites.find((s) => s.id === run?.suiteId);
  const matrix = useMl("getMlEvalMatrix", { suiteId: run?.suiteId ?? "" }, { enabled: !!run });
  const m = dataOf(matrix);
  const defaultBaseline = m?.rows.find((r) => r.id === m.baselineRowId)?.evalRunId;
  const baselineId = search.baseline || defaultBaseline || "";
  const baselineQ = useMl("getMlEvalRun", { evalRunId: baselineId }, { enabled: !!baselineId && baselineId !== id });
  const baseline = baselineId === id ? run : dataOf(baselineQ)?.evalRun;
  const row = m?.rows.find((r) => r.evalRunId === id);
  // The matrix's cells are against the production row; use them only when that is the baseline here.
  const cells = useMemo(() => (m && row && baselineId === defaultBaseline ? m.cells.filter((c) => c.rowId === row.id) : []), [m, row, baselineId, defaultBaseline]);
  const results = useMemo(() => (run ? resultsVm(run, suite, baseline, cells) : []), [run, suite, baseline, cells]);
  const slices = useMemo(() => (run ? slicesVm(run, baseline, search.task) : []), [run, baseline, search.task]);
  const task = search.task ? suite?.tasks.find((t) => t.key === search.task) : undefined;
  const title = run ? `${suite ? `${suite.name} ${suite.version}` : run.suiteId} on ${run.subject?.label ?? "—"}` : id;
  const seconds = run ? ((tsMs(run.endedAt) ?? 0) - (tsMs(run.startedAt) ?? 0)) / 1000 : Number.NaN;
  const judgeUsd = (cfg: MlEvalRun["config"]) => /\$([\d.]+)/.exec(cfg.find((c) => c.label === "judge")?.value ?? "")?.[1];

  return (
    <MlFrame
      view="evals"
      title={title}
      subtitle={run ? <span className="font-mono text-code">{id}</span> : undefined}
      meta={run ? <StatusDot tone={run.state === "failed" ? "danger" : run.state === "running" ? "info" : "success"} label={run.state} pulse={run.state === "running"} /> : undefined}
      crumbs={[{ label: id, href: mlHref("evals", ["runs", id]) }, ...(search.task ? [{ label: taskTitle(task, search.task) }] : [])]}
      testId="ml-page-eval-run"
    >
      <MlRead query={runQ} what="eval run">
        {({ evalRun: r }) =>
          !r ? (
            <EmptyNote>No such eval run.</EmptyNote>
          ) : (
            <div className="space-y-4">
              <KpiStrip
                testId="ml-eval-run-kpis"
                items={[
                  { label: "Subject", value: <Subject run={r} />, sub: row?.alias ? `alias ${row.alias}` : r.phase?.id ? `in ${r.phase.label}` : undefined },
                  {
                    label: "Gate",
                    value: row?.gateStatus ? (row.gateStatus === "fail" ? "Fails" : "Passes") : "—",
                    tone: row?.gateStatus ? (row.gateStatus === "fail" ? "danger" : "success") : undefined,
                    sub: row ? row.gateFailures.join(" · ") || "every gate passed" : "not a matrix row",
                    hero: true,
                  },
                  { label: "Samples", value: fmtCompact(r.results.reduce((s, x) => s + x.n, 0)), sub: `${r.results.length} tasks${r.sampleLimit ? ` · limit ${fmtInt(r.sampleLimit)}` : ""}` },
                  { label: "Took", value: fmtDuration(seconds), sub: `${fmtTime(tsMs(r.startedAt))} · by ${r.ranBy}` },
                  { label: "Tokens", value: fmtCompact(Number(r.tokens)), sub: `latency p50 ${fmtMs(r.latencyP50Ms)} · p95 ${fmtMs(r.latencyP95Ms)}` },
                  {
                    label: "Cost",
                    value: fmtUsd(r.cost?.usd),
                    sub: `${r.cost && r.cost.gpuHours > 0 ? fmtGpuHours(r.cost.gpuHours) : "API"}${judgeUsd(r.config) ? ` · judge $${judgeUsd(r.config)}` : ""}`,
                  },
                ]}
              />

              {search.task && <SamplesDrill run={r} baseline={baseline} task={search.task} suite={suite} />}

              <Panel
                title="Results"
                note={
                  baseline && baseline.id !== r.id ? (
                    <>
                      against <MlLink href={mlHref("evals", ["runs", baseline.id], search.task ? { task: search.task } : undefined)}>{baseline.subject?.label ?? baseline.id}</MlLink>
                      {cells.length ? " · dot: significant at 95% (paired)" : ""}
                    </>
                  ) : (
                    "this run is the baseline"
                  )
                }
                flush
                testId="ml-eval-run-results"
              >
                <MlTable
                  testId="ml-eval-run-results-table"
                  rows={results}
                  rowKey={(x) => x.key}
                  selected={search.task}
                  onRowClick={(x) => setSearch({ task: x.key, sel: undefined, filter: undefined, group: undefined }, { push: true })}
                  columns={[
                    { key: "task", header: "Task", render: (x) => <span className="text-ink">{taskTitle(x.task, x.key)}</span> },
                    { key: "metric", header: "Metric", render: (x) => <span className="text-meta text-ink-muted">{x.metric}</span> },
                    { key: "dataset", header: "Dataset", render: (x) => (x.task ? <span className="font-mono text-code text-ink-muted" title={x.task.datasetDigest}>{shortDigest(x.task.datasetDigest)}</span> : "—") },
                    { key: "n", header: "n", numeric: true, render: (x) => fmtInt(x.n) },
                    {
                      key: "score",
                      header: "Score",
                      numeric: true,
                      sort: (x) => x.value,
                      render: (x) => (
                        <span>
                          <span className="text-ink">{fmtScore(x.value)}</span> <span className="text-meta text-ink-muted">{fmtCi(x.stderr)}</span>
                        </span>
                      ),
                    },
                    { key: "base", header: "Baseline", numeric: true, render: (x) => <span className="text-ink-muted">{fmtScore(x.baseline)}</span> },
                    { key: "delta", header: "Δ pt", numeric: true, sort: (x) => x.delta ?? 0, render: (x) => <Delta r={x} /> },
                    { key: "gate", header: "Gate", render: (x) => (x.cell?.gateFail ? <StatusDot tone="danger" label="fails" /> : x.cell ? <span className="text-meta text-ink-muted">ok</span> : "") },
                    {
                      key: "open",
                      header: "",
                      render: (x) => (
                        <MlLink href={mlHref("evals", ["runs", r.id], { task: x.key, ...(search.baseline ? { baseline: search.baseline } : {}) })} className="text-meta">
                          Samples
                        </MlLink>
                      ),
                    },
                  ]}
                />
              </Panel>

              <Grid cols={2}>
                <Panel title={search.task ? `Slices · ${taskTitle(task, search.task)}` : "Slices"} note="Score by subtask, difficulty, length, tier or language" flush testId="ml-eval-run-slices">
                  <MlTable
                    testId="ml-eval-run-slices-table"
                    rows={slices}
                    rowKey={(s) => `${s.task}:${s.slice}`}
                    onRowClick={(s) => setSearch({ task: s.task, group: s.slice, sel: undefined }, { push: true })}
                    empty={<EmptyNote>{search.task ? "This task reports no slices." : "No task reports slices."}</EmptyNote>}
                    columns={[
                      ...(search.task ? [] : [{ key: "task", header: "Task", render: (s: (typeof slices)[number]) => <span className="text-ink-muted">{suite?.tasks.find((t) => t.key === s.task)?.name ?? s.task}</span> }]),
                      { key: "slice", header: "Slice", render: (s) => (s.dimension ? <span><span className="text-ink-muted">{s.dimension} </span><span className="text-ink">{s.value}</span></span> : s.value) },
                      { key: "n", header: "n", numeric: true, render: (s) => fmtInt(s.n) },
                      { key: "score", header: "Score", numeric: true, render: (s) => fmtScore(s.score), sort: (s) => s.score },
                      { key: "base", header: "Baseline", numeric: true, render: (s) => <span className="text-ink-muted">{fmtScore(s.baseline)}</span> },
                      {
                        key: "delta",
                        header: "Δ pt",
                        numeric: true,
                        sort: (s) => s.delta ?? 0,
                        render: (s) => <span className={s.delta !== null && s.delta <= -0.02 ? "text-warning-ink" : undefined}>{fmtDeltaPt(s.delta)}</span>,
                      },
                    ]}
                  />
                </Panel>
                <Panel title="How it ran" testId="ml-eval-run-facts">
                  <FactList
                    columns={1}
                    facts={[
                      { label: "Harness", value: `${r.harness} ${r.harnessVersion}` },
                      { label: "Suite", value: suite ? <MlLink href={mlHref("evals", [], { suite: suite.id })}>{`${suite.name} ${r.suiteVersion}`}</MlLink> : r.suiteId },
                      { label: "Seed", value: <span className="tabular-nums">{String(r.seed)}</span> },
                      { label: "Sample limit", value: r.sampleLimit ? fmtInt(r.sampleLimit) : "none (every sample)" },
                      { label: "Judge", value: r.judgeModel ? `${r.judgeModel} · rubric ${r.judgeRubricVersion}` : "—" },
                      { label: "Hardware", value: r.config.find((c) => c.label === "hardware")?.value ?? "—" },
                      ...(r.phase?.id ? [{ label: "Run phase", value: <SubjectLink subject={r.phase} /> }] : []),
                      { label: "Started", value: fmtTime(tsMs(r.startedAt)) },
                      { label: "Ran by", value: r.ranBy },
                    ]}
                  />
                  <details className="mt-3 text-ui" data-testid="ml-eval-run-config">
                    <summary className="cursor-pointer text-meta text-ink-muted hover:text-ink">Config ({r.config.length} keys)</summary>
                    <div className="mt-2">
                      <FactList columns={1} facts={r.config.map((c) => ({ label: c.label, value: <span className="font-mono text-code" title={c.value}>{c.value}</span> }))} />
                    </div>
                  </details>
                </Panel>
              </Grid>
            </div>
          )
        }
      </MlRead>
    </MlFrame>
  );
}
