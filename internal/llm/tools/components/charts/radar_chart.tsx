import React from "react";

/**
 * RadarChart — Spider/radar chart with multiple overlaid datasets.
 *
 * Pass axes (the spokes) and one or more datasets.
 * The component handles all polar → Cartesian math, grid rings,
 * spoke labels, and polygon fills.
 *
 * Usage:
 * ```tsx
 * <RadarChart
 *   axes={[
 *     { label: "Speed" },
 *     { label: "Reliability" },
 *     { label: "Cost", max: 50 },
 *     { label: "Features" },
 *     { label: "Support" },
 *   ]}
 *   datasets={[
 *     { label: "Us", values: [90, 85, 30, 95, 80], color: "#a855f7" },
 *     { label: "Competitor", values: [70, 90, 45, 60, 50], color: "#3b82f6", fillOpacity: 0.1 },
 *   ]}
 * />
 * ```
 */

interface RadarChartProps {
  /** Axes (spokes) of the radar */
  axes: Array<{
    /** Axis label displayed at the tip */
    label: string;
    /** Maximum value for this axis (default 100) */
    max?: number;
  }>;
  /** Data sets to overlay on the chart */
  datasets: Array<{
    /** Dataset label (for legend) */
    label: string;
    /** Values corresponding to each axis (same order as axes array) */
    values: number[];
    /** Line/fill color (auto-assigned if omitted) */
    color?: string;
    /** Fill opacity (default 0.15) */
    fillOpacity?: number;
  }>;
  /** Outer size in px (default 300) */
  size?: number;
  /** Number of concentric grid rings (default 4) */
  gridRings?: number;
}

const DEFAULT_COLORS = [
  "#a855f7",
  "#3b82f6",
  "#22c55e",
  "#f59e0b",
  "#ef4444",
  "#06b6d4",
  "#ec4899",
];

/**
 * Convert a value on an axis to Cartesian coordinates.
 * Angle 0 = top (12 o'clock), proceeding clockwise.
 */
function valueToPoint(
  cx: number,
  cy: number,
  radius: number,
  value: number,
  max: number,
  axisIndex: number,
  totalAxes: number,
): [number, number] {
  const fraction = Math.max(0, Math.min(1, value / max));
  const angleDeg = (360 / totalAxes) * axisIndex - 90; // -90 so index 0 = top
  const angleRad = (angleDeg * Math.PI) / 180;
  return [
    cx + radius * fraction * Math.cos(angleRad),
    cy + radius * fraction * Math.sin(angleRad),
  ];
}

export default function RadarChart({
  axes,
  datasets,
  size = 300,
  gridRings = 4,
}: RadarChartProps) {
  const n = axes.length;
  if (n < 3) return null; // need at least 3 axes for a polygon

  const cx = size / 2;
  const cy = size / 2;
  const labelMargin = 36; // space for labels outside the chart
  const maxR = size / 2 - labelMargin;

  /** Generate polygon points string for a grid ring at given fraction. */
  const ringPolygon = (fraction: number): string =>
    axes
      .map((_, i) => {
        const angleDeg = (360 / n) * i - 90;
        const angleRad = (angleDeg * Math.PI) / 180;
        const x = cx + maxR * fraction * Math.cos(angleRad);
        const y = cy + maxR * fraction * Math.sin(angleRad);
        return `${x},${y}`;
      })
      .join(" ");

  /** Spoke endpoint at full radius for axis i. */
  const spokeEnd = (i: number): [number, number] => {
    const angleDeg = (360 / n) * i - 90;
    const angleRad = (angleDeg * Math.PI) / 180;
    return [cx + maxR * Math.cos(angleRad), cy + maxR * Math.sin(angleRad)];
  };

  /** Label position slightly beyond the spoke end. */
  const labelPos = (i: number): { x: number; y: number; anchor: string } => {
    const angleDeg = (360 / n) * i - 90;
    const angleRad = (angleDeg * Math.PI) / 180;
    const lR = maxR + 18;
    const x = cx + lR * Math.cos(angleRad);
    const y = cy + lR * Math.sin(angleRad);

    // Determine text-anchor based on position
    let anchor = "middle";
    if (Math.cos(angleRad) > 0.3) anchor = "start";
    else if (Math.cos(angleRad) < -0.3) anchor = "end";

    return { x, y: y + 4, anchor };
  };

  return (
    <div className="inline-flex flex-col items-center gap-3">
      <svg
        width={size}
        height={size}
        viewBox={`0 0 ${size} ${size}`}
        xmlns="http://www.w3.org/2000/svg"
        className="select-none"
      >
        {/* Grid rings */}
        {Array.from({ length: gridRings }, (_, ri) => {
          const fraction = (ri + 1) / gridRings;
          return (
            <polygon
              key={`ring-${ri}`}
              points={ringPolygon(fraction)}
              fill="none"
              stroke="#334155"
              strokeWidth={ri === gridRings - 1 ? 1 : 0.5}
              strokeDasharray={ri === gridRings - 1 ? "none" : "3 3"}
            />
          );
        })}

        {/* Spokes */}
        {axes.map((_, i) => {
          const [sx, sy] = spokeEnd(i);
          return (
            <line
              key={`spoke-${i}`}
              x1={cx}
              y1={cy}
              x2={sx}
              y2={sy}
              stroke="#334155"
              strokeWidth={0.5}
            />
          );
        })}

        {/* Axis labels */}
        {axes.map((axis, i) => {
          const lp = labelPos(i);
          return (
            <text
              key={`label-${i}`}
              x={lp.x}
              y={lp.y}
              textAnchor={lp.anchor}
              fill="#94a3b8"
              fontSize={11}
            >
              {axis.label}
            </text>
          );
        })}

        {/* Dataset polygons */}
        {datasets.map((ds, di) => {
          const color =
            ds.color || DEFAULT_COLORS[di % DEFAULT_COLORS.length];
          const fillOpacity = ds.fillOpacity ?? 0.15;

          const points = axes
            .map((axis, i) => {
              const val = ds.values[i] ?? 0;
              const max = axis.max ?? 100;
              const [px, py] = valueToPoint(cx, cy, maxR, val, max, i, n);
              return `${px},${py}`;
            })
            .join(" ");

          // Dot positions
          const dots = axes.map((axis, i) => {
            const val = ds.values[i] ?? 0;
            const max = axis.max ?? 100;
            return valueToPoint(cx, cy, maxR, val, max, i, n);
          });

          return (
            <g key={di}>
              <polygon
                points={points}
                fill={color}
                fillOpacity={fillOpacity}
                stroke={color}
                strokeWidth={2}
                strokeLinejoin="round"
              />
              {dots.map(([dx, dy], i) => (
                <circle
                  key={i}
                  cx={dx}
                  cy={dy}
                  r={3.5}
                  fill={color}
                  stroke="#0f172a"
                  strokeWidth={1.5}
                />
              ))}
            </g>
          );
        })}
      </svg>

      {/* Legend */}
      {datasets.length > 1 && (
        <div className="flex flex-wrap gap-x-4 gap-y-1">
          {datasets.map((ds, di) => {
            const color =
              ds.color || DEFAULT_COLORS[di % DEFAULT_COLORS.length];
            return (
              <div key={di} className="flex items-center gap-1.5 text-xs">
                <span
                  className="inline-block w-3 h-3 rounded-full"
                  style={{ backgroundColor: color }}
                />
                <span className="text-slate-400">{ds.label}</span>
              </div>
            );
          })}
        </div>
      )}
    </div>
  );
}
