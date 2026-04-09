import React from "react";

/**
 * BarChart — Horizontal or vertical stacked bar chart.
 *
 * Pass bars with one or more segments. The component computes
 * all bar widths, stacking offsets, labels, and spacing.
 *
 * Usage:
 * ```tsx
 * <BarChart
 *   bars={[
 *     {
 *       label: "Q1",
 *       segments: [
 *         { label: "Product", value: 40, color: "#a855f7" },
 *         { label: "Services", value: 25, color: "#3b82f6" },
 *       ],
 *     },
 *     {
 *       label: "Q2",
 *       segments: [
 *         { label: "Product", value: 55, color: "#a855f7" },
 *         { label: "Services", value: 30, color: "#3b82f6" },
 *       ],
 *     },
 *   ]}
 * />
 * ```
 */

interface BarChartProps {
  /** Bars to render */
  bars: Array<{
    /** Bar group label */
    label: string;
    /** Segments for stacked bars, or a single-element array for simple bars */
    segments: Array<{
      label: string;
      value: number;
      color?: string;
    }>;
  }>;
  /** Whether bars are horizontal (default true) */
  horizontal?: boolean;
  /** Show value labels on segments (default true) */
  showValues?: boolean;
  /** Chart width in px (default 500) */
  width?: number;
  /** Chart height in px (default 300) */
  height?: number;
}

const DEFAULT_COLORS = [
  "#a855f7",
  "#3b82f6",
  "#22c55e",
  "#f59e0b",
  "#ef4444",
  "#06b6d4",
  "#ec4899",
  "#8b5cf6",
];

export default function BarChart({
  bars,
  horizontal = true,
  showValues = true,
  width = 500,
  height = 300,
}: BarChartProps) {
  if (bars.length === 0) return null;

  const padding = { top: 20, right: 20, bottom: 30, left: 80 };
  if (!horizontal) {
    padding.left = 40;
    padding.bottom = 60;
  }

  const plotW = width - padding.left - padding.right;
  const plotH = height - padding.top - padding.bottom;

  // Find max total for scaling
  const maxTotal = Math.max(
    ...bars.map((b) => b.segments.reduce((s, seg) => s + seg.value, 0)),
    1,
  );

  const n = bars.length;
  const barGap = 0.3; // fraction of slot used as gap
  const slotSize = horizontal ? plotH / n : plotW / n;
  const barThickness = slotSize * (1 - barGap);

  // Collect unique segment labels for legend
  const segmentLabels = new Map<string, string>();
  let colorIdx = 0;
  for (const bar of bars) {
    for (const seg of bar.segments) {
      if (!segmentLabels.has(seg.label)) {
        segmentLabels.set(
          seg.label,
          seg.color || DEFAULT_COLORS[colorIdx % DEFAULT_COLORS.length],
        );
        colorIdx++;
      }
    }
  }

  const resolveColor = (seg: { label: string; color?: string }) =>
    seg.color || segmentLabels.get(seg.label) || "#a855f7";

  return (
    <div className="inline-flex flex-col gap-2">
      <svg
        width={width}
        height={height}
        viewBox={`0 0 ${width} ${height}`}
        xmlns="http://www.w3.org/2000/svg"
        className="select-none"
      >
        {horizontal ? (
          /* ── Horizontal bars ── */
          <>
            {/* Y axis line */}
            <line
              x1={padding.left}
              y1={padding.top}
              x2={padding.left}
              y2={padding.top + plotH}
              stroke="#475569"
              strokeWidth={1}
            />

            {bars.map((bar, bi) => {
              const slotY = padding.top + bi * slotSize;
              const barY = slotY + (slotSize - barThickness) / 2;
              let offsetX = 0;

              return (
                <g key={bi}>
                  {/* Row label */}
                  <text
                    x={padding.left - 8}
                    y={barY + barThickness / 2 + 4}
                    textAnchor="end"
                    fill="#cbd5e1"
                    fontSize={12}
                  >
                    {bar.label}
                  </text>

                  {bar.segments.map((seg, si) => {
                    const segW = (seg.value / maxTotal) * plotW;
                    const x = padding.left + offsetX;
                    offsetX += segW;

                    return (
                      <g key={si}>
                        <rect
                          x={x}
                          y={barY}
                          width={Math.max(segW, 1)}
                          height={barThickness}
                          rx={3}
                          fill={resolveColor(seg)}
                          opacity={0.85}
                        />
                        {showValues && segW > 30 && (
                          <text
                            x={x + segW / 2}
                            y={barY + barThickness / 2 + 4}
                            textAnchor="middle"
                            fill="#ffffff"
                            fontSize={11}
                            fontWeight={500}
                          >
                            {seg.value}
                          </text>
                        )}
                      </g>
                    );
                  })}
                </g>
              );
            })}
          </>
        ) : (
          /* ── Vertical bars ── */
          <>
            {/* X axis line */}
            <line
              x1={padding.left}
              y1={padding.top + plotH}
              x2={padding.left + plotW}
              y2={padding.top + plotH}
              stroke="#475569"
              strokeWidth={1}
            />

            {bars.map((bar, bi) => {
              const slotX = padding.left + bi * slotSize;
              const barX = slotX + (slotSize - barThickness) / 2;
              let offsetY = 0;
              const total = bar.segments.reduce((s, seg) => s + seg.value, 0);

              return (
                <g key={bi}>
                  {/* Column label (rotated for readability) */}
                  <text
                    x={barX + barThickness / 2}
                    y={padding.top + plotH + 16}
                    textAnchor="middle"
                    fill="#cbd5e1"
                    fontSize={11}
                    transform={`rotate(${bar.label.length > 6 ? -30 : 0}, ${barX + barThickness / 2}, ${padding.top + plotH + 16})`}
                  >
                    {bar.label}
                  </text>

                  {/* Segments stacked bottom-up */}
                  {[...bar.segments].reverse().map((seg, si) => {
                    const segH = (seg.value / maxTotal) * plotH;
                    const y = padding.top + plotH - offsetY - segH;
                    offsetY += segH;

                    return (
                      <g key={si}>
                        <rect
                          x={barX}
                          y={y}
                          width={barThickness}
                          height={Math.max(segH, 1)}
                          rx={3}
                          fill={resolveColor(seg)}
                          opacity={0.85}
                        />
                        {showValues && segH > 18 && (
                          <text
                            x={barX + barThickness / 2}
                            y={y + segH / 2 + 4}
                            textAnchor="middle"
                            fill="#ffffff"
                            fontSize={10}
                            fontWeight={500}
                          >
                            {seg.value}
                          </text>
                        )}
                      </g>
                    );
                  })}
                </g>
              );
            })}
          </>
        )}
      </svg>

      {/* Legend */}
      {segmentLabels.size > 1 && (
        <div className="flex flex-wrap gap-x-4 gap-y-1 px-2">
          {Array.from(segmentLabels.entries()).map(([label, color]) => (
            <div key={label} className="flex items-center gap-1.5 text-xs">
              <span
                className="inline-block w-2.5 h-2.5 rounded-sm"
                style={{ backgroundColor: color }}
              />
              <span className="text-slate-400">{label}</span>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}
