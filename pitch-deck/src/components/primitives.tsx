import type { ReactNode } from "react";

/**
 * Small shared building blocks used across layouts. Keeping them here avoids
 * repeating the eyebrow/title/footnote treatment in every layout component.
 */

/** Uppercase tracked label above a title. */
export function Eyebrow({ children }: { children: ReactNode }) {
  return (
    <p className="text-sm font-medium uppercase tracking-widest text-brand-blue">
      {children}
    </p>
  );
}

/**
 * Slide title. Supports embedded newlines (\n) so data can control line breaks.
 * `as` lets callers keep a single h1 per slide for heading hierarchy.
 */
export function Title({
  children,
  className = "",
  gradient = false,
}: {
  children: string;
  className?: string;
  gradient?: boolean;
}) {
  return (
    <h1
      className={`whitespace-pre-line font-bold tracking-tight text-foreground ${
        gradient ? "text-gradient" : ""
      } ${className}`}
    >
      {children}
    </h1>
  );
}

/** Small-print caveat/source line pinned near the bottom of a slide. */
export function Footnote({ children }: { children: ReactNode }) {
  return (
    <p className="max-w-3xl text-xs leading-relaxed text-muted">{children}</p>
  );
}

/** A thin brand-gradient rule used as a divider accent. */
export function GradientRule({ className = "" }: { className?: string }) {
  return (
    <div
      className={`h-1 w-16 rounded-full bg-brand-gradient ${className}`}
      aria-hidden="true"
    />
  );
}
