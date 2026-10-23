import React from "react";

/**
 * Button — generic action primitive used across the app and by frontend
 * packs. Tasteful Tailwind defaults; not a full design system.
 *
 * Variants: primary | secondary | outline | ghost | danger.
 * Sizes:    sm | md | lg.
 *
 * Standard <button> attributes (type, disabled, onClick, aria-*) are
 * forwarded; pass `className` to extend or override the defaults.
 */
export type ButtonVariant = "primary" | "secondary" | "outline" | "ghost" | "danger";
export type ButtonSize = "sm" | "md" | "lg";

export interface ButtonProps extends React.ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: ButtonVariant;
  size?: ButtonSize;
  fullWidth?: boolean;
  isLoading?: boolean;
}

const variantStyles: Record<ButtonVariant, string> = {
  primary: "bg-accent text-on-accent hover:bg-accent-hover disabled:hover:bg-accent",
  secondary: "bg-surface-muted text-ink hover:bg-border disabled:hover:bg-surface-muted",
  outline:
    "border border-border-strong bg-surface text-ink hover:bg-surface-muted disabled:hover:bg-surface",
  ghost: "bg-transparent text-ink hover:bg-surface-muted disabled:hover:bg-transparent",
  danger: "bg-danger text-on-danger hover:bg-danger-hover disabled:hover:bg-danger",
};

const sizeStyles: Record<ButtonSize, string> = {
  sm: "h-8 px-3 text-xs",
  md: "h-10 px-4 text-sm",
  lg: "h-12 px-6 text-base",
};

export default function Button({
  variant = "primary",
  size = "md",
  fullWidth,
  isLoading,
  disabled,
  className,
  type,
  children,
  ...rest
}: ButtonProps) {
  const composed = [
    "inline-flex items-center justify-center gap-2 rounded-md font-medium transition-colors",
    "focus:outline-none focus-visible:ring-2 focus-visible:ring-accent focus-visible:ring-offset-1",
    "disabled:cursor-not-allowed disabled:opacity-60",
    variantStyles[variant],
    sizeStyles[size],
    fullWidth ? "w-full" : "",
    className ?? "",
  ]
    .filter(Boolean)
    .join(" ");

  return (
    <button type={type ?? "button"} disabled={disabled || isLoading} className={composed} {...rest}>
      {isLoading ? (
        // Reduced motion: spinners convey "busy", so they are slowed rather
        // than frozen (WCAG 2.3.3); decorative pulses are disabled outright.
        <span
          aria-hidden
          className="inline-block h-3.5 w-3.5 animate-spin motion-reduce:[animation-duration:3s] rounded-full border-2 border-current border-t-transparent"
        />
      ) : null}
      {children}
    </button>
  );
}
