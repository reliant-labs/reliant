// Copyright (c) 2026 Reliant Labs

/**
 * A run's Config & lineage tab (02-views §3.11): the effective params with
 * where each came from, the device mesh (each rank a cell, coloured by its
 * step compute, linking its System view), environment, hardware, data and
 * code, then the lineage graph.
 */

import { MlParamSource, MlSubjectKind, type MlParam } from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";
import { cn } from "@/lib/utils";

import { Chip, EmptyNote, FactList, fmtCompact, Grid, MlLink, MlTable, Panel, subjectHref } from "../../kit";
import { rankId } from "../../kit/ids";
import { LineageGraph } from "../../Models/LineageGraph";
import { Disclosure } from "../parts";
import type { RunScope } from "../runScope";
import { meshGrid, scopeRanks } from "../trainingModel";

const SOURCE: Record<number, string> = { [MlParamSource.TEMPLATE]: "template", [MlParamSource.LAUNCH]: "launch", [MlParamSource.PHASE]: "phase" };

export function ConfigTab({ scope }: { scope: RunScope }) {
  const { detail, run, phase } = scope;
  const cfg = detail.config;
  const par = cfg?.parallelism;
  const params = (cfg?.params ?? []).filter((p) => !p.phaseKey || !scope.phaseScoped || p.phaseKey === phase?.key);
  return (
    <div className="space-y-4" data-testid="ml-run-config">
      <Grid cols={2}>
        <Panel title="Parameters" note="template → launch → phase overrides" flush>
          <MlTable<MlParam>
            rows={params}
            rowKey={(p) => `${p.phaseKey}/${p.key}`}
            empty={<EmptyNote>No parameters reported.</EmptyNote>}
            columns={[
              { key: "key", header: "Key", render: (p) => <span className="font-mono text-code">{p.key}</span> },
              { key: "value", header: "Value", render: (p) => <span className="text-ink">{p.value}</span> },
              {
                key: "source",
                header: "From",
                render: (p) => (
                  <Chip title={p.phaseKey ? `Overridden for phase ${p.phaseKey}` : undefined}>
                    {SOURCE[p.source] ?? "—"}
                    {p.phaseKey ? `: ${p.phaseKey}` : ""}
                  </Chip>
                ),
              },
            ]}
          />
        </Panel>
        <Panel title="Parallelism" note={par ? `${par.framework} · world ${par.worldSize}` : undefined}>
          {par ? (
            <div className="space-y-3">
              <FactList
                columns={3}
                facts={[
                  { label: "DP", value: par.data },
                  { label: "TP", value: par.tensor },
                  { label: "PP", value: par.pipeline },
                  { label: "CP", value: par.context },
                  { label: "EP", value: par.expert },
                  { label: "ZeRO", value: par.zeroStage === 3 ? "3 (full shard)" : par.zeroStage },
                  { label: "Micro batch", value: par.microBatch },
                  { label: "Grad accum", value: par.gradAccumulation },
                  { label: "Global batch", value: par.globalBatch },
                ]}
              />
              <Mesh scope={scope} />
            </div>
          ) : (
            <EmptyNote>No parallelism reported.</EmptyNote>
          )}
        </Panel>
        <Panel title="Environment">{cfg?.environment.length ? <FactList facts={cfg.environment.map((f) => ({ label: f.label, value: <span className="font-mono text-code">{f.value}</span> }))} columns={1} /> : <EmptyNote>None reported.</EmptyNote>}</Panel>
        <Panel title="Hardware">{cfg?.hardware.length ? <FactList facts={cfg.hardware.map((f) => ({ label: f.label, value: f.value }))} columns={1} /> : <EmptyNote>None reported.</EmptyNote>}</Panel>
        <Panel title="Data" flush>
          <MlTable
            rows={cfg?.datasets ?? []}
            rowKey={(d) => d.id}
            empty={<EmptyNote>No datasets reported.</EmptyNote>}
            columns={[
              { key: "name", header: "Dataset", render: (d) => <span className="text-ink">{d.name}</span> },
              { key: "version", header: "Version", render: (d) => <span className="text-ink-muted">{d.version}</span> },
              { key: "tokens", header: "Tokens", numeric: true, render: (d) => fmtCompact(d.tokens) },
              { key: "mix", header: "Mix", numeric: true, render: (d) => `${Math.round(d.mixtureWeight * 100)}%` },
              { key: "digest", header: "Digest", render: (d) => <span className="font-mono text-code text-ink-muted" title={d.digest}>{d.digest.slice(0, 19)}</span> },
            ]}
          />
        </Panel>
        <Panel title="Code">
          {cfg?.code ? (
            <div className="space-y-2">
              <FactList
                columns={1}
                facts={[
                  { label: "Repository", value: cfg.code.repository },
                  { label: "Release", value: cfg.code.release || "—" },
                  { label: "Commit", value: <span className="font-mono text-code" title={cfg.code.commitMessage}>{cfg.code.commit.slice(0, 12)}</span> },
                  { label: "Image", value: <span className="font-mono text-code" title={cfg.code.imageDigest}>{cfg.code.imageDigest.slice(0, 19)}</span> },
                  ...(cfg.code.previousCommit ? [{ label: "Previous run's commit", value: <span className="font-mono text-code">{cfg.code.previousCommit.slice(0, 12)}</span> }] : []),
                ]}
              />
              {cfg.code.diffSummary && <p className="text-meta text-ink-muted">{cfg.code.diffSummary}</p>}
            </div>
          ) : (
            <EmptyNote>No code reference: an observed job.</EmptyNote>
          )}
        </Panel>
      </Grid>
      <Panel title="Lineage" note="datasets → phases → checkpoints → model versions → releases → services">
        <LineageGraph subject={{ kind: MlSubjectKind.RUN, id: run.id }} />
      </Panel>
      {cfg && (
        <Disclosure summary="Effective config as JSON" testId="ml-run-config-json">
          <pre className="max-h-80 overflow-auto rounded-md border border-border bg-surface-sunken px-3 py-2 font-mono text-code text-ink">
            {JSON.stringify(
              { params: Object.fromEntries(params.map((p) => [p.phaseKey ? `${p.phaseKey}.${p.key}` : p.key, p.value])), parallelism: par && { dp: par.data, tp: par.tensor, pp: par.pipeline, cp: par.context, ep: par.expert, zero: par.zeroStage, micro_batch: par.microBatch, global_batch: par.globalBatch, grad_accumulation: par.gradAccumulation } },
              null,
              2,
            )}
          </pre>
        </Disclosure>
      )}
    </div>
  );
}

/** The dp × tp mesh: a cell per rank, shaded by step compute vs the median, the straggler called out. */
function Mesh({ scope }: { scope: RunScope }) {
  const par = scope.detail.config!.parallelism!;
  const { attempt, ranks } = scopeRanks(scope.phase, scope.attempt);
  const grid = meshGrid(par.data, par.tensor, ranks);
  const phaseKey = scope.phase?.key ?? scope.run.currentPhaseKey;
  return (
    <div className="space-y-1.5" data-testid="ml-run-mesh">
      <div className="flex items-baseline justify-between text-meta text-ink-muted">
        <span>
          Device mesh ({par.meshOrder || "dp,tp"}){attempt ? ` · ${phaseKey} attempt ${attempt.n}` : ""}
        </span>
        <span>rows dp · columns tp</span>
      </div>
      <div className="inline-grid gap-px" style={{ gridTemplateColumns: `1.5rem repeat(${par.tensor}, minmax(1.75rem, 2.25rem))` }}>
        <span />
        {Array.from({ length: par.tensor }, (_, t) => (
          <span key={t} className="text-center text-meta tabular-nums text-ink-muted">
            {t}
          </span>
        ))}
        {grid.map((row, d) => [
          <span key={`l${d}`} className="pr-1 text-right text-meta tabular-nums text-ink-muted">
            {d}
          </span>,
          ...row.map((cell) => {
            const href = attempt ? subjectHref({ kind: MlSubjectKind.RANK, id: rankId(scope.run.id, phaseKey, attempt.n, cell.rank) }) : null;
            const excess = cell.computeRatio ? Math.min(1, Math.max(0, (cell.computeRatio - 0.97) / 0.25)) : 0;
            const body = (
              <span
                className={cn("flex h-6 items-center justify-center rounded-sm text-meta tabular-nums", cell.straggler ? "font-semibold text-ink ring-2 ring-warning" : "text-ink-muted")}
                style={{ background: cell.straggler ? "hsl(var(--warning) / 0.55)" : `hsl(var(--chart-1) / ${0.12 + excess * 0.6})` }}
                title={`rank ${cell.rank} · dp ${cell.dp} · tp ${cell.tp}${cell.rankRow ? ` · ${cell.rankRow.nodeId.split("/").pop()} GPU ${cell.rankRow.deviceIndex}` : ""}${cell.straggler ? ` · straggler: ${cell.rankRow?.stragglerReason}` : ""}`}
              >
                {cell.rank}
              </span>
            );
            return href ? (
              <MlLink key={cell.rank} href={href} className="hover:no-underline" testId={`ml-mesh-rank-${cell.rank}`}>
                {body}
              </MlLink>
            ) : (
              <span key={cell.rank}>{body}</span>
            );
          }),
        ])}
      </div>
      <p className="text-meta text-ink-muted">{ranks.length ? "Darker: more compute per step than the median. Each cell opens the rank's System view." : "Per-rank stats are reported for the running attempt only."}</p>
    </div>
  );
}
