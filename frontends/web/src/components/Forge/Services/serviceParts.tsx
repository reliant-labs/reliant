// Copyright (c) 2026 Reliant Labs

/**
 * The pieces a service's page and a static site's page share: a section with
 * its heading, and a chart card (one headline number, one chart).
 */

import type { ReactNode } from "react";

import Card from "@/components/forge-ui/card";
import { cn } from "@/lib/utils";

import { UPlotChart, type ChartSeries } from "../Observability/charts/UPlotChart";
import type { ValueUnit } from "../Observability/format";

export function Section({ title, meta, actions, testId, children }: { title: ReactNode; meta?: ReactNode; actions?: ReactNode; testId?: string; children: ReactNode }) {
  return (
    <section className="min-w-0 space-y-3 pt-4" data-testid={testId}>
      <div className="flex min-h-7 items-center justify-between gap-3">
        <h2 className="min-w-0 text-section text-ink">
          {title}
          {meta && <span className="ml-2 text-meta font-normal text-ink-muted">{meta}</span>}
        </h2>
        {actions && <div className="flex shrink-0 items-center gap-2">{actions}</div>}
      </div>
      {children}
    </section>
  );
}

export function MetricCard({
  title,
  figure,
  detail,
  tone,
  loading,
  empty,
  testId,
  children,
}: {
  title: ReactNode;
  figure: ReactNode;
  detail?: ReactNode;
  tone?: "warning";
  loading?: boolean;
  empty?: string | null;
  testId: string;
  children: ReactNode;
}) {
  return (
    <Card padding="none" className="min-w-0" data-testid={testId}>
      <div className="px-4 pt-3">
        <h3 className="text-ui font-medium text-ink-muted">{title}</h3>
        {loading ? (
          <div className="mt-1 h-7 w-20 animate-pulse rounded bg-ink/5 motion-reduce:animate-none" />
        ) : (
          <div className={cn("mt-0.5 text-2xl font-semibold tabular-nums", tone === "warning" ? "text-warning-ink" : "text-ink")}>{figure}</div>
        )}
        {detail && <div className="truncate text-meta text-ink-muted">{detail}</div>}
      </div>
      <div className="px-2 pb-2 pt-2">
        {loading ? (
          <div className="h-[110px] animate-pulse rounded bg-ink/5 motion-reduce:animate-none" />
        ) : empty ? (
          <div className="flex h-[110px] items-center justify-center px-6 text-center text-meta text-ink-muted">{empty}</div>
        ) : (
          children
        )}
      </div>
    </Card>
  );
}

/** A MetricCard's chart: one series, or several on one axis (`series`). */
export function MetricLine({
  timestamps,
  values,
  series,
  unit,
  format,
  label,
  tone,
}: {
  timestamps: number[];
  values?: (number | null)[];
  series?: ChartSeries[];
  unit: ValueUnit;
  /** How a value reads where the unit's formatter is not the page's (binary bytes). */
  format?: (value: number) => string;
  label: string;
  tone?: ChartSeries["tone"];
}) {
  return <UPlotChart height={110} timestamps={timestamps} series={series ?? [{ label, values: values ?? [], tone }]} unit={unit} format={format} label={label} legend={false} />;
}
