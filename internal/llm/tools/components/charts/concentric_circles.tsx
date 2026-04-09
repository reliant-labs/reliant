import React from "react";

/**
 * ConcentricCircles — Nested rings for TAM/SAM/SOM or layered data.
 *
 * Pass rings from outermost to innermost. The component handles
 * all sizing, spacing, label positioning within visible bands,
 * and increasing opacity toward center.
 *
 * Usage:
 * ```tsx
 * <ConcentricCircles
 *   rings={[
 *     { label: "TAM", value: "$45B+", description: "AI Dev Tools by 2030" },
 *     { label: "SAM", value: "$12B", description: "Enterprise AI Tooling" },
 *     { label: "SOM", value: "$800M", description: "First 3 Years" },
 *   ]}
 * />
 * ```
 */

interface ConcentricCirclesProps {
  /** Rings from outermost to innermost */
  rings: Array<{
    /** Ring label (e.g., "TAM") */
    label: string;
    /** Value text (e.g., "$45B+") */
    value: string;
    /** Description text (e.g., "AI Dev Tools by 2030") */
    description: string;
  }>;
  /** Max diameter in px (default 380) */
  size?: number;
  /** Accent color (default "#a855f7") */
  accentColor?: string;
}

/** Convert a hex color to an rgba string. */
function hexToRgba(hex: string, alpha: number): string {
  const r = parseInt(hex.slice(1, 3), 16);
  const g = parseInt(hex.slice(3, 5), 16);
  const b = parseInt(hex.slice(5, 7), 16);
  return `rgba(${r},${g},${b},${alpha})`;
}

export default function ConcentricCircles({
  rings,
  size = 380,
  accentColor = "#a855f7",
}: ConcentricCirclesProps) {
  const n = rings.length;
  if (n === 0) return null;

  const cx = size / 2;
  const cy = size / 2;
  const maxR = size / 2 - 4; // small outer margin
  const minR = maxR * 0.18; // smallest ring keeps a visible center

  /**
   * Ring radii: evenly distributed from maxR down to minR.
   * Ring i occupies the band from radii[i] (outer) to radii[i+1] (inner).
   * The last ring's inner edge is minR.
   */
  const radii: number[] = [];
  for (let i = 0; i <= n; i++) {
    radii.push(maxR - (i / n) * (maxR - minR));
  }

  /**
   * Opacity increases toward the center.
   * Outermost ring is lightest, innermost is most saturated.
   */
  const opacityForRing = (i: number) => 0.1 + (i / (n - 1 || 1)) * 0.35;

  return (
    <div className="inline-flex flex-col items-center gap-3">
      <svg
        width={size}
        height={size}
        viewBox={`0 0 ${size} ${size}`}
        xmlns="http://www.w3.org/2000/svg"
        className="select-none"
      >
        <defs>
          {rings.map((_, i) => (
            <radialGradient
              key={`rg-${i}`}
              id={`ring-grad-${i}`}
              cx="50%"
              cy="50%"
              r="50%"
            >
              <stop
                offset="0%"
                stopColor={accentColor}
                stopOpacity={opacityForRing(i) + 0.1}
              />
              <stop
                offset="100%"
                stopColor={accentColor}
                stopOpacity={opacityForRing(i) - 0.05}
              />
            </radialGradient>
          ))}
        </defs>

        {/* Draw rings outermost to innermost so inner rings paint on top */}
        {rings.map((ring, i) => {
          const outerR = radii[i];
          const innerR = radii[i + 1];
          const bandMidR = (outerR + innerR) / 2;
          const bandThickness = outerR - innerR;

          // Position label in the visible band, at the top of the ring
          const labelY = cy - bandMidR;

          return (
            <g key={i}>
              {/* Ring background */}
              <circle
                cx={cx}
                cy={cy}
                r={outerR}
                fill={hexToRgba(accentColor, opacityForRing(i))}
                stroke={hexToRgba(accentColor, opacityForRing(i) + 0.15)}
                strokeWidth={1.5}
              />

              {/* Label + value positioned in the visible band (top arc) */}
              {bandThickness > 28 && (
                <>
                  <text
                    x={cx}
                    y={labelY - 2}
                    textAnchor="middle"
                    dominantBaseline="auto"
                    fill="#ffffff"
                    fontSize={13}
                    fontWeight={700}
                    letterSpacing="0.05em"
                  >
                    {ring.label}
                  </text>
                  <text
                    x={cx}
                    y={labelY + 14}
                    textAnchor="middle"
                    dominantBaseline="auto"
                    fill="#e2e8f0"
                    fontSize={11}
                  >
                    {ring.value}
                  </text>
                </>
              )}
            </g>
          );
        })}

        {/* Innermost circle fill (solid-ish) */}
        <circle
          cx={cx}
          cy={cy}
          r={radii[n]}
          fill={hexToRgba(accentColor, 0.55)}
        />
      </svg>

      {/* Legend below the chart */}
      <div className="flex flex-col gap-1.5">
        {rings.map((ring, i) => (
          <div key={i} className="flex items-baseline gap-2 text-sm">
            <span
              className="inline-block w-3 h-3 rounded-full shrink-0"
              style={{
                backgroundColor: hexToRgba(accentColor, opacityForRing(i) + 0.3),
              }}
            />
            <span className="text-white font-semibold">{ring.label}</span>
            <span className="text-slate-400">{ring.value}</span>
            <span className="text-slate-500">— {ring.description}</span>
          </div>
        ))}
      </div>
    </div>
  );
}
