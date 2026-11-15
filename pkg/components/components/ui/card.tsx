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
 *   page                     bg-surface-muted (the app shell behind panels)
 *   primary surface          <Card>          bg-surface + border-border, NO shadow
 *   inset inside a surface   <CardInset>     bg-surface-sunken + border-border/60
 *   overlays only            menus, modals, drawers — the only things that cast a shadow
 *   never nest a card inside a card — use an inset, or a flush table
 *
 * One surface level. A panel is told apart from the page by its border and
 * its fill, never by a shadow: a shadow on every panel makes them float over
 * each other, and then the shadow can no longer say "this is on top" for the
 * menu or dialog that really is.
 *
 * Hover and selection are an ink wash (`bg-ink/5`, `bg-ink/8`), not a
 * surface token: it darkens a light surface and lightens a dark one, which is
 * what "pressed" means in both modes, whatever the palette maps the surface
 * tokens to.
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
 * THE HEADING LADDER — sentence case, three rungs, and only three
 * ──────────────────────────────────────────────────────────────────────
 *
 *   page heading   `<PageHeader>`, one per screen
 *                  text-xl font-semibold tracking-tight text-ink
 *   panel heading  the title of a self-contained panel — `<CardHeader title>`
 *                  text-sm font-medium text-ink
 *   section label  a label grouping rows WITHIN a panel, a table header, a
 *                  caption: text-xs text-ink-muted
 *
 * Never `uppercase tracking-wider`. Small caps on every label and header make
 * the chrome shout over the data, and they were the single most dated thing
 * in the old ladder. Sentence case, muted, one size down does the same job.
 *
 * One rung below the ladder: a STAT CAPTION — the word above a single number
 * in a tile, `text-xs text-ink-muted`, over a figure in
 * `text-xl font-semibold tabular-nums`.
 *
 * Spacing uses one scale: 4 / 8 / 12 / 16 / 24 / 32 (`1 2 3 4 6 8`). Panel
 * padding is 16, panels sit 16 apart, page sections 32.
 */
export type CardPadding = "none" | "sm" | "md" | "lg";

export interface CardProps extends React.HTMLAttributes<HTMLDivElement> {
  /** Inner padding. Defaults to "md". */
  padding?: CardPadding;
  /** Clickable card: the border strengthens on hover and keyboard focus. */
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
    "rounded-lg border border-border bg-surface",
    paddingStyles[padding],
    interactive ? "transition-colors hover:border-ink-subtle focus-within:border-ink-subtle" : "",
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
 *
 * No rule under it: the title is part of the panel, not a band on top of it.
 * A divider plus its padding stacked ~30px of chrome above every panel's
 * first line of content.
 */
export interface CardHeaderProps {
  title?: React.ReactNode;
  description?: React.ReactNode;
  actions?: React.ReactNode;
  className?: string;
  children?: React.ReactNode;
}

export function CardHeader({ title, description, actions, className, children }: CardHeaderProps) {
  const composed = ["mb-3 flex items-start justify-between gap-3", className ?? ""]
    .filter(Boolean)
    .join(" ");
  return (
    <div className={composed}>
      <div className="min-w-0">
        {title ? <div className="text-sm font-medium text-ink">{title}</div> : null}
        {description ? <div className="mt-0.5 text-xs text-ink-muted">{description}</div> : null}
        {children}
      </div>
      {actions ? <div className="flex items-center gap-2">{actions}</div> : null}
    </div>
  );
}

export interface CardBodyProps {
  className?: string;
  children: React.ReactNode;
}

export function CardBody({ className, children }: CardBodyProps) {
  const composed = ["text-sm text-ink", className ?? ""].filter(Boolean).join(" ");
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

export function CardInset({ padding = "sm", className, children, ...rest }: CardInsetProps) {
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
