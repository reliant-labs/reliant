import React from "react";

/**
 * QuadrantChart — A 2x2 quadrant scatter plot.
 *
 * Place items on a normalized 0–1 coordinate system.
 * The component handles all pixel math, axis rendering,
 * quadrant shading, midlines, arrows, and label positioning.
 *
 * Usage:
 * ```tsx
 * <QuadrantChart
 *   xLabels={["Low Effort", "High Effort"]}
 *   yLabels={["Low Impact", "High Impact"]}
 *   items={[
 *     { name: "Quick Win", x: 0.2, y: 0.8, highlight: true },
 *     { name: "Big Bet", x: 0.8, y: 0.9 },
 *     { name: "Fill-In", x: 0.2, y: 0.2 },
 *     { name: "Money Pit", sub: "$2M spent", note: "Kill this", x: 0.75, y: 0.15 },
 *   ]}
 * />
 * ```
 */

interface QuadrantChartProps {
  /** Chart title */
  title?: string;
  /** X-axis labels [left, right] */
  xLabels: [string, string];
  /** Y-axis labels [bottom, top] */
  yLabels: [string, string];
  /** Items to plot */
  items: Array<{
    /** Display name */
    name: string;
    /** Optional subtitle (e.g., valuation) */
    sub?: string;
    /** Optional note below (e.g., "Can't write code") */
    note?: string;
    /** X position 0-1 (0=left, 1=right) */
    x: number;
    /** Y position 0-1 (0=bottom, 1=top) */
    y: number;
    /** Whether to highlight this item (gradient glow) */
    highlight?: boolean;
  }>;
  /** Chart width in px (default 800) */
  width?: number;
  /** Chart height in px (default 450) */
  height?: number;
  /** Accent color for highlighted items (default "#a855f7") */
  accentColor?: string;
}

const PADDING = { top: 40, right: 30, bottom: 50, left: 60 };
const ARROW_SIZE = 8;

export default function QuadrantChart({
  title,
  xLabels,
  yLabels,
  items,
  width = 800,
  height = 450,
  accentColor = "#a855f7",
}: QuadrantChartProps) {
  const plotLeft = PADDING.left;
  const plotTop = PADDING.top;
  const plotW = width - PADDING.left - PADDING.right;
  const plotH = height - PADDING.top - PADDING.bottom;
  const plotRight = plotLeft + plotW;
  const plotBottom = plotTop + plotH;

  const midX = plotLeft + plotW / 2;
  const midY = plotTop + plotH / 2;

  /** Convert normalized 0-1 coords to SVG pixel coords */
  const toPixel = (nx: number, ny: number): [number, number] => [
    plotLeft + nx * plotW,
    plotBottom - ny * plotH, // invert Y: 0=bottom, 1=top
  ];

  const glowId = "quadrant-glow";

  return (
    <div className="inline-block" style={{ width }}>
      {title && (
        <h3 className="text-lg font-semibold text-white mb-2 text-center">
          {title}
        </h3>
      )}
      <svg
        width={width}
        height={height}
        viewBox={`0 0 ${width} ${height}`}
        xmlns="http://www.w3.org/2000/svg"
        className="select-none"
      >
        <defs>
          {/* Glow filter for highlighted items */}
          <filter id={glowId} x="-50%" y="-50%" width="200%" height="200%">
            <feGaussianBlur stdDeviation="6" result="blur" />
            <feMerge>
              <feMergeNode in="blur" />
              <feMergeNode in="SourceGraphic" />
            </feMerge>
          </filter>
          {/* Arrow marker */}
          <marker
            id="arrow"
            viewBox={`0 0 ${ARROW_SIZE} ${ARROW_SIZE}`}
            refX={ARROW_SIZE}
            refY={ARROW_SIZE / 2}
            markerWidth={ARROW_SIZE}
            markerHeight={ARROW_SIZE}
            orient="auto-start-reverse"
          >
            <path
              d={`M 0 0 L ${ARROW_SIZE} ${ARROW_SIZE / 2} L 0 ${ARROW_SIZE} z`}
              fill="#94a3b8"
            />
          </marker>
        </defs>

        {/* Quadrant shading — subtle alternating fills */}
        <rect
          x={plotLeft}
          y={plotTop}
          width={plotW / 2}
          height={plotH / 2}
          fill="rgba(255,255,255,0.02)"
        />
        <rect
          x={midX}
          y={plotTop}
          width={plotW / 2}
          height={plotH / 2}
          fill="rgba(255,255,255,0.05)"
        />
        <rect
          x={plotLeft}
          y={midY}
          width={plotW / 2}
          height={plotH / 2}
          fill="rgba(255,255,255,0.05)"
        />
        <rect
          x={midX}
          y={midY}
          width={plotW / 2}
          height={plotH / 2}
          fill="rgba(255,255,255,0.02)"
        />

        {/* Dashed midlines */}
        <line
          x1={midX}
          y1={plotTop}
          x2={midX}
          y2={plotBottom}
          stroke="#475569"
          strokeWidth={1}
          strokeDasharray="6 4"
        />
        <line
          x1={plotLeft}
          y1={midY}
          x2={plotRight}
          y2={midY}
          stroke="#475569"
          strokeWidth={1}
          strokeDasharray="6 4"
        />

        {/* X axis with arrow */}
        <line
          x1={plotLeft}
          y1={plotBottom}
          x2={plotRight}
          y2={plotBottom}
          stroke="#94a3b8"
          strokeWidth={1.5}
          markerEnd="url(#arrow)"
        />
        {/* Y axis with arrow */}
        <line
          x1={plotLeft}
          y1={plotBottom}
          x2={plotLeft}
          y2={plotTop}
          stroke="#94a3b8"
          strokeWidth={1.5}
          markerEnd="url(#arrow)"
        />

        {/* X-axis labels */}
        <text
          x={plotLeft + 8}
          y={plotBottom + 28}
          fill="#94a3b8"
          fontSize={12}
          textAnchor="start"
        >
          {xLabels[0]}
        </text>
        <text
          x={plotRight - 8}
          y={plotBottom + 28}
          fill="#94a3b8"
          fontSize={12}
          textAnchor="end"
        >
          {xLabels[1]}
        </text>

        {/* Y-axis labels (rotated) */}
        <text
          x={plotLeft - 16}
          y={plotBottom - 8}
          fill="#94a3b8"
          fontSize={12}
          textAnchor="end"
          transform={`rotate(-90, ${plotLeft - 16}, ${plotBottom - 8})`}
        >
          {yLabels[0]}
        </text>
        <text
          x={plotLeft - 16}
          y={plotTop + 8}
          fill="#94a3b8"
          fontSize={12}
          textAnchor="end"
          transform={`rotate(-90, ${plotLeft - 16}, ${plotTop + 8})`}
        >
          {yLabels[1]}
        </text>

        {/* Plot items */}
        {items.map((item, i) => {
          const [px, py] = toPixel(
            Math.max(0, Math.min(1, item.x)),
            Math.max(0, Math.min(1, item.y)),
          );
          const dotR = item.highlight ? 7 : 5;
          const dotColor = item.highlight ? accentColor : "#60a5fa";

          return (
            <g key={i}>
              {/* Dot */}
              <circle
                cx={px}
                cy={py}
                r={dotR}
                fill={dotColor}
                filter={item.highlight ? `url(#${glowId})` : undefined}
              />

              {/* Name label */}
              <text
                x={px + 12}
                y={py - 2}
                fill={item.highlight ? "#ffffff" : "#e2e8f0"}
                fontSize={item.highlight ? 13 : 12}
                fontWeight={item.highlight ? 600 : 400}
              >
                {item.name}
              </text>

              {/* Optional subtitle */}
              {item.sub && (
                <text
                  x={px + 12}
                  y={py + 13}
                  fill="#94a3b8"
                  fontSize={10}
                >
                  {item.sub}
                </text>
              )}

              {/* Optional note */}
              {item.note && (
                <text
                  x={px + 12}
                  y={py + (item.sub ? 26 : 13)}
                  fill="#f87171"
                  fontSize={10}
                  fontStyle="italic"
                >
                  {item.note}
                </text>
              )}
            </g>
          );
        })}
      </svg>
    </div>
  );
}
