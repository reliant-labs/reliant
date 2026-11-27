// Copyright (c) 2026 Reliant Labs

/**
 * A service's Cost (GPU-hours, $ per hour, $ per 1M input and output tokens,
 * idle replicas) and Alerts (what AlertingService has pending or firing on
 * it). Reliant never rolls back on its own: a regression halts promotion
 * and notifies; the previous release keeps serving.
 */

import type { GetMlInferenceServiceResponse, MlEvent } from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";
import type { AlertInstance } from "@/services/controlPlane/observability/alertingTypes";

import { MlRead, type MlQuery } from "../MlParts";
import { costCategoryOf, EmptyNote, FactList, fmtGpuHours, fmtUsd, Grid, MlAlertsTable, Panel, StackedBar } from "../kit";
import { byKey } from "./inferenceModel";
import { serviceSubject, SeriesTile, useServingSeries } from "./parts";

export function CostTab({ d, events }: { d: GetMlInferenceServiceResponse; events: readonly MlEvent[] }) {
  const svc = d.service!;
  const c = d.servingCost;
  const series = useServingSeries(serviceSubject(svc.id), ["serve.cost_usd_per_hour", "serve.replicas"]);
  const generative = Number.isFinite(c?.usdPerMillionOutputTokens ?? NaN);
  return (
    <div className="space-y-4">
      <Grid cols={2}>
        <Panel title="What serving costs" note="Accelerator time × the price sheet (L4 on-demand)">
          {c ? (
            <FactList
              columns={2}
              facts={[
                { label: "GPU-hours · 24h", value: fmtGpuHours(c.gpuHours24h) },
                { label: "Per hour", value: fmtUsd(c.usdPerHour) },
                { label: "Per day", value: fmtUsd(c.usdPerHour * 24) },
                { label: "Per 30 days", value: fmtUsd(c.usdPerHour * 24 * 30) },
                { label: "$ / 1M input tokens", value: generative ? fmtUsd(c.usdPerMillionInputTokens) : "n/a" },
                { label: "$ / 1M output tokens", value: generative ? fmtUsd(c.usdPerMillionOutputTokens) : "n/a" },
                { label: "Idle replicas", value: <span className={c.idleReplicas ? "text-warning-ink" : undefined}>{c.idleReplicas}</span> },
                { label: "Idle cost / day", value: fmtUsd(c.idleUsdPerDay) },
              ]}
            />
          ) : (
            <EmptyNote>No price sheet covers this service.</EmptyNote>
          )}
        </Panel>
        <Panel title="Where the GPU-hours went" note="24h: busy serving vs allocated and idle">
          {svc.cost && svc.cost.byCategory.length > 0 ? (
            <StackedBar
              segments={svc.cost.byCategory.map((s) => ({ label: costCategoryOf(s.category).label, value: s.usd, tone: costCategoryOf(s.category).tone === "neutral" ? undefined : costCategoryOf(s.category).tone }))}
              format={fmtUsd}
              testId="ml-inference-cost-split"
            />
          ) : (
            <EmptyNote>No breakdown.</EmptyNote>
          )}
        </Panel>
      </Grid>
      <MlRead query={series} what="cost over time">
        {({ series: all }) => (
          <Grid cols={2}>
            <SeriesTile title="$ per hour" series={byKey(all, "serve.cost_usd_per_hour")} events={events} />
            <SeriesTile title="Replicas vs desired" series={byKey(all, "serve.replicas")} events={events} />
          </Grid>
        )}
      </MlRead>
    </div>
  );
}

export function AlertsTab({ alerts, query }: { alerts: readonly AlertInstance[]; query: MlQuery<unknown> }) {
  return (
    <div className="space-y-3">
      <p className="text-ui text-ink-muted" data-testid="ml-inference-no-rollback">
        Reliant never rolls back automatically: a regression halts promotion and notifies, and the previous release keeps serving. Roll forward with a fix.
      </p>
      <Panel title="Alerts" note="pending and firing on this service" flush>
        <MlRead query={query} what="alerts">
          {() => <MlAlertsTable alerts={alerts} now={Date.now()} showSubject testId="ml-inference-alerts" empty={<EmptyNote>Nothing is pending or firing on this service.</EmptyNote>} />}
        </MlRead>
      </Panel>
    </div>
  );
}
