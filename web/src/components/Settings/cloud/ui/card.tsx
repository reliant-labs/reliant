import React from "react";
import { cn } from "../../../../lib/utils";

/**
 * Card family — surface primitives for the cloud settings sections, shaped
 * as a shadcn-style Card / CardHeader / CardTitle / CardContent quartet (the
 * names the vertical agents import). Ported from admin-web's `ui/card.tsx`
 * aesthetic (bordered, rounded, subtle shadow) but rendered against
 * reliant's `bg-card` / `border-border` tokens so it flips with dark mode.
 */
export interface CardProps extends React.HTMLAttributes<HTMLDivElement> {
  interactive?: boolean;
}

export function Card({ interactive, className, children, ...rest }: CardProps) {
  return (
    <div
      className={cn(
        "rounded-lg border border-border bg-card text-card-foreground shadow-sm",
        interactive &&
          "transition-shadow hover:shadow-md focus-within:shadow-md",
        className
      )}
      {...rest}
    >
      {children}
    </div>
  );
}

export type CardHeaderProps = React.HTMLAttributes<HTMLDivElement>;

export function CardHeader({ className, children, ...rest }: CardHeaderProps) {
  return (
    <div
      className={cn(
        "flex flex-col gap-1 border-b border-border px-5 py-4",
        className
      )}
      {...rest}
    >
      {children}
    </div>
  );
}

export type CardTitleProps = React.HTMLAttributes<HTMLHeadingElement>;

export function CardTitle({ className, children, ...rest }: CardTitleProps) {
  return (
    <h3
      className={cn(
        "text-sm font-semibold leading-none tracking-tight text-foreground",
        className
      )}
      {...rest}
    >
      {children}
    </h3>
  );
}

export type CardContentProps = React.HTMLAttributes<HTMLDivElement>;

export function CardContent({ className, children, ...rest }: CardContentProps) {
  return (
    <div className={cn("px-5 py-4 text-sm text-foreground", className)} {...rest}>
      {children}
    </div>
  );
}

export default Card;
