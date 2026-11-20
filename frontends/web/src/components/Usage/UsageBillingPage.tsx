// Copyright (c) 2026 Reliant Labs

/**
 * USAGE & BILLING (U6c, prototype): /usage and /o/<org>/usage, the nav
 * footer's one org-wide page.
 *
 * One headline and one chart: the organization's estimated spend over the
 * period the URL names (`?range=7d|30d|90d|12mo`, default 30 days), from
 * BillingService.ListOrganizationUsage. Below it:
 *
 *   - By project: each hosted project's month so far and forecast, from
 *     GetProjectCost (hosted compute, database storage, CDN), linking to the
 *     project's own Usage page.
 *   - AI and agent sandboxes: billed to the organization, not to a project
 *     (the ledger has no project), so they are org-level figures here.
 *   - Billing: plan, credits (the wallet), recent invoices and the Stripe
 *     portal for payment methods. Changing any of them is Settings › Billing.
 *
 * Honest figures only: an absent amount reads "n/a" or "Unavailable", never
 * $0, and settled charges are never added to list-price estimates.
 */

import { useMemo, useState, type ReactNode } from "react";
import { useNavigate, useSearch } from "@tanstack/react-router";
import { useQueries } from "@tanstack/react-query";
import { CreditCard, FolderOpen, Receipt, Wallet } from "lucide-react";

import Badge from "@/components/forge-ui/badge";
import Button from "@/components/forge-ui/button";
import Card from "@/components/forge-ui/card";
import EmptyState from "@/components/forge-ui/empty_state";
import Link from "@/components/forge-ui/link";
import PageHeader from "@/components/forge-ui/page_header";
import { consolePath } from "@/components/Forge/Console/paths";
import {
  bucketStarts,
  categoryAmount,
  formatQuantity,
  formatUsd,
  quantitySum,
  quantityUnit,
  spendSeries,
  USAGE_CATEGORIES,
  USAGE_RANGES,
  usageWindow,
  type UsageRangeId,
} from "@/components/Forge/Observability/Dashboards/organizationUsageModel";
import {
  AmountText,
  amountCaption,
  BillingPermissionRequired,
  isPermissionDenied,
  Loaded,
  Money,
  RangePicker,
  UsageChart,
} from "@/components/Forge/Observability/Dashboards/usageParts";
import {
  formatBillingError,
  formatCentsAsDollars,
  formatCurrencyFromWalletFields,
  formatTimestampDate,
  isBackendModalError,
  normalizeInvoiceStatus,
  planDisplayName,
} from "@/components/Settings/cloud/billingUtils";
import { SubscriptionStatus } from "@/gen/controlplane/controlplane/v1/shared_pb";
import { useApps } from "@/hooks/apps-queries";
import { organizationUsageKeys, useOrganizationUsage } from "@/hooks/organization-usage-queries";
import {
  useComputeSubscription,
  useCreateBillingPortalSession,
  useCurrentUserInvoices,
  useWalletOverview,
} from "@/hooks/useCloudBillingQueries";
import { projectsPath, sandboxesPath, settingsPath, usageBillingPath, withSearch } from "@/lib/paths";
import { buildCheckoutReturnUrls, openCheckout } from "@/lib/stripeCheckout";
import { getProjectCost } from "@/services/controlPlane/observability/organizationCostsApi";
import { useActiveOrgStore } from "@/store/activeOrgStore";

import { AreaShell } from "../Layout/AreaShell";
import {
  DEFAULT_USAGE_RANGE,
  listEstimateTotal,
  projectAmountText,
  projectCategoryLabel,
  projectCostRow,
  sortProjectRows,
  usageCategoryLabel,
  usageRangeOf,
  type ProjectCostRow,
} from "./usageBillingModel";

const LINK = "no-color text-primary-ink hover:underline";

/** The route's page (routes.tsx): the view in the app's area chrome. */
export function UsageBillingPage() {
  return (
    <AreaShell areaPath={usageBillingPath()} areaLabel="Usage & billing" areaNoun="usage and billing">
      <UsageBillingView />
    </AreaShell>
  );
}

/** The view alone; `nowMs` pins the clock for tests and previews. */
export function UsageBillingView({ nowMs: fixedNow }: { nowMs?: number } = {}) {
  const orgId = useActiveOrgStore((s) => s.effectiveId) ?? "";
  const navigate = useNavigate();
  const search = useSearch({ strict: false }) as Record<string, unknown>;
  const range = usageRangeOf(search.range);
  const setRange = (next: UsageRangeId) =>
    void navigate({
      to: ".",
      search: (prev: Record<string, unknown>) => ({ ...prev, range: next === DEFAULT_USAGE_RANGE ? undefined : next }),
    } as never);

  const [mountedAt] = useState(() => Date.now());
  const nowMs = fixedNow ?? mountedAt;
  const span = useMemo(() => usageWindow(range, nowMs), [range, nowMs]);
  const usage = useOrganizationUsage(orgId ? { orgId, ...span, groupBy: "category" } : null);
  const denied = isPermissionDenied(usage.error);

  return (
    <div className="space-y-6" data-testid="usage-billing-page">
      <PageHeader
        title="Usage & billing"
        meta={<span>What your organization uses and pays for, across every project.</span>}
        controls={<RangePicker value={range} onChange={setRange} />}
        actions={[{ label: "Billing admin in Settings ›", href: settingsPath("billing"), variant: "secondary" }]}
        className="mb-0"
      />
      {!orgId ? (
        <p role="status" className="text-ui text-ink-muted">
          Select an organization to see its usage and billing.
        </p>
      ) : denied ? (
        <BillingPermissionRequired what="this organization's usage" />
      ) : (
        <>
          <Headline query={usage} orgId={orgId} range={range} />
          <ProjectBreakdown orgId={orgId} />
          <OrgLevel query={usage} orgId={orgId} range={range} />
        </>
      )}
      <BillingSummary />
    </div>
  );
}

type UsageQuery = ReturnType<typeof useOrganizationUsage>;

const rangeLabel = (range: UsageRangeId) => `last ${USAGE_RANGES.find((r) => r.value === range)?.label ?? range}`;

/** The one headline number and the one chart: estimated spend over the period. */
function Headline({ query, orgId, range }: { query: UsageQuery; orgId: string; range: UsageRangeId }) {
  return (
    <Card padding="none" className="min-w-0" data-testid="usage-billing-headline">
      <Loaded query={query} orgId={orgId} what="your organization's usage">
        {(usage) => {
          const total = listEstimateTotal(usage);
          const series = spendSeries(usage, "list_equivalent", USAGE_CATEGORIES).map((s) => ({
            ...s,
            label: s.label.replace(/^[^(]+/, `${usageCategoryLabel(s.key)} `).trim(),
          }));
          return (
            <>
              <div className="px-5 pt-4">
                <h2 className="text-ui font-medium text-ink-muted">Estimated spend, {rangeLabel(range)}</h2>
                <div className="mt-0.5 text-figure tabular-nums text-ink" data-testid="usage-billing-total">
                  {total ? <Money nanos={total.nanos} unpricedDimensions={total.unpricedDimensions} /> : "n/a"}
                </div>
                <p className="text-meta text-ink-muted">
                  {total
                    ? "Hosting, databases, CDN and observability at list price, before your plan's allowances. AI is charged separately, below."
                    : "Nothing priced was recorded in this period. That is no usage or not measured, never $0."}
                </p>
              </div>
              <div className="px-3 pb-3 pt-2">
                <UsageChart
                  label="Estimated spend at list price in US dollars, by category"
                  bucketsMs={bucketStarts(usage)}
                  series={series}
                  stacked
                  empty="No category carried a price in this period."
                />
              </div>
            </>
          );
        }}
      </Loaded>
    </Card>
  );
}

/** Each hosted project's month so far (GetProjectCost), linking to its own Usage page. */
function ProjectBreakdown({ orgId }: { orgId: string }) {
  const { apps, loading, availability } = useApps();
  const costs = useQueries({
    queries: apps.map((app) => ({
      queryKey: organizationUsageKeys.projectCost(orgId, app.name),
      queryFn: () => getProjectCost(orgId, app.name),
      enabled: !!orgId,
      staleTime: 60_000,
      retry: false,
    })),
  });
  const rows = sortProjectRows(
    apps.map((app, i) => {
      const cost = costs[i]?.data;
      return cost ? projectCostRow(app.name, cost) : { name: app.name, monthToDate: undefined, forecast: undefined };
    }),
  );
  const stateOf = (name: string) => costs[apps.findIndex((app) => app.name === name)];

  return (
    <section className="space-y-3" aria-labelledby="usage-billing-projects" data-testid="usage-billing-projects">
      <h2 id="usage-billing-projects" className="text-section text-ink">
        By project <span className="ml-2 text-meta font-normal text-ink-muted">hosted compute, database storage and CDN, this month</span>
      </h2>
      {loading ? (
        <div className="h-32 animate-pulse rounded-xl border border-border bg-surface motion-reduce:animate-none" aria-label="Loading projects" />
      ) : apps.length === 0 ? (
        <Card padding="none" data-testid="usage-billing-projects-empty">
          <EmptyState
            icon={<FolderOpen aria-hidden="true" />}
            title={availability === "available" ? "No hosted projects yet" : "No hosted projects to show"}
            description={
              availability === "available"
                ? "When a project deploys, what its hosting, database storage and CDN cost shows up here, one line per project."
                : "This organization's hosted projects could not be listed, so there is nothing to break down."
            }
          />
          <p className="-mt-10 pb-8 text-center text-meta">
            <Link href={projectsPath()} className={LINK}>
              Go to Projects ›
            </Link>
          </p>
        </Card>
      ) : (
        <div className="overflow-x-auto rounded-xl border border-border bg-surface">
          <table className="w-full border-collapse">
            <thead>
              <tr className="border-b border-border">
                {["Project", "This month", "Forecast", "Biggest cost"].map((h, i) => (
                  <th key={h} className={`h-9 px-3 text-left text-meta font-medium text-ink-muted ${i === 1 || i === 2 ? "text-right" : ""}`}>
                    {h}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {rows.map((row) => (
                <ProjectRow key={row.name} row={row} state={stateOf(row.name)} />
              ))}
            </tbody>
          </table>
        </div>
      )}
    </section>
  );
}

function ProjectRow({ row, state }: { row: ProjectCostRow; state: { isPending: boolean; isError: boolean; error: unknown } | undefined }) {
  const pending = state?.isPending;
  const failed = state?.isError;
  const cell = (text: string) => (pending ? <span className="text-ink-muted">…</span> : failed ? <span className="text-ink-muted">Unavailable</span> : text);
  return (
    <tr className="border-b border-border-subtle last:border-b-0" data-testid="usage-billing-project-row">
      <td className="h-11 px-3 text-ui font-medium">
        <Link href={consolePath(row.name, { kind: "usage" })} className="no-color text-ink hover:underline">
          {row.name}
        </Link>
      </td>
      <td className="h-11 px-3 text-right text-ui tabular-nums text-ink">{cell(projectAmountText(row.monthToDate))}</td>
      <td className="h-11 px-3 text-right text-ui tabular-nums text-ink-muted">{cell(projectAmountText(row.forecast))}</td>
      <td className="h-11 px-3 text-ui text-ink-muted">
        {pending || failed
          ? cell("")
          : row.top
            ? `${projectCategoryLabel(row.top.category)} · ${Math.round(row.top.share * 100)}%`
            : "No hosted usage this month"}
      </td>
    </tr>
  );
}

/** AI and agent sandbox time: billed to the organization, so org-level only. */
function OrgLevel({ query, orgId, range }: { query: UsageQuery; orgId: string; range: UsageRangeId }) {
  return (
    <section className="space-y-3" aria-labelledby="usage-billing-org-level" data-testid="usage-billing-org-level">
      <h2 id="usage-billing-org-level" className="text-section text-ink">
        AI and agent sandboxes <span className="ml-2 text-meta font-normal text-ink-muted">billed to the organization, not to a project · {rangeLabel(range)}</span>
      </h2>
      <Loaded query={query} orgId={orgId} what="AI and sandbox usage">
        {(usage) => {
          const ai = categoryAmount(usage, "managed_llm");
          const sandboxes = categoryAmount(usage, "machines");
          const sandboxPoints = usage.points.filter((p) => p.category === "machines");
          const running = quantitySum(sandboxPoints, "running_seconds");
          return (
            <div className="grid gap-4 md:grid-cols-2">
              <Figure title="AI (managed models)" testId="usage-billing-ai" figure={<AmountText amount={ai} />} detail={amountCaption(ai)} />
              <Figure
                title="Agent sandboxes"
                testId="usage-billing-sandboxes"
                figure={running !== undefined ? formatQuantity(running, quantityUnit(sandboxPoints, "running_seconds")) : <AmountText amount={sandboxes} />}
                detail={
                  <>
                    {running !== undefined ? `running · ${sandboxes.kind === "amount" ? formatUsd(sandboxes.nanos) : amountCaption(sandboxes)}` : amountCaption(sandboxes)}
                    {" · "}
                    <Link href={sandboxesPath()} className={LINK}>
                      Manage sandboxes ›
                    </Link>
                  </>
                }
              />
            </div>
          );
        }}
      </Loaded>
    </section>
  );
}

const STATUS_LABEL: Partial<Record<SubscriptionStatus, string>> = {
  [SubscriptionStatus.ACTIVE]: "Active",
  [SubscriptionStatus.TRIALING]: "Trial",
  [SubscriptionStatus.PAST_DUE]: "Past due",
  [SubscriptionStatus.CANCELED]: "Canceled",
};

/** Plan, credits, recent invoices and the payment method: read here, changed in Settings › Billing. */
function BillingSummary() {
  const subQ = useComputeSubscription();
  const walletQ = useWalletOverview();
  const invoicesQ = useCurrentUserInvoices();
  const portal = useCreateBillingPortalSession();
  const [portalError, setPortalError] = useState("");

  const subscription = subQ.data?.subscription;
  const wallet = walletQ.data?.overview?.wallet;
  const invoices = (invoicesQ.data?.invoices ?? []).slice(0, 3);

  const openPortal = () => {
    setPortalError("");
    const returnUrl = buildCheckoutReturnUrls(usageBillingPath()).successUrl;
    portal.mutate(returnUrl, {
      onSuccess: (res) => void openCheckout(res.portalUrl, returnUrl),
      onError: (err) => {
        if (!isBackendModalError(err)) setPortalError(formatBillingError(err, "Could not open the billing portal"));
      },
    });
  };

  return (
    <section className="space-y-3" aria-labelledby="usage-billing-billing" data-testid="usage-billing-billing">
      <h2 id="usage-billing-billing" className="text-section text-ink">
        Billing{" "}
        <Link href={settingsPath("billing")} className={`ml-2 text-meta font-normal ${LINK}`}>
          Billing admin in Settings ›
        </Link>
      </h2>
      <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-4">
        <Figure
          title="Plan"
          testId="usage-billing-plan"
          figure={subQ.isLoading ? "…" : subQ.error ? "Unavailable" : subscription?.plan ? planDisplayName(subscription.plan) : "No plan"}
          detail={
            subscription?.plan ? (
              [STATUS_LABEL[subscription.status], subscription.currentPeriodEnd && `renews ${formatTimestampDate(subscription.currentPeriodEnd)}`].filter(Boolean).join(" · ")
            ) : subQ.isLoading || subQ.error ? null : (
              <Link href={withSearch(settingsPath("billing"), { tab: "plans" })} className={LINK}>
                See plans ›
              </Link>
            )
          }
        />
        <Figure
          title="Credits"
          icon={<Wallet className="h-4 w-4" aria-hidden="true" />}
          testId="usage-billing-credits"
          figure={walletQ.isLoading ? "…" : walletQ.error && !wallet ? "Unavailable" : formatCurrencyFromWalletFields(wallet?.balanceUsdNanos, wallet?.balanceUsdMicros, wallet?.balanceCents)}
          detail={
            <Link href={settingsPath("billing")} className={LINK}>
              Add credits ›
            </Link>
          }
        />
        <Card padding="none" className="px-4 py-3 md:col-span-2 xl:col-span-1" data-testid="usage-billing-invoices">
          <h3 className="flex items-center gap-1.5 text-ui font-medium text-ink-muted">
            <Receipt className="h-4 w-4" aria-hidden="true" /> Invoices
          </h3>
          {invoicesQ.isLoading ? (
            <p className="mt-2 text-meta text-ink-muted">Loading invoices…</p>
          ) : invoicesQ.error ? (
            <p className="mt-2 text-meta text-ink-muted">Invoices could not be loaded.</p>
          ) : invoices.length === 0 ? (
            <p className="mt-2 text-meta text-ink-muted">No invoices yet. They appear once your first billing period closes.</p>
          ) : (
            <ul className="mt-1.5 space-y-1">
              {invoices.map((invoice) => {
                const status = normalizeInvoiceStatus(invoice.status);
                return (
                  <li key={invoice.id} className="flex items-center gap-2 text-ui">
                    <span className="min-w-0 flex-1 truncate text-ink-muted">{formatTimestampDate(invoice.periodStart ?? invoice.createdAt)}</span>
                    <span className="tabular-nums text-ink">{formatCentsAsDollars(invoice.amountDue)}</span>
                    <Badge size="sm" variant={status === "Paid" ? "success" : status === "Pending" ? "warning" : "error"} label={status} />
                    {invoice.pdfUrl && (
                      <a href={invoice.pdfUrl} target="_blank" rel="noopener noreferrer" className={`text-meta ${LINK}`}>
                        PDF
                      </a>
                    )}
                  </li>
                );
              })}
            </ul>
          )}
          <p className="mt-2 text-meta">
            <Link href={withSearch(settingsPath("billing"), { tab: "usage" })} className={LINK}>
              All invoices ›
            </Link>
          </p>
        </Card>
        <Card padding="none" className="px-4 py-3" data-testid="usage-billing-payment">
          <h3 className="flex items-center gap-1.5 text-ui font-medium text-ink-muted">
            <CreditCard className="h-4 w-4" aria-hidden="true" /> Payment method
          </h3>
          <p className="mt-1 text-meta text-ink-muted">Cards and billing details are kept by Stripe.</p>
          <Button variant="secondary" size="sm" className="mt-2" onClick={openPortal} loading={portal.isPending}>
            Manage payment method
          </Button>
          {portalError && (
            <p role="alert" className="mt-1 text-meta text-destructive-ink">
              {portalError}
            </p>
          )}
        </Card>
      </div>
    </section>
  );
}

function Figure({ title, figure, detail, icon, testId }: { title: string; figure: ReactNode; detail?: ReactNode; icon?: ReactNode; testId: string }) {
  return (
    <Card padding="none" className="min-w-0 px-4 py-3" data-testid={testId}>
      <h3 className="flex items-center gap-1.5 text-ui font-medium text-ink-muted">
        {icon}
        {title}
      </h3>
      <div className="mt-0.5 text-2xl font-semibold tabular-nums text-ink">{figure}</div>
      {detail ? <div className="text-meta text-ink-muted">{detail}</div> : null}
    </Card>
  );
}
