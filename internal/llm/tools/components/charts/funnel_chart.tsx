import React from "react";

/**
 * FunnelChart — Vertical funnel with tapering stages.
 *
 * Pass stages from top (widest) to bottom (narrowest).
 * The component computes all trapezoid geometry, spacing,
 * alert highlights, and side annotations.
 *
 * Usage:
 * ```tsx
 * <FunnelChart
 *   stages={[
 *     { label: "Visitors", value: "10,000" },
 *     { label: "Signups", value: "3,200" },
 *     { label: "Activated", value: "1,800", note: "56% conversion" },
 *     { label: "Paid", value: "400", alert: true, note: "⚠ Drop-off here" },
 *     { label: "Enterprise", value: "45" },
 *   ]}
 * />
 * ```
 */

interface FunnelChartProps {
  /** Funnel stages from top (widest) to bottom (narrowest) */
  stages: Array<{
    /** Stage label */
    label: string;
    /** Display value */
    value: string;
    /** Flag this stage as problematic */
    alert?: boolean;
    /** Optional note shown beside this stage */
    note?: string;
  }>;
  /** Width in px (default 300) */
  width?: number;
  /** Height in px (default 400) */
  height?: number;
  /** Accent color (default "#a855f7") */
  accentColor?: string;
  /** Alert color for flagged stages (default "#ef4444") */
  alertColor?: string;
}

function hexToRgba(hex: string, alpha: number): string {
  const r = parseInt(hex.slice(1, 3), 16);
  const g = parseInt(hex.slice(3, 5), 16);
  const b = parseInt(hex.slice(5, 7), 16);
  return `rgba(${r},${g},${b},${alpha})`;
}

export default function FunnelChart({
  stages,
  width = 300,
  height = 400,
  accentColor = "#a855f7",
  alertColor = "#ef4444",
}: FunnelChartProps) {
  const n = stages.length;
  if (n === 0) return null;

  const paddingX = 16;
  const paddingTop = 12;
  const paddingBottom = 12;
  const gap = 4; // gap between trapezoids
  const noteAreaWidth = 140; // space for side notes

  const totalWidth = width + noteAreaWidth;
  const usableHeight = height - paddingTop - paddingBottom - gap * (n - 1);
  const stageH = usableHeight / n;

  const maxW = width - paddingX * 2;
  const minW = maxW * 0.25;

  /**
   * Each stage tapers linearly.
   * Stage i top width → stage i bottom width = stage i+1 top width.
   */
  const widthAt = (i: number) => maxW - (i / n) * (maxW - minW);

  const cx = width / 2; // center X of funnel area

  return (
    <svg
      width={totalWidth}
      height={height}
      viewBox={`0 0 ${totalWidth} ${height}`}
      xmlns="http://www.w3.org/2000/svg"
      className="select-none"
    >
      <defs>
        <filter id="funnel-glow" x="-20%" y="-20%" width="140%" height="140%">
          <feGaussianBlur stdDeviation="4" result="blur" />
          <feMerge>
            <feMergeNode in="blur" />
            <feMergeNode in="SourceGraphic" />
          </feMerge>
        </filter>
      </defs>

      {stages.map((stage, i) => {
        const y = paddingTop + i * (stageH + gap);

        const topW = widthAt(i);
        const botW = widthAt(i + 1);

        const topLeft = cx - topW / 2;
        const topRight = cx + topW / 2;
        const botLeft = cx - botW / 2;
        const botRight = cx + botW / 2;

        const path = [
          `M ${topLeft} ${y}`,
          `L ${topRight} ${y}`,
          `L ${botRight} ${y + stageH}`,
          `L ${botLeft} ${y + stageH}`,
          "Z",
        ].join(" ");

        const color = stage.alert ? alertColor : accentColor;
        // Opacity increases slightly toward the bottom (narrower = more intense)
        const opacity = 0.25 + (i / (n - 1 || 1)) * 0.45;

        const midY = y + stageH / 2;

        return (
          <g key={i}>
            {/* Trapezoid */}
            <path
              d={path}
              fill={hexToRgba(color, opacity)}
              stroke={hexToRgba(color, opacity + 0.15)}
              strokeWidth={1}
              filter={stage.alert ? "url(#funnel-glow)" : undefined}
            />

            {/* Label + value centered in trapezoid */}
            <text
              x={cx}
              y={midY - 4}
              textAnchor="middle"
              fill="#ffffff"
              fontSize={12}
              fontWeight={600}
            >
              {stage.label}
            </text>
            <text
              x={cx}
              y={midY + 12}
              textAnchor="middle"
              fill="#cbd5e1"
              fontSize={11}
            >
              {stage.value}
            </text>

            {/* Side note */}
            {stage.note && (
              <>
                <line
                  x1={topRight + 4}
                  y1={midY}
                  x2={width + 8}
                  y2={midY}
                  stroke="#475569"
                  strokeWidth={0.75}
                  strokeDasharray="3 2"
                />
                <text
                  x={width + 14}
                  y={midY + 4}
                  fill={stage.alert ? alertColor : "#94a3b8"}
                  fontSize={11}
                >
                  {stage.note}
                </text>
              </>
            )}
          </g>
        );
      })}
    </svg>
  );
}
