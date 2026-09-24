import React from "react";
import ForgeCard, { CardInset } from "../../../forge-ui/card";
import { cn } from "../../../../lib/utils";

/**
 * Card family for the cloud settings sections — a thin shadcn-shaped
 * adapter (Card / CardHeader / CardTitle / CardContent, the names these
 * sections import) over forge-ui's Card.
 *
 * THE ELEVATION RULE AND THE HEADING LADDER LIVE IN ONE PLACE:
 * `components/forge-ui/card.tsx`, which is forge's component-library card
 * installed verbatim. They used to be written out here, and that was the
 * best-reasoned copy — so they were upstreamed into forge rather than kept
 * as a second, drifting one. In reliant's tokens the rule reads:
 *
 *   page                     bg-background
 *   primary surface          bg-card + border-border        (<Card>)
 *   inset inside a surface   bg-background + border-border/60 (<CardInset>)
 *   never nest a card inside a card
 *   bg-muted is INTERACTION state (hover, selected, disabled) — not structure
 *
 * because the `.forge-ui` token bridge in src/index.css maps forge's
 * `surface` → `--card` and `surface-sunken` → `--background`, and
 * `--background` is the only reliant token that recesses below `--card` in
 * every scheme, light and dark.
 *
 * What stays here is only the shadcn-shaped layout that forge's card does not
 * have (forge's CardHeader takes a `title` prop; these sections compose a
 * CardTitle child). The SURFACE — border, radius, fill, shadow — is forge's.
 */
export interface CardProps extends React.HTMLAttributes<HTMLDivElement> {
  interactive?: boolean;
}

export function Card({ interactive, className, children, ...rest }: CardProps) {
  return (
    <ForgeCard
      padding="none"
      interactive={interactive}
      // min-w-0 so a card inside a grid/flex track can shrink; text colour
      // because these sections render reliant text inside it.
      className={cn("min-w-0 text-card-foreground", className)}
      {...rest}
    >
      {children}
    </ForgeCard>
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

/** The PANEL HEADING rung of the ladder in forge-ui/card.tsx. */
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

export { CardInset };

export default Card;
