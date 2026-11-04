// Copyright (c) 2026 Reliant Labs

/**
 * Dashboard 1, "Spend and usage" (`org-usage`, DASHBOARD_VIEW_KIND_ORGANIZATION_USAGE).
 *
 * A typed built-in view over `BillingService.ListOrganizationUsage` (history,
 * from durable ledgers) and `GetOrganizationCostSummary` (allowances and
 * credits). Scoped to the organization and gated by billing:read, which the
 * server re-checks on every call. Independent of environment, service and the
 * telemetry range: it has its own range, in UTC buckets.
 *
 * Panels: period-to-date by category, spend over time (one chart per basis),
 * allowances, spend by environment, credit, caps, top cost drivers, the spend
 * rate and coverage. Every figure says whether it is a settled charge or a
 * list-price estimate, and an unmeasured one reads n/a, never $0.
 */

import { useMemo, useState } from "react";

import ProgressBar from "@/components/forge-ui/progress_bar";
import { useOwnManagedPurposeUsage } from "@/hooks/llm-usage-queries";
import { useOrganizationCostSummary, useOrganizationUsage } from "@/hooks/organization-usage-queries";
import type { OrganizationCostSummary } from "@/services/controlPlane/observability/organizationCostsApi";
import type { OrganizationUsage, OrganizationUsageRequest } from "@/services/controlPlane/observability/organizationUsageApi";

import { costSourceLabel } from "./organizationCostModel";
import {
  allowanceRows,
  bucketStarts,
  categoryAmount,
  categoryLabel,
  completenessOf,
  coverageOf,
  formatQuantity,
  formatUsd,
  groupTotals,
  limitationsOf,
  monthToDateRequest,
  promotionalCredits,
  quantitySum,
  quantityUnit,
  spendSeries,
  USAGE_CATEGORIES,
  usageWindow,
  type UsageRangeId,
} from "./organizationUsageModel";
import {
  AmountText,
  amountCaption,
  BasisTag,
  BillingPermissionRequired,
  CompletenessBadge,
  CoverageNotes,
  ElsewhereNote,
  isPermissionDenied,
  Loaded,
  Money,
  PurposeTable,
  RangePicker,
  RefreshButton,
  RowsTable,
  SpendRatePanel,
  StatTile,
  UsageChart,
  UsagePanel,
  UsageViewHeader,
} from "./usageParts";

export interface OrganizationUsageViewProps {
  orgId: string;
  /** The clock, for tests and previews. */
  nowMs?: number;
}

const BILLING_SETTINGS = "/settings/billing";
const HOSTED = ["hosted_apps", "databases", "static_hosting", "hosted_other", "observability"] as const;

export function OrganizationUsageView({ orgId, nowMs: fixedNow }: OrganizationUsageViewProps) {
  const [range, setRange] = useState<UsageRangeId>("30d");
  const [clock, setClock] = useState(() => fixedNow ?? Date.now());
  const nowMs = fixedNow ?? clock;
  const span = useMemo(() => usageWindow(range, nowMs), [range, nowMs]);
  const request = (groupBy: OrganizationUsageRequest["groupBy"]): OrganizationUsageRequest | null => (orgId ? { orgId, ...span, groupBy } : null);
  const mtd = useOrganizationUsage(orgId ? monthToDateRequest(orgId, nowMs) : null);
  const byCategory = useOrganizationUsage(request("category"));
  const byEnv = useOrganizationUsage(request("env"));
  const byModel = useOrganizationUsage(request("model"));
  const cost = useOrganizationCostSummary(orgId || null);
  const purposes = useOwnManagedPurposeUsage({ startMs: span.startMs, endMs: span.endMs });
  const queries = [mtd, byCategory, byEnv, byModel, cost];
  // A live refusal hides everything, including anything cached before it.
  const denied = queries.some((q) => isPermissionDenied(q.error));
  const fetching = queries.some((q) => q.isFetching) || purposes.fetching;
  const refresh = () => {
    if (fixedNow === undefined) setClock(Date.now());
    for (const q of queries) void q.refetch();
    purposes.refresh();
  };

  return (
    <section className="min-w-0 space-y-4 p-4 text-ink sm:p-6" aria-labelledby="organization-usage-title" data-testid="organization-usage-view">
      <UsageViewHeader
        id="organization-usage-title"
        title="Spend and usage"
        scope={`Organization ${orgId || "not selected"} · All projects and environments · UTC`}
        completeness={!denied && byCategory.data?.orgId === orgId ? byCategory.data.completeness : undefined}
        controls={
          <>
            <RangePicker value={range} onChange={setRange} />
            <RefreshButton onClick={refresh} busy={fetching} disabled={!orgId || denied} />
          </>
        }
      />
      {!orgId ? (
        <p role="status">Select an organization to view its spend and usage.</p>
      ) : denied ? (
        <BillingPermissionRequired what="this organization's spend and usage" />
      ) : (
        <>
          <p className="text-xs text-ink-muted">
            Settled charges and list-price estimates are separate facts and are never added into one total. n/a means measured without a price, or not measured: never zero.
          </p>
          <UsagePanel title="Period to date" description="This calendar month (UTC), by category." testId="usage-period-to-date">
            <Loaded query={mtd} orgId={orgId} what="month-to-date usage">
              {(usage) => <PeriodTiles usage={usage} />}
            </Loaded>
          </UsagePanel>
          <div className="grid min-w-0 gap-4 lg:grid-cols-2">
            <UsagePanel title="Settled charges over time" description="Managed LLM wallet debits, stacked by category." testId="usage-spend-actual" actions={<BasisTag basis="actual" />}>
              <Loaded query={byCategory} orgId={orgId} what="spend history">
                {(usage) => <SpendChart usage={usage} basis="actual" />}
              </Loaded>
            </UsagePanel>
            <UsagePanel
              title="Estimated spend over time"
              description="Hosting and observability at list price, before included allowances, stacked by category."
              testId="usage-spend-estimate"
              actions={<BasisTag basis="list_equivalent" />}
            >
              <Loaded query={byCategory} orgId={orgId} what="spend history">
                {(usage) => <SpendChart usage={usage} basis="list_equivalent" />}
              </Loaded>
            </UsagePanel>
          </div>
          <div className="grid min-w-0 gap-4 lg:grid-cols-2">
            <UsagePanel title="Allowances" description="Used and included per meter this period, with a straight-line projection." testId="usage-allowances">
              <Loaded query={cost} orgId={orgId} what="allowances">
                {(summary) => <Allowances summary={summary} nowMs={nowMs} />}
              </Loaded>
            </UsagePanel>
            <UsagePanel title="Spend by environment" description="Hosting and observability, list-price estimate over the selected range." testId="usage-by-env" actions={<BasisTag basis="list_equivalent" />}>
              <Loaded query={byEnv} orgId={orgId} what="spend by environment">
                {(usage) => <ByEnvironment usage={usage} />}
              </Loaded>
            </UsagePanel>
          </div>
          <div className="grid min-w-0 gap-4 lg:grid-cols-2">
            <UsagePanel title="Credit" description="Promotional credit granted this period." testId="usage-credit">
              <Loaded query={cost} orgId={orgId} what="credit">
                {(summary) => <Credit summary={summary} />}
              </Loaded>
            </UsagePanel>
            <UsagePanel title="Caps in force" description="Spend caps and how close each one is." testId="usage-caps">
              <ElsewhereNote href={BILLING_SETTINGS} linkLabel="Open Usage & limits">
                Caps are set and tracked in Usage & limits, which shows the distance to each one.
              </ElsewhereNote>
            </UsagePanel>
          </div>
          <UsagePanel
            title="Your Reliant AI calls by purpose"
            description="Where your own Reliant AI calls went over the selected range: chat turns, context compaction, chat titles and automation drafts. Your calls only, not the organization's, and counts rather than dollars."
            testId="usage-llm-purpose"
          >
            <ManagedPurposes purposes={purposes} />
          </UsagePanel>
          <UsagePanel title="Top cost drivers" description="The largest models and environments over the selected range. Each list keeps its own basis." testId="usage-top-drivers">
            <div className="grid min-w-0 gap-4 lg:grid-cols-2">
              <Loaded query={byModel} orgId={orgId} what="spend by model">
                {(usage) => <TopDrivers usage={usage} categories={["managed_llm"]} title="Models (managed LLM)" basis="actual" empty="No settled managed-LLM charges in range." />}
              </Loaded>
              <Loaded query={byEnv} orgId={orgId} what="spend by environment">
                {(usage) => <TopDrivers usage={usage} categories={HOSTED} title="Environments (hosting and observability)" basis="list_equivalent" empty="No priced hosting usage in range." />}
              </Loaded>
            </div>
            <p className="mt-3 text-xs text-ink-muted">A breakdown by individual deployment or dev box is not available from the billing history yet.</p>
          </UsagePanel>
          <Loaded query={byCategory} orgId={orgId} what="spend rate">
            {(usage) => <SpendRatePanel usage={usage} monthToDate={mtd.data?.orgId === orgId ? mtd.data : undefined} nowMs={nowMs} />}
          </Loaded>
          {byCategory.data?.orgId === orgId && <CoverageNotes entries={limitationsOf(byCategory.data)} />}
        </>
      )}
    </section>
  );
}

function ManagedPurposes({ purposes }: { purposes: ReturnType<typeof useOwnManagedPurposeUsage> }) {
  if (purposes.status === "ready") {
    return (
      <div className="space-y-2">
        <PurposeTable buckets={purposes.buckets} />
        <p className="flex flex-wrap items-center gap-2 text-xs text-ink-muted">
          Your usage <CompletenessBadge value={purposes.completeness} label="Your usage" />
          {purposes.note && <span className="min-w-0 break-words">{purposes.note}</span>}
        </p>
      </div>
    );
  }
  const text =
    purposes.status === "loading"
      ? "Loading your calls by purpose…"
      : purposes.status === "not-deployed"
        ? "Not available from this server yet. Nothing is estimated in its place."
        : "Could not load your calls by purpose. It is unknown, not zero.";
  return (
    <p role={purposes.status === "loading" ? "status" : undefined} className="text-sm text-ink-muted" data-testid="usage-llm-purpose-state">
      {text}
    </p>
  );
}

function PeriodTiles({ usage }: { usage: OrganizationUsage }) {
  return (
    <div className="grid min-w-0 grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-4">
      {USAGE_CATEGORIES.map((category) => {
        const amount = categoryAmount(usage, category);
        const points = usage.points.filter((p) => p.category === category);
        const running = category === "machines" ? quantitySum(points, "running_seconds") : undefined;
        return (
          <StatTile
            key={category}
            testId={`usage-tile-${category}`}
            caption={categoryLabel(category)}
            badge={<CompletenessBadge value={coverageOf(usage, category)?.completeness} label={categoryLabel(category)} limitations={coverageOf(usage, category)?.limitations} />}
            value={<AmountText amount={amount} />}
            limitations={completenessOf(coverageOf(usage, category)?.completeness) === "partial" ? coverageOf(usage, category)?.limitations : undefined}
            footnote={
              running !== undefined ? `${amountCaption(amount)} · ${formatQuantity(running, quantityUnit(points, "running_seconds"))} running` : amountCaption(amount)
            }
          />
        );
      })}
    </div>
  );
}

function SpendChart({ usage, basis }: { usage: OrganizationUsage; basis: "actual" | "list_equivalent" }) {
  const buckets = bucketStarts(usage);
  const series = spendSeries(usage, basis);
  return (
    <UsageChart
      label={basis === "actual" ? "Settled charges in US dollars per bucket" : "Estimated spend at list price in US dollars per bucket"}
      bucketsMs={buckets}
      series={series}
      stacked
      empty={basis === "actual" ? "No category carried settled charges in this range." : "No category carried a list-price figure in this range."}
    />
  );
}

function Allowances({ summary, nowMs }: { summary: OrganizationCostSummary; nowMs: number }) {
  const rows = allowanceRows(summary, nowMs);
  if (rows.length === 0) return <p className="text-sm text-ink-muted">No included allowances were reported for this organization.</p>;
  return (
    <ul className="space-y-3">
      {rows.map((row, index) => {
        const name = `${costSourceLabel(row.source)} · ${row.id || "allowance"}`;
        const fmt = (n: number) => n.toLocaleString("en-US", { maximumFractionDigits: 1 });
        return (
          <li key={`${row.source}-${row.id}-${index}`} className="min-w-0 space-y-1" data-testid="usage-allowance">
            <div className="flex flex-wrap items-baseline justify-between gap-2 text-sm">
              <span className="text-ink">{name}</span>
              <span className="tabular-nums text-ink-muted">
                {row.consumed === undefined ? "Consumption not reported" : `${fmt(row.consumed)} of ${fmt(row.included)} ${row.unit}`}
              </span>
            </div>
            {row.consumed !== undefined && row.included > 0 && <ProgressBar value={row.consumed} max={row.included} ariaLabel={`${name} used`} size="sm" />}
            <p className="text-xs text-ink-muted">{row.projected === undefined ? "Projection: n/a until a day of the period has passed" : `Projected by period end: ${fmt(row.projected)} ${row.unit}`}</p>
          </li>
        );
      })}
    </ul>
  );
}

function ByEnvironment({ usage }: { usage: OrganizationUsage }) {
  const totals = groupTotals(usage, HOSTED).filter((t) => t.basis === "list_equivalent");
  const byEnv = new Map<string, { label: string; nanos: bigint; categories: Set<string>; unpriced: Set<string> }>();
  for (const t of totals) {
    const cur = byEnv.get(t.key) ?? { label: t.label, nanos: 0n, categories: new Set<string>(), unpriced: new Set<string>() };
    cur.nanos += t.nanos;
    t.unpricedDimensions.forEach((d) => cur.unpriced.add(d));
    cur.categories.add(categoryLabel(t.category));
    byEnv.set(t.key, cur);
  }
  const rows = [...byEnv.values()].sort((a, b) => (b.nanos > a.nanos ? 1 : b.nanos < a.nanos ? -1 : 0)).map((env) => [
    <span key="e">
      {env.label}
      <span className="block text-xs text-ink-muted">{[...env.categories].join(", ")}</span>
    </span>,
    <Money key="m" nanos={env.nanos} unpricedDimensions={[...env.unpriced]} />,
  ]);
  return (
    <>
      <RowsTable caption="Estimated spend by environment" head={["Environment", "Estimate"]} rows={rows} empty="No priced hosting or observability usage in range." />
      <p className="mt-2 text-xs text-ink-muted">Observability is not attributed to an environment yet, so it appears as Unattributed.</p>
    </>
  );
}

function Credit({ summary }: { summary: OrganizationCostSummary }) {
  const credits = promotionalCredits(summary);
  return (
    <div className="space-y-3">
      <RowsTable
        caption="Promotional credit by source"
        head={["Source", "Granted"]}
        rows={credits.map((c) => [costSourceLabel(c.source), formatUsd(c.nanos)])}
        empty="No promotional credit was reported this period."
      />
      <ElsewhereNote href={BILLING_SETTINGS} linkLabel="Open billing">
        Wallet balance and auto-recharge are managed in billing settings.
      </ElsewhereNote>
    </div>
  );
}

function TopDrivers({
  usage,
  categories,
  title,
  basis,
  empty,
}: {
  usage: OrganizationUsage;
  categories: readonly string[];
  title: string;
  basis: "actual" | "list_equivalent";
  empty: string;
}) {
  const totals = groupTotals(usage, categories).filter((t) => t.basis === basis).slice(0, 10);
  return (
    <div className="min-w-0 space-y-2" data-testid={`usage-drivers-${basis}`}>
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h3 className="text-xs text-ink-muted">{title}</h3>
        <BasisTag basis={basis} />
      </div>
      <RowsTable
        caption={title}
        head={[basis === "actual" ? "Model" : "Environment", basis === "actual" ? "Charged" : "Estimate"]}
        rows={totals.map((t) => [
          <span key="l">
            {t.label}
            {categories.length > 1 && <span className="block text-xs text-ink-muted">{categoryLabel(t.category)}</span>}
          </span>,
          <Money key="m" nanos={t.nanos} unpricedDimensions={t.unpricedDimensions} />,
        ])}
        empty={empty}
      />
    </div>
  );
}
