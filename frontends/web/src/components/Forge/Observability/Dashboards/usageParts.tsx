// Copyright (c) 2026 Reliant Labs

/**
 * The pieces the organization usage, billing and LLM usage views share: a
 * panel, a completeness badge, a basis label, an amount that knows the
 * difference between $0 and "not measured", a chart that says when it has
 * nothing to draw, and the permission and unavailable states.
 */

import type { ReactNode } from "react";
import { Code, ConnectError } from "@connectrpc/connect";

import Badge from "@/components/forge-ui/badge";
import Card, { CardHeader, CardInset } from "@/components/forge-ui/card";
import Tag from "@/components/forge-ui/tag";

import { formatCount } from "../format";
import { UPlotChart } from "../charts/UPlotChart";
import { purposeLabel, totalTokens, type LLMBucket } from "./llmUsageModel";
import { BUTTON_SECONDARY, Segmented } from "./parts";
import type { OrganizationUsage } from "@/services/controlPlane/observability/organizationUsageApi";

import {
  basisLabel,
  categoryAmount,
  categoryLabel,
  completenessLabel,
  completenessOf,
  dimensionLabel,
  formatUsd,
  PROJECTION_MIN_HOURS,
  projectedMonthEnd,
  sourceLabel,
  USAGE_RANGES,
  type CategoryAmount,
  type UsageRangeId,
  type UsageSeries,
} from "./organizationUsageModel";

export function isPermissionDenied(error: unknown): boolean {
  return error instanceof ConnectError && error.code === Code.PermissionDenied;
}

const COMPLETENESS_VARIANT = { complete: "success", partial: "warning", unavailable: "error", unknown: "neutral" } as const;

export function CompletenessBadge({ value, label, limitations = [] }: { value: string | undefined; label?: string; limitations?: readonly string[] }) {
  const text = completenessLabel(value);
  const title = [label ? `${label}: ${text}` : text, ...limitations].join(". ");
  return (
    <span className="shrink-0" title={title} data-testid="completeness-badge" data-completeness={completenessOf(value)}>
      <Badge size="sm" variant={COMPLETENESS_VARIANT[completenessOf(value)]} label={text} />
    </span>
  );
}

/** "Settled charges" or "Estimate at list price": every dollar figure says which. */
export function BasisTag({ basis }: { basis: string }) {
  return <Tag title={basis === "list_equivalent" ? "List price before included allowances; not what you are billed" : undefined}>{basisLabel(basis)}</Tag>;
}

/** One category's dollar figure, or the honest reason there is none. */
export function AmountText({ amount, className }: { amount: CategoryAmount; className?: string }) {
  switch (amount.kind) {
    case "amount":
      return (
        <span className={className}>
          <Money nanos={amount.nanos} unpricedDimensions={amount.unpricedDimensions} />
        </span>
      );
    case "unpriced":
      return <span className={className} title="Usage is measured but carries no price">n/a</span>;
    case "no-usage":
      return <span className={className}>No usage</span>;
    default:
      return <span className={className}>Unavailable</span>;
  }
}

export function amountCaption(amount: CategoryAmount): string {
  switch (amount.kind) {
    case "amount":
      return amount.unpricedDimensions.length > 0 ? `${basisLabel(amount.basis)} · lower bound, excludes ${amount.unpricedDimensions.map(dimensionLabel).join(", ")}` : basisLabel(amount.basis);
    case "unpriced":
      return "Measured, not priced";
    case "no-usage":
      return "None recorded in range";
    default:
      return "Not measured; not zero";
  }
}

export function UsagePanel({
  title,
  description,
  actions,
  children,
  className,
  testId,
}: {
  title: ReactNode;
  description?: ReactNode;
  actions?: ReactNode;
  children: ReactNode;
  className?: string;
  testId?: string;
}) {
  return (
    <section className={className} data-testid={testId} aria-label={typeof title === "string" ? title : undefined}>
      <Card className="h-full min-w-0">
        <CardHeader title={title} description={description} actions={actions} />
        {children}
      </Card>
    </section>
  );
}

/** A number tile inside a panel: caption, figure, footnote. */
export function StatTile({ caption, value, footnote, badge, limitations, testId }: { caption: string; value: ReactNode; footnote?: ReactNode; badge?: ReactNode; limitations?: readonly string[]; testId?: string }) {
  return (
    <CardInset className="min-w-0" data-testid={testId} aria-label={caption} role="group">
      <div className="flex items-start justify-between gap-2">
        <div className="min-w-0 text-xs text-ink-muted">{caption}</div>
        {badge}
      </div>
      <div className="mt-1 break-words text-xl font-semibold tabular-nums text-ink">{value}</div>
      {footnote ? <div className="mt-1 text-xs text-ink-muted">{footnote}</div> : null}
      {limitations && limitations.length > 0 ? (
        <ul className="mt-1 list-disc space-y-0.5 pl-4 text-xs text-ink-muted" data-testid="tile-limitations">
          {limitations.map((l) => (
            <li key={l} className="break-words">
              {l}
            </li>
          ))}
        </ul>
      ) : null}
    </CardInset>
  );
}

/** A time-series panel body: the chart, or a sentence saying why there is none. */
export function UsageChart({
  label,
  bucketsMs,
  series,
  unit,
  stacked,
  empty,
}: {
  label: string;
  bucketsMs: number[];
  series: UsageSeries[];
  unit?: "count" | "bytes";
  stacked?: boolean;
  empty: string;
}) {
  const drawable = series.filter((s) => s.values.some((v) => v !== null));
  if (bucketsMs.length === 0 || drawable.length === 0) return <p className="text-sm text-ink-muted">{empty}</p>;
  return (
    <UPlotChart
      label={label}
      timestamps={bucketsMs.map((ms) => Math.round(ms / 1000))}
      series={drawable.map((s) => ({ label: s.label, values: s.values }))}
      unit={unit}
      height={200}
      area={stacked ? "stacked" : "fill"}
    />
  );
}

export function RangePicker({ value, onChange }: { value: UsageRangeId; onChange: (range: UsageRangeId) => void }) {
  return <Segmented label="Range" value={value} options={USAGE_RANGES} onChange={onChange} testId="usage-range" />;
}

/** The view's title block: heading, scope line, completeness, and the controls. */
export function UsageViewHeader({
  id,
  title,
  scope,
  completeness,
  controls,
}: {
  id: string;
  title: string;
  scope: ReactNode;
  completeness?: string;
  controls?: ReactNode;
}) {
  return (
    <header className="flex flex-wrap items-start justify-between gap-4">
      <div className="min-w-0 space-y-1">
        <div className="flex flex-wrap items-center gap-2">
          <h1 id={id} className="text-xl font-semibold tracking-tight text-ink">
            {title}
          </h1>
          {completeness !== undefined && <CompletenessBadge value={completeness} label="Overall coverage" />}
        </div>
        <p className="break-words text-sm text-ink-muted">{scope}</p>
      </div>
      {controls ? <div className="flex flex-wrap items-center gap-2">{controls}</div> : null}
    </header>
  );
}

export function RefreshButton({ onClick, busy, disabled }: { onClick: () => void; busy: boolean; disabled?: boolean }) {
  return (
    <button type="button" className={BUTTON_SECONDARY} onClick={onClick} disabled={disabled || busy}>
      {busy ? "Refreshing…" : "Refresh"}
    </button>
  );
}

export function BillingPermissionRequired({ what }: { what: string }) {
  return (
    <Card role="alert" data-testid="billing-permission-required">
      <h2 className="text-sm font-medium text-ink">Billing read permission required</h2>
      <p className="mt-1 text-sm text-ink-muted">
        Your role in this organization does not include billing:read, so {what} cannot be shown. Ask an organization owner for billing:read. Billing management is not required.
      </p>
    </Card>
  );
}

export function UsageUnavailable({ what, onRetry }: { what: string; onRetry?: () => void }) {
  return (
    <Card role="alert" data-testid="usage-unavailable">
      <h2 className="text-sm font-medium text-ink">{what} could not be loaded</h2>
      <p className="mt-1 text-sm text-ink-muted">The billing service did not answer. Amounts are unknown, not zero.</p>
      {onRetry && (
        <button type="button" className={`${BUTTON_SECONDARY} mt-3`} onClick={onRetry}>
          Try again
        </button>
      )}
    </Card>
  );
}

/**
 * A panel body over one query: loading, unavailable (unknown, not zero), a
 * response for another organization (refused), or the content.
 */
export function Loaded<T extends { orgId: string }>({
  query,
  orgId,
  what,
  children,
}: {
  query: { isPending: boolean; isError: boolean; data: T | undefined };
  orgId: string;
  what: string;
  children: (data: T) => ReactNode;
}) {
  if (query.isPending) {
    return (
      <p role="status" className="text-sm text-ink-muted">
        Loading {what}…
      </p>
    );
  }
  if (query.isError || !query.data) {
    return (
      <p role="alert" className="text-sm text-ink-muted" data-testid="panel-unavailable">
        Could not load {what}. It is unknown, not zero.
      </p>
    );
  }
  if (query.data.orgId !== orgId) {
    return (
      <p role="alert" className="text-sm text-ink-muted">
        The answer was for a different organization, so it is not shown. Refresh to try again.
      </p>
    );
  }
  return <>{children(query.data)}</>;
}

/** What each source covered, and the gaps the server stated. */
export function CoverageNotes({ entries }: { entries: { source: string; completeness: string; limitations: string[] }[] }) {
  if (entries.length === 0) return null;
  return (
    <UsagePanel title="Coverage" description="What each billing source measured in this range. A gap means unknown, never zero." testId="usage-coverage">
      <ul className="space-y-2 text-sm">
        {entries.map((entry) => (
          <li key={entry.source} className="min-w-0">
            <div className="flex flex-wrap items-center gap-2">
              <span className="text-ink">{sourceLabel(entry.source)}</span>
              <CompletenessBadge value={entry.completeness} label={sourceLabel(entry.source)} />
            </div>
            {entry.limitations.length > 0 && (
              <ul className="mt-1 list-disc space-y-0.5 pl-5 text-xs text-ink-muted">
                {entry.limitations.map((limitation) => (
                  <li key={limitation} className="break-words">
                    {limitation}
                  </li>
                ))}
              </ul>
            )}
          </li>
        ))}
      </ul>
    </UsagePanel>
  );
}

/** A panel whose data lives elsewhere today: says where, with a link. */
export function ElsewhereNote({ children, href, linkLabel }: { children: ReactNode; href: string; linkLabel: string }) {
  return (
    <p className="text-sm text-ink-muted">
      {children}{" "}
      <a href={href} className="font-medium text-ink underline decoration-ink-muted/40 underline-offset-2 hover:decoration-ink">
        {linkLabel}
      </a>
    </p>
  );
}

/** Hosted categories: a rate can be absent because none of the category's usage is priced. */
const HOSTED_CATEGORIES = ["hosted_apps", "databases", "static_hosting", "hosted_other"] as const;

/**
 * A dollar figure that is a lower bound: "≥ $4.00", with a title naming the
 * dimensions it leaves out because the plan has no rate for them. Not a bound:
 * the plain figure.
 */
export function Money({ nanos, unpricedDimensions = [], bounded }: { nanos: bigint | undefined; unpricedDimensions?: readonly string[]; bounded?: boolean }) {
  if (nanos === undefined || (!bounded && unpricedDimensions.length === 0)) return <>{formatUsd(nanos)}</>;
  const left = unpricedDimensions.map(dimensionLabel).join(", ");
  return (
    <span title={left ? `Lower bound: excludes ${left}, which the plan has no rate for` : "Lower bound: some usage is not priced"} data-testid="lower-bound">
      ≥ {formatUsd(nanos)}
    </span>
  );
}

/**
 * Trailing spend per category: the last 24 hours, the 7-day daily average,
 * and a month-end projection that appears only after 24 measured hours of
 * that category. Each row says its basis.
 *
 * Two kinds of "no figure" are kept apart. A hosted category with usage but no
 * rate at all is "Not priced" (no dimension of it has a plan rate), never $0.
 * A rate with unpriced dimensions is a lower bound: it counts only the priced
 * dimensions, so it reads "≥" and lists what it excludes.
 */
export function SpendRatePanel({ usage, monthToDate, nowMs }: { usage: OrganizationUsage; monthToDate: OrganizationUsage | undefined; nowMs: number }) {
  const rows: ReactNode[][] = usage.spendRates.map((rate) => {
    const mtd = monthToDate ? categoryAmount(monthToDate, rate.category) : undefined;
    const projected = projectedMonthEnd(mtd?.kind === "amount" ? mtd.nanos : undefined, rate, nowMs);
    const bounded = rate.lowerBound || rate.unpricedDimensions.length > 0;
    const money = (nanos: bigint | undefined) => <Money nanos={nanos} unpricedDimensions={rate.unpricedDimensions} bounded={bounded} />;
    return [
      <span key="c" className="inline-flex flex-wrap items-center gap-2">
        {categoryLabel(rate.category)} <BasisTag basis={rate.basis} />
        {bounded && (
          <Tag tone="warning" title="Counts only the priced dimensions">
            Lower bound{rate.unpricedDimensions.length > 0 ? ` · excludes ${rate.unpricedDimensions.map(dimensionLabel).join(", ")}` : ""}
          </Tag>
        )}
      </span>,
      money(rate.last24hUsdNanos),
      money(rate.averageDaily7dUsdNanos),
      projected === undefined ? (
        <span key="p" title={rate.measuredHours < PROJECTION_MIN_HOURS ? `Only ${rate.measuredHours} of 24 hours measured` : "No month-to-date figure"}>
          n/a
        </span>
      ) : (
        money(projected)
      ),
    ];
  });
  const rated = new Set(usage.spendRates.map((r) => r.category));
  for (const category of HOSTED_CATEGORIES) {
    if (rated.has(category) || !usage.points.some((p) => p.category === category)) continue;
    rows.push([
      <span key="c" className="inline-flex flex-wrap items-center gap-2">
        {categoryLabel(category)} <Tag title="Usage is measured, but no part of it has a plan rate">Not priced</Tag>
      </span>,
      "n/a",
      "n/a",
      "n/a",
    ]);
  }
  return (
    <UsagePanel
      title="Spend rate"
      description="Trailing rate from durable ledgers. A projection needs at least 24 measured hours of that category. A lower bound leaves out usage the plan does not price. Bases are never added together."
      testId="usage-spend-rate"
    >
      <RowsTable caption="Spend rate by category" head={["Category", "Last 24 h", "7-day avg / day", "Projected month end"]} rows={rows} empty="No category reported a rate." />
    </UsagePanel>
  );
}

/** The caller's calls by purpose, from GetLLMUsage group_by PURPOSE. A purpose with no calls is absent, not a zero row. */
export function PurposeTable({ buckets }: { buckets: readonly LLMBucket[] }) {
  return (
    <RowsTable
      caption="Your calls by purpose"
      head={["Purpose", "Calls", "Errors", "Tokens"]}
      rows={buckets.map((b) => [purposeLabel(b.key), formatCount(b.calls), formatCount(b.errorCalls), formatCount(totalTokens(b))])}
      empty="No model calls in this range."
    />
  );
}

/** A two-column table of label and value rows, flush in its panel. */
export function RowsTable({ caption, head, rows, empty }: { caption: string; head: [string, ...string[]]; rows: ReactNode[][]; empty: string }) {
  if (rows.length === 0) return <p className="text-sm text-ink-muted">{empty}</p>;
  return (
    <div className="-mx-1 overflow-x-auto">
      <table className="w-full min-w-0 text-sm">
        <caption className="sr-only">{caption}</caption>
        <thead>
          <tr className="text-left text-xs text-ink-muted">
            {head.map((h, i) => (
              <th key={h} scope="col" className={i === 0 ? "px-1 pb-2 font-normal" : "px-1 pb-2 text-right font-normal"}>
                {h}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {rows.map((cells, r) => (
            <tr key={r} className="border-t border-border/60">
              {cells.map((cell, i) => (
                <td key={i} className={i === 0 ? "min-w-0 break-words px-1 py-1.5 text-ink" : "whitespace-nowrap px-1 py-1.5 text-right tabular-nums text-ink"}>
                  {cell}
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
