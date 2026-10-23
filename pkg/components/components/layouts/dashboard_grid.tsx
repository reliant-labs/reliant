import React from "react";

interface DashboardGridProps {
  /** Header content */
  header?: React.ReactNode;
  /** Metric cards */
  metrics?: Array<{
    label: string;
    value: string;
    trend?: "up" | "down" | "flat";
  }>;
  /** Main content area */
  children: React.ReactNode;
}

const trendIndicator: Record<string, { symbol: string; color: string }> = {
  up: { symbol: "\u2191", color: "text-success" },
  down: { symbol: "\u2193", color: "text-danger" },
  flat: { symbol: "\u2192", color: "text-ink-muted" },
};

export default function DashboardGrid({ header, metrics, children }: DashboardGridProps) {
  return (
    <div className="min-h-screen bg-surface-muted">
      {header && <header className="border-b border-border bg-surface px-6 py-4">{header}</header>}

      <div className="p-6">
        {metrics && metrics.length > 0 && (
          <div className="mb-6 grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-4">
            {metrics.map((metric, i) => {
              const trend = metric.trend ? trendIndicator[metric.trend] : undefined;
              return (
                <div key={i} className="rounded-xl border border-border bg-surface p-5">
                  <p className="text-sm font-medium text-ink-muted">{metric.label}</p>
                  <div className="mt-1 flex items-baseline gap-2">
                    <p className="text-2xl font-semibold text-ink">{metric.value}</p>
                    {trend && (
                      <span className={`text-sm font-medium ${trend.color}`}>{trend.symbol}</span>
                    )}
                  </div>
                </div>
              );
            })}
          </div>
        )}

        <div className="rounded-xl border border-border bg-surface p-6">{children}</div>
      </div>
    </div>
  );
}
