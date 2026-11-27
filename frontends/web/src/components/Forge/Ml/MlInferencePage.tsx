// Copyright (c) 2026 Reliant Labs

/**
 * ML › Inference (MlEarlyAccessService.ListMlInferenceServices), early
 * access: each inference service — its runtime, the forge release and model
 * digest it runs, replicas, TTFT p95, queue wait, throughput, accelerator and
 * KV-cache use, where a new replica's start goes, and the release health
 * verdict of its latest promotion. `?app=` narrows to one app.
 */

import Status from "@/components/forge-ui/status";
import { useMlInference } from "@/hooks/ml-queries";
import type { MlInferenceService } from "@/services/controlPlane/ml/types";

import { formatAgo } from "../Observability/format";
import { millis, percent, shortDigest, VERDICT } from "./mlModel";
import { AppFilter, MlFrame, MlRead, Section, StageBreakdown, useMlGate } from "./MlParts";
import type { MlAppPageProps } from "./MlRunsPage";

export function MlInferencePage({ app, onApp, now }: MlAppPageProps) {
  const { orgId } = useMlGate();
  const query = useMlInference(orgId, app);
  const apps = query.data?.kind === "ok" ? query.data.data.map((service) => service.project) : [];
  return (
    <MlFrame view="inference" controls={<AppFilter app={app} apps={apps} onApp={onApp} testId="ml-inference-filters" />}>
      <MlRead query={query} what="inference services">
        {(services) =>
          services.length === 0 ? (
            <p className="text-ui text-ink-muted" data-testid="ml-inference-none">No inference services recorded.</p>
          ) : (
            <div className="space-y-4" data-testid="ml-inference">
              {services.map((service) => (
                <ServiceSection key={`${service.project}/${service.environment}/${service.name}`} service={service} now={now ?? Date.now()} />
              ))}
            </div>
          )
        }
      </MlRead>
    </MlFrame>
  );
}

function Figure({ label, value }: { label: string; value: string }) {
  return (
    <div className="min-w-0">
      <dt className="text-meta text-ink-muted">{label}</dt>
      <dd className="text-2xl font-semibold tabular-nums text-ink">{value}</dd>
    </div>
  );
}

function ServiceSection({ service, now }: { service: MlInferenceService; now: number }) {
  const verdict = VERDICT[service.releaseVerdict];
  return (
    <Section
      title={service.name}
      note={[service.project, service.environment, service.runtime].filter(Boolean).join(" · ")}
      testId={`ml-service-${service.name}`}
    >
      <div className="space-y-4">
        <div className="flex flex-wrap items-baseline gap-x-4 gap-y-1 text-ui" data-testid={`ml-service-release-${service.name}`} data-verdict={service.releaseVerdict}>
          <Status tone={verdict.tone} label={verdict.label} />
          <span className="text-ink">
            Release <span className="font-mono text-code">{service.release || "—"}</span>
          </span>
          <span className="text-ink-muted">
            model{" "}
            <code className="font-mono text-code text-ink" title={service.modelDigest}>
              {service.modelDigest ? shortDigest(service.modelDigest) : "—"}
            </code>
          </span>
          {service.releaseVerdictSummary && <span className="text-ink-muted">{service.releaseVerdictSummary}</span>}
          {service.releaseCheckedAtMs !== null && <span className="text-meta text-ink-muted">checked {formatAgo(service.releaseCheckedAtMs, now)}</span>}
        </div>
        <dl className="grid grid-cols-2 gap-4 sm:grid-cols-3 lg:grid-cols-6">
          <Figure label="TTFT p95" value={millis(service.ttftP95Ms)} />
          <Figure label="Queue wait p95" value={millis(service.queueWaitP95Ms)} />
          <Figure label="Tokens / s" value={Math.round(service.tokensPerSecond).toLocaleString()} />
          <Figure label="Replicas" value={String(service.replicas)} />
          <Figure label="Accelerator use" value={percent(service.acceleratorUtilizationPercent)} />
          <Figure label="KV cache" value={percent(service.kvCacheUsagePercent)} />
        </dl>
        {service.coldStart && service.coldStart.stages.length > 0 && <StageBreakdown breakdown={service.coldStart} title="Cold start" testId={`ml-service-coldstart-${service.name}`} />}
      </div>
    </Section>
  );
}
