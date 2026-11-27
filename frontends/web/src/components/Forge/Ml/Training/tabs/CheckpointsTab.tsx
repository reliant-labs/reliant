// Copyright (c) 2026 Reliant Labs

/**
 * A run's Checkpoints tab (02-views §3.9): where can I resume from, what
 * checkpointing costs, which checkpoint is best, which became a model.
 */

import { FlaskConical, RotateCcw, Upload } from "lucide-react";

import { MlCheckpointStatus, MlSubjectKind, type MlCheckpoint } from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";

import { Chip, fmtAgo, fmtBytes, fmtDuration, fmtStep, KpiStrip, MlLink, MlTable, mlHref, num, Panel, StatusDot, SubjectLink, tail, tsMs, useMlSearch } from "../../kit";
import { DemoAction } from "../parts";
import type { RunScope } from "../runScope";
import { checkpointFacts, scopeCheckpoints, seriesPhase } from "../trainingModel";

const STATUS: Record<number, { label: string; tone: "success" | "info" | "danger" | "neutral" }> = {
  [MlCheckpointStatus.DURABLE]: { label: "durable", tone: "success" },
  [MlCheckpointStatus.UPLOADING]: { label: "uploading", tone: "info" },
  [MlCheckpointStatus.PARTIAL]: { label: "partial", tone: "danger" },
  [MlCheckpointStatus.DELETED]: { label: "deleted (retention)", tone: "neutral" },
};

export function CheckpointsTab({ scope, now }: { scope: RunScope; now: number }) {
  const [search] = useMlSearch();
  const { detail, phase, attempt, phaseScoped } = scope;
  const rows = scopeCheckpoints(detail.checkpoints, phase, attempt, phaseScoped);
  const p = seriesPhase(detail, phaseScoped ? phase : null);
  const facts = checkpointFacts(detail.checkpoints, p);
  const best = [...rows].filter((c) => !p || c.phaseKey === p.key).filter((c) => Number.isFinite(c.metrics["val/loss"])).sort((a, b) => a.metrics["val/loss"] - b.metrics["val/loss"])[0];
  return (
    <div className="space-y-4" data-testid="ml-run-checkpoints">
      <KpiStrip
        testId="ml-ckpt-kpis"
        items={[
          { label: `Last durable · ${p?.key ?? ""}`, value: facts.lastDurableStep !== null ? fmtStep(facts.lastDurableStep) : "—", sub: facts.lastDurableAt ? fmtAgo(facts.lastDurableAt, now) : undefined, hero: true },
          { label: "Steps at risk", value: facts.atRiskSteps !== null ? fmtStep(facts.atRiskSteps) : "—", sub: facts.atRiskSteps !== null ? "redone if it failed now" : "phase not running", tone: facts.atRiskSteps && facts.cadenceSteps && facts.atRiskSteps > facts.cadenceSteps ? "warning" : undefined },
          { label: "Cadence", value: facts.cadenceSteps ? `${fmtStep(facts.cadenceSteps)} steps` : "—", sub: facts.cadenceSeconds ? `every ${fmtDuration(facts.cadenceSeconds)}` : undefined },
          { label: "Storage held", value: fmtBytes(facts.storageBytes), sub: `${facts.durable} durable${facts.partial ? ` · ${facts.partial} partial` : ""}` },
          { label: `Best val loss · ${p?.key ?? ""}`, value: best ? best.metrics["val/loss"].toFixed(3) : "—", sub: best ? `${best.phaseKey} step ${fmtStep(best.step)}` : undefined },
        ]}
      />
      <Panel
        title="Checkpoints"
        note={`${rows.length} · ${phaseScoped && phase ? phase.key : "all phases"}${attempt ? ` · attempt ${attempt.n}` : ""}`}
        actions={
          <>
            <DemoAction icon={<FlaskConical className="h-3.5 w-3.5" />}>Evaluate</DemoAction>
            <DemoAction icon={<Upload className="h-3.5 w-3.5" />}>Promote</DemoAction>
            <DemoAction icon={<RotateCcw className="h-3.5 w-3.5" />}>Rewind</DemoAction>
          </>
        }
        flush
      >
        <MlTable<MlCheckpoint>
          rows={rows}
          rowKey={(c) => c.id}
          selected={search.sel}
          maxRows={40}
          testId="ml-ckpt-table"
          initialSort={{ key: "step", desc: true }}
          columns={[
            { key: "step", header: "Step", numeric: true, sort: (c) => (c.phaseKey === p?.key ? 1e9 : 0) + num(c.step), render: (c) => <span className="font-medium">{num(c.step).toLocaleString("en-US")}</span> },
            { key: "phase", header: "Phase", render: (c) => <span className="text-ink-muted">{c.phaseKey}</span> },
            { key: "attempt", header: "Attempt", render: (c) => <MlLink href={mlHref("runs", [scope.run.name, "phases", c.phaseKey], { attempt: c.attempt, tab: "checkpoints" })}>a{c.attempt}</MlLink> },
            { key: "kind", header: "Kind", render: (c) => <span className="text-ink-muted">{c.kind}</span> },
            { key: "size", header: "Size", numeric: true, render: (c) => fmtBytes(c.sizeBytes) },
            { key: "save", header: "Save", numeric: true, render: (c) => fmtDuration(c.saveSeconds) },
            { key: "upload", header: "Upload", numeric: true, render: (c) => (c.uploadSeconds ? fmtDuration(c.uploadSeconds) : "—") },
            { key: "blocking", header: "Blocked loop", numeric: true, render: (c) => fmtDuration(c.blockingSeconds) },
            { key: "async", header: "Async", render: (c) => <span className="text-ink-muted">{c.async ? "yes" : "no"}</span> },
            { key: "val", header: "Val loss", numeric: true, sort: (c) => c.metrics["val/loss"] ?? 99, render: (c) => (Number.isFinite(c.metrics["val/loss"]) ? c.metrics["val/loss"].toFixed(3) : "—") },
            {
              key: "evals",
              header: "Evals",
              render: (c) =>
                c.evalRunIds.length ? (
                  <span className="inline-flex gap-2">
                    {c.evalRunIds.map((id) => (
                      <SubjectLink key={id} subject={{ kind: MlSubjectKind.EVAL_RUN, id, label: id.includes("PERIODIC") ? `◆ lite${Number.isFinite(c.evalScores.mmlu_pro) ? ` ${(c.evalScores.mmlu_pro * 100).toFixed(1)}` : ""}` : "◆ gate" }} />
                    ))}
                  </span>
                ) : (
                  <span className="text-ink-muted">—</span>
                ),
            },
            {
              key: "model",
              header: "Model",
              render: (c) => (c.modelVersionId ? <SubjectLink subject={{ kind: MlSubjectKind.MODEL_VERSION, id: c.modelVersionId, label: tail(c.modelVersionId) }} /> : <span className="text-ink-muted">—</span>),
            },
            { key: "status", header: "Status", render: (c) => <StatusDot tone={STATUS[c.status]?.tone ?? "neutral"} label={STATUS[c.status]?.label ?? "unknown"} /> },
            { key: "saved", header: "Saved", numeric: true, sort: (c) => tsMs(c.savedAt) ?? 0, render: (c) => <span className="text-ink-muted">{fmtAgo(tsMs(c.savedAt), now)}</span> },
          ]}
        />
      </Panel>
      {rows.some((c) => c.status === MlCheckpointStatus.PARTIAL) && (
        <p className="text-meta text-ink-muted">
          <Chip tone="danger">partial</Chip> A partial checkpoint never became durable (an emergency save cut off by the termination grace period); resume falls back to the last durable one.
        </p>
      )}
    </div>
  );
}
