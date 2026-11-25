// Copyright (c) 2026 Reliant Labs

/**
 * ML › Models (MlEarlyAccessService.ListMlModels), early access: each model
 * and its versions by OCI digest, newest first — the tag pointing at it, its
 * eval metrics, the run that produced it, and where it serves now. `?app=`
 * narrows to one app.
 */

import DataTable, { type DataTableColumn } from "@/components/forge-ui/data_table";
import Tag from "@/components/forge-ui/tag";
import { useMlModels } from "@/hooks/ml-queries";
import type { MlModel, MlModelVersion } from "@/services/controlPlane/ml/types";

import { formatAgo, formatBytes } from "../Observability/format";
import { metricValue, shortDigest } from "./mlModel";
import { AppFilter, MlFrame, MlRead, Section, useMlGate } from "./MlParts";
import type { MlAppPageProps } from "./MlRunsPage";

export function MlModelsPage({ app, onApp, now }: MlAppPageProps) {
  const { orgId } = useMlGate();
  const query = useMlModels(orgId, app);
  const apps = query.data?.kind === "ok" ? query.data.data.map((model) => model.project) : [];
  return (
    <MlFrame view="models" controls={<AppFilter app={app} apps={apps} onApp={onApp} testId="ml-models-filters" />}>
      <MlRead query={query} what="model versions">
        {(models) =>
          models.length === 0 ? (
            <p className="text-ui text-ink-muted" data-testid="ml-models-none">No model versions recorded.</p>
          ) : (
            <div className="space-y-4" data-testid="ml-models">
              {models.map((model) => (
                <ModelSection key={`${model.project}/${model.name}`} model={model} now={now ?? Date.now()} />
              ))}
            </div>
          )
        }
      </MlRead>
    </MlFrame>
  );
}

type VersionRow = { version: MlModelVersion };

function ModelSection({ model, now }: { model: MlModel; now: number }) {
  const columns: DataTableColumn<VersionRow>[] = [
    {
      key: "digest",
      header: "Version",
      render: (_, { version }) => (
        <span className="block whitespace-nowrap">
          <code className="font-mono text-code text-ink" title={version.digest}>
            {shortDigest(version.digest)}
          </code>
          {version.tag && <Tag className="ml-2">{version.tag}</Tag>}
          {version.createdAtMs !== null && <span className="block text-meta text-ink-muted">{formatAgo(version.createdAtMs, now)}</span>}
        </span>
      ),
    },
    {
      key: "evals",
      header: "Evals",
      className: "w-full max-w-0",
      render: (_, { version }) => {
        const metrics = Object.entries(version.evalMetrics).sort(([a], [b]) => a.localeCompare(b));
        return metrics.length === 0 ? (
          <span className="text-ink-muted">—</span>
        ) : (
          <span className="flex flex-wrap gap-x-3 gap-y-0.5">
            {metrics.map(([name, value]) => (
              <span key={name} className="whitespace-nowrap">
                <span className="text-ink-muted">{name}</span> <span className="tabular-nums text-ink">{metricValue(value)}</span>
              </span>
            ))}
          </span>
        );
      },
    },
    { key: "run", header: "Run", render: (_, { version }) => <span className="font-mono text-code text-ink-muted">{version.runId || "pushed by hand"}</span> },
    {
      key: "serving",
      header: "Serving in",
      render: (_, { version }) =>
        version.servingIn.length === 0 ? (
          <span className="text-ink-muted">—</span>
        ) : (
          <ul>
            {version.servingIn.map((where) => (
              <li key={where} className="whitespace-nowrap font-mono text-code text-ink">
                {where}
              </li>
            ))}
          </ul>
        ),
    },
    { key: "size", header: "Size", numeric: true, render: (_, { version }) => <span className="text-ink-muted">{version.sizeBytes > 0 ? formatBytes(version.sizeBytes) : "—"}</span> },
  ];
  return (
    <Section title={model.name} note={[model.project, model.repository].filter(Boolean).join(" · ")} testId={`ml-model-${model.name}`} flush>
      <DataTable<VersionRow>
        flush
        stacked
        ariaLabel={`Versions of ${model.name}`}
        columns={columns}
        data={model.versions.map((version) => ({ version }))}
        getRowKey={({ version }) => version.digest}
        getRowProps={({ version }) => ({ className: "align-top", "data-testid": `ml-model-version-${version.digest}` })}
      />
    </Section>
  );
}
