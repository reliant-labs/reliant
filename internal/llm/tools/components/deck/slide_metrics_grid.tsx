import React from "react";

interface SlideMetricsGridProps {
  title: string;
  subtitle?: string;
  metrics: Array<{
    value: string;
    label: string;
    detail?: string;
    /** Highlight this metric */
    highlight?: boolean;
  }>;
}

export const SlideMetricsGrid: React.FC<SlideMetricsGridProps> = ({
  title,
  subtitle,
  metrics,
}) => {
  const colCount = metrics.length <= 3 ? metrics.length : metrics.length <= 4 ? 2 : 3;

  return (
    <div
      className="slide relative flex flex-col overflow-hidden bg-gray-950"
      style={{ width: 1280, height: 720 }}
    >
      {/* Header */}
      <div className="flex-shrink-0 px-16 pt-12 pb-6">
        <h1 className="text-4xl font-bold tracking-tight text-white">
          {title}
        </h1>
        {subtitle && (
          <p className="mt-2 text-lg text-gray-400">{subtitle}</p>
        )}
      </div>

      {/* Metrics grid */}
      <div className="flex flex-1 items-center justify-center px-16 pb-14">
        <div
          className="grid w-full gap-5"
          style={{
            gridTemplateColumns: `repeat(${colCount}, minmax(0, 1fr))`,
          }}
        >
          {metrics.map((metric, i) => (
            <div
              key={i}
              className={`flex flex-col items-center justify-center gap-2 rounded-xl border p-8 text-center ${
                metric.highlight
                  ? "border-indigo-500/50 bg-indigo-950/20"
                  : "border-gray-800 bg-gray-900/40"
              }`}
            >
              <span
                className={`text-5xl font-black tracking-tight ${
                  metric.highlight
                    ? "bg-gradient-to-r from-indigo-400 to-purple-400 bg-clip-text text-transparent"
                    : "text-white"
                }`}
              >
                {metric.value}
              </span>

              <span className="text-base font-medium text-gray-300">
                {metric.label}
              </span>

              {metric.detail && (
                <span className="text-sm text-gray-500">
                  {metric.detail}
                </span>
              )}
            </div>
          ))}
        </div>
      </div>
    </div>
  );
};

export default SlideMetricsGrid;
