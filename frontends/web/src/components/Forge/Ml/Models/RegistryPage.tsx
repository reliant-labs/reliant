// Copyright (c) 2026 Reliant Labs

/**
 * ML › Models: what models exist, which version each env runs, what the
 * next candidate is and whether it passes, and how much traffic each serves
 * (02-views §5.1). The candidate comes from the eval matrix: a checkpoint is
 * a candidate before it is registered as a version.
 */

import type { UseQueryResult } from "@tanstack/react-query";

import { MlSubjectKind, type MlModel } from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";
import type { MlOutcome } from "@/services/controlPlane/ml/types";

import { MlFrame, MlRead } from "../MlParts";
import { Chip, EmptyNote, fmtCompact, KpiStrip, MlLink, mlHref, MlTable, Panel, StatusDot, SubjectLink, tsMs, useMl } from "../kit";
import { aliasOf, candidateRow, gateTone, gateWord, versionOf } from "./modelsModel";
import { VersionLink } from "./parts";

function dataOf<T>(q: UseQueryResult<MlOutcome<T>, unknown>): T | undefined {
  return q.data?.kind === "ok" ? q.data.data : undefined;
}

/** The model's candidate (the matrix row aliased "candidate" on the release-gate suite). */
function CandidateCell({ model, suiteId }: { model: MlModel; suiteId: string | undefined }) {
  const q = useMl("getMlEvalMatrix", { suiteId: suiteId ?? "", modelId: model.id }, { enabled: !!suiteId });
  const row = candidateRow(dataOf(q));
  if (!row) return <span className="text-ink-muted">—</span>;
  return (
    <span className="inline-flex items-center gap-2">
      {row.subject && row.subject.kind !== MlSubjectKind.UNSPECIFIED ? <SubjectLink subject={row.subject} /> : row.label}
      <MlLink href={mlHref("evals", ["runs", row.evalRunId])} title={row.gateFailures.join("\n") || "Every gate passed"}>
        <StatusDot tone={gateTone(row.gateStatus)} label={`gate ${gateWord(row.gateStatus)}`} />
      </MlLink>
    </span>
  );
}

function AliasCell({ model, name }: { model: MlModel; name: string }) {
  const a = aliasOf(model, name);
  if (!a) return <span className="text-ink-muted">—</span>;
  return (
    <span className="inline-flex items-center gap-1.5">
      <VersionLink versionId={a.versionId} />
      {a.env && <Chip>{a.env}</Chip>}
    </span>
  );
}

export function RegistryPage() {
  const models = useMl("listMlModels", {});
  const suites = useMl("listMlEvalSuites", {});
  const gateSuite = dataOf(suites)?.suites[0]?.id;
  const list = dataOf(models)?.models ?? [];
  const lastGate = [...list].filter((m) => m.lastGateStatus).sort((a, b) => (tsMs(b.updatedAt) ?? 0) - (tsMs(a.updatedAt) ?? 0))[0];

  return (
    <MlFrame view="models">
      <KpiStrip
        testId="ml-models-kpis"
        items={[
          { label: "Models", value: models.data ? `${list.length}` : "—", sub: "in the OCI registry" },
          { label: "Versions", value: models.data ? `${list.reduce((s, m) => s + m.versionCount, 0)}` : "—", sub: "by digest" },
          { label: "Serving services", value: models.data ? `${list.reduce((s, m) => s + m.servingServices, 0)}` : "—", sub: "across prod and staging", href: mlHref("inference") },
          { label: "Requests served", value: models.data ? `${fmtCompact(list.reduce((s, m) => s + m.servingRps, 0))} rps` : "—", sub: "now" },
          {
            label: "Last gate",
            value: lastGate ? `${lastGate.name} ${versionOf(lastGate.lastGateVersionId)}` : "—",
            tone: lastGate ? gateTone(lastGate.lastGateStatus) : undefined,
            sub: lastGate ? `release gate ${gateWord(lastGate.lastGateStatus)}` : undefined,
            href: lastGate ? mlHref("models", [lastGate.id, versionOf(lastGate.lastGateVersionId)]) : undefined,
            hero: true,
          },
        ]}
      />
      <Panel title="Registry" note="Every cell opens what it names" flush testId="ml-models-registry">
        <MlRead query={models} what="models">
          {(d) => (
            <MlTable
              testId="ml-models-table"
              rows={d.models}
              rowKey={(m) => m.id}
              rowHref={(m) => mlHref("models", [m.id])}
              empty={<EmptyNote>No model has been pushed or trained yet.</EmptyNote>}
              columns={[
                {
                  key: "model",
                  header: "Model",
                  render: (m) => (
                    <span className="block">
                      <MlLink href={mlHref("models", [m.id])} className="font-medium">
                        {m.name}
                      </MlLink>
                      <span className="block max-w-xs truncate font-mono text-code text-ink-muted" title={m.repository}>
                        {m.repository.split("/").slice(-2).join("/")}
                      </span>
                    </span>
                  ),
                },
                {
                  key: "latest",
                  header: "Latest",
                  render: (m) => <VersionLink versionId={m.latestVersionId} />,
                },
                { key: "prod", header: "production", render: (m) => <AliasCell model={m} name="production" /> },
                { key: "staging", header: "staging", render: (m) => <AliasCell model={m} name="staging" /> },
                { key: "candidate", header: "candidate", render: (m) => <CandidateCell model={m} suiteId={gateSuite} /> },
                { key: "versions", header: "Versions", numeric: true, render: (m) => m.versionCount, sort: (m) => m.versionCount },
                {
                  key: "serving",
                  header: "Serving",
                  numeric: true,
                  sort: (m) => m.servingRps,
                  render: (m) => (
                    <MlLink href={mlHref("inference")}>
                      {m.servingServices} {m.servingServices === 1 ? "service" : "services"} · {fmtCompact(m.servingRps)} rps
                    </MlLink>
                  ),
                },
                {
                  key: "gate",
                  header: "Last eval gate",
                  render: (m) =>
                    m.lastGateStatus ? (
                      <span className="inline-flex items-center gap-1.5">
                        <StatusDot tone={gateTone(m.lastGateStatus)} label={m.lastGateStatus} />
                        <VersionLink versionId={m.lastGateVersionId} />
                      </span>
                    ) : (
                      <span className="text-ink-muted">—</span>
                    ),
                },
              ]}
            />
          )}
        </MlRead>
      </Panel>
    </MlFrame>
  );
}
