// Copyright (c) 2026 Reliant Labs

/**
 * A run's Evals tab (02-views §3.10): each task's score against the step
 * from eval-periodic, and the latest evaluated checkpoint against
 * production with deltas, each cell linking the eval run behind it.
 */

import { create } from "@bufbuild/protobuf";

import { MlAxis, MlCollection, MlSeriesSchema, MlSubjectKind, type MlSeries } from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";
import { cn } from "@/lib/utils";

import { ChartTile, EmptyNote, fmtStep, MlLink, MlTable, mlHref, num, Panel, SubjectLink, useMl, useMlSeries } from "../../kit";
import { scopeRange, type RunScope } from "../runScope";
import { evalCurves, evaluatedCheckpoints } from "../trainingModel";
import { seriesSubject } from "./OverviewTab";

const pct = (v: number) => `${(v * 100).toFixed(1)}`;
const EVAL_KEYS = ["eval/*"];

export function EvalsTab({ scope }: { scope: RunScope }) {
  const { detail, run } = scope;
  const subject = seriesSubject(scope);
  const series = useMlSeries(subject, EVAL_KEYS, { axis: "step", range: scopeRange(scope) });
  const suites = useMl("listMlEvalSuites", {});
  const suiteList = suites.data?.kind === "ok" ? suites.data.data.suites : [];
  const gate = suiteList.find((s) => s.name === "release-gate") ?? suiteList[0];
  const matrix = useMl("getMlEvalMatrix", { suiteId: gate?.id ?? "" }, { enabled: !!gate });
  const m = matrix.data?.kind === "ok" ? matrix.data.data : null;
  const prodRow = m?.rows.find((r) => r.alias === "production") ?? m?.rows.find((r) => r.id === m?.baselineRowId);
  const prodScore = (task: string) => m?.cells.find((c) => c.rowId === prodRow?.id && c.taskKey === task)?.value;

  const served = (series.data?.kind === "ok" ? series.data.data.series : []).filter((s) => s.collection !== MlCollection.NOT_COLLECTED && s.x.length > 0);
  const curves: MlSeries[] = served.length
    ? served
    : evalCurves(detail.checkpoints).map((c) => create(MlSeriesSchema, { key: `eval/${c.task}`, label: c.task, unit: "ratio", axis: MlAxis.STEP, x: c.x, y: c.y, collection: MlCollection.COLLECTED }));
  const latest = evaluatedCheckpoints(detail.checkpoints)[0];
  const tasks = latest ? Object.keys(latest.evalScores) : [];
  const syncKey = `run-evals-${run.id}`;

  if (!latest && !curves.length) return <EmptyNote>No checkpoint of this run has been evaluated.</EmptyNote>;

  return (
    <div className="space-y-4" data-testid="ml-run-evals">
      <Panel title="Score vs step" note={`${curves.length} tasks · eval-periodic on each checkpoint`}>
        <div className="grid grid-cols-1 gap-3 md:grid-cols-2 xl:grid-cols-4">
          {curves.map((s) => (
            <ChartTile key={s.key} title={s.label || s.key.replace("eval/", "")} series={[s]} unit="ratio" height={90} syncKey={syncKey} note={prodScore(s.key.replace("eval/", "")) !== undefined ? `production ${pct(prodScore(s.key.replace("eval/", ""))!)}` : undefined} />
          ))}
        </div>
      </Panel>

      {latest && (
        <Panel
          title={`Step ${num(latest.step).toLocaleString("en-US")} vs production`}
          note={prodRow ? `baseline ${prodRow.label}` : "production baseline unavailable"}
          actions={gate ? <MlLink href={mlHref("evals", [], { suite: gate.id })}>Open the Evals matrix →</MlLink> : undefined}
          flush
        >
          <MlTable
            testId="ml-run-evals-table"
            rows={tasks}
            rowKey={(t) => t}
            columns={[
              { key: "task", header: "Task", render: (t) => <span className="font-mono text-code">{t}</span> },
              {
                key: "ckpt",
                header: `${latest.phaseKey} ${fmtStep(latest.step)}`,
                numeric: true,
                render: (t) => (latest.evalRunIds[0] ? <MlLink href={mlHref("evals", ["runs", latest.evalRunIds[latest.evalRunIds.length - 1]], { task: t })}>{pct(latest.evalScores[t])}</MlLink> : pct(latest.evalScores[t])),
              },
              {
                key: "prod",
                header: prodRow?.label ?? "production",
                numeric: true,
                render: (t) => {
                  const v = prodScore(t);
                  return v === undefined ? <span className="text-ink-muted">—</span> : prodRow?.evalRunId ? <MlLink href={mlHref("evals", ["runs", prodRow.evalRunId], { task: t })}>{pct(v)}</MlLink> : pct(v);
                },
              },
              {
                key: "delta",
                header: "Δ",
                numeric: true,
                sort: (t) => latest.evalScores[t] - (prodScore(t) ?? latest.evalScores[t]),
                render: (t) => {
                  const v = prodScore(t);
                  if (v === undefined) return <span className="text-ink-muted">—</span>;
                  const d = (latest.evalScores[t] - v) * 100;
                  return <span className={cn(d < -0.5 ? "text-danger-ink" : d > 0.5 ? "text-ink" : "text-ink-muted")}>{`${d >= 0 ? "+" : ""}${d.toFixed(1)}`}</span>;
                },
              },
            ]}
          />
          <p className="px-4 py-2 text-meta text-ink-muted">
            Eval runs:{" "}
            {latest.evalRunIds.map((id) => (
              <SubjectLink key={id} subject={{ kind: MlSubjectKind.EVAL_RUN, id, label: id }} mono className="mr-2" />
            ))}
          </p>
        </Panel>
      )}
    </div>
  );
}
