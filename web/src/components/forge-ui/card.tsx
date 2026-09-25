import React from "react";

/**
 * Card — generic surface primitive. A bordered, rounded panel with optional
 * padding. Compose with `<CardHeader>`, `<CardBody>`, `<CardFooter>` for the
 * common shapes, `<CardInset>` for a recessed well inside it, or pass
 * children directly for a raw surface.
 *
 * Distinct from `<MetricCard>` / `<StatGrid>` (which are layout-bound
 * domain components); this is the bare building block.
 *
 * ──────────────────────────────────────────────────────────────────────
 * THE ELEVATION RULE — and the inversion that makes it non-obvious
 * ──────────────────────────────────────────────────────────────────────
 *
 *   page                     bg-surface (the app shell)
 *   primary surface          <Card>          bg-surface + border-border
 *   inset inside a surface   <CardInset>     bg-surface-sunken + border-border/60
 *   never nest a card inside a card — use an inset
 *   bg-surface-muted is a SUBTLE / INTERACTION fill (hover, a selected tab,
 *   a table head) — not structure
 *
 * The obvious choice for an inset — `bg-surface-muted`, possibly at an
 * alpha — is wrong, and wrong in a way that only shows up in half the
 * themes: the scaffolded dark palette puts surface-muted ABOVE surface
 * (lighter), the light palette puts it below. So a surface-muted well
 * recesses on a light theme and lifts on a dark one — "nested cards with no
 * contrast" on whichever mode the author did not build on. The same trap
 * exists in any shadcn-style theme, where `--muted` moves in opposite
 * directions between light and dark.
 *
 * `surface-sunken` is the one token declared BELOW surface in every
 * palette; forge's palette contract test
 * (TestScaffoldedPaletteHasARecessThatRecessesInBothModes) pins that
 * ordering. If you retheme, keep it true.
 *
 * THE BORDER IS LOAD-BEARING, not decoration. A light palette gives an inset
 * only a few points of lightness to work with, so the border is what makes
 * the well read there. Do not drop it as "subtle enough without".
 *
 * ──────────────────────────────────────────────────────────────────────
 * THE HEADING LADDER — three rungs, and only three
 * ──────────────────────────────────────────────────────────────────────
 *
 *   page heading   `<PageHeader>`, one per screen
 *   panel heading  the title of a self-contained panel — `<CardHeader title>`
 *                  text-sm font-semibold text-ink
 *   section label  a label grouping rows WITHIN a panel
 *                  text-xs font-semibold uppercase tracking-wide text-ink-muted
 *
 * One rung below the ladder: a STAT CAPTION — the word above a single number
 * in a tile. Lighter than a section label on purpose, so it does not compete
 * with the figure beneath it:
 *   text-xs font-medium uppercase tracking-wide text-ink-muted
 */
export type CardPadding = "none" | "sm" | "md" | "lg";

export interface CardProps extends React.HTMLAttributes<HTMLDivElement> {
  /** Inner padding. Defaults to "md". */
  padding?: CardPadding;
  /** Render with a hover lift — useful for clickable cards. */
  interactive?: boolean;
}

const paddingStyles: Record<CardPadding, string> = {
  none: "",
  sm: "p-3",
  md: "p-4",
  lg: "p-6",
};

export default function Card({
  padding = "md",
  interactive,
  className,
  children,
  ...rest
}: CardProps) {
  const composed = [
    "rounded-lg border border-border bg-surface shadow-sm",
    paddingStyles[padding],
    interactive
      ? "transition-shadow hover:shadow-md focus-within:shadow-md"
      : "",
    className ?? "",
  ]
    .filter(Boolean)
    .join(" ");
  return (
    <div className={composed} {...rest}>
      {children}
    </div>
  );
}

/**
 * CardHeader — top section of a card with optional title/description.
 * Pass `actions` for a right-aligned action cluster.
 */
export interface CardHeaderProps {
  title?: React.ReactNode;
  description?: React.ReactNode;
  actions?: React.ReactNode;
  className?: string;
  children?: React.ReactNode;
}

export function CardHeader({
  title,
  description,
  actions,
  className,
  children,
}: CardHeaderProps) {
  const composed = [
    "flex items-start justify-between gap-3 border-b border-border pb-3 mb-3",
    className ?? "",
  ]
    .filter(Boolean)
    .join(" ");
  return (
    <div className={composed}>
      <div>
        {title ? (
          <div className="text-sm font-semibold text-ink">{title}</div>
        ) : null}
        {description ? (
          <div className="mt-0.5 text-xs text-ink-muted">{description}</div>
        ) : null}
        {children}
      </div>
      {actions ? (
        <div className="flex items-center gap-2">{actions}</div>
      ) : null}
    </div>
  );
}

export interface CardBodyProps {
  className?: string;
  children: React.ReactNode;
}

export function CardBody({ className, children }: CardBodyProps) {
  const composed = ["text-sm text-ink", className ?? ""]
    .filter(Boolean)
    .join(" ");
  return <div className={composed}>{children}</div>;
}

export interface CardFooterProps {
  className?: string;
  children: React.ReactNode;
}

export function CardFooter({ className, children }: CardFooterProps) {
  const composed = [
    "mt-3 flex items-center justify-end gap-2 border-t border-border pt-3",
    className ?? "",
  ]
    .filter(Boolean)
    .join(" ");
  return <div className={composed}>{children}</div>;
}

/**
 * CardInset — a recessed well INSIDE a card: a code block, a nested list, a
 * key/value group that must read as belonging to the card rather than as a
 * second card. See the elevation rule at the top of this file for why this is
 * `surface-sunken` and never `surface-muted`, and why the border stays.
 */
export interface CardInsetProps extends React.HTMLAttributes<HTMLDivElement> {
  /** Inner padding. Defaults to "sm". */
  padding?: CardPadding;
}

export function CardInset({
  padding = "sm",
  className,
  children,
  ...rest
}: CardInsetProps) {
  const composed = [
    "rounded-md border border-border/60 bg-surface-sunken",
    paddingStyles[padding],
    className ?? "",
  ]
    .filter(Boolean)
    .join(" ");
  return (
    <div className={composed} {...rest}>
      {children}
    </div>
  );
}
