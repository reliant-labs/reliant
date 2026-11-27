// Copyright (c) 2026 Reliant Labs

/**
 * The run's hero timeline (02-views §3.2): one row per phase, each attempt a
 * bar segmented into queued / recovering / starting / running /
 * checkpointing / stalled, an icon where it ended, checkpoints as ticks and
 * evals as diamonds. On the step axis the steps a failure threw away are
 * drawn as "redone" and the data segments as a band row. A row scopes the
 * page to its phase; a bar scopes it to that attempt.
 */

import { useNavigate } from "@tanstack/react-router";

import { MlCheckpointStatus, MlPhaseKind, MlPhaseState, MlSubjectKind, type MlCheckpoint, type MlTimeSpan, type MlPhase, type MlRun } from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";
import { cn } from "@/lib/utils";

import { endReasonOf, fmtDuration, fmtStep, fmtTime, Gantt, GANTT_STATES, mlHref, num, phaseStateOf, subjectHref, tsMs, type GanttMark, type GanttRow, type GanttSpan, type GanttStateStyle } from "../kit";
import { attemptEndMs, isFailure } from "./trainingModel";

export type TimelineAxis = "time" | "step";

const SEGMENT_STATES: Record<string, GanttStateStyle> = {
  seg0: { label: "Data segment", fill: "hsl(var(--chart-2) / 0.45)" },
  seg1: { label: "Data segment", fill: "hsl(var(--chart-5) / 0.45)" },
  segWarn: { label: "Data incident", fill: "hsl(var(--warning) / 0.7)" },
};

export function timelineStates(axis: TimelineAxis): Record<string, GanttStateStyle> {
  return {
    ...GANTT_STATES,
    idle: { label: "Idle (allocated)", fill: "hsl(var(--muted-foreground) / 0.2)", hatched: true },
    stalled: { label: "Stalled / slowed", fill: "hsl(var(--warning))" },
    recovering: axis === "step" ? { label: "Redone (lost to failure)", fill: "hsl(var(--destructive) / 0.45)", hatched: true } : GANTT_STATES.recovering,
    ...SEGMENT_STATES,
  };
}

interface TimelineProps {
  run: MlRun;
  checkpoints: readonly MlCheckpoint[];
  axis: TimelineAxis;
  selectedPhase: string | null;
  selectedAttempt: number | null;
  now: number;
}

/** The Gantt rows of a run on an axis (exported for the view-model tests). */
export function timelineRows(run: MlRun, checkpoints: readonly MlCheckpoint[], axis: TimelineAxis, now: number, sel: { phase: string | null; attempt: number | null }): { rows: GanttRow[]; from: number; to: number } {
  const base = run.name || run.id;
  const attemptHref = (phase: string, n: number) => mlHref("runs", [base, "phases", phase], { attempt: n, axis: axis === "step" ? "step" : undefined });
  const rows: GanttRow[] = [];
  const allStarts = run.phases.flatMap((p) => p.attempts.map((a) => tsMs(a.queuedAt))).filter((v): v is number => v !== null);
  const runEnd = Math.max(now, ...run.phases.flatMap((p) => p.attempts.map((a) => attemptEndMs(a, now))));
  const timeFrom = allStarts.length ? Math.min(...allStarts) : (tsMs(run.createdAt) ?? now - 3600_000);
  const timeTo = tsMs(run.endedAt) ?? runEnd;
  const sftLike = run.phases.find((p) => p.kind === MlPhaseKind.SFT) ?? run.phases[0];
  const stepTo = Math.max(1, ...run.phases.map((p) => num(p.totalSteps)));

  for (const p of run.phases) {
    const isEval = p.kind === MlPhaseKind.EVAL;
    const spans: GanttSpan[] = [];
    const marks: GanttMark[] = [];
    for (const a of p.attempts) {
      const end = endReasonOf(a.endReason);
      const title = attemptTitle(p, a.n, now);
      const href = attemptHref(p.key, a.n);
      if (axis === "time") {
        let labelled = false;
        for (const s of mergeSpans(a.spans)) {
          const label = !labelled && (s.state === "running" || s.state === "stalled") && !isEval ? `a${a.n}` : undefined;
          if (label) labelled = true;
          spans.push({ ...s, label, title: `${title}\n${s.state}${s.label ? `: ${s.label}` : ""}`, href });
        }
        const ended = tsMs(a.endedAt);
        if (ended !== null) marks.push({ at: ended, label: end.label, tone: end.tone, shape: isFailure(a) ? "cross" : "dot", title: `a${a.n}: ${end.label}${a.endDetail ? ` — ${a.endDetail}` : ""}`, href });
      } else if (!isEval) {
        const lost = num(a.stepsLost);
        const startStep = num(a.startStep);
        const endStep = num(a.endStep);
        if (a.state === MlPhaseState.QUEUED) spans.push({ start: 0, end: num(p.totalSteps), state: "queued", label: `a${a.n} queued`, title, href });
        else {
          spans.push({ start: startStep, end: endStep - lost, state: "running", label: `a${a.n}`, title, href });
          if (lost > 0) spans.push({ start: endStep - lost, end: endStep, state: "recovering", title: `a${a.n}: ${fmtStep(lost)} steps redone after ${end.label.toLowerCase()}`, href });
          if (isFailure(a)) marks.push({ at: endStep, label: end.label, tone: end.tone, shape: "cross", title: `a${a.n}: ${end.label} at step ${fmtStep(endStep)}`, href });
        }
      }
    }
    if (!isEval) {
      for (const c of checkpoints.filter((c) => c.phaseKey === p.key && c.status !== MlCheckpointStatus.DELETED)) {
        const x = axis === "time" ? tsMs(c.savedAt) : num(c.step);
        if (x === null) continue;
        marks.push({
          at: x,
          label: `checkpoint ${fmtStep(c.step)}`,
          tone: c.status === MlCheckpointStatus.PARTIAL ? "danger" : "neutral",
          shape: "tick",
          title: `Checkpoint ${num(c.step).toLocaleString("en-US")}${c.status === MlCheckpointStatus.PARTIAL ? " (partial)" : ""}`,
          href: subjectHref({ kind: MlSubjectKind.CHECKPOINT, id: c.id }) ?? undefined,
        });
      }
    } else {
      const evalSpans = p.attempts.flatMap((a) => a.spans.filter((s) => s.state === "running"));
      p.evaluatedSteps.forEach((step, i) => {
        const ck = checkpoints.find((c) => c.phaseKey === sftLike?.key && num(c.step) === num(step));
        const x = axis === "time" ? tsMs(evalSpans[i]?.start) : num(step);
        if (x === null) return;
        const evalRun = ck?.evalRunIds[0];
        marks.push({ at: x, label: `eval of ${fmtStep(step)}`, tone: "info", shape: "diamond", title: `Eval of checkpoint ${num(step).toLocaleString("en-US")}`, href: evalRun ? mlHref("evals", ["runs", evalRun]) : undefined });
      });
    }
    if (axis === "step" && !p.attempts.length && num(p.totalSteps) > 0) spans.push({ start: 0, end: num(p.totalSteps), state: "pending", label: "pending", title: `${p.key}: ${phaseStateOf(p.state).label}` });
    const st = phaseStateOf(p.state);
    rows.push({
      id: p.key,
      label: (
        <span title={`${p.label || p.key}: ${st.label}`}>
          {p.key} <span className="text-meta text-ink-muted">{p.state === MlPhaseState.PENDING ? `pending${p.dependsOn.length ? ` on ${p.dependsOn.join(", ")}` : ""}` : p.attempts.length > 1 ? `${p.attempts.length} attempts` : st.label.toLowerCase()}</span>
        </span>
      ),
      spans,
      marks,
      selected: sel.phase === p.key,
    });
    if (axis === "step" && p.segments.length && (sel.phase === p.key || (!sel.phase && p.key === run.currentPhaseKey))) {
      rows.push({
        id: `${p.key}-data`,
        label: <span className="text-ink-muted">{p.key} · data</span>,
        spans: p.segments.map((s, i) => ({ start: num(s.fromStep), end: num(s.toStep), state: /spike|shard|incident/i.test(`${s.name} ${s.note}`) ? "segWarn" : `seg${i % 2}`, label: s.name, title: `${s.name}: steps ${fmtStep(s.fromStep)}–${fmtStep(s.toStep)}${s.note ? `\n${s.note}` : ""}` })),
      });
    }
  }
  return axis === "time" ? { rows, from: timeFrom, to: timeTo + (timeTo - timeFrom) * 0.01 } : { rows, from: 0, to: stepTo };
}

/**
 * An attempt's spans for a time axis: a checkpoint save (seconds) is
 * invisible beside hours of running, so it folds into the run around it, and
 * neighbours in one state join; the ticks mark the saves.
 */
export function mergeSpans(spans: readonly MlTimeSpan[]): { start: number; end: number | null; state: string; label: string }[] {
  const out: { start: number; end: number | null; state: string; label: string }[] = [];
  for (const s of spans) {
    const start = tsMs(s.start);
    if (start === null) continue;
    const end = tsMs(s.end);
    const last = out[out.length - 1];
    const state = s.state === "checkpointing" ? (last?.state ?? "running") : s.state;
    if (last && last.state === state && last.end !== null && Math.abs(last.end - start) < 1000) {
      last.end = end;
      continue;
    }
    out.push({ start, end, state, label: s.state === "checkpointing" ? "" : s.label });
  }
  return out;
}

function attemptTitle(p: MlPhase, n: number, now: number): string {
  const a = p.attempts.find((x) => x.n === n)!;
  const start = tsMs(a.runningAt) ?? tsMs(a.queuedAt);
  const dur = start !== null ? (attemptEndMs(a, now) - start) / 1000 : Number.NaN;
  const end = endReasonOf(a.endReason);
  return [
    `${p.key} attempt ${n} · ${fmtDuration(dur)}`,
    `steps ${fmtStep(a.startStep)} → ${fmtStep(a.endStep)}${num(a.stepsLost) ? ` · ${fmtStep(a.stepsLost)} lost` : ""}`,
    `${end.label}${a.endDetail ? `: ${a.endDetail}` : ""}`,
    `${a.nodeIds.length} node${a.nodeIds.length === 1 ? "" : "s"} · ${a.worldSize} GPUs`,
  ].join("\n");
}

export function RunTimeline({ run, checkpoints, axis, selectedPhase, selectedAttempt, now }: TimelineProps) {
  const navigate = useNavigate();
  const { rows, from, to } = timelineRows(run, checkpoints, axis, now, { phase: selectedPhase, attempt: selectedAttempt });
  const base = run.name || run.id;
  const withSelect = rows.map((r) =>
    run.phases.some((p) => p.key === r.id) ? { ...r, onSelect: () => void navigate({ href: mlHref("runs", [base, "phases", r.id], { axis: axis === "step" ? "step" : undefined }) } as never) } : r,
  );
  return (
    <div className={cn("min-w-0")} data-testid="ml-run-timeline">
      <Gantt
        rows={withSelect}
        from={from}
        to={to}
        now={axis === "time" && !run.endedAt ? now : undefined}
        formatTick={axis === "time" ? (v) => fmtTime(v) : (v) => `step ${fmtStep(Math.round(v))}`}
        states={timelineStates(axis)}
        labelWidth={176}
        testId="ml-run-gantt"
      />
    </div>
  );
}
