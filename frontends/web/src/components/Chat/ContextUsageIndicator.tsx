import { memo, useMemo } from "react";
import { cn } from "../../lib/utils";
import { Tooltip } from "../ui/Tooltip";

interface ContextUsageIndicatorProps {
  /** Current token count in thread */
  threadTokenCount: number;
  /** Threshold at which compaction triggers (default: 200000) */
  compactionThreshold?: number;
  /** Whether compaction is currently in progress */
  isCompacting?: boolean;
  /** Compact mode for smaller displays */
  compact?: boolean;
}

/**
 * ContextUsageIndicator - Circular progress indicator showing context usage
 *
 * Shows the percentage of context window used with a consistent primary color.
 */
export const ContextUsageIndicator = memo(function ContextUsageIndicator({
  threadTokenCount,
  compactionThreshold = 200000,
  isCompacting = false,
  compact: _compact = false,
}: ContextUsageIndicatorProps) {
  // Calculate usage percentage
  const usagePercent = useMemo(() => {
    if (compactionThreshold <= 0) return 0;
    return Math.min((threadTokenCount / compactionThreshold) * 100, 100);
  }, [threadTokenCount, compactionThreshold]);

  // Circle SVG properties - match scroll button size (w-6 h-6 = 24px)
  const size = 20;
  const strokeWidth = 2.5;
  const radius = (size - strokeWidth) / 2;
  const circumference = 2 * Math.PI * radius;
  const strokeDashoffset = circumference - (usagePercent / 100) * circumference;

  // Use consistent primary color throughout
  const strokeColor = "hsl(var(--primary))";
  const textClass = "text-primary";

  // Format token counts for tooltip
  const formatTokens = (count: number) => {
    if (count >= 1000) {
      return `${(count / 1000).toFixed(1)}k`;
    }
    return count.toString();
  };

  const tooltipContent = isCompacting
    ? "Compacting context..."
    : threadTokenCount === 0
    ? "No context usage yet"
    : `Context: ${usagePercent.toFixed(0)}% used (${formatTokens(
        threadTokenCount
      )}/${formatTokens(compactionThreshold)} tokens)`;

  return (
    <Tooltip content={tooltipContent} placement="bottom">
      <div
        className={cn(
          "relative flex items-center justify-center transition-all duration-200 flex-shrink-0",
          "w-6 h-6", // Match scroll button size
          isCompacting && "animate-pulse"
        )}
        aria-label={tooltipContent}
      >
        {/* SVG Circle Progress */}
        <svg width={size} height={size} className="transform -rotate-90">
          {/* Background circle */}
          <circle
            cx={size / 2}
            cy={size / 2}
            r={radius}
            fill="none"
            stroke="currentColor"
            strokeWidth={strokeWidth}
            className="opacity-20"
          />
          {/* Progress circle */}
          <circle
            cx={size / 2}
            cy={size / 2}
            r={radius}
            fill="none"
            stroke={strokeColor}
            strokeWidth={strokeWidth}
            strokeLinecap="round"
            strokeDasharray={circumference}
            strokeDashoffset={strokeDashoffset}
            className="transition-all duration-500 ease-out"
          />
        </svg>

        {/* Center text showing percentage. px, not a scale step: it is centred
            inside a fixed-size SVG ring and must not grow with the font-size
            preference or it overflows the circle. */}
        <span
          className={cn(
            "absolute inset-0 flex items-center justify-center font-medium",
            textClass,
            // eslint-disable-next-line no-restricted-syntax
            "text-[7px]"
          )}
        >
          {threadTokenCount > 0 ? `${Math.round(usagePercent)}` : "—"}
        </span>
      </div>
    </Tooltip>
  );
});
