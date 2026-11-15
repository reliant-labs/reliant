import React from "react";

/**
 * Chip — small removable tag primitive. Distinct from <Badge> (which is
 * status-shaped, success/warning/error etc.); Chip is the building block
 * for filter pills, selected tokens, and other "active selection" UI.
 *
 * Pass `onRemove` to render a built-in dismiss button.
 */
export type ChipVariant = "neutral" | "primary" | "muted";
export type ChipSize = "sm" | "md";

export interface ChipProps {
  label: React.ReactNode;
  variant?: ChipVariant;
  size?: ChipSize;
  /** Render an "x" button; called when clicked. */
  onRemove?: () => void;
  className?: string;
}

const variantStyles: Record<ChipVariant, string> = {
  neutral: "bg-surface-muted text-ink hover:bg-border",
  primary: "bg-accent-surface text-accent-ink hover:bg-accent-border",
  muted: "bg-transparent text-ink-muted ring-1 ring-inset ring-border-strong",
};

const sizeStyles: Record<ChipSize, string> = {
  sm: "px-2 py-0.5 text-[11px]",
  md: "px-2.5 py-1 text-xs",
};

export default function Chip({
  label,
  variant = "primary",
  size = "sm",
  onRemove,
  className,
}: ChipProps) {
  const composed = [
    "inline-flex items-center gap-1 rounded-full font-medium transition-colors",
    variantStyles[variant],
    sizeStyles[size],
    className ?? "",
  ]
    .filter(Boolean)
    .join(" ");

  return (
    <span className={composed}>
      {label}
      {onRemove ? (
        <button
          type="button"
          onClick={onRemove}
          aria-label="Remove"
          className="ml-0.5 inline-flex h-3.5 w-3.5 items-center justify-center rounded-full hover:bg-black/10"
        >
          <svg
            aria-hidden
            className="h-2.5 w-2.5"
            fill="none"
            viewBox="0 0 24 24"
            strokeWidth={3}
            stroke="currentColor"
          >
            <path strokeLinecap="round" strokeLinejoin="round" d="M6 18L18 6M6 6l12 12" />
          </svg>
        </button>
      ) : null}
    </span>
  );
}
