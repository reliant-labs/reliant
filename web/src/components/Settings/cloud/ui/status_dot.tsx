import React from "react";
import { cn } from "../../../../lib/utils";

/**
 * StatusDot — colored dot plus optional label for compact status display in
 * dense table cells. Ported from admin-web's `ui/status_dot.tsx`; the color
 * map is reliant semantic tokens so it reads in light and dark.
 */
export type StatusDotVariant =
  | "active"
  | "paused"
  | "pending"
  | "error"
  | "warning"
  | "neutral";
export type StatusDotSize = "sm" | "md" | "lg";

export interface StatusDotProps {
  variant?: StatusDotVariant;
  label?: React.ReactNode;
  size?: StatusDotSize;
  pulse?: boolean;
  className?: string;
}

const dotTint: Record<StatusDotVariant, string> = {
  active: "bg-success",
  paused: "bg-warning",
  pending: "bg-info",
  error: "bg-destructive",
  warning: "bg-warning",
  neutral: "bg-muted-foreground",
};

const labelTint: Record<StatusDotVariant, string> = {
  active: "text-success",
  paused: "text-warning",
  pending: "text-info",
  error: "text-destructive",
  warning: "text-warning",
  neutral: "text-muted-foreground",
};

const dotSize: Record<StatusDotSize, string> = {
  sm: "h-1.5 w-1.5",
  md: "h-2 w-2",
  lg: "h-2.5 w-2.5",
};

export function StatusDot({
  variant = "neutral",
  label,
  size = "md",
  pulse,
  className,
}: StatusDotProps) {
  return (
    <span className={cn("inline-flex items-center gap-1.5", className)}>
      <span
        aria-hidden="true"
        className={cn(
          "inline-block rounded-full",
          dotTint[variant],
          dotSize[size],
          pulse && "animate-pulse"
        )}
      />
      {label !== undefined && (
        <span className={cn("text-xs font-medium", labelTint[variant])}>
          {label}
        </span>
      )}
    </span>
  );
}

export default StatusDot;
