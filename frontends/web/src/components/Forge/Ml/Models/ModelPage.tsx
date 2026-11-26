// Copyright (c) 2026 Reliant Labs

/**
 * ML › Models › <model> (02-views §5.2): which version each alias names,
 * every version with where it came from and whether it passed, the lineage
 * from data to the services that serve it, the candidate's scorecard
 * against production, and what stands between each env and its next
 * version. Promote opens a release PR; it never edits a cluster.
 */

import { useMemo } from "react";
import type { UseQueryResult } from "@tanstack/react-query";

import Button from "@/components/forge-ui/button";
import { MlSubjectKind, type MlModelVersion } from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";
import type { MlOutcome } from "@/services/controlPlane/ml/types";
import { cn } from "@/lib/utils";

import { MlFrame, MlRead } from "../MlParts";
import {
  Chip,
  EmptyNote,
  fmtAgo,
  fmtCompact,
  fmtPct,
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
} from "../kit";
import { cellTone, fmtCi, fmtDeltaPt, fmtScore } from "../Evals/evalsModel";
import { LineageGraph } from "./LineageGraph";
import { aliasOf, checkpointLink, fmtGB, fmtParams, gateTone, gateWord, provenance, scorecard, shareIn, shortDigest, SOURCE_LABEL, versionOf } from "./modelsModel";
import { AliasHistory, GateChecks, VersionLink } from "./parts";

function dataOf<T>(q: UseQueryResult<MlOutcome<T>, unknown>): T | undefined {
  return q.data?.kind === "ok" ? q.data.data : undefined;
}

/** Run id → name, from the Training list (ids stay readable when it has none). */
export function useRunNames(): Record<string, string> {
  const runs = useMl("listMlRuns", {});
  return useMemo(() => Object.fromEntries((dataOf(runs)?.runs ?? []).map((r) => [r.id, r.name])), [runs]);
}

export function ModelPage({ modelId }: { modelId: string }) {
  const q = useMl("getMlModel", { modelId });
  const suites = useMl("listMlEvalSuites", {});
  const suite = dataOf(suites)?.suites[0];
  const matrix = useMl("getMlEvalMatrix", { suiteId: suite?.id ?? "", modelId }, { enabled: !!suite });
  const card = useMemo(() => scorecard(dataOf(matrix)), [matrix]);
  const runNames = useRunNames();
  const now = Date.now();
  const d = dataOf(q);
  const model = d?.model;

  return (
    <MlFrame
      view="models"
      title={model?.name ?? modelId}
      subtitle={model ? model.description : undefined}
      meta={model ? <span className="font-mono text-code text-ink-muted">{model.repository}</span> : undefined}
      crumbs={[{ label: model?.name ?? modelId }]}
      testId="ml-page-model"
    >
      <MlRead query={q} what="model">
        {({ model: m, versions, aliasHistory, promotion }) => {
          if (!m) return <EmptyNote>No such model.</EmptyNote>;
          const prod = aliasOf(m, "production");
          const staging = aliasOf(m, "staging");
          const cand = card.candidate;
          return (
            <div className="space-y-4">
              <KpiStrip
                testId="ml-model-kpis"
                items={[
                  {
                    label: "production",
                    value: prod ? <VersionLink versionId={prod.versionId} /> : "—",
                    sub: prod ? `${prod.env} · moved ${fmtAgo(tsMs(prod.movedAt), now)} by ${prod.movedBy}` : "no version",
                    hero: true,
                  },
                  { label: "staging", value: staging ? <VersionLink versionId={staging.versionId} /> : "—", sub: staging ? `${staging.env} · moved ${fmtAgo(tsMs(staging.movedAt), now)} by ${staging.movedBy}` : "no version" },
                  {
                    label: "candidate",
                    value: cand?.subject ? <SubjectLink subject={cand.subject} /> : "—",
                    tone: cand ? gateTone(cand.gateStatus) : undefined,
                    sub: cand ? `gate ${gateWord(cand.gateStatus)}${cand.gateFailures[0] ? `: ${cand.gateFailures[0]}` : ""}` : "none evaluated",
                    href: cand ? mlHref("evals", ["runs", cand.evalRunId]) : undefined,
                  },
                  { label: "Versions", value: `${m.versionCount}`, sub: `latest ${versionOf(m.latestVersionId)}` },
                  { label: "Serving", value: `${fmtCompact(m.servingRps)} rps`, sub: `${m.servingServices} services`, href: mlHref("inference") },
                ]}
              />

              <Panel title="Versions" flush testId="ml-model-versions">
                <MlTable
                  testId="ml-model-versions-table"
                  rows={versions}
                  rowKey={(v) => v.id}
                  rowHref={(v) => mlHref("models", [m.id, v.version])}
                  columns={[
                    {
                      key: "v",
                      header: "Version",
                      render: (v) => (
                        <span className="inline-flex items-center gap-1.5" title={`${SOURCE_LABEL[v.source] ?? "unknown"} ${fmtAgo(tsMs(v.createdAt), now)} by ${v.createdBy}`}>
                          <VersionLink versionId={v.id} />
                          {v.aliases.map((a) => (
                            <Chip key={a}>{a}</Chip>
                          ))}
                        </span>
                      ),
                    },
                    { key: "digest", header: "Digest", render: (v) => <span className="font-mono text-code text-ink-muted" title={v.digest}>{shortDigest(v.digest)}</span> },
                    { key: "from", header: "From", render: (v) => <From v={v} runNames={runNames} /> },
                    { key: "base", header: "Base", render: (v) => <Base v={v} /> },
                    { key: "params", header: "Params", numeric: true, render: (v) => fmtParams(v.card?.paramsTotal ?? 0n) },
                    { key: "dtype", header: "dtype", render: (v) => v.card?.dtype ?? "—" },
                    { key: "size", header: "Size", numeric: true, render: (v) => fmtGB(v.card?.sizeBytes ?? 0n), sort: (v) => Number(v.card?.sizeBytes ?? 0n) },
                    {
                      key: "gate",
                      header: "Gate",
                      render: (v) => (v.gateTotal ? <StatusDot tone={gateTone(v.gateStatus)} label={`${v.gateStatus === "pass" ? "✓" : v.gateStatus === "fail" ? "✕" : "…"} ${v.gatePassed}/${v.gateTotal}`} /> : <span className="text-ink-muted">no gate</span>),
                    },
                    { key: "serving", header: "Serving", render: (v) => <Serving v={v} /> },
                  ]}
                />
              </Panel>

              <Panel title="Lineage" note="From the data and base model to the services that serve each version; hover an edge for what links them" testId="ml-model-lineage">
                <LineageGraph subject={{ kind: MlSubjectKind.MODEL, id: m.id }} />
              </Panel>

              <Grid cols={2}>
                <Panel
                  title={cand ? `Scorecard: ${cand.label} vs ${card.production?.label ?? "production"}` : "Scorecard"}
                  note={suite ? `${suite.name} ${suite.version} · dot: significant at 95%` : undefined}
                  flush
                  testId="ml-model-scorecard"
                  actions={suite ? <MlLink href={mlHref("evals", [], { suite: suite.id })} className="text-meta">Matrix</MlLink> : undefined}
                >
                  <MlRead query={matrix} what="eval matrix">
                    {() =>
                      cand ? (
                        <MlTable
                          testId="ml-model-scorecard-table"
                          rows={card.lines}
                          rowKey={(l) => l.task.key}
                          rowHref={(l) => mlHref("evals", ["runs", cand.evalRunId], { task: l.task.key })}
                          columns={[
                            { key: "task", header: "Task", render: (l) => <span className="text-ink">{l.task.name}</span> },
                            { key: "prod", header: "production", numeric: true, render: (l) => <span className="text-ink-muted">{fmtScore(l.production?.value)}</span> },
                            { key: "cand", header: "candidate", numeric: true, render: (l) => fmtScore(l.candidate?.value) },
                            {
                              key: "delta",
                              header: "Δ pt",
                              numeric: true,
                              sort: (l) => l.candidate?.delta ?? 0,
                              render: (l) => {
                                if (!l.candidate) return "—";
                                const tone = cellTone(l.candidate);
                                return (
                                  <span className={cn("inline-flex items-center gap-1.5", l.candidate.gateFail && "rounded-md border border-danger-ink/50 px-1")}>
                                    {tone !== "neutral" && <span className={cn("h-1.5 w-1.5 rounded-full", TONE_DOT[tone])} aria-hidden="true" />}
                                    <span className={tone === "danger" ? "text-danger-ink" : undefined}>{fmtDeltaPt(l.candidate.delta)}</span>
                                  </span>
                                );
                              },
                            },
                            { key: "ci", header: "95%", numeric: true, render: (l) => <span className="text-meta text-ink-muted">{l.candidate ? fmtCi(l.candidate.stderr) : ""}</span> },
                          ]}
                        />
                      ) : (
                        <div className="px-4">
                          <EmptyNote>No candidate has been evaluated against production.</EmptyNote>
                        </div>
                      )
                    }
                  </MlRead>
                  {cand && Number.isFinite(cand.judgeWinRate) && (
                    <p className="border-t border-border-subtle px-4 py-2 text-meta text-ink-muted">
                      LLM judge prefers the candidate on {fmtPct(cand.judgeWinRate)} of support tickets (gpt-4o, rubric v3). Online metrics appear once it serves.
                    </p>
                  )}
                </Panel>

                <Panel title="Promotion" note="Declared gates per env: evals, approvals, the inference obs check" testId="ml-model-promotion">
                  {promotion.length === 0 ? (
                    <EmptyNote>No delivery policy declares a gate for this model.</EmptyNote>
                  ) : (
                    <div className="space-y-4">
                      {promotion.map((p) => {
                        const failing = p.checks.filter((c) => c.status === "fail" || c.status === "pending");
                        return (
                          <section key={p.env} className="space-y-1.5" data-testid={`ml-model-promotion-${p.env}`}>
                            <div className="flex flex-wrap items-center justify-between gap-2">
                              <h3 className="flex items-center gap-2 text-ui font-medium text-ink">
                                {p.env}
                                <span className="font-normal text-ink-muted">←</span>
                                <VersionLink versionId={p.versionId} />
                              </h3>
                              <Button
                                variant="outline"
                                size="sm"
                                disabled={!p.canPromote}
                                title={p.canPromote ? `Promote: ${p.action}` : `Blocked: ${failing.map((c) => c.name).join("; ") || p.action}`}
                                data-testid={`ml-model-promote-${p.env}`}
                              >
                                Promote → {p.env}
                              </Button>
                            </div>
                            <GateChecks checks={p.checks} />
                            <p className="text-meta text-ink-muted">
                              {p.canPromote ? `Promote ${p.action}.` : failing.length ? `Blocked by ${failing.length} ${failing.length === 1 ? "check" : "checks"}. Promote ${p.action} once they pass.` : p.action}
                            </p>
                          </section>
                        );
                      })}
                    </div>
                  )}
                </Panel>
              </Grid>

              <Panel title="Alias history" note="Who moved each alias, when, why, and in which release" testId="ml-model-alias-history">
                <AliasHistory moves={aliasHistory} now={now} />
              </Panel>
            </div>
          );
        }}
      </MlRead>
    </MlFrame>
  );
}

function From({ v, runNames }: { v: MlModelVersion; runNames: Record<string, string> }) {
  const parts = provenance(v, runNames);
  const ckpt = checkpointLink(v);
  return (
    <span className="inline-flex items-center gap-1">
      {parts.map((p, i) => (
        <span key={p.label} className="inline-flex items-center gap-1">
          {i > 0 && <span className="text-ink-muted">/</span>}
          {p.href ? <MlLink href={p.href}>{p.label}</MlLink> : <span className="text-ink-muted">{p.label}</span>}
        </span>
      ))}
      {ckpt?.href && (
        <>
          <span className="text-ink-muted">·</span>
          <MlLink href={ckpt.href} className="text-meta">
            {ckpt.label}
          </MlLink>
        </>
      )}
    </span>
  );
}

function Base({ v }: { v: MlModelVersion }) {
  if (v.baseModel.startsWith(`${v.modelId}/`)) return <VersionLink versionId={v.baseModel}>{`${v.modelId} ${versionOf(v.baseModel)}`}</VersionLink>;
  return <span className="text-ink-muted">{v.baseModel.split("/").at(-1)}</span>;
}

function Serving({ v }: { v: MlModelVersion }) {
  if (v.serving.length === 0) return <span className="text-ink-muted">not serving</span>;
  const envs = [...new Set(v.serving.map((s) => s.env))];
  return (
    <span className="inline-flex flex-wrap items-center gap-x-2">
      {envs.map((env) => {
        const svc = v.serving.find((s) => s.env === env)!;
        return (
          <MlLink key={env} href={mlHref("inference", [svc.serviceId])}>
            {env} {fmtPct(shareIn(v, env))}
          </MlLink>
        );
      })}
    </span>
  );
}
