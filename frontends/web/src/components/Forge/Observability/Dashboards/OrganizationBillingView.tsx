// Copyright (c) 2026 Reliant Labs

/**
 * Dashboard 3, "Billing and infrastructure" (`org-billing`,
 * DASHBOARD_VIEW_KIND_ORGANIZATION_BILLING).
 *
 * What the organization runs, what it used and what it costs: dev boxes,
 * hosted apps, databases, static sites and observability. A typed built-in
 * view over `BillingService.ListOrganizationUsage` (history, spend rate and
 * live inventory) and `GetOrganizationCostSummary` (allowances), gated by
 * billing:read and independent of environment and telemetry filters.
 *
 * Dev boxes are measured in seconds and carry no dollar figure; static egress,
 * builds and the registry carry usage and no price. Those read n/a, never $0.
 */

import { useMemo, useState } from "react";

import { useOrganizationCostSummary, useOrganizationUsage } from "@/hooks/organization-usage-queries";
import type { OrganizationCostSummary } from "@/services/controlPlane/observability/organizationCostsApi";
import type { OrganizationUsage } from "@/services/controlPlane/observability/organizationUsageApi";

import { formatBytes } from "../format";
import {
  allowanceRows,
  bucketStarts,
  categoryAmount,
  categoryLabel,
  countBy,
  formatQuantity,
  inventoryCount,
  inventoryCoverage,
  inventoryReadable,
  inventoryOf,
  limitationsOf,
  monthToDateRequest,
  quantitySeriesByDetail,
  quantitySum,
  quantityUnit,
  usageWindow,
  type UsageRangeId,
} from "./organizationUsageModel";
import {
  AmountText,
  amountCaption,
  BillingPermissionRequired,
  CompletenessBadge,
  CoverageNotes,
  ElsewhereNote,
  isPermissionDenied,
  Loaded,
  RangePicker,
  RefreshButton,
  RowsTable,
  SpendRatePanel,
  StatTile,
  UsageChart,
  UsagePanel,
  UsageViewHeader,
} from "./usageParts";

export interface OrganizationBillingViewProps {
  orgId: string;
  /** The clock, for tests and previews. */
  nowMs?: number;
}

const BILLING_SETTINGS = "/settings/billing";
const fmt = (n: number) => n.toLocaleString("en-US", { maximumFractionDigits: 1 });

export function OrganizationBillingView({ orgId, nowMs: fixedNow }: OrganizationBillingViewProps) {
  const [range, setRange] = useState<UsageRangeId>("30d");
  const [clock, setClock] = useState(() => fixedNow ?? Date.now());
  const nowMs = fixedNow ?? clock;
  const span = useMemo(() => usageWindow(range, nowMs), [range, nowMs]);
  const usage = useOrganizationUsage(orgId ? { orgId, ...span, groupBy: "category" } : null);
  const mtd = useOrganizationUsage(orgId ? monthToDateRequest(orgId, nowMs) : null);
  const cost = useOrganizationCostSummary(orgId || null);
  const queries = [usage, mtd, cost];
  const denied = queries.some((q) => isPermissionDenied(q.error));
  const refresh = () => {
    if (fixedNow === undefined) setClock(Date.now());
    for (const q of queries) void q.refetch();
  };
  const current = !denied && usage.data?.orgId === orgId ? usage.data : undefined;

  return (
    <section className="min-w-0 space-y-4 p-4 text-ink sm:p-6" aria-labelledby="organization-billing-title" data-testid="organization-billing-view">
      <UsageViewHeader
        id="organization-billing-title"
        title="Billing and infrastructure"
        scope={`Organization ${orgId || "not selected"} · All projects and environments · UTC`}
        completeness={current?.completeness}
        controls={
          <>
            <RangePicker value={range} onChange={setRange} />
            <RefreshButton onClick={refresh} busy={queries.some((q) => q.isFetching)} disabled={!orgId || denied} />
          </>
        }
      />
      {!orgId ? (
        <p role="status">Select an organization to view its billing and infrastructure.</p>
      ) : denied ? (
        <BillingPermissionRequired what="this organization's billing and infrastructure" />
      ) : (
        <>
          <div className="grid min-w-0 gap-4 lg:grid-cols-2">
            <UsagePanel title="Dev boxes now" description="Count by size and state, and provisioned storage." testId="billing-devboxes-now">
              <Loaded query={usage} orgId={orgId} what="dev boxes">
                {(u) => <DevBoxesNow usage={u} summary={cost.data?.orgId === orgId ? cost.data : undefined} nowMs={nowMs} />}
              </Loaded>
            </UsagePanel>
            <UsagePanel title="Dev-box running hours" description="Hours running per bucket, stacked by machine size. Dev boxes are measured, not priced here." testId="billing-devbox-hours">
              <Loaded query={usage} orgId={orgId} what="dev-box hours">
                {(u) => (
                  <UsageChart
                    label="Dev-box running hours by size"
                    bucketsMs={bucketStarts(u)}
                    series={quantitySeriesByDetail(u, "machines", "running_seconds", 1 / 3600)}
                    unit="count"
                    stacked
                    empty="No dev box ran in this range."
                  />
                )}
              </Loaded>
            </UsagePanel>
          </div>
          <div className="grid min-w-0 gap-4 lg:grid-cols-2">
            <Loaded query={usage} orgId={orgId} what="spend rate">
              {(u) => <SpendRatePanel usage={u} monthToDate={mtd.data?.orgId === orgId ? mtd.data : undefined} nowMs={nowMs} />}
            </Loaded>
            <UsagePanel title="Dev-box activity by hour of day" description="When dev boxes run, hour by hour." testId="billing-devbox-heatmap">
              <p className="text-sm text-ink-muted">Not available yet: the billing history is kept per day, so an hour-of-day breakdown cannot be drawn from it.</p>
            </UsagePanel>
          </div>
          <div className="grid min-w-0 gap-4 lg:grid-cols-3">
            <UsagePanel title="Hosted apps" description="Count by environment, and resource hours in range." testId="billing-hosted-apps">
              <Loaded query={usage} orgId={orgId} what="hosted apps">
                {(u) => <HostedCategory usage={u} category="hosted_apps" summary={cost.data?.orgId === orgId ? cost.data : undefined} nowMs={nowMs} />}
              </Loaded>
            </UsagePanel>
            <UsagePanel title="Databases" description="Count, and storage and compute in range." testId="billing-databases">
              <Loaded query={usage} orgId={orgId} what="databases">
                {(u) => <HostedCategory usage={u} category="databases" extra={[["Backups", "Not reported"]]} />}
              </Loaded>
            </UsagePanel>
            <UsagePanel title="Static sites" description="Count and CDN egress in range." testId="billing-static">
              <Loaded query={usage} orgId={orgId} what="static sites">
                {(u) => <HostedCategory usage={u} category="static_hosting" extra={[["Free-site allowance", "Not reported"]]} />}
              </Loaded>
            </UsagePanel>
          </div>
          <UsagePanel title="Observability" description="Billed ingest per bucket by signal, and totals in range." testId="billing-observability">
            <Loaded query={usage} orgId={orgId} what="observability usage">
              {(u) => <Observability usage={u} />}
            </Loaded>
          </UsagePanel>
          <div className="grid min-w-0 gap-4 lg:grid-cols-2">
            <UsagePanel title="Builds and registry" description="Build compute and image storage in range." testId="billing-hosted-other">
              <Loaded query={usage} orgId={orgId} what="builds and registry">
                {(u) => <HostedCategory usage={u} category="hosted_other" />}
              </Loaded>
            </UsagePanel>
            <UsagePanel title="Invoices" description="Past invoices with status and amount." testId="billing-invoices">
              <ElsewhereNote href={BILLING_SETTINGS} linkLabel="Open invoices">
                Invoices are listed in billing settings. Invoice lines are not mirrored into the billing history yet.
              </ElsewhereNote>
            </UsagePanel>
          </div>
          {current && <CoverageNotes entries={limitationsOf(current)} />}
        </>
      )}
    </section>
  );
}

function DevBoxesNow({ usage, summary, nowMs }: { usage: OrganizationUsage; summary: OrganizationCostSummary | undefined; nowMs: number }) {
  const items = inventoryOf(usage, "machines");
  const storage = items.reduce((total, item) => total + Number(item.storageGib), 0);
  const allowances = summary ? allowanceRows(summary, nowMs, ["machines"]) : [];
  const storageAllowance = allowances.find((a) => /gib|storage/i.test(`${a.id} ${a.unit}`));
  const inventory = inventoryCoverage(usage);
  const known = inventoryReadable(usage);
  return (
    <div className="space-y-3">
      <div className="grid min-w-0 grid-cols-1 gap-3 sm:grid-cols-2">
        <StatTile
          caption="Dev boxes"
          testId="billing-devbox-count"
          value={known ? fmt(inventoryCount(items)) : "Unavailable"}
          badge={<CompletenessBadge value={inventory?.completeness} label="Inventory" limitations={inventory?.limitations} />}
          footnote={known ? countBy(items, "state").map((s) => `${fmt(s.count)} ${s.key || "unknown"}`).join(" · ") || "None running or suspended" : "Current dev boxes could not be read; unknown, not zero"}
        />
        <StatTile
          caption="Provisioned storage"
          testId="billing-devbox-storage"
          value={known ? `${fmt(storage)} GiB` : "Unavailable"}
          footnote={!known ? "Unknown, not zero" : storageAllowance ? `Pooled allowance: ${fmt(storageAllowance.included)} ${storageAllowance.unit}` : "Pooled allowance: not reported"}
        />
      </div>
      <RowsTable caption="Dev boxes by size" head={["Size", "Count"]} rows={known ? countBy(items, "size").map((s) => [s.key || "Unspecified", fmt(s.count)]) : []} empty={known ? "No dev boxes right now." : "Current dev boxes could not be read; unknown, not zero."} />
    </div>
  );
}

/** Quantities a hosted category reports, with friendly names. */
const QUANTITY_LABELS: Record<string, string> = {
  infra_cpu: "CPU",
  infra_memory: "Memory",
  infra_storage: "Storage",
  egress_kib: "Egress",
};

function HostedCategory({
  usage,
  category,
  summary,
  nowMs,
  extra = [],
}: {
  usage: OrganizationUsage;
  category: string;
  summary?: OrganizationCostSummary;
  nowMs?: number;
  extra?: [string, string][];
}) {
  const items = inventoryOf(usage, category);
  const inventory = inventoryCoverage(usage);
  const known = inventoryReadable(usage);
  const points = usage.points.filter((p) => p.category === category);
  const ids = [...new Set(points.flatMap((p) => p.quantities.map((q) => q.id)))];
  const amount = categoryAmount(usage, category);
  const included = summary && nowMs !== undefined ? allowanceRows(summary, nowMs, ["hosted_infrastructure"]) : [];
  const rows = [
    ...ids.map((id) => {
      const allowance = included.find((a) => a.id && id.endsWith(a.id));
      const value = formatQuantity(quantitySum(points, id), quantityUnit(points, id));
      return [QUANTITY_LABELS[id] ?? id, allowance ? `${value} · ${fmt(allowance.included)} ${allowance.unit} incl.` : value];
    }),
    ...extra,
  ];
  return (
    <div className="space-y-3">
      <StatTile
        caption={`${categoryLabel(category)} now`}
        testId={`billing-count-${category}`}
        value={known ? fmt(inventoryCount(items)) : "Unavailable"}
        badge={<CompletenessBadge value={inventory?.completeness} label="Inventory" limitations={inventory?.limitations} />}
        footnote={known ? countBy(items, "environmentName").map((e) => `${fmt(e.count)} in ${e.key || "no environment"}`).join(" · ") || "None deployed" : "What is deployed now could not be read; unknown, not zero"}
      />
      <RowsTable caption={`${categoryLabel(category)} usage in range`} head={["Measure", "In range"]} rows={rows} empty="No metered usage in range." />
      <p className="text-xs text-ink-muted" data-testid={`billing-amount-${category}`}>
        Cost in range: <AmountText amount={amount} className="text-ink" /> · {amountCaption(amount)}
      </p>
    </div>
  );
}

function Observability({ usage }: { usage: OrganizationUsage }) {
  const points = usage.points.filter((p) => p.category === "observability");
  const signals = [...new Set(points.map((p) => p.detail))].sort();
  const amount = categoryAmount(usage, "observability");
  return (
    <div className="grid min-w-0 gap-4 lg:grid-cols-2">
      <UsageChart
        label="Billed observability ingest by signal"
        bucketsMs={bucketStarts(usage)}
        series={quantitySeriesByDetail(usage, "observability", "billed_bytes")}
        unit="bytes"
        stacked
        empty="No billed observability ingest in this range."
      />
      <div className="min-w-0 space-y-3">
        <RowsTable
          caption="Observability totals by signal"
          head={["Signal", "Billed", "Stored"]}
          rows={signals.map((signal) => {
            const of = points.filter((p) => p.detail === signal);
            const billed = quantitySum(of, "billed_bytes");
            const stored = quantitySum(of, "stored_bytes");
            return [signal || "Unspecified", billed === undefined ? "n/a" : formatBytes(billed), stored === undefined ? "n/a" : formatBytes(stored)];
          })}
          empty="No observability usage in range."
        />
        <p className="text-xs text-ink-muted">
          Cost in range: <AmountText amount={amount} className="text-ink" /> · {amountCaption(amount)}
        </p>
        <ElsewhereNote href={BILLING_SETTINGS} linkLabel="Open Usage & limits">
          Current storage and the ingest cap state are in Usage & limits.
        </ElsewhereNote>
      </div>
    </div>
  );
}
