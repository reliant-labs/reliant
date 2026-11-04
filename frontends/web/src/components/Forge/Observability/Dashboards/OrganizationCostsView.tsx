// Copyright (c) 2026 Reliant Labs

import { Code, ConnectError } from "@connectrpc/connect";
import { useQuery } from "@tanstack/react-query";
import { getOrganizationCostSummary, type OrganizationCostSummary } from "@/services/controlPlane/observability/organizationCostsApi";
import { costCompletenessLabel, costPeriodLabel, costSourceLabel, costTimestamp, formatCostUSD, organizationCostGroups, type CostSource } from "./organizationCostModel";

export interface OrganizationCostsViewProps {
  orgId: string;
}

const actionClass = "rounded-md border border-border bg-surface-sunken px-3 py-1.5 text-sm text-ink hover:bg-ink/5 focus-visible:outline focus-visible:outline-2 focus-visible:outline-ring disabled:opacity-50";

/** Organization billing is deliberately independent of telemetry environment, service and range. */
export function OrganizationCostsView({ orgId }: OrganizationCostsViewProps) {
  const query = useQuery({
    queryKey: ["organization-cost-summary", orgId],
    queryFn: () => getOrganizationCostSummary(orgId),
    enabled: !!orgId,
    retry: false,
  });
  const denied = query.error instanceof ConnectError && query.error.code === Code.PermissionDenied;
  // Never render stale sensitive data after a live permission refusal.
  const summary = !denied && query.data?.orgId === orgId ? query.data : undefined;
  return (
    <section className="min-w-0 space-y-6 p-4 text-ink sm:p-6" aria-labelledby="organization-costs-title">
      <header className="flex flex-wrap items-start justify-between gap-4">
        <div className="min-w-0 space-y-1">
          <h1 id="organization-costs-title" className="text-xl font-semibold tracking-tight">Organization costs</h1>
          <p className="break-words text-sm text-ink-muted">Organization: {orgId || "Not selected"} · All projects and environments</p>
          <p className="text-xs text-ink-muted">Source: canonical billing records · Current billing periods · Not telemetry</p>
        </div>
        <button type="button" className={actionClass} onClick={() => void query.refetch()} disabled={!orgId || query.isFetching}>
          {query.isFetching ? "Refreshing…" : "Refresh costs"}
        </button>
      </header>
      {!orgId ? <p role="status">Select an organization to view its costs.</p> : denied ? (
        <div role="alert" className="rounded-lg border border-border bg-surface p-5">
          <h2 className="font-semibold">Billing read permission required</h2>
          <p className="mt-2 text-sm text-ink-muted">Your current grant does not allow reading this organization’s costs. Ask an organization owner for billing:read. Billing-management access is not required.</p>
        </div>
      ) : (
        <>
          {query.isPending && <p role="status">Loading organization costs…</p>}
          {query.isError && <p role="alert" className="text-sm text-danger-ink">Costs could not be refreshed. {summary ? "Showing the last received summary; check its timestamps below." : "Billing data is unavailable, not zero. Try refreshing."}</p>}
          {query.data && !summary && !query.isError && <p role="alert">The summary does not match this organization. Refresh to try again.</p>}
          {summary && <OrganizationCostSummaryContent summary={summary} />}
        </>
      )}
    </section>
  );
}

/** Pure renderer, also used to verify this surface without a live billing service. */
export function OrganizationCostSummaryContent({ summary }: { summary: OrganizationCostSummary }) {
  const groups = organizationCostGroups(summary);
  return (
    <div className="min-w-0 space-y-6">
      <div className="space-y-2 rounded-lg border border-border bg-surface-sunken p-4 text-sm text-ink-muted">
        <p>Summary generated: <span className="text-ink">{costTimestamp(summary.generatedAt)}</span></p>
        <p>Amounts are USD. Charges, list-equivalent values, promotional credits and pending estimates are separate facts, not a total. Periods can differ.</p>
        <p>Charges represent billed consumption, not cash collected. Wallet top-ups are funding, not additional usage; promotional grants are not deducted from consumption.</p>
        <p>Missing values mean not reported, never zero. Provider-billed BYOK spend is not measured here. Freshness and coverage are reported per source below.</p>
      </div>
      {summary.sources.length === 0 && <p role="status" className="rounded-lg border border-border bg-surface p-5 text-sm">No cost sources were returned for this organization. This does not mean there were no charges.</p>}
      {groups.map(({ id, period, sources }) => (
        <section key={id} className="min-w-0 space-y-3" aria-label={costPeriodLabel(id)}>
          <div className="space-y-1">
            <h2 className="text-base font-semibold">{costPeriodLabel(id)}</h2>
            <p className="break-words text-xs text-ink-muted">Period ID: {id || "Not reported"}</p>
            <p className="text-xs text-ink-muted">Start (inclusive): {costTimestamp(period?.start)} · End (exclusive): {costTimestamp(period?.end)}</p>
          </div>
          {sources.length === 0 && <p className="text-sm text-ink-muted">No sources reported for this period.</p>}
          <div className="grid min-w-0 gap-4 xl:grid-cols-2">
            {sources.map((source, index) => <SourceCosts key={`${source.category}-${index}`} source={source} />)}
          </div>
        </section>
      ))}
    </div>
  );
}

function SourceCosts({ source }: { source: CostSource }) {
  const amounts = [
    ["Actual charges", source.actualChargesUsdNanos],
    ["List-equivalent value", source.listEquivalentUsdNanos],
    ["Promotional credits granted", source.creditsUsdNanos],
    ["Pending estimate", source.pendingEstimateUsdNanos],
  ] as const;
  return (
    <article className="min-w-0 space-y-4 rounded-lg border border-border bg-surface p-4" aria-label={`${costSourceLabel(source.category)} costs`}>
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h3 className="text-sm font-semibold">{costSourceLabel(source.category)}</h3>
        <span className="rounded border border-border px-2 py-0.5 text-xs text-ink-muted">{costCompletenessLabel(source.completeness)}</span>
      </div>
      <p className="break-words text-xs text-ink-muted">Billing source: {source.category || "Not reported"}</p>
      <dl className="grid min-w-0 grid-cols-1 gap-4 rounded-md border border-border/60 bg-surface-sunken p-3 sm:grid-cols-2">
        {amounts.map(([label, value]) => (
          <div key={label} className="min-w-0">
            <dt className="text-xs text-ink-muted">{label}</dt>
            <dd className="mt-1 break-words text-base font-medium tabular-nums">{formatCostUSD(value)}</dd>
          </div>
        ))}
      </dl>
      <p className="text-xs text-ink-muted">Pending items: <span className="text-ink">{source.pendingCount === undefined ? "Not reported" : source.pendingCount.toLocaleString("en-US")}</span></p>
      {source.allowances.length > 0 && (
        <div className="space-y-2">
          <h4 className="text-xs font-semibold">Included allowances · native units</h4>
          <ul className="space-y-2 text-xs text-ink-muted">
            {source.allowances.map((allowance, index) => (
              <li key={`${allowance.id}-${index}`} className="break-words">
                <span className="text-ink">{allowance.id || "Allowance"}</span>: {allowance.included.toLocaleString("en-US", { maximumFractionDigits: 20 })} {allowance.unit} included · {allowance.consumed === undefined ? "Consumption not reported" : `${allowance.consumed.toLocaleString("en-US", { maximumFractionDigits: 20 })} ${allowance.unit} consumed`}
              </li>
            ))}
          </ul>
        </div>
      )}
      <dl className="space-y-1 border-t border-border pt-3 text-xs text-ink-muted">
        <div><dt className="inline">Coverage start: </dt><dd className="inline">{costTimestamp(source.coverageStart)}</dd></div>
        <div><dt className="inline">Coverage end: </dt><dd className="inline">{costTimestamp(source.coverageEnd)}</dd></div>
        <div><dt className="inline">Measured through: </dt><dd className="inline">{costTimestamp(source.measuredThrough)}</dd></div>
      </dl>
      {source.limitations.length > 0 && <div className="space-y-1 text-xs text-ink-muted"><h4 className="font-semibold">Limitations</h4><ul className="list-disc space-y-1 pl-4">{source.limitations.map((limitation, index) => <li key={index} className="break-words">{limitation}</li>)}</ul></div>}
    </article>
  );
}
