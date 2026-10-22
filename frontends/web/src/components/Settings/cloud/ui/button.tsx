import React from "react";
import { cn } from "../../../../lib/utils";

/**
 * Button — action primitive for the cloud settings sections.
 *
 * Ported from admin-web's `ui/button.tsx` but re-implemented against
 * reliant's CSS-variable theme tokens (bg-primary / bg-muted / border-border
 * …) so both the light and dark reliant themes render correctly. admin-web
 * hardcoded `bg-blue-600` / `bg-gray-100`; here every color is a semantic
 * token that flips with the global `.dark` class.
 *
 * Variants: primary | secondary | outline | ghost | danger.
 * Sizes:    sm | md | lg.
 */
export type ButtonVariant =
  | "primary"
  | "secondary"
  | "outline"
  | "ghost"
  | "danger";
export type ButtonSize = "sm" | "md" | "lg";

export interface ButtonProps
  extends React.ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: ButtonVariant;
  size?: ButtonSize;
  fullWidth?: boolean;
  isLoading?: boolean;
}

const variantStyles: Record<ButtonVariant, string> = {
  primary:
    "bg-primary text-primary-foreground shadow-sm hover:bg-primary/90 disabled:hover:bg-primary",
  secondary:
    "bg-muted text-foreground shadow-sm hover:bg-muted/70 disabled:hover:bg-muted",
  outline:
    "border border-border bg-card text-foreground shadow-sm hover:bg-muted disabled:hover:bg-card",
  ghost:
    "bg-transparent text-foreground hover:bg-muted disabled:hover:bg-transparent",
  danger:
    "bg-destructive text-destructive-foreground shadow-sm hover:bg-destructive/90 disabled:hover:bg-destructive",
};

const sizeStyles: Record<ButtonSize, string> = {
  sm: "h-8 px-3 text-xs",
  md: "h-10 px-4 text-sm",
  lg: "h-12 px-6 text-base",
};

export function Button({
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
  return (
    <button
      type={type ?? "button"}
      disabled={disabled || isLoading}
      className={cn(
        "inline-flex items-center justify-center gap-2 rounded-md font-medium transition-colors",
        "focus:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-1 focus-visible:ring-offset-background",
        "disabled:cursor-not-allowed disabled:opacity-60",
        variantStyles[variant],
        sizeStyles[size],
        fullWidth && "w-full",
        className
      )}
      {...rest}
    >
      {isLoading ? (
        <span
          aria-hidden
          className="inline-block h-3.5 w-3.5 animate-spin rounded-full border-2 border-current border-t-transparent"
        />
      ) : null}
      {children}
    </button>
  );
}

export default Button;
