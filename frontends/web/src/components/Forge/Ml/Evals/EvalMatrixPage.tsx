// Copyright (c) 2026 Reliant Labs

/**
 * ML › Evals: which candidate is best, what regressed, is it significant,
 * does it pass the gate, and what did it cost (02-views §4.1). The header
 * strip answers it for the newest candidate; the matrix is the evidence;
 * eval-periodic's score vs step shows how the training run got there.
 */

import { useMemo } from "react";
import type { UseQueryResult } from "@tanstack/react-query";

import { MlEventKind, MlSubjectKind } from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";
import { FilterBar, FilterMenu } from "@/components/forge-ui/filter_bar";
import type { MlOutcome } from "@/services/controlPlane/ml/types";

import { MlFrame, MlRead } from "../MlParts";
import {
  ChartTile,
  EmptyNote,
  fmtAgo,
  fmtCompact,
  fmtDuration,
  fmtUsd,
  Grid,
  KpiStrip,
  mlHref,
  MlLink,
  MlTable,
  Panel,
  StatusDot,
  SubjectLink,
  tsMs,
  useMl,
  useMlSearch,
} from "../kit";
import { EvalMatrix } from "./EvalMatrix";
import {
  buildMatrix,
  byEnded,
  candidatesEvaluated,
  fmtDeltaPt,
  fmtScore,
  newestCandidate,
  pickSuite,
  runsWithin,
  samplesOf,
  scoreVsStep,
  significantRegressions,
  spendOf,
} from "./evalsModel";

function dataOf<T>(q: UseQueryResult<MlOutcome<T>, unknown>): T | undefined {
  return q.data?.kind === "ok" ? q.data.data : undefined;
}

/** The events worth marking on a step axis: what restarted or upset the training run. */
const STEP_MARKS = new Set([MlEventKind.RESTART, MlEventKind.LOSS_SPIKE, MlEventKind.REWIND, MlEventKind.NCCL_TIMEOUT, MlEventKind.XID, MlEventKind.PREEMPTION, MlEventKind.NAN]);

export function EvalMatrixPage() {
  const [search, setSearch] = useMlSearch();
  const now = Date.now();
  const suites = useMl("listMlEvalSuites", {});
  const suiteList = useMemo(() => dataOf(suites)?.suites ?? [], [suites]);
  const suite = pickSuite(suiteList, search.suite);
  const matrix = useMl("getMlEvalMatrix", { suiteId: suite?.id ?? "", ...(search.baseline ? { baseline: search.baseline } : {}) }, { enabled: !!suite });
  const runs = useMl("listMlEvalRuns", {});
  const allRuns = useMemo(() => dataOf(runs)?.evalRuns ?? [], [runs]);

  // eval-periodic: the newest run that ran inside a run phase, and the training phase its checkpoints come from.
  const periodic = useMemo(() => byEnded(allRuns).find((r) => r.phase?.id && r.subject?.kind === MlSubjectKind.CHECKPOINT), [allRuns]);
  const periodicSuite = suiteList.find((s) => s.id === periodic?.suiteId);
  const trainingPhase = periodic?.subject ? periodic.subject.id.split("/").slice(0, 2).join("/") : "";
  const reference = useMemo(
    () => byEnded(allRuns).find((r) => r.suiteId === periodic?.suiteId && r.subject?.kind === MlSubjectKind.MODEL_VERSION && !r.phase?.id),
    [allRuns, periodic],
  );
  const events = useMl("listMlEvents", { subject: { kind: MlSubjectKind.PHASE, id: trainingPhase } }, { enabled: !!trainingPhase });
  const marks = useMemo(() => (dataOf(events)?.events ?? []).filter((e) => STEP_MARKS.has(e.kind) && Number(e.step) > 0), [events]);
  const panels = useMemo(() => scoreVsStep(allRuns, periodicSuite, periodic?.phase?.id ?? "", reference), [allRuns, periodicSuite, periodic, reference]);

  const m = dataOf(matrix);
  const cand = m ? newestCandidate(m.rows) : undefined;
  const regressions = m ? significantRegressions(m) : [];
  const week = runsWithin(allRuns, now, 7);
  const vm = useMemo(() => (m ? buildMatrix(m, search.filter) : undefined), [m, search.filter]);
  const taskName = (key: string) => m?.suite?.tasks.find((t) => t.key === key)?.name ?? key;

  return (
    <MlFrame
      view="evals"
      controls={
        <FilterBar
          label="Matrix"
          active={(search.baseline ? 1 : 0) + (search.filter ? 1 : 0)}
          onClear={() => setSearch({ baseline: undefined, filter: undefined })}
          testId="ml-evals-filters"
        >
          <FilterMenu
            label="Suite"
            value={suite?.id}
            options={suiteList.map((s) => ({ value: s.id, label: `${s.name} ${s.version}`, count: s.tasks.length }))}
            onChange={(v) => setSearch({ suite: v, baseline: undefined, filter: undefined })}
            testId="ml-evals-suite"
          />
          <FilterMenu
            label="Baseline"
            value={search.baseline}
            options={(m?.rows ?? []).map((r) => ({ value: r.id, label: `${r.label}${r.alias ? ` (${r.alias})` : ""}` }))}
            onChange={(v) => setSearch({ baseline: v })}
            testId="ml-evals-baseline"
          />
          <FilterMenu
            label="Category"
            value={search.filter}
            options={(vm?.categories ?? []).map((c) => ({ value: c, label: c, count: m?.suite?.tasks.filter((t) => t.category === c).length }))}
            onChange={(v) => setSearch({ filter: v })}
            testId="ml-evals-category"
          />
        </FilterBar>
      }
    >
      <KpiStrip
        testId="ml-evals-kpis"
        items={[
          {
            label: "Candidates evaluated",
            value: m ? `${candidatesEvaluated(m)}` : "—",
            sub: m?.suite ? `on ${m.suite.name} ${m.suite.version} · vs ${m.rows.find((r) => r.id === m.baselineRowId)?.label ?? "—"}` : undefined,
          },
          {
            label: "Newest candidate",
            value: cand ? (cand.gateStatus === "fail" ? "Gate fails" : "Gate passes") : "—",
            tone: cand ? (cand.gateStatus === "fail" ? "danger" : "success") : undefined,
            sub: cand ? `${cand.label} · ${cand.gateFailures[0] ?? "every gate passed"}` : undefined,
            href: cand ? mlHref("evals", ["runs", cand.evalRunId]) : undefined,
            hero: true,
          },
          {
            label: "Significant regressions",
            value: m ? `${regressions.length}` : "—",
            tone: regressions.length ? "warning" : undefined,
            sub: regressions.length
              ? regressions
                  .filter((c) => c.rowId === cand?.id)
                  .map((c) => `${taskName(c.taskKey)} ${fmtDeltaPt(c.delta)}`)
                  .join(", ") || `across ${new Set(regressions.map((c) => c.rowId)).size} rows`
              : "none at 95%",
          },
          { label: "Eval spend (7d)", value: runs.data ? fmtUsd(spendOf(week)) : "—", sub: `${week.length} eval runs` },
          { label: "Samples scored (7d)", value: runs.data ? fmtCompact(samplesOf(week)) : "—", sub: "paired against each baseline" },
        ]}
      />

      <Panel
        title={m?.suite ? `${m.suite.name} ${m.suite.version}` : "Matrix"}
        note={m?.suite?.description}
        flush
        testId="ml-evals-matrix-panel"
        actions={m?.suite?.gates.length ? <span className="text-meta text-ink-muted">{m.suite.gates.length} gates</span> : undefined}
      >
        <MlRead query={matrix} what="eval matrix">
          {() => (vm && vm.rows.length ? <EvalMatrix vm={vm} now={now} /> : <div className="px-4"><EmptyNote>No subject has been evaluated on this suite yet.</EmptyNote></div>)}
        </MlRead>
      </Panel>

      <Panel
        title="Score vs step"
        note={
          periodic ? (
            <>
              <SubjectLink subject={periodic.phase!} /> · {periodicSuite ? `${periodicSuite.name} ${periodicSuite.version}` : periodic.suiteId} on every checkpoint · dashed: production
            </>
          ) : undefined
        }
        testId="ml-evals-score-vs-step"
      >
        <MlRead query={runs} what="eval runs">
          {() =>
            panels.length ? (
              <div className="grid grid-cols-1 gap-4 md:grid-cols-3 xl:grid-cols-5">
                {panels.map((p) => (
                  <ChartTile
                    key={p.task.key}
                    title={p.task.name}
                    series={p.series}
                    events={marks}
                    syncKey="ml-evals-step"
                    height={130}
                    unit="ratio"
                    value={fmtScore(p.latest)}
                    note={p.reference !== null && p.latest !== null ? `${fmtDeltaPt(p.latest - p.reference)} vs prod` : undefined}
                    testId={`ml-evals-step-${p.task.key}`}
                  />
                ))}
              </div>
            ) : (
              <EmptyNote>No run evaluates its checkpoints during training.</EmptyNote>
            )
          }
        </MlRead>
      </Panel>

      <Grid cols={2}>
        <Panel title="Gates" note={m?.suite ? `Declared on ${m.suite.name} ${m.suite.version}; a failure blocks promotion to prod` : undefined} flush testId="ml-evals-gates">
          {m?.suite?.gates.length ? (
            <MlTable
              testId="ml-evals-gates-table"
              rows={m.suite.gates}
              rowKey={(g) => g.id}
              columns={[
                { key: "gate", header: "Gate", render: (g) => <span className="text-ink">{g.description}</span> },
                { key: "expr", header: "Declared as", render: (g) => <code className="font-mono text-code text-ink-muted">{g.expression}</code> },
                {
                  key: "fails",
                  header: "Failing",
                  render: (g) => {
                    const failing = m.rows.filter((r) => r.gateFailures.includes(g.description));
                    return failing.length ? <StatusDot tone="danger" label={failing.map((r) => r.label).join(", ")} /> : <StatusDot tone="success" label="none" />;
                  },
                },
              ]}
            />
          ) : (
            <div className="px-4">
              <EmptyNote>This suite declares no gates.</EmptyNote>
            </div>
          )}
        </Panel>
        <Panel title="Recent eval runs" flush testId="ml-evals-recent" actions={<span className="text-meta text-ink-muted">{allRuns.length} runs</span>}>
          <MlRead query={runs} what="eval runs">
            {(d) => (
              <MlTable
                testId="ml-evals-runs"
                rows={byEnded(d.evalRuns)}
                rowKey={(r) => r.id}
                rowHref={(r) => mlHref("evals", ["runs", r.id])}
                maxRows={8}
                columns={[
                  { key: "id", header: "Eval run", render: (r) => <MlLink href={mlHref("evals", ["runs", r.id])} mono>{r.id}</MlLink> },
                  { key: "subject", header: "Subject", render: (r) => (r.subject && r.subject.kind !== MlSubjectKind.UNSPECIFIED ? <SubjectLink subject={r.subject} /> : r.subject?.label) },
                  { key: "suite", header: "Suite", render: (r) => <span className="text-ink-muted">{r.suiteId}</span> },
                  { key: "ended", header: "Ended", numeric: true, render: (r) => fmtAgo(tsMs(r.endedAt), now), sort: (r) => tsMs(r.endedAt) ?? 0 },
                  { key: "dur", header: "Took", numeric: true, render: (r) => fmtDuration(((tsMs(r.endedAt) ?? 0) - (tsMs(r.startedAt) ?? 0)) / 1000) },
                  { key: "usd", header: "Cost", numeric: true, render: (r) => fmtUsd(r.cost?.usd), sort: (r) => r.cost?.usd ?? 0 },
                ]}
              />
            )}
          </MlRead>
        </Panel>
      </Grid>
    </MlFrame>
  );
}
