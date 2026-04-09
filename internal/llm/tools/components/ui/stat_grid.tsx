import React from "react";

interface StatGridProps {
  stats: Array<{
    value: string;
    label: string;
    icon?: React.ReactNode;
    trend?: { direction: "up" | "down" | "flat"; value: string };
  }>;
  columns?: 2 | 3 | 4;
}

const columnClasses: Record<number, string> = {
  2: "grid-cols-1 sm:grid-cols-2",
  3: "grid-cols-1 sm:grid-cols-2 lg:grid-cols-3",
  4: "grid-cols-1 sm:grid-cols-2 lg:grid-cols-4",
};

function TrendIndicator({
  trend,
}: {
  trend: { direction: "up" | "down" | "flat"; value: string };
}) {
  const colors: Record<string, string> = {
    up: "text-green-600 bg-green-50",
    down: "text-red-600 bg-red-50",
    flat: "text-gray-600 bg-gray-50",
  };

  const arrows: Record<string, string> = {
    up: "↑",
    down: "↓",
    flat: "→",
  };

  return (
    <span
      className={`inline-flex items-center gap-0.5 rounded-full px-2 py-0.5 text-xs font-medium ${colors[trend.direction]}`}
    >
      {arrows[trend.direction]} {trend.value}
    </span>
  );
}

export default function StatGrid({ stats, columns = 4 }: StatGridProps) {
  return (
    <div className={`grid gap-6 ${columnClasses[columns]}`}>
      {stats.map((stat) => (
        <div
          key={stat.label}
          className="rounded-xl border border-gray-200 bg-white p-6"
        >
          <div className="flex items-center justify-between">
            {stat.icon && (
              <div className="flex h-10 w-10 items-center justify-center rounded-lg bg-blue-50 text-blue-600">
                {stat.icon}
              </div>
            )}
            {stat.trend && <TrendIndicator trend={stat.trend} />}
          </div>
          <p className="mt-4 text-3xl font-bold tracking-tight text-gray-900">
            {stat.value}
          </p>
          <p className="mt-1 text-sm text-gray-500">{stat.label}</p>
        </div>
      ))}
    </div>
  );
}
