// Copyright (c) 2026 Reliant Labs

/**
 * The org's app hosting spend this period: what it has cost so far, where the
 * month is heading, whether new deploys would pause before it ends, and which
 * app and environment the money went to.
 *
 * ── THE PROJECTION IS AN ESTIMATE, AND SAYS SO ─────────────────────────────
 *
 * The month-end figure is spend so far plus the running apps' hourly rate
 * carried to the period end. Anything deployed, scaled or stopped later moves
 * it, so when the server marks it an estimate the UI labels it one and says
 * how it was made, instead of presenting it beside the measured spend as if
 * it were the same kind of number.
 *
 * ── UNKNOWN IS NEVER ZERO ──────────────────────────────────────────────────
 *
 * An unserved RPC and a failed read each get their own sentence. Neither
 * renders a $0.00 or an empty table: "you spent nothing" is the reassuring
 * direction to be wrong in while real usage accrues.
 */

import { useMemo } from "react";
import { Code, ConnectError } from "@connectrpc/connect";

import Button from "@/components/forge-ui/button";
import DataTable from "@/components/forge-ui/data_table";
import ProgressBar from "@/components/forge-ui/progress_bar";
import { formatCents } from "./usageModel";
import { at, day, gb } from "./observabilityBillingModel";
import {
  isUnimplemented,
  useOrgHostingUsageSummary,
  type HostingAppUsage,
  type OrgHostingUsageSummary,
} from "./hostingBillingQueries";

export interface HostingUsagePanelProps {
  orgId: string;
}

export function HostingUsagePanel({ orgId }: HostingUsagePanelProps) {
  const summary = useOrgHostingUsageSummary(orgId);
  // Dollars need billing:read. A member without it sees the per-resource
  // quantities further down the tab, not an error about money.
  if (summary.error instanceof ConnectError && summary.error.code === Code.PermissionDenied) return null;
  return (
    <HostingUsageView
      summary={summary.data}
      loading={summary.isLoading}
      error={summary.error}
      onRetry={() => void summary.refetch()}
    />
  );
}

export interface HostingUsageViewProps {
  summary?: OrgHostingUsageSummary;
  loading?: boolean;
  error?: unknown;
  onRetry?: () => void;
}

/** A sub-cent hourly rate still costs money: never round it to "$0.00". */
function hourlyRate(cents: number): string {
  if (cents > 0 && cents < 1) return "<$0.01";
  return formatCents(cents);
}

export function HostingUsageView({ summary, loading, error, onRetry }: HostingUsageViewProps) {
  const rows = useMemo(
    () =>
      [...(summary?.apps ?? [])].sort(
        (a, b) =>
          (a.deploymentName || a.deploymentId).localeCompare(b.deploymentName || b.deploymentId) ||
          (a.environmentName || a.environmentId).localeCompare(b.environmentName || b.environmentId),
      ),
    [summary?.apps],
  );

  const heading = <h3 className="text-section text-foreground">App hosting this month</h3>;

  if (loading) {
    return (
      <section className="space-y-3" data-testid="hosting-usage-panel">
        {heading}
        <p role="status" className="text-ui text-muted-foreground">
          Loading hosting usage…
        </p>
      </section>
    );
  }

  if (isUnimplemented(error)) {
    return (
      <section className="space-y-3" data-testid="hosting-usage-panel">
        {heading}
        <p role="status" className="text-ui text-muted-foreground">
          Usage by app isn&apos;t available on this server yet.
        </p>
      </section>
    );
  }

  if (error || !summary) {
    return (
      <section className="space-y-3" data-testid="hosting-usage-panel">
        {heading}
        <div role="alert" className="space-y-3">
          <p className="text-ui text-foreground">
            Could not load hosting usage — this is a problem reading it, not zero usage.
          </p>
          {onRetry && (
            <Button size="sm" variant="outline" onClick={onRetry}>
              Try again
            </Button>
          )}
        </div>
      </section>
    );
  }

  const spent = Number(summary.spentCents);
  const budget = Number(summary.budgetCents);
  const projected = Number(summary.projectedMonthEndCents);
  const periodEnd = day(summary.periodEnd);

  return (
    <section className="space-y-5" data-testid="hosting-usage-panel">
      {heading}

      <div className="grid gap-4 sm:grid-cols-2">
        <div>
          <p className="text-meta text-muted-foreground">Spent so far this period</p>
          <p className="text-figure tabular-nums text-foreground" data-testid="hosting-usage-spent">
            {formatCents(spent)}
          </p>
          {budget > 0 && (
            <p className="text-ui text-muted-foreground">
              of a <span className="tabular-nums text-foreground">{formatCents(budget)}</span> monthly budget
            </p>
          )}
        </div>
        <div data-testid="hosting-usage-projection">
          <p className="flex items-center gap-2 text-meta text-muted-foreground">
            Projected by {periodEnd}
            {summary.projectionIsEstimate && (
              <span className="rounded-md border border-border/60 px-1.5 text-meta text-muted-foreground">Estimate</span>
            )}
          </p>
          <p className="text-2xl font-semibold tabular-nums text-foreground">{formatCents(projected)}</p>
          {summary.projectionIsEstimate && (
            <p className="text-meta text-muted-foreground">
              Spend so far plus what your running apps cost per hour (≈{hourlyRate(summary.runningRateCentsPerHour)}/hour), until month end.
            </p>
          )}
        </div>
      </div>

      {budget > 0 && (
        <div className="forge-ui">
          <ProgressBar value={spent} max={budget} variant="default" ariaLabel="Hosting spend against the monthly budget" />
        </div>
      )}

      <div className="space-y-1">
        <BudgetStatus summary={summary} periodEnd={periodEnd} budget={budget} />
        {!summary.billingActive && (
          <p className="text-ui text-muted-foreground" data-testid="hosting-usage-billing-inactive">
            Billing isn&apos;t set up for this organization, so hosting beyond the free tier needs billing before it can deploy.
          </p>
        )}
        {summary.measuredThrough && <p className="text-meta text-muted-foreground">Measured through {at(summary.measuredThrough)}</p>}
      </div>

      {rows.length === 0 ? (
        <p className="text-ui text-muted-foreground">No hosted apps this period.</p>
      ) : (
        <div className="forge-ui">
          <DataTable<{ id: string; app: HostingAppUsage }>
            ariaLabel="Hosting usage by app and environment"
            columns={[
              { key: "app", header: "App", render: (_, { app }) => app.deploymentName || app.deploymentId },
              { key: "env", header: "Environment", render: (_, { app }) => app.environmentName || app.environmentId },
              { key: "spent", header: "Spent", numeric: true, render: (_, { app }) => formatCents(Number(app.spentCents)) },
              {
                key: "projected",
                header: summary.projectionIsEstimate ? "Projected (estimate)" : "Projected",
                numeric: true,
                render: (_, { app }) => formatCents(Number(app.projectedCents)),
              },
              { key: "storage", header: "Static storage", numeric: true, render: (_, { app }) => (app.staticStorageMeasured === false ? "Not measured yet" : gb(app.staticStorageBytes)) },
              { key: "transfer", header: "Static transfer", numeric: true, render: (_, { app }) => gb(app.staticTransferBytes) },
            ]}
            data={rows.map((app) => ({ id: `${app.deploymentId}/${app.environmentId}`, app }))}
            getRowKey={(row) => row.id}
          />
        </div>
      )}
    </section>
  );
}

function BudgetStatus({ summary, periodEnd, budget }: { summary: OrgHostingUsageSummary; periodEnd: string; budget: number }) {
  if (summary.budgetReached) {
    return (
      <p className="flex items-center gap-2 text-ui text-foreground" data-testid="hosting-usage-budget-status">
        <span aria-hidden className="h-2 w-2 shrink-0 rounded-full bg-destructive" />
        Budget reached — new deploys are paused until {periodEnd} or you raise the budget.
      </p>
    );
  }
  if (summary.blockedAt) {
    return (
      <p className="flex items-center gap-2 text-ui text-foreground" data-testid="hosting-usage-budget-status">
        <span aria-hidden className="h-2 w-2 shrink-0 rounded-full bg-warning" />
        At this rate new deploys would pause around {day(summary.blockedAt)}.
      </p>
    );
  }
  if (budget > 0) {
    return (
      <p className="text-ui text-muted-foreground" data-testid="hosting-usage-budget-status">
        Your budget covers the projected month.
      </p>
    );
  }
  return null;
}
