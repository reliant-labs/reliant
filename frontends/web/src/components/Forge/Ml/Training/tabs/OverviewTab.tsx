// Copyright (c) 2026 Reliant Labs

/**
 * A run's Overview tab (02-views §3.3): is it healthy, how far, how fast,
 * where did the time and money go, what the model's vitals say, and the
 * Kueue facts of the attempt in scope.
 */

import { AlertTriangle, Check, EyeOff, X } from "lucide-react";

import { MlAxis, MlEventKind, MlSubjectKind, type MlHealthCheck, type MlKueueWorkload, type MlSeries } from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";

import {
  EmptyNote,
  FactList,
  FindingLink,
  fmtDuration,
  fmtGpuHours,
  fmtMs,
  fmtStep,
  fmtTime,
  fmtUsd,
  MlChart,
  num,
  Panel,
  severityOf,
  StackedBar,
  StatusDot,
  SubjectLink,
  tsMs,
  useMl,
  type MlBand,
} from "../../kit";
import { phaseId } from "../../kit/ids";
import type { RunScope } from "../runScope";
import { costSegments, scopeCost, seriesPhase } from "../trainingModel";

const CHART_EVENTS = new Set([MlEventKind.CHECKPOINT, MlEventKind.RESTART, MlEventKind.LOSS_SPIKE, MlEventKind.XID, MlEventKind.NCCL_TIMEOUT, MlEventKind.STRAGGLER, MlEventKind.QUEUE_EVICTED, MlEventKind.EVAL]);

/** The subject a tab's series read: the scope's, but a run-wide scope reads its current phase (steps restart per phase). */
export function seriesSubject(scope: RunScope) {
  if (scope.subject.kind !== MlSubjectKind.RUN) return scope.subject;
  const p = seriesPhase(scope.detail, scope.phase);
  return p ? { kind: MlSubjectKind.PHASE, id: phaseId(scope.run.id, p.key), label: p.key } : scope.subject;
}

export function OverviewTab({ scope }: { scope: RunScope }) {
  const { detail, run, phase, attempt } = scope;
  const subject = seriesSubject(scope);
  const loss = useMl("queryMlSeries", { subject, keys: ["train/loss", "val/loss"], axis: MlAxis.STEP, maxPoints: 400 });
  const tput = useMl("queryMlSeries", { subject, keys: ["throughput/tokens_per_s", "throughput/mfu"], axis: MlAxis.STEP, maxPoints: 400 });
  const events = useMl("listMlEvents", { subject, kinds: [...CHART_EVENTS] });
  const evs = events.data?.kind === "ok" ? events.data.data.events : [];
  const p = seriesPhase(detail, phase);
  const bands: MlBand[] = (p?.segments ?? [])
    .filter((s) => /spike|shard|warmup|anneal/i.test(`${s.name} ${s.note}`))
    .map((s) => ({ from: num(s.fromStep), to: num(s.toStep), label: s.name, tone: /spike|shard/i.test(`${s.name} ${s.note}`) ? "warning" : "neutral" }));
  const cost = scopeCost(run, phase, attempt, scope.phaseScoped);
  const segs = costSegments(cost);
  const productive = segs.find((s) => s.label === "Productive");
  const syncKey = `run-overview-${run.id}`;
  const lossSeries = named(loss.data?.kind === "ok" ? loss.data.data.series : []);
  const tputSeries = named(tput.data?.kind === "ok" ? tput.data.data.series : []);
  const workloadAttempt = attempt ?? [...(p?.attempts ?? [])].sort((a, b) => b.n - a.n)[0] ?? null;

  // Two columns that stack on their own: a tall Health list never stretches the Loss chart beside it.
  return (
    <div className="grid min-w-0 grid-cols-1 items-start gap-4 xl:grid-cols-2">
      <div className="min-w-0 space-y-4">
        <Panel title="Health" note={`${detail.health.filter((h) => h.status !== "ok").length} need attention`} flush testId="ml-run-health">
          <HealthList checks={detail.health} />
        </Panel>

        <Panel title="Where the time went" note={scope.attempt ? `attempt ${scope.attempt.n}` : scope.phaseScoped ? `phase ${phase?.key}` : "whole run"} testId="ml-run-goodput">
          {segs.length ? (
            <div className="space-y-3">
              <div className="flex items-baseline gap-2">
                <span className="text-2xl font-semibold tabular-nums text-ink">{productive ? `${Math.round(productive.share * 100)}%` : "—"}</span>
                <span className="text-meta text-ink-muted">goodput · {fmtGpuHours(cost?.gpuHours)} · {fmtUsd(cost?.usd)} allocated</span>
              </div>
              <StackedBar segments={segs.map((s) => ({ label: s.label, value: s.gpuHours, color: s.color, title: `${s.label}: ${fmtGpuHours(s.gpuHours)} · ${fmtUsd(s.usd)}` }))} format={fmtGpuHours} height={12} testId="ml-run-goodput-bar" />
              <ul className="grid grid-cols-2 gap-x-6 gap-y-0.5 text-meta">
                {segs.map((s) => (
                  <li key={s.label} className="flex justify-between gap-2 border-b border-border-subtle py-0.5">
                    <span className="text-ink-muted">{s.label}</span>
                    <span className="tabular-nums text-ink">{fmtUsd(s.usd)}</span>
                  </li>
                ))}
              </ul>
            </div>
          ) : (
            <EmptyNote>Nothing allocated yet.</EmptyNote>
          )}
        </Panel>

        <Panel title="Step time breakdown" note={`p50 per step · ${run.currentPhaseKey}`} testId="ml-run-step-breakdown">
          {detail.stepBreakdown.length ? (
            <div className="space-y-2">
              <p className="text-meta text-ink-muted">
                <span className="text-xl font-semibold tabular-nums text-ink">{fmtMs(detail.stepBreakdown.reduce((s, x) => s + x.msP50, 0))}</span> per step
              </p>
              <StackedBar segments={detail.stepBreakdown.map((s, i) => ({ label: s.stage.replace("_", " "), value: s.msP50, color: s.stage === "comm_exposed" ? 3 : i }))} format={fmtMs} height={12} />
            </div>
          ) : (
            <EmptyNote>No step timers: the run does not report them.</EmptyNote>
          )}
        </Panel>

        <Panel title="Kueue workload" note={workloadAttempt ? `${p?.key} attempt ${workloadAttempt.n}` : undefined} testId="ml-run-workload">
          {workloadAttempt?.workload ? <WorkloadFacts w={workloadAttempt.workload} /> : <EmptyNote>No workload: the phase has not been queued.</EmptyNote>}
        </Panel>

      </div>
      <div className="min-w-0 space-y-4">
        <Panel title="Loss" note={`${p?.key ?? ""} · step axis · markers: checkpoints, restarts, spikes`} testId="ml-run-loss">
          <MlChart series={lossSeries} events={evs} bands={bands} label="Loss" height={200} syncKey={syncKey} what="Loss" testId="ml-run-loss-chart" />
        </Panel>

        <Panel title="Throughput" note="tokens/s and MFU" testId="ml-run-throughput">
          <MlChart series={tputSeries.filter((s) => s.key === "throughput/tokens_per_s")} events={evs.filter((e) => e.kind !== MlEventKind.CHECKPOINT)} label="Tokens/s" height={110} syncKey={syncKey} what="Tokens/s" compact={false} />
          <MlChart series={tputSeries.filter((s) => s.key === "throughput/mfu")} label="MFU" height={90} syncKey={syncKey} what="MFU" />
        </Panel>

        <Panel title="Key numbers" testId="ml-run-key-numbers">
          {detail.keyNumbers.length ? <FactList facts={detail.keyNumbers.map((f) => ({ label: f.label, value: <span className="tabular-nums">{f.value}</span> }))} columns={1} /> : <EmptyNote>None reported.</EmptyNote>}
        </Panel>

        <Panel title="Model insights" note="what the vitals say" flush testId="ml-run-insights">
          {detail.insights.length ? (
            <ul className="divide-y divide-border-subtle">
              {detail.insights.map((i) => {
                const sev = severityOf(i.severity);
                return (
                  <li key={i.id} className="space-y-0.5 px-4 py-2.5">
                    <div className="flex flex-wrap items-center gap-x-2">
                      <StatusDot tone={sev.tone} label={<span className="font-medium text-ink">{i.title}</span>} />
                      <span className="text-meta tabular-nums text-ink-muted">
                        {i.phaseKey} {fmtStep(i.fromStep)}–{fmtStep(i.toStep)}
                      </span>
                    </div>
                    <p className="text-meta text-ink-muted">{i.detail}</p>
                    {i.suggestion && <p className="text-meta text-ink">→ {i.suggestion}</p>}
                    <p className="text-meta text-ink-muted">
                      {i.seriesKeys.map((k) => (
                        <span key={k} className="mr-2 font-mono text-code">
                          {k}
                        </span>
                      ))}
                      {i.findingId && <FindingLink id={i.findingId}>{i.findingId}</FindingLink>}
                    </p>
                  </li>
                );
              })}
            </ul>
          ) : (
            <div className="px-4">
              <EmptyNote>No insights: the run reports no model vitals.</EmptyNote>
            </div>
          )}
        </Panel>
      </div>
    </div>
  );
}

const SHORT: Record<string, string> = { "train/loss": "train", "val/loss": "val", "throughput/tokens_per_s": "tok/s", "throughput/mfu": "MFU" };

/** Series named for a legend shared by several keys: "train · a3", not "attempt 3" twice. */
function named(series: readonly MlSeries[]): MlSeries[] {
  return series.map((s) => ({ ...s, group: [SHORT[s.key] ?? s.label ?? s.key, s.attempt ? `a${s.attempt}` : s.group].filter(Boolean).join(" · ") }));
}

const STATUS_ICON: Record<string, React.ReactNode> = {
  ok: <Check className="h-3.5 w-3.5 text-ink-muted" aria-hidden="true" />,
  warn: <AlertTriangle className="h-3.5 w-3.5 text-warning-ink" aria-hidden="true" />,
  fail: <X className="h-3.5 w-3.5 text-danger-ink" aria-hidden="true" />,
  "not-collected": <EyeOff className="h-3.5 w-3.5 text-ink-muted" aria-hidden="true" />,
};

const STATUS_ORDER: Record<string, number> = { fail: 0, warn: 1, "not-collected": 2, ok: 3 };

function HealthList({ checks }: { checks: readonly MlHealthCheck[] }) {
  if (!checks.length)
    return (
      <div className="px-4">
        <EmptyNote>No health checks.</EmptyNote>
      </div>
    );
  return (
    <ul className="divide-y divide-border-subtle">
      {[...checks]
        .sort((a, b) => (STATUS_ORDER[a.status] ?? 9) - (STATUS_ORDER[b.status] ?? 9))
        .map((h) => (
          <li key={h.key} className="flex gap-2 px-4 py-2" data-testid={`ml-health-${h.key}`}>
            <span className="mt-0.5">{STATUS_ICON[h.status] ?? STATUS_ICON.ok}</span>
            <div className="min-w-0 space-y-0.5">
              <div className="flex flex-wrap items-baseline gap-x-2 text-ui">
                {h.subject?.id ? <SubjectLink subject={h.subject} className="font-medium">{h.title}</SubjectLink> : <span className="font-medium text-ink">{h.title}</span>}
                {h.findingId && <FindingLink id={h.findingId}><span className="text-meta">fix</span></FindingLink>}
              </div>
              {h.detail && <p className="text-meta text-ink-muted">{h.detail}</p>}
            </div>
          </li>
        ))}
    </ul>
  );
}

function WorkloadFacts({ w }: { w: MlKueueWorkload }) {
  const created = tsMs(w.createdAt);
  const admitted = tsMs(w.admittedAt);
  return (
    <FactList
      columns={2}
      testId="ml-run-workload-facts"
      facts={[
        { label: "Workload", value: <SubjectLink subject={{ kind: MlSubjectKind.WORKLOAD, id: w.name, label: w.name }} mono /> },
        { label: "Local queue", value: <span className="font-mono text-code">{w.localQueue}</span> },
        { label: "Cluster queue", value: w.clusterQueue },
        { label: "Cohort", value: w.cohort || "—" },
        { label: "Priority", value: `${w.priority} · ${w.priorityClass}` },
        { label: "Flavor", value: `${w.flavor} × ${w.gpus}` },
        { label: "Admitted", value: admitted ? fmtTime(admitted) : "waiting" },
        { label: "Waited", value: created && admitted ? fmtDuration((admitted - created) / 1000) : "—" },
        { label: "Borrowed", value: w.borrowedGpus ? `${w.borrowedGpus} GPUs from the cohort` : "none" },
        { label: "Preemptions", value: `${w.preemptions}${w.evictionReason ? ` · evicted: ${w.evictionReason}` : ""}` },
        { label: "Requeues", value: w.requeues },
        { label: "Topology", value: w.topology || "none" },
        { label: "Admission checks", value: w.admissionChecks.length ? w.admissionChecks.join("; ") : "none" },
      ]}
    />
  );
}
