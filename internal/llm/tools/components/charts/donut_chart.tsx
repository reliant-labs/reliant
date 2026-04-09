import React from "react";

/**
 * DonutChart — SVG donut/pie chart with optional center label.
 *
 * Pass segments with values (raw numbers — the component normalizes to percentages).
 * Handles all arc math, label positioning, and color assignment.
 *
 * Usage:
 * ```tsx
 * <DonutChart
 *   segments={[
 *     { label: "Product", value: 55, color: "#a855f7" },
 *     { label: "Services", value: 25, color: "#3b82f6" },
 *     { label: "Support", value: 20, color: "#22c55e" },
 *   ]}
 *   centerLabel="Revenue"
 *   centerValue="$4.2M"
 * />
 * ```
 */

interface DonutChartProps {
  /** Segments of the donut */
  segments: Array<{
    /** Segment label */
    label: string;
    /** Raw value (will be normalized to percentages) */
    value: number;
    /** Segment color (auto-assigned if omitted) */
    color?: string;
  }>;
  /** Label in the center hole (e.g., "Revenue") */
  centerLabel?: string;
  /** Value in the center hole (e.g., "$4.2M") */
  centerValue?: string;
  /** Outer size in px (default 240) */
  size?: number;
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

/**
 * Convert polar to Cartesian for SVG arc paths.
 */
function polarToCartesian(
  cx: number,
  cy: number,
  r: number,
  angleDeg: number,
): [number, number] {
  const rad = ((angleDeg - 90) * Math.PI) / 180;
  return [cx + r * Math.cos(rad), cy + r * Math.sin(rad)];
}

/**
 * Build an SVG arc path for a donut segment.
 */
function describeArc(
  cx: number,
  cy: number,
  outerR: number,
  innerR: number,
  startAngle: number,
  endAngle: number,
): string {
  const sweep = endAngle - startAngle;
  const largeArc = sweep > 180 ? 1 : 0;

  const [ox1, oy1] = polarToCartesian(cx, cy, outerR, startAngle);
  const [ox2, oy2] = polarToCartesian(cx, cy, outerR, endAngle);
  const [ix1, iy1] = polarToCartesian(cx, cy, innerR, endAngle);
  const [ix2, iy2] = polarToCartesian(cx, cy, innerR, startAngle);

  return [
    `M ${ox1} ${oy1}`,
    `A ${outerR} ${outerR} 0 ${largeArc} 1 ${ox2} ${oy2}`,
    `L ${ix1} ${iy1}`,
    `A ${innerR} ${innerR} 0 ${largeArc} 0 ${ix2} ${iy2}`,
    "Z",
  ].join(" ");
}

export default function DonutChart({
  segments,
  centerLabel,
  centerValue,
  size = 240,
}: DonutChartProps) {
  if (segments.length === 0) return null;

  const total = segments.reduce((s, seg) => s + seg.value, 0);
  if (total === 0) return null;

  const cx = size / 2;
  const cy = size / 2;
  const outerR = size / 2 - 4;
  const innerR = outerR * 0.58; // donut hole
  const labelR = (outerR + innerR) / 2; // midpoint for percentage labels

  const gapDeg = segments.length > 1 ? 1.5 : 0; // small gap between segments

  // Build angle spans
  let cursor = 0;
  const arcs = segments.map((seg, i) => {
    const spanDeg = (seg.value / total) * 360;
    const start = cursor + gapDeg / 2;
    const end = cursor + spanDeg - gapDeg / 2;
    cursor += spanDeg;

    const color =
      seg.color || DEFAULT_COLORS[i % DEFAULT_COLORS.length];
    const midAngle = (start + end) / 2;
    const [lx, ly] = polarToCartesian(cx, cy, labelR, midAngle);
    const pct = Math.round((seg.value / total) * 100);

    return { seg, start, end, color, lx, ly, pct, spanDeg };
  });

  const legendWidth = 160;
  const totalW = size + legendWidth;

  return (
    <div className="inline-flex items-center gap-4">
      <svg
        width={size}
        height={size}
        viewBox={`0 0 ${size} ${size}`}
        xmlns="http://www.w3.org/2000/svg"
        className="select-none"
      >
        {arcs.map(({ seg, start, end, color, lx, ly, pct, spanDeg }, i) => (
          <g key={i}>
            <path
              d={describeArc(cx, cy, outerR, innerR, start, end)}
              fill={color}
              opacity={0.85}
              stroke="rgba(0,0,0,0.3)"
              strokeWidth={1}
            />
            {/* Show percentage label if segment is large enough */}
            {spanDeg > 25 && (
              <text
                x={lx}
                y={ly + 4}
                textAnchor="middle"
                fill="#ffffff"
                fontSize={11}
                fontWeight={600}
              >
                {pct}%
              </text>
            )}
          </g>
        ))}

        {/* Center text */}
        {(centerLabel || centerValue) && (
          <>
            {centerValue && (
              <text
                x={cx}
                y={cy + (centerLabel ? -2 : 5)}
                textAnchor="middle"
                fill="#ffffff"
                fontSize={18}
                fontWeight={700}
              >
                {centerValue}
              </text>
            )}
            {centerLabel && (
              <text
                x={cx}
                y={cy + 16}
                textAnchor="middle"
                fill="#94a3b8"
                fontSize={11}
              >
                {centerLabel}
              </text>
            )}
          </>
        )}
      </svg>

      {/* Legend */}
      <div className="flex flex-col gap-1.5">
        {arcs.map(({ seg, color, pct }, i) => (
          <div key={i} className="flex items-center gap-2 text-sm">
            <span
              className="inline-block w-3 h-3 rounded-sm shrink-0"
              style={{ backgroundColor: color }}
            />
            <span className="text-slate-300">{seg.label}</span>
            <span className="text-slate-500 ml-auto pl-2">{pct}%</span>
          </div>
        ))}
      </div>
    </div>
  );
}
