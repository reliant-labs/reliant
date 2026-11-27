// Copyright (c) 2026 Reliant Labs

/**
 * "Failures & recovery" under the timeline (owner, 2026-11-26 02:20Z):
 * for every attempt that failed, what ended it, the steps it threw away, the
 * time to the next attempt's first step stage by stage (detect → drain →
 * reschedule → image pull → NCCL init → checkpoint load → first step), what
 * the platform did about it, the hot spare it used and what it cost.
 */

import { MlSubjectKind, type MlRun } from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";

import { Chip, EmptyNote, fmtDuration, fmtGpuHours, fmtStep, fmtUsd, MlLink, mlHref, Panel, StackedBar, SubjectLink, tail, endReasonOf } from "../kit";
import { EndIcon } from "./parts";
import { failureCards, lostToFailures } from "./trainingModel";

/** Stage colours, in order: detection, then the platform's stages, then the training job's. */
const STAGE_COLOR: Record<string, number> = { detect: 3, drain: 4, reschedule: 5, "image pull": 1, "NCCL init": 2, "checkpoint load": 0, "first step": 1 };

export function FailuresStrip({ run }: { run: MlRun }) {
  const cards = failureCards(run);
  const lost = lostToFailures(run);
  const base = run.name || run.id;
  return (
    <Panel
      title="Failures & recovery"
      note={cards.length ? `${cards.length} failure${cards.length === 1 ? "" : "s"} · ${fmtGpuHours(lost.gpuHours)} · ${fmtUsd(lost.usd)} lost` : undefined}
      flush
      testId="ml-run-failures"
    >
      {cards.length === 0 ? (
        <div className="px-4">
          <EmptyNote>No attempt of this run has failed.</EmptyNote>
        </div>
      ) : (
        <ul className="divide-y divide-border-subtle">
          {cards.map((c) => {
            const end = endReasonOf(c.failed.endReason);
            const r = c.next.recovery!;
            const longest = [...c.stages].sort((a, b) => b.seconds - a.seconds)[0];
            return (
              <li key={`${c.phaseKey}-${c.failed.n}`} className="grid gap-x-6 gap-y-2 px-4 py-3 lg:grid-cols-[minmax(0,15rem)_minmax(0,1fr)_minmax(0,15rem)]" data-testid={`ml-failure-${c.phaseKey}-${c.failed.n}`}>
                <div className="min-w-0 space-y-0.5">
                  <div className="flex items-center gap-1.5 text-ui">
                    <EndIcon reason={c.failed.endReason} />
                    <MlLink href={mlHref("runs", [base, "phases", c.phaseKey], { attempt: c.failed.n })} className="font-medium">
                      {c.phaseKey} attempt {c.failed.n}
                    </MlLink>
                    <span className="text-ink-muted">{end.label}</span>
                  </div>
                  <p className="line-clamp-2 text-meta text-ink-muted" title={c.failed.endDetail}>
                    {c.failed.endDetail}
                  </p>
                  <p className="text-meta text-ink-muted">
                    at step <span className="tabular-nums text-ink">{fmtStep(c.failed.endStep)}</span> · <span className="tabular-nums text-ink">{fmtStep(c.stepsLost)}</span> steps redone
                  </p>
                </div>
                <div className="min-w-0 space-y-1.5">
                  <div className="flex items-baseline justify-between gap-2 text-meta text-ink-muted">
                    <span>
                      Back to training in <span className="tabular-nums text-ink">{fmtDuration(c.totalSeconds)}</span> · longest: {longest.stage} {fmtDuration(longest.seconds)}
                    </span>
                    <MlLink href={mlHref("runs", [base, "phases", c.phaseKey], { attempt: c.next.n })}>attempt {c.next.n} →</MlLink>
                  </div>
                  <StackedBar
                    segments={c.stages.map((s) => ({ label: s.stage, value: s.seconds, color: STAGE_COLOR[s.stage] ?? 5, title: `${s.stage}: ${fmtDuration(s.seconds)}${s.detail ? ` — ${s.detail}` : ""}` }))}
                    format={(v) => fmtDuration(v)}
                    height={8}
                  />
                  <p className="text-meta text-ink">
                    {r.automatic ? (
                      <Chip className="mr-1.5" title="An opt-in alert action took it">
                        automatic
                      </Chip>
                    ) : (
                      <Chip className="mr-1.5" title="No platform action: the queue requeued it">
                        queue requeue
                      </Chip>
                    )}
                    {r.remediation}
                    {r.alertId && (
                      <>
                        {" "}
                        <MlLink href={mlHref("runs", [base], { tab: "automations", sel: r.alertId })} mono className="text-meta">
                          {r.alertId}
                        </MlLink>
                      </>
                    )}
                  </p>
                </div>
                <dl className="grid grid-cols-2 gap-x-3 gap-y-0.5 self-start text-meta lg:grid-cols-1">
                  <div className="flex justify-between gap-2">
                    <dt className="text-ink-muted">Lost</dt>
                    <dd className="tabular-nums text-ink">
                      {fmtGpuHours(c.gpuHoursLost)} · {fmtUsd(c.usdLost)}
                    </dd>
                  </div>
                  <div className="flex justify-between gap-2">
                    <dt className="text-ink-muted">Hot spare</dt>
                    <dd className="truncate text-ink">{r.hotSpareNodeId ? <SubjectLink subject={{ kind: MlSubjectKind.NODE, id: r.hotSpareNodeId, label: tail(r.hotSpareNodeId) }} mono /> : <span className="text-ink-muted">none</span>}</dd>
                  </div>
                  {r.cordonedNodeIds.length > 0 && (
                    <div className="flex justify-between gap-2">
                      <dt className="text-ink-muted">Cordoned</dt>
                      <dd className="truncate">
                        {r.cordonedNodeIds.map((n) => (
                          <SubjectLink key={n} subject={{ kind: MlSubjectKind.NODE, id: n, label: tail(n) }} mono />
                        ))}
                      </dd>
                    </div>
                  )}
                  <div className="flex justify-between gap-2">
                    <dt className="text-ink-muted">Resumed from</dt>
                    <dd className="truncate">
                      <SubjectLink subject={{ kind: MlSubjectKind.CHECKPOINT, id: r.resumedFromCheckpointId, label: `step ${fmtStep(r.resumedStep)}` }} />
                    </dd>
                  </div>
                </dl>
              </li>
            );
          })}
        </ul>
      )}
    </Panel>
  );
}
