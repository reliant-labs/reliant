import React from "react";

interface DashboardAnalyticsProps {
  /** Page title rendered above everything. */
  title: string;
  /** Subtitle / context below the title. Optional. */
  subtitle?: string;
  /** Filter bar slot — date range, segment selectors, etc. Rendered right of the title. */
  filters?: React.ReactNode;
  /** KPI strip — typically a row of metric_card / stat_grid. Rendered as the first content row. */
  kpis: React.ReactNode;
  /** Hero chart — the primary visualization for the page. Rendered full-width below KPIs. */
  chart: React.ReactNode;
  /** Drilldown table — detail data backing the chart. Optional. */
  table?: React.ReactNode;
}

export default function DashboardAnalytics({
  title,
  subtitle,
  filters,
  kpis,
  chart,
  table,
}: DashboardAnalyticsProps) {
  return (
    <div className="mx-auto flex w-full max-w-[1400px] flex-col gap-6 px-6 py-8">
      <header className="flex flex-wrap items-start justify-between gap-4">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight text-ink">{title}</h1>
          {subtitle && <p className="mt-1 text-sm text-ink-muted">{subtitle}</p>}
        </div>
        {filters && <div className="flex flex-wrap items-center gap-2">{filters}</div>}
      </header>

      <section aria-label="Key metrics">{kpis}</section>

      <section
        aria-label="Primary chart"
        className="overflow-hidden rounded-xl border border-border bg-surface p-5"
      >
        {chart}
      </section>

      {table && (
        <section
          aria-label="Detail data"
          className="overflow-hidden rounded-xl border border-border bg-surface"
        >
          {table}
        </section>
      )}
    </div>
  );
}
