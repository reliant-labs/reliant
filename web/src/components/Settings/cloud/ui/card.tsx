import React from "react";
import { cn } from "../../../../lib/utils";

/**
 * Card family — surface primitives for the cloud settings sections, shaped
 * as a shadcn-style Card / CardHeader / CardTitle / CardContent quartet (the
 * names the vertical agents import). Ported from admin-web's `ui/card.tsx`
 * aesthetic (bordered, rounded, subtle shadow) but rendered against
 * reliant's `bg-card` / `border-border` tokens so it flips with dark mode.
 *
 * ──────────────────────────────────────────────────────────────────────
 * THE ELEVATION RULE — and the inversion that makes it non-obvious
 * ──────────────────────────────────────────────────────────────────────
 *
 *   page                     bg-background
 *   primary surface          bg-card + border-border
 *   inset inside a surface   bg-background + border-border/60
 *   never nest a card inside a card
 *   bg-muted is INTERACTION state (hover, selected, disabled) — not structure
 *
 * The reason to write this down is that the obvious choice for an inset —
 * `bg-muted`, possibly at an alpha — is wrong, and wrong in a way that only
 * shows up in half the themes. `--muted` moves in OPPOSITE directions between
 * light and dark (`themes/professional-themes.css`, all seven schemes):
 *
 *   |              | dark schemes          | light schemes          |
 *   |--------------|-----------------------|------------------------|
 *   | --background | 11–14% (0% pure-black)| 97–98%                 |
 *   | --card       | 17–20% (10%)          | 100% — lightest        |
 *   | --muted      | 21–24% (15%)          | 94–96%                 |
 *   | --popover    | 22–25% (16%)          | 100%                   |
 *
 * So `bg-muted` RECESSES in light mode and LIFTS in dark mode. A `bg-muted/40`
 * inset inside a `bg-card` section therefore reads as a well to whoever built
 * it on a light theme and as a faint floating slab on a dark one — which is
 * exactly the "nested cards with no contrast" report that prompted this pass.
 * At 40% alpha over a near-identical parent it is barely visible at all.
 *
 * `bg-background` is the only token that recesses in BOTH modes: six points of
 * separation below the card in dark, two in light. `--config-input-bg: 0 0% 6%`
 * in the pure-black scheme is the same idea already in the tokens — a well that
 * sits below the card, not above it.
 *
 * THE BORDER IS LOAD-BEARING, not decoration. Light mode gives an inset only
 * two points of luminance to work with, so `border-border/60` is what actually
 * makes the well read there. Do not drop it as "subtle enough without".
 *
 * ──────────────────────────────────────────────────────────────────────
 * THE HEADING LADDER — three rungs, and only three
 * ──────────────────────────────────────────────────────────────────────
 *
 *   page heading   <PageHeader>, one per screen
 *                  text-2xl font-semibold tracking-tight text-foreground
 *
 *   panel heading  the title of a self-contained panel the user reads as a
 *                  thing — "Invoices", "Choose your machine", <CardTitle>
 *                  text-sm font-semibold ... text-foreground
 *
 *   section label  a label grouping rows WITHIN a panel — "Compute",
 *                  "Pay with card", "Or use a code", "Cloud machines"
 *                  text-xs font-semibold uppercase tracking-wide
 *                  text-muted-foreground
 *
 * One rung below the ladder, not part of it: a STAT CAPTION — the word above a
 * single number in a tile ("Used", "Monthly cap"). It is lighter than a section
 * label on purpose, because it must not compete with the figure beneath it:
 *   text-xs font-medium uppercase tracking-wide text-muted-foreground
 * Table column headers keep `tracking-wider` in `ui/table.tsx`; a column head
 * is a table affordance, not a heading, and it reads across a row of siblings.
 *
 * These billing surfaces previously used three different weights for the same
 * level (`text-base font-semibold` h3, `CardTitle`, and two spellings of the
 * uppercase label differing in `font-medium`/`font-semibold` and
 * `tracking-wide`/`tracking-wider`), so nothing told the reader which level
 * they were looking at. The class strings are copied rather than exported as
 * a component because `components/Billing/` is consumed BY this directory and
 * by OnboardingFlow — a shared primitive here would invert that dependency.
 */
export interface CardProps extends React.HTMLAttributes<HTMLDivElement> {
  interactive?: boolean;
}

export function Card({ interactive, className, children, ...rest }: CardProps) {
  return (
    <div
      className={cn(
        "min-w-0 rounded-lg border border-border bg-card text-card-foreground shadow-sm",
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
