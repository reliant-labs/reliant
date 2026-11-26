// Copyright (c) 2026 Reliant Labs

/**
 * A service's Cost (GPU-hours, $ per hour, $ per 1M input and output tokens,
 * idle replicas) and Alerts (the rules on it, their state and where they
 * route). Reliant never rolls back on its own: a regression halts promotion
 * and notifies; the previous release keeps serving.
 */

import type { UseQueryResult } from "@tanstack/react-query";

import { MlAlertState, type GetMlInferenceServiceResponse, type ListMlAlertsResponse, type MlAlertRule, type MlEvent } from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";
import type { MlOutcome } from "@/services/controlPlane/ml/types";

import { MlRead } from "../MlParts";
import { costCategoryOf, EmptyNote, FactList, fmtAgo, fmtGpuHours, fmtUsd, Grid, MlTable, Panel, severityOf, StackedBar, StatusDot, tsMs, type MlColumn } from "../kit";
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

export function AlertsTab({ alerts, query }: { alerts: readonly MlAlertRule[]; query: UseQueryResult<MlOutcome<ListMlAlertsResponse>, unknown> }) {
  const now = Date.now();
  const columns: MlColumn<MlAlertRule>[] = [
    {
      key: "state",
      header: "State",
      render: (a) => {
        const s = a.state === MlAlertState.FIRING ? { tone: severityOf(a.severity).tone, label: "Firing" } : a.state === MlAlertState.PENDING ? { tone: "info" as const, label: "Pending" } : a.state === MlAlertState.SILENCED ? { tone: "neutral" as const, label: "Silenced" } : { tone: "success" as const, label: "OK" };
        return <StatusDot tone={s.tone} label={s.label} />;
      },
      sort: (a) => -a.state,
    },
    { key: "name", header: "Alert", render: (a) => <span className="text-ink">{a.name}</span> },
    { key: "expr", header: "Condition", render: (a) => <span className="block max-w-sm truncate font-mono text-code text-ink-muted" title={a.expression}>{a.expression}</span> },
    { key: "sev", header: "Severity", render: (a) => <span className="text-ink-muted">{severityOf(a.severity).label}</span> },
    { key: "fired", header: "Last fired", render: (a) => <span className="tabular-nums text-ink-muted">{fmtAgo(tsMs(a.lastFiredAt), now)}</span> },
    { key: "n", header: "Fired 7d", numeric: true, render: (a) => a.fired7d },
    { key: "to", header: "Routed to", render: (a) => a.routedTo || "—" },
    { key: "action", header: "On its own", render: (a) => <span className="text-ink-muted">{a.autoAction ? `${a.autoAction}${a.autoActionEnabled ? "" : " (off)"}` : "notify only"}</span> },
    { key: "decl", header: "Declared in", render: (a) => <span className="font-mono text-code text-ink-muted">{a.declaredIn}</span> },
  ];
  return (
    <div className="space-y-3">
      <p className="text-ui text-ink-muted" data-testid="ml-inference-no-rollback">
        Reliant never rolls back automatically: a regression halts promotion and notifies, and the previous release keeps serving. Roll forward with a fix.
      </p>
      <MlRead query={query} what="alerts">
        {() =>
          alerts.length === 0 ? (
            <EmptyNote>No alert rules on this service.</EmptyNote>
          ) : (
            <Panel title="Alert rules" flush>
              <MlTable columns={columns} rows={alerts} rowKey={(a) => a.id} initialSort={{ key: "state" }} testId="ml-inference-alerts" />
              <ul className="space-y-1 border-t border-border-subtle px-4 py-3 text-meta text-ink-muted">
                {alerts
                  .filter((a) => a.lastAction)
                  .map((a) => (
                    <li key={a.id}>
                      <span className="text-ink">{a.name}:</span> {a.lastAction}
                    </li>
                  ))}
              </ul>
            </Panel>
          )
        }
      </MlRead>
    </div>
  );
}
