import React from "react";

interface VariationGridProps {
  /** Variations to lay out side by side. 2-3 is typical; 4 fits but gets cramped. */
  variations: Array<{
    /** Short identifier — "A", "B", "C" or "Stack", "Wizard", "Fan-out". */
    label: string;
    /** One-line description of what this option explores. Optional. */
    note?: string;
    /** The actual design — typically a component or an HTML mock. */
    content: React.ReactNode;
  }>;
  /** Section title shown above the grid. */
  title?: string;
  /** Section subtitle / context. */
  subtitle?: string;
  /** Force a column count. Default: auto from variations.length (1/2/3/4). */
  columns?: 1 | 2 | 3 | 4;
  /**
   * Fixed pixel height per artboard, or "auto" to size to content.
   * Use a fixed height when variations are meant to be visually compared at parity.
   */
  artboardHeight?: number | "auto";
}

const columnClasses: Record<number, string> = {
  1: "grid-cols-1",
  2: "grid-cols-1 lg:grid-cols-2",
  3: "grid-cols-1 md:grid-cols-2 lg:grid-cols-3",
  4: "grid-cols-1 md:grid-cols-2 lg:grid-cols-4",
};

export default function VariationGrid({
  variations,
  title,
  subtitle,
  columns,
  artboardHeight = "auto",
}: VariationGridProps) {
  const cols = columns ?? (Math.min(Math.max(variations.length, 1), 4) as 1 | 2 | 3 | 4);
  const heightStyle: React.CSSProperties =
    artboardHeight === "auto" ? {} : { height: artboardHeight };

  return (
    <section className="w-full">
      {(title || subtitle) && (
        <header className="mb-6">
          {title && <h2 className="text-xl font-semibold leading-tight text-ink">{title}</h2>}
          {subtitle && <p className="mt-1 text-sm text-ink-muted">{subtitle}</p>}
        </header>
      )}

      <div className={`grid gap-6 ${columnClasses[cols]}`}>
        {variations.map((v, i) => (
          <figure
            key={i}
            className="flex min-w-0 flex-col overflow-hidden rounded-xl border border-border bg-surface"
          >
            <figcaption className="flex items-baseline justify-between gap-3 border-b border-border px-4 py-3">
              <span className="text-sm font-semibold text-ink">{v.label}</span>
              {v.note && <span className="truncate text-xs text-ink-muted">{v.note}</span>}
            </figcaption>
            <div
              className="min-w-0 flex-1 relative overflow-auto bg-surface-muted p-4"
              style={heightStyle}
            >
              {v.content}
            </div>
          </figure>
        ))}
      </div>
    </section>
  );
}
