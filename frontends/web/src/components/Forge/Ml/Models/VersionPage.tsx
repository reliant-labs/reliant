// Copyright (c) 2026 Reliant Labs

/**
 * ML › Models › <model> › <version> (02-views §5.2, version page): what the
 * artifact is (model card, ModelPack files by digest), whether it passed
 * its gate, where it serves and at what share, how it performs per
 * hardware ($/1M tokens), how fast each cluster pulls it, who moved its
 * aliases, and its lineage.
 */

import type { UseQueryResult } from "@tanstack/react-query";

import { MlSubjectKind } from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";
import type { MlOutcome } from "@/services/controlPlane/ml/types";

import { MlFrame, MlRead } from "../MlParts";
import {
  Chip,
  EmptyNote,
  eventKindOf,
  FactList,
  fmtAgo,
  fmtCompact,
  fmtDuration,
  fmtInt,
  fmtMs,
  fmtUsd,
  Grid,
  has,
  KpiStrip,
  Meter,
  MlLink,
  mlHref,
  MlTable,
  Panel,
  StatusDot,
  SubjectLink,
  tsMs,
  useMl,
  useMlEvents,
} from "../kit";
import { LineageGraph } from "./LineageGraph";
import { useRunNames } from "./ModelPage";
import { checkpointLink, fmtGB, fmtParams, gateTone, gateWord, provenance, shortDigest, SOURCE_LABEL, versionOf } from "./modelsModel";
import { AliasHistory, GateChecks, VersionLink } from "./parts";

/** The version's events (registration, alias moves), newest first, with the hook each emits. */
function VersionEvents({ versionId, now }: { versionId: string; now: number }) {
  const q = useMlEvents({ kind: MlSubjectKind.MODEL_VERSION, id: versionId }, { range: "30d" });
  return (
    <MlRead query={q} what="events">
      {(d) =>
        d.events.length === 0 ? (
          <EmptyNote>Nothing recorded yet.</EmptyNote>
        ) : (
          <ul className="divide-y divide-border-subtle">
            {d.events.map((e) => (
              <li key={e.id} className="flex items-start justify-between gap-3 py-1.5">
                <div className="min-w-0">
                  <StatusDot tone={eventKindOf(e.kind).tone} label={<span className="text-ink">{e.title}</span>} />
                  {e.detail && <p className="pl-3.5 text-meta text-ink-muted">{e.detail}</p>}
                </div>
                <div className="shrink-0 text-right text-meta text-ink-muted">
                  <div>{fmtAgo(tsMs(e.at), now)}</div>
                  {e.hookEventType && <code className="font-mono text-code">{e.hookEventType}</code>}
                </div>
              </li>
            ))}
          </ul>
        )
      }
    </MlRead>
  );
}

function dataOf<T>(q: UseQueryResult<MlOutcome<T>, unknown>): T | undefined {
  return q.data?.kind === "ok" ? q.data.data : undefined;
}

const MEDIA_SHORT: Record<string, string> = {
  "application/vnd.cncf.model.weight.v1.raw": "weights",
  "application/vnd.cncf.model.weight.config.v1.raw": "config",
  "application/vnd.cncf.model.doc.v1.raw": "doc",
};

export function VersionPage({ modelId, versionId }: { modelId: string; versionId: string }) {
  const q = useMl("getMlModelVersion", { versionId });
  const model = dataOf(useMl("getMlModel", { modelId }))?.model;
  const runNames = useRunNames();
  const now = Date.now();
  const version = versionOf(versionId);

  return (
    <MlFrame
      view="models"
      title={`${model?.name ?? modelId} ${version}`}
      subtitle={<span className="font-mono text-code">{dataOf(q)?.version?.digest ?? versionId}</span>}
      meta={dataOf(q)?.version?.aliases.map((a) => <Chip key={a}>{a}</Chip>)}
      crumbs={[{ label: model?.name ?? modelId, href: mlHref("models", [modelId]) }, { label: version }]}
      testId="ml-page-model-version"
    >
      <MlRead query={q} what="model version">
        {({ version: v, files, gate, perf, pulls, aliasHistory }) => {
          if (!v) return <EmptyNote>No such version.</EmptyNote>;
          const c = v.card;
          const prodShare = v.serving.filter((s) => s.env === "prod").reduce((sum, s) => sum + s.trafficShare, 0);
          const from = provenance(v, runNames);
          const ckpt = checkpointLink(v);
          const cheapest = perf.filter((p) => p.serviceId).sort((a, b) => a.usdPerMillionTokens - b.usdPerMillionTokens)[0];
          return (
            <div className="space-y-4">
              <KpiStrip
                testId="ml-version-kpis"
                items={[
                  {
                    label: "Release gate",
                    value: v.gateTotal ? `${v.gatePassed} / ${v.gateTotal} passed` : "no gate",
                    tone: v.gateTotal ? gateTone(v.gateStatus) : undefined,
                    sub: gateWord(v.gateStatus),
                    hero: true,
                  },
                  { label: "Serving", value: v.serving.length ? `${Math.round(prodShare * 100)}% of prod` : "not serving", sub: v.serving.length ? `${v.serving.length} ${v.serving.length === 1 ? "revision" : "revisions"}` : undefined },
                  { label: "Size", value: fmtGB(c?.sizeBytes ?? 0n), sub: `${c?.shards ?? 0} shards · ${c?.dtype ?? "—"}` },
                  { label: "Parameters", value: fmtParams(c?.paramsTotal ?? 0n), sub: `${fmtInt(c?.contextLength)} context · ${c?.layers ?? 0} layers` },
                  { label: "Cost to serve", value: cheapest ? `${fmtUsd(cheapest.usdPerMillionTokens)} / 1M tok` : "—", sub: cheapest ? `${cheapest.hardware}, in ${cheapest.serviceId}` : "not serving" },
                  { label: "Created", value: fmtAgo(tsMs(v.createdAt), now), sub: `${SOURCE_LABEL[v.source] ?? "unknown"} by ${v.createdBy}` },
                ]}
              />

              <Grid cols={2}>
                <Panel title="Model card" testId="ml-version-card">
                  <FactList
                    columns={1}
                    facts={[
                      { label: "Architecture", value: c?.architecture ?? "—" },
                      { label: "Parameters", value: `${fmtParams(c?.paramsTotal ?? 0n)} total · ${fmtParams(c?.paramsActive ?? 0n)} active` },
                      { label: "Context length", value: `${fmtInt(c?.contextLength)} tokens` },
                      { label: "dtype", value: c?.dtype ?? "—" },
                      { label: "Quantization", value: c?.quantization || "none" },
                      { label: "Tokenizer", value: c?.tokenizer ?? "—" },
                      { label: "License", value: c?.license ?? "—" },
                      { label: "Layers · shards", value: `${c?.layers ?? 0} · ${c?.shards ?? 0}` },
                      { label: "Adapters", value: c?.adapters.length ? c.adapters.join(", ") : "none" },
                      {
                        label: "Base",
                        value: v.baseModel.startsWith(`${v.modelId}/`) ? <VersionLink versionId={v.baseModel}>{`${v.modelId} ${versionOf(v.baseModel)}`}</VersionLink> : v.baseModel,
                      },
                      {
                        label: "From",
                        value: (
                          <span className="inline-flex flex-wrap justify-end gap-x-1">
                            {from.map((p) => (p.href ? <MlLink key={p.label} href={p.href}>{p.label}</MlLink> : <span key={p.label}>{p.label}</span>))}
                            {ckpt?.href && (
                              <MlLink href={ckpt.href} className="text-meta">
                                · checkpoint {ckpt.label}
                              </MlLink>
                            )}
                          </span>
                        ),
                      },
                      { label: "ModelPack manifest", value: <span className="font-mono text-code" title={c?.mediaType}>{c?.mediaType}</span> },
                      { label: "Signatures · SBOM", value: <span className="text-ink-muted">not attached (OCI referrers)</span> },
                    ]}
                  />
                  {(c?.trainingData || c?.intendedUse) && (
                    <div className="mt-3 space-y-1 text-meta text-ink-muted">
                      {c?.trainingData && <p><span className="text-ink">Training data.</span> {c.trainingData}</p>}
                      {c?.intendedUse && <p><span className="text-ink">Intended use.</span> {c.intendedUse}</p>}
                    </div>
                  )}
                </Panel>
                <Panel title="Gate" note={`${v.gatePassed} of ${v.gateTotal} checks passed`} testId="ml-version-gate">
                  <GateChecks checks={gate} testId="ml-version-gate-checks" />
                </Panel>
              </Grid>

              <Grid cols={2}>
                <Panel title="Where it serves" flush testId="ml-version-serving">
                  <MlTable
                    testId="ml-version-serving-table"
                    rows={v.serving}
                    rowKey={(s) => s.revisionId}
                    rowHref={(s) => mlHref("inference", [s.serviceId])}
                    empty={<EmptyNote>Not serving anywhere.</EmptyNote>}
                    columns={[
                      { key: "svc", header: "Service", render: (s) => <SubjectLink subject={{ kind: MlSubjectKind.INFERENCE_SERVICE, id: s.serviceId }} /> },
                      { key: "env", header: "Env", render: (s) => <Chip>{s.env}</Chip> },
                      { key: "rev", header: "Revision", render: (s) => <SubjectLink subject={{ kind: MlSubjectKind.REVISION, id: s.revisionId }} mono>{s.revisionId.split("/").slice(1).join("/")}</SubjectLink> },
                      { key: "share", header: "Traffic", numeric: true, render: (s) => <Meter ratio={s.trafficShare} />, sort: (s) => s.trafficShare },
                    ]}
                  />
                </Panel>
                <Panel title="Serving performance" note="Per hardware, from Inference; benchmarks where it does not serve" flush testId="ml-version-perf">
                  <MlTable
                    testId="ml-version-perf-table"
                    rows={perf}
                    rowKey={(p) => `${p.hardware}:${p.serviceId}`}
                    rowHref={(p) => (p.serviceId ? mlHref("inference", [p.serviceId]) : null)}
                    empty={<EmptyNote>No serving or benchmark numbers yet.</EmptyNote>}
                    columns={[
                      {
                        key: "hw",
                        header: "Hardware",
                        render: (p) => (
                          <span className="block">
                            <span className="text-ink">{p.hardware}</span>
                            <span className="block text-meta text-ink-muted">{p.runtime}{p.serviceId ? ` · ${p.serviceId}` : ""}</span>
                          </span>
                        ),
                      },
                      { key: "ttft", header: "TTFT p95", numeric: true, render: (p) => (has(p.ttftP95Ms) ? fmtMs(p.ttftP95Ms) : <span className="text-ink-muted">n/a</span>) },
                      { key: "itl", header: "ITL p95", numeric: true, render: (p) => (has(p.itlP95Ms) ? fmtMs(p.itlP95Ms) : <span className="text-ink-muted">n/a</span>) },
                      { key: "tps", header: "tok/s/GPU", numeric: true, render: (p) => fmtCompact(p.tokensPerSecondPerGpu), sort: (p) => p.tokensPerSecondPerGpu },
                      { key: "usd", header: "$ / 1M tok", numeric: true, render: (p) => fmtUsd(p.usdPerMillionTokens), sort: (p) => p.usdPerMillionTokens },
                    ]}
                  />
                </Panel>
              </Grid>

              <Grid cols={2}>
                <Panel title="Files" note={`${files.length} layers · ${fmtGB(files.reduce((s, f) => s + Number(f.sizeBytes), 0))}`} flush testId="ml-version-files">
                  <MlTable
                    testId="ml-version-files-table"
                    rows={files}
                    rowKey={(f) => f.path}
                    maxRows={8}
                    columns={[
                      { key: "path", header: "Path", render: (f) => <span className="font-mono text-code text-ink">{f.path}</span> },
                      { key: "kind", header: "Layer", render: (f) => <span className="text-meta text-ink-muted" title={f.mediaType}>{MEDIA_SHORT[f.mediaType] ?? f.mediaType}</span> },
                      { key: "size", header: "Size", numeric: true, render: (f) => fmtGB(f.sizeBytes), sort: (f) => Number(f.sizeBytes) },
                      { key: "digest", header: "Digest", render: (f) => <span className="font-mono text-code text-ink-muted" title={f.digest}>{shortDigest(f.digest)}</span> },
                    ]}
                  />
                </Panel>
                <Panel title="Pull performance" note="Time for a node to have the weights, per cluster and source" flush testId="ml-version-pulls">
                  <MlTable
                    testId="ml-version-pulls-table"
                    rows={pulls}
                    rowKey={(p) => `${p.clusterId}:${p.source}`}
                    empty={<EmptyNote>No cluster has pulled this version.</EmptyNote>}
                    columns={[
                      { key: "cluster", header: "Cluster", render: (p) => <SubjectLink subject={{ kind: MlSubjectKind.CLUSTER, id: p.clusterId }} /> },
                      { key: "source", header: "From", render: (p) => p.source },
                      { key: "size", header: "Size", numeric: true, render: (p) => fmtGB(p.sizeBytes) },
                      { key: "secs", header: "Pull", numeric: true, render: (p) => fmtDuration(p.pullSeconds), sort: (p) => p.pullSeconds },
                      { key: "rate", header: "Rate", numeric: true, render: (p) => (p.pullSeconds > 0 ? `${fmtGB(Number(p.sizeBytes) / p.pullSeconds)}/s` : "—") },
                    ]}
                  />
                </Panel>
              </Grid>

              <Grid cols={2}>
                <Panel title="Alias history" testId="ml-version-alias-history">
                  <AliasHistory moves={aliasHistory} now={now} />
                </Panel>
                <Panel title="Events" note="What happened to this version; each can trigger a workflow" testId="ml-version-events">
                  <VersionEvents versionId={v.id} now={now} />
                </Panel>
              </Grid>

              <Panel title="Lineage" note="Hover an edge for what links two nodes; the outlined node is this version" testId="ml-version-lineage">
                <LineageGraph subject={{ kind: MlSubjectKind.MODEL_VERSION, id: v.id }} />
              </Panel>
            </div>
          );
        }}
      </MlRead>
    </MlFrame>
  );
}
