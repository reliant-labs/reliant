// Copyright (c) 2026 Reliant Labs

/**
 * USAGE (U6b round 3, prototype): /forge/p/:app/usage, under Configure.
 *
 * What this app's hosting costs this month, from BillingService.GetProjectCost:
 * the month to date and the forecast, one chart of the days by category, and
 * the split by environment, by category and by workload (the drivers). Every
 * figure says its basis (estimated, list, invoiced); an absent amount is "n/a",
 * never $0.
 *
 * AI and machine time are billed to the organization, not to a project (the
 * ledger has no project): a note says so and links to Settings › Billing.
 */

import { useMemo, type ReactNode } from "react";

import { ProjectEmptyCtaRow } from "@/components/EmptyState/ProjectEmptyCta";
import Card from "@/components/forge-ui/card";
import Link from "@/components/forge-ui/link";
import Tag from "@/components/forge-ui/tag";
import type { ProjectCostAmount } from "@/gen/controlplane/services/billing/v1/billing_pb";
import { useObservabilityOrg } from "@/hooks/observability-queries";
import { useProjectCost } from "@/hooks/organization-usage-queries";
import { settingsPath } from "@/lib/paths";
import { cn } from "@/lib/utils";

import { useForgeConsole } from "../Console/consoleContext";
import { Header, Page } from "../Console/consolePageFrame";
import { PAGE_SUBTITLES } from "../Console/consoleCrumbs";
import { DeployWithAgentButton } from "../Observability/AskReliant/AskReliant";
import { UPlotChart } from "../Observability/charts/UPlotChart";
import { formatPercent } from "../Observability/format";
import { aggregateAmountText, projectDaily, type AggregateAmount } from "./projectUsageModel";

const CATEGORY_LABEL: Record<string, string> = {
  compute: "Hosted compute",
  databases: "Databases",
  storage: "Database storage",
  bandwidth: "CDN and bandwidth",
  static_sites: "Static sites",
  builds: "Builds",
};
const CATEGORY_COLOR = ["--chart-1", "--chart-2", "--chart-3", "--chart-4", "--chart-5", "--chart-6"];

const BASIS_LABEL: Record<string, string> = {
  invoiced: "Invoiced",
  estimated: "Estimated · final on the invoice",
  list: "At list price, before allowances",
  not_billed: "Recorded, not billed yet",
  not_measured: "Not measured",
};

function dollars(nanos: bigint | number | undefined | null): number | null {
  return nanos === undefined || nanos === null ? null : Number(nanos) / 1e9;
}

export function usd(value: number | null, lowerBound = false): string {
  if (value === null) return "n/a";
  return `${lowerBound ? "≥ " : ""}$${value.toLocaleString("en-US", { minimumFractionDigits: 2, maximumFractionDigits: 2 })}`;
}

function amountText(amount: ProjectCostAmount | undefined): string {
  return usd(dollars(amount?.usdNanos), amount?.lowerBound);
}

const categoryLabel = (category: string) => CATEGORY_LABEL[category] ?? category.replace(/_/g, " ");

export function ProjectUsagePage() {
  const { forgeProject, roster } = useForgeConsole();
  const org = useObservabilityOrg();
  const cost = useProjectCost(org.orgId, forgeProject);
  const data = cost.data;
  const envName = (id: string) => roster.envs.find((candidate) => candidate.live?.id === id)?.name ?? data?.drivers.find((d) => d.environmentId === id)?.environmentName ?? id;

  const { days, categories, byEnv, byCategory, total: listTotal } = useMemo(() => {
    const view = projectDaily(data?.daily ?? []);
    return {
      ...view,
      categories: view.categories.map((c, i) => ({ label: categoryLabel(c.category), values: c.values, color: CATEGORY_COLOR[i % CATEGORY_COLOR.length] })),
    };
  }, [data]);

  const forecast = data?.forecast;
  const low = dollars(forecast?.lowUsdNanos);
  const high = dollars(forecast?.highUsdNanos);

  return (
    <Page testId="console-usage">
      <Header title="Usage" subtitle={PAGE_SUBTITLES.usage} />
      {cost.isLoading ? (
        <div className="h-64 animate-pulse rounded-xl border border-border bg-surface motion-reduce:animate-none" aria-label="Loading usage" />
      ) : cost.error || !data ? (
        <p className="rounded-xl border border-dashed border-border px-4 py-5 text-ui text-ink-muted" data-testid="usage-unavailable">
          This app&apos;s costs could not be read right now. Your organization&apos;s bill is in{" "}
          <Link href={settingsPath("billing")} className="no-color text-primary-ink hover:underline">
            Settings › Billing
          </Link>
          .
        </p>
      ) : (
        <>
          <div className="grid gap-4 md:grid-cols-3" data-testid="usage-headline">
            <Figure title="This month so far" figure={amountText(data.monthToDate)} detail={BASIS_LABEL[data.monthToDate?.basis ?? ""] ?? data.monthToDate?.basis} big />
            <Figure
              title="Forecast for the month"
              figure={forecast?.amount ? amountText(forecast.amount) : "n/a"}
              detail={forecast?.amount ? (low !== null && high !== null ? `Likely ${usd(low)} to ${usd(high)}` : BASIS_LABEL[forecast.amount.basis]) : forecast?.unavailableReason || "Not enough measured hours yet"}
            />
            <Figure title="Last month" figure={amountText(data.lastPeriod)} detail={BASIS_LABEL[data.lastPeriod?.basis ?? ""] ?? ""} />
          </div>

          <Card padding="none" className="min-w-0" data-testid="usage-daily">
            <div className="flex flex-wrap items-baseline justify-between gap-2 px-4 pt-3">
              <h2 className="text-section text-ink">By day</h2>
              <span className="text-meta text-ink-muted" data-testid="usage-daily-total">
                At list price, before allowances · {listTotal.nanos === undefined ? "nothing measured yet" : `${aggregateAmountText(listTotal)} so far`}
              </span>
            </div>
            <div className="px-2 pb-3 pt-2">
              {days.length === 0 ? (
                <div className="flex flex-col items-center gap-3 px-2 py-8" data-testid="usage-none">
                  <p className="text-center text-meta text-ink-muted">No hosted usage recorded this month yet.</p>
                  <ProjectEmptyCtaRow deploy={<DeployWithAgentButton env={null} />} learn="usage" className="justify-center" testId="usage-empty-cta" />
                </div>
              ) : days.length === 1 ? (
                // One point is not a chart: say the day's figure in words until a second day lands.
                <p className="px-2 py-8 text-center text-ui text-ink" data-testid="usage-one-day">
                  {formatDay(days[0])}: <span className="font-semibold tabular-nums">{aggregateAmountText(listTotal)}</span>
                  <span className="block text-meta text-ink-muted">The daily chart starts once a second day is recorded.</span>
                </p>
              ) : (
                <UPlotChart height={200} timestamps={days} series={categories} unit="" label={`Daily cost of ${forgeProject} by category`} bars area="stacked" />
              )}
            </div>
          </Card>

          <div className="grid gap-4 lg:grid-cols-2">
            <Breakdown title="By environment" rows={byEnv.map(([id, v]) => ({ key: id, label: <Tag>{envName(id)}</Tag>, value: v }))} total={listTotal.nanos} testId="usage-by-env" />
            <Breakdown title="By category" rows={byCategory.map(([c, v]) => ({ key: c, label: categoryLabel(c), value: v }))} total={listTotal.nanos} testId="usage-by-category" />
          </div>

          <section className="space-y-3" data-testid="usage-drivers">
            <h2 className="text-section text-ink">
              What costs the most <span className="ml-2 text-meta font-normal text-ink-muted">each workload at list price, month to date</span>
            </h2>
            <div className="overflow-x-auto rounded-xl border border-border bg-surface">
              <table className="w-full border-collapse">
                <thead>
                  <tr className="border-b border-border">
                    {["Workload", "Environment", "Category", "Requested", "Cost", "Share"].map((h, i) => (
                      <th key={h} className={cn("h-9 px-3 text-left text-meta font-medium text-ink-muted", i >= 4 && "text-right")}>
                        {h}
                      </th>
                    ))}
                  </tr>
                </thead>
                <tbody>
                  {data.drivers.map((d) => (
                    <tr key={`${d.environmentId}/${d.deploymentId || d.deploymentName}`} className="border-b border-border-subtle last:border-b-0">
                      <td className="h-11 px-3 text-ui font-medium text-ink">
                        {d.deploymentName || "Static site bandwidth"}
                        {d.deleted && <span className="ml-2 text-meta font-normal text-ink-muted">deleted</span>}
                      </td>
                      <td className="h-11 px-3">
                        <Tag>{d.environmentName || envName(d.environmentId)}</Tag>
                      </td>
                      <td className="h-11 px-3 text-ui text-ink-muted">{categoryLabel(d.category)}</td>
                      <td className="h-11 px-3 text-meta tabular-nums text-ink-muted">
                        {d.requested
                          ? [d.requested.replicas ? `${d.requested.replicas}×` : "", d.requested.cpuMilli ? `${d.requested.cpuMilli}m CPU` : "", d.requested.memoryMib ? `${d.requested.memoryMib} MiB` : "", d.requested.storageGib ? `${d.requested.storageGib} GiB` : ""]
                              .filter(Boolean)
                              .join(" · ")
                          : "—"}
                      </td>
                      <td className="h-11 px-3 text-right text-ui tabular-nums text-ink">{amountText(d.list)}</td>
                      <td className="h-11 px-3 text-right text-ui tabular-nums text-ink-muted">{formatPercent(d.share)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </section>
        </>
      )}
      <p className="text-meta text-ink-muted" data-testid="usage-org-note">
        AI and machine time are billed to the org — see{" "}
        <Link href={settingsPath("billing")} className="no-color text-primary-ink hover:underline">
          Settings › Billing
        </Link>
        .
      </p>
    </Page>
  );
}

function Figure({ title, figure, detail, big = false }: { title: string; figure: string; detail?: ReactNode; big?: boolean }) {
  return (
    <Card padding="none" className="px-4 py-3">
      <h2 className="text-ui font-medium text-ink-muted">{title}</h2>
      <div className={cn("mt-0.5 font-semibold tabular-nums text-ink", big ? "text-figure" : "text-2xl")}>{figure}</div>
      {detail && <div className="truncate text-meta text-ink-muted">{detail}</div>}
    </Card>
  );
}

function formatDay(epochSeconds: number): string {
  return new Date(epochSeconds * 1000).toLocaleDateString("en-US", { month: "short", day: "numeric", timeZone: "UTC" });
}

function Breakdown({ title, rows, total, testId }: { title: string; rows: { key: string; label: ReactNode; value: AggregateAmount }[]; total: bigint | undefined; testId: string }) {
  const share = (value: AggregateAmount) => (total && value.nanos !== undefined ? Math.round((Number(value.nanos) / Number(total)) * 100) : 0);
  return (
    <Card padding="none" className="min-w-0" data-testid={testId}>
      <h2 className="px-4 pt-3 text-section text-ink">{title}</h2>
      <ul className="px-4 pb-3 pt-2">
        {rows.map((row) => (
          <li key={row.key} className="flex items-center gap-3 py-1.5 text-ui">
            <span className="min-w-0 flex-1 truncate">{row.label}</span>
            <span className="h-1.5 w-24 overflow-hidden rounded-full bg-ink/10" aria-hidden="true">
              <span className="block h-full rounded-full bg-primary" style={{ width: `${share(row.value)}%` }} />
            </span>
            <span className="w-20 text-right tabular-nums text-ink">{aggregateAmountText(row.value)}</span>
          </li>
        ))}
      </ul>
    </Card>
  );
}
