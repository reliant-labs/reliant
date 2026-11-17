// Inline `style` below carries RUNTIME-computed width — a percentage that
// cannot be expressed as a static Tailwind utility. Each frontend's own
// eslint config exempts src/components/ui/** for exactly this reason; the
// exemption lives there and NOT as an in-file directive, because a
// directive naming a framework-specific rule is a hard error in any tree
// whose config does not load that plugin (a Vite SPA does not load
// eslint-plugin-react or @next/eslint-plugin-next).
import React from "react";

type ProgressVariant = "default" | "success" | "warning" | "danger";
type ProgressSize = "sm" | "md" | "lg";

interface ProgressBarProps {
  /** Current value (clamped to [0, max]). */
  value: number;
  /** Maximum value. Defaults to 100 (treat value as a percentage). */
  max?: number;
  /** Optional label rendered above the bar. Also names the bar itself. */
  label?: React.ReactNode;
  /**
   * Accessible name for the bar when no visible `label` is rendered.
   * A progressbar announces its value ("60 percent") and nothing else, so
   * without one of the two a screen-reader user hears a number with no
   * subject. Defaults to "Progress".
   */
  ariaLabel?: string;
  /** When true, render the numeric value/max on the right of the label row. */
  showValue?: boolean;
  /** Force a variant. When omitted, the bar auto-tints red >80% (quota-shape default). */
  variant?: ProgressVariant;
  size?: ProgressSize;
  className?: string;
}

const trackSize: Record<ProgressSize, string> = {
  sm: "h-1.5",
  md: "h-2",
  lg: "h-3",
};

const barTint: Record<ProgressVariant, string> = {
  default: "bg-accent",
  success: "bg-success",
  warning: "bg-warning",
  danger: "bg-danger",
};

/**
 * ProgressBar — value/max bar with optional label and auto-warning tint.
 *
 * Used in billing usage / quota / capacity displays. The default variant
 * auto-shifts to "danger" tint when fill exceeds 80% so the consumer
 * doesn't have to thread state through.
 */
export default function ProgressBar({
  value,
  max = 100,
  label,
  ariaLabel,
  showValue,
  variant,
  size = "md",
  className,
}: ProgressBarProps) {
  // Two bars on one page must not share a label id — useId per instance.
  const labelId = React.useId();
  const clampedMax = max <= 0 ? 1 : max;
  const clampedValue = Math.max(0, Math.min(value, clampedMax));
  const pct = (clampedValue / clampedMax) * 100;

  const resolvedVariant: ProgressVariant = variant ?? (pct > 80 ? "danger" : "default");

  return (
    <div className={className}>
      {(label || showValue) && (
        <div className="mb-1 flex items-baseline justify-between text-xs text-ink-muted">
          {label && <span id={labelId}>{label}</span>}
          {showValue && (
            <span className="tabular-nums text-ink-muted">
              {clampedValue}/{clampedMax}
            </span>
          )}
        </div>
      )}
      {/* The visible label names the bar when there is one; otherwise the
          caller's ariaLabel does. Reusing the rendered label beats a second
          copy of the string in aria-label, which would silently drift out of
          sync with what the eye reads. */}
      <div
        role="progressbar"
        aria-labelledby={label ? labelId : undefined}
        aria-label={label ? undefined : (ariaLabel ?? "Progress")}
        aria-valuemin={0}
        aria-valuemax={clampedMax}
        aria-valuenow={clampedValue}
        className={`w-full overflow-hidden rounded-full bg-border ${trackSize[size]}`}
      >
        <div
          className={`h-full rounded-full transition-[width] duration-300 ${barTint[resolvedVariant]}`}
          style={{ width: `${pct}%` }}
        />
      </div>
    </div>
  );
}
