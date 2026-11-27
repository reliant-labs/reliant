// Copyright (c) 2026 Reliant Labs

/**
 * A service's revisions (releases), each with its verdict and SLI deltas
 * against the one before, and the Changes comparison of two revisions side
 * by side: every metric with its confidence interval on a shared scale,
 * significance, and whether the arm is better or worse. A row selects the
 * revision (`?sel=`), showing its verdict detail and serving config.
 */

import { MlSubjectKind, type GetMlInferenceServiceResponse, type MlChangesComparison, type MlEvent, type MlOnlineEval, type MlRevision, type MlSeries } from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";

import { MlRead } from "../MlParts";
import {
  Chip,
  EmptyNote,
  FactList,
  FindingLink,
  fmtAgo,
  fmtCompact,
  fmtInt,
  fmtPct,
  fmtTime,
  fmtUnit,
  Grid,
  Meter,
  MlTable,
  Panel,
  StatusDot,
  SubjectLink,
  tail,
  tsMs,
  useMlSearch,
  verdictOf,
  type MlColumn,
} from "../kit";
import { armDeltaLabel, armMetricRows, armVerdict, byKey, daysSince, deltaTone, fmtDelta, orderRevisions, relDelta, type ArmMetricRow } from "./inferenceModel";
import { SeriesTile, useServingSeries } from "./parts";

export function RevisionsTab({ d, events }: { d: GetMlInferenceServiceResponse; events: readonly MlEvent[] }) {
  const [search, setSearch] = useMlSearch();
  const now = Date.now();
  const rows = orderRevisions(d.revisions);
  const selected = rows.find((r) => r.id === search.sel) ?? rows[0];
  const columns: MlColumn<MlRevision>[] = [
    { key: "rev", header: "Revision", render: (r) => <span className="font-mono text-code">{r.release}</span> },
    { key: "model", header: "Model", render: (r) => <SubjectLink subject={{ kind: MlSubjectKind.MODEL_VERSION, id: r.modelVersionId }}>{`${tail(r.modelVersionId.split("/")[0])} ${tail(r.modelVersionId)}`}</SubjectLink> },
    { key: "runtime", header: "Runtime", render: (r) => <span className="text-ink-muted">{r.runtime}</span> },
    { key: "started", header: "Started", render: (r) => <span className="tabular-nums text-ink-muted" title={fmtTime(tsMs(r.startedAt))}>{fmtAgo(tsMs(r.startedAt), now)}</span> },
    { key: "ended", header: "Ended", render: (r) => <span className="tabular-nums text-ink-muted">{r.endedAt ? fmtAgo(tsMs(r.endedAt), now) : "serving"}</span> },
    { key: "traffic", header: "Traffic", numeric: true, render: (r) => (r.endedAt ? <span className="text-ink-muted">—</span> : <Meter ratio={r.trafficShare} width={48} />) },
    {
      key: "verdict",
      header: "Verdict",
      render: (r) => {
        const v = verdictOf(r.verdict);
        return <span title={r.verdictDetail}><StatusDot tone={v.tone} label={v.label} /></span>;
      },
    },
    {
      key: "deltas",
      header: "SLI deltas vs previous",
      render: (r) => (
        <span className="inline-flex flex-wrap gap-1">
          {r.deltas.slice(0, 4).map((x) => (
            <Chip key={x.key} tone={deltaTone(x)} title={`${x.label}: ${fmtUnit(x.before, x.unit)} → ${fmtUnit(x.after, x.unit)}${x.significant ? " (significant)" : " (not significant)"}`}>
              {x.label} {fmtDelta(relDelta(x))}
            </Chip>
          ))}
        </span>
      ),
    },
  ];
  return (
    <div className="space-y-4">
      <Panel title="Revisions" note="Reliant rolls forward: a regressed revision halts promotion and the previous one keeps serving" flush>
        <MlTable columns={columns} rows={rows} rowKey={(r) => r.id} selected={selected?.id} onRowClick={(r) => setSearch({ sel: r.id })} testId="ml-inference-revisions" />
      </Panel>
      {selected && <RevisionDetail rev={selected} findingIds={d.findingIds} />}
      {d.comparison ? <ChangesPanel c={d.comparison} evals={d.onlineEvals} serviceId={d.service!.id} events={events} /> : <EmptyNote>No Changes comparison has run on this service.</EmptyNote>}
    </div>
  );
}

function RevisionDetail({ rev, findingIds }: { rev: MlRevision; findingIds: readonly string[] }) {
  const v = verdictOf(rev.verdict);
  const gate = rev.verdictDetail.includes("release gate") ? findingIds.find((id) => id.includes("fp8")) : undefined;
  return (
    <Grid cols={2}>
      <Panel title={<span className="font-mono">{rev.release}</span>} note={<StatusDot tone={v.tone} label={v.label} />} testId="ml-inference-revision-detail">
        <div className="space-y-3">
          <p className="text-ui text-ink">
            {rev.verdictDetail}
            {gate && (
              <>
                {" "}
                <FindingLink id={gate}>See the finding</FindingLink>
              </>
            )}
          </p>
          {rev.deltas.length > 0 && (
            <table className="w-full text-ui">
              <thead>
                <tr className="h-8 border-b border-border text-left text-meta text-ink-muted">
                  <th className="font-medium">SLI</th>
                  <th className="text-right font-medium">Before</th>
                  <th className="text-right font-medium">After</th>
                  <th className="text-right font-medium">Δ</th>
                  <th className="pl-3 font-medium">Significance</th>
                </tr>
              </thead>
              <tbody>
                {rev.deltas.map((x) => (
                  <tr key={x.key} className="h-8 border-b border-border-subtle last:border-0">
                    <td>{x.label}</td>
                    <td className="text-right tabular-nums text-ink-muted">{fmtUnit(x.before, x.unit)}</td>
                    <td className="text-right tabular-nums">{fmtUnit(x.after, x.unit)}</td>
                    <td className="text-right tabular-nums">{fmtDelta(relDelta(x))}</td>
                    <td className="pl-3">
                      <StatusDot tone={deltaTone(x)} label={x.significant ? (x.regression ? "worse" : "better") : "not significant"} />
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
      </Panel>
      <Panel title="Serving config" note={rev.commit ? `commit ${rev.commit}` : undefined}>
        <FactList facts={rev.config.map((f) => ({ label: f.label, value: <span className="font-mono text-code">{f.value}</span> }))} columns={1} />
      </Panel>
    </Grid>
  );
}

function ChangesPanel({ c, evals, serviceId, events }: { c: MlChangesComparison; evals: readonly MlOnlineEval[]; serviceId: string; events: readonly MlEvent[] }) {
  const now = Date.now();
  const rows = armMetricRows(c);
  const control = c.arms.find((a) => a.arm === c.controlArm) ?? c.arms[0];
  const ab = useServingSeries({ kind: MlSubjectKind.INFERENCE_SERVICE, id: serviceId }, ["serve.ttft", "serve.itl"], "arm");
  return (
    <Panel
      title={`Changes: ${c.name}`}
      note={`${c.status} · ${daysSince(tsMs(c.startedAt), now)} days · ${c.decision || "no decision yet"}`}
      testId="ml-inference-changes"
    >
      <div className="space-y-4">
        <Grid cols={2}>
          {c.arms.map((a) => (
            <div key={a.arm} className="space-y-2 rounded-md border border-border-subtle px-3 py-2">
              <div className="flex items-baseline justify-between gap-2">
                <h3 className="text-ui font-medium text-ink">
                  {a.arm} {a === control && <span className="text-meta font-normal text-ink-muted">(control)</span>}
                </h3>
                <span className="text-meta tabular-nums text-ink-muted">{fmtPct(a.trafficShare)} of traffic</span>
              </div>
              <FactList
                columns={2}
                facts={[
                  { label: "Revision", value: <SubjectLink subject={{ kind: MlSubjectKind.REVISION, id: a.revisionId }} mono>{tail(a.revisionId)}</SubjectLink> },
                  { label: "Model", value: <SubjectLink subject={{ kind: MlSubjectKind.MODEL_VERSION, id: a.modelVersionId }}>{tail(a.modelVersionId)}</SubjectLink> },
                  { label: "Requests", value: <span className="tabular-nums">{fmtCompact(a.requests)}</span> },
                  { label: "Metrics", value: a.metrics.length },
                ]}
              />
            </div>
          ))}
        </Grid>
        <ArmTable rows={rows} />
        <MlRead query={ab} what="the A/B series">
          {({ series }) => {
            const p95 = (key: string) => byKey(series, key).filter((s) => !s.stat) as MlSeries[];
            return (
              <Grid cols={2}>
                <SeriesTile title="TTFT p95 by arm" series={p95("serve.ttft")} events={events} />
                <SeriesTile title="ITL p95 by arm" series={p95("serve.itl")} events={events} />
              </Grid>
            );
          }}
        </MlRead>
        {evals.length > 0 && (
          <div className="space-y-1">
            <h3 className="text-ui font-medium text-ink">Online evals</h3>
            <MlTable
              columns={[
                { key: "arm", header: "Arm", render: (e: MlOnlineEval) => e.arm },
                { key: "rev", header: "Revision", render: (e: MlOnlineEval) => <SubjectLink subject={{ kind: MlSubjectKind.REVISION, id: e.revisionId }} mono>{tail(e.revisionId)}</SubjectLink> },
                { key: "fb", header: "Thumbs up", numeric: true, render: (e: MlOnlineEval) => fmtPct(e.feedbackPositiveRatio, 1) },
                { key: "ok", header: "Task success", numeric: true, render: (e: MlOnlineEval) => fmtPct(e.taskSuccessRatio, 1) },
                { key: "guard", header: "Guardrail triggers", numeric: true, render: (e: MlOnlineEval) => fmtPct(e.guardrailRatio, 1) },
                { key: "judge", header: "Judge (1–5)", numeric: true, render: (e: MlOnlineEval) => e.judgeScore.toFixed(2) },
                { key: "n", header: "Samples", numeric: true, render: (e: MlOnlineEval) => fmtInt(e.samples) },
              ]}
              rows={evals}
              rowKey={(e) => e.arm}
              testId="ml-inference-online-evals"
            />
          </div>
        )}
      </div>
    </Panel>
  );
}

/** Metric rows: the control and each arm with its 95% CI drawn on one scale. */
function ArmTable({ rows }: { rows: readonly ArmMetricRow[] }) {
  if (rows.length === 0) return null;
  const armNames = rows[0].arms.map((a) => a.arm);
  return (
    <div className="min-w-0 overflow-x-auto">
      <table className="w-full text-ui" data-testid="ml-inference-arm-metrics">
        <thead>
          <tr className="h-8 border-b border-border text-left text-meta text-ink-muted">
            <th className="font-medium">Metric</th>
            <th className="text-right font-medium">control</th>
            {armNames.map((a) => (
              <th key={a} className="text-right font-medium">
                {a}
              </th>
            ))}
            <th className="px-3 font-medium">95% CI (control dashed)</th>
            <th className="text-right font-medium">Δ</th>
            <th className="pl-3 font-medium">Verdict</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((row) => {
            const arm = row.arms[0]?.metric;
            const v = arm ? armVerdict(arm) : undefined;
            return (
              <tr key={row.key} className="h-9 border-b border-border-subtle last:border-0">
                <td className="text-ink">{row.label}</td>
                <td className="text-right tabular-nums text-ink-muted">{fmtUnit(row.control.value, row.unit)}</td>
                {row.arms.map((a) => (
                  <td key={a.arm} className="text-right tabular-nums">
                    {fmtUnit(a.metric.value, row.unit)}
                  </td>
                ))}
                <td className="w-56 px-3">
                  <CiBars row={row} />
                </td>
                <td className="text-right tabular-nums">{arm ? armDeltaLabel(row, arm) : "—"}</td>
                <td className="pl-3">{v && <StatusDot tone={v.tone} label={v.label} />}</td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

function CiBars({ row }: { row: ArmMetricRow }) {
  const span = row.hi - row.lo || 1;
  const x = (v: number) => `${((v - row.lo) / span) * 100}%`;
  const bar = (m: ArmMetricRow["control"], dashed: boolean, y: number, tone: string) => (
    <g>
      <line x1={x(m.ciLow)} x2={x(m.ciHigh)} y1={y} y2={y} stroke={tone} strokeWidth={2} strokeDasharray={dashed ? "3 2" : undefined} />
      <circle cx={x(m.value)} cy={y} r={3} fill={tone} />
    </g>
  );
  const arm = row.arms[0]?.metric;
  const armTone = arm ? (arm.significant ? (arm.better ? "hsl(var(--success))" : "hsl(var(--warning))") : "hsl(var(--chart-1))") : "hsl(var(--chart-1))";
  return (
    <svg width="100%" height={18} role="img" aria-label={`${row.label}: control ${fmtUnit(row.control.value, row.unit)}${arm ? `, arm ${fmtUnit(arm.value, row.unit)}` : ""}`}>
      {bar(row.control, true, 5, "hsl(var(--muted-foreground))")}
      {arm && bar(arm, false, 13, armTone)}
    </svg>
  );
}
