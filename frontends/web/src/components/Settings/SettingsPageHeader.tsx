import type { ReactNode } from "react";
import { cn } from "@/lib/utils";

/**
 * SettingsPageHeader — the title block every settings section opens with.
 *
 * Settings sections used to each hand-roll their own `<h2>` with a different
 * size (text-base / text-lg / text-xl / text-2xl) and, about half the time, no
 * description at all, so a user landing on "Connectors" or "Prompts" had to
 * reverse-engineer what the page was for from its controls. This is the one
 * header: a page heading, a sentence that says what the page is FOR, and an
 * optional action cluster.
 *
 * It follows forge's heading ladder (components/forge-ui/card.tsx): this is
 * the PAGE rung, one per screen. Panels inside a section use a panel heading
 * (text-sm font-semibold), and rows within a panel use a section label
 * (text-xs uppercase tracking-wide text-muted-foreground).
 *
 * `description` is required on purpose. A section that cannot say in one
 * sentence what it is for is the defect this component exists to surface.
 */
export interface SettingsPageHeaderProps {
  title: ReactNode;
  /** One or two sentences: what this page is for, in the user's terms. */
  description: ReactNode;
  /** Optional right-aligned action cluster (buttons, a refresh control). */
  actions?: ReactNode;
  /** Optional row under the description — context chips, a scope note. */
  meta?: ReactNode;
  className?: string;
}

export function SettingsPageHeader({
  title,
  description,
  actions,
  meta,
  className,
}: SettingsPageHeaderProps) {
  return (
    <header
      className={cn(
        "mb-6 flex flex-col gap-4 border-b border-border pb-5 sm:flex-row sm:items-start sm:justify-between",
        className,
      )}
    >
      <div className="min-w-0">
        <h1 className="text-xl font-semibold tracking-tight text-foreground text-balance">
          {title}
        </h1>
        <p className="mt-1 max-w-2xl text-sm leading-relaxed text-muted-foreground text-pretty">
          {description}
        </p>
        {meta && (
          <div className="mt-3 flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
            {meta}
          </div>
        )}
      </div>
      {actions && (
        <div className="flex flex-shrink-0 flex-wrap items-center gap-2">{actions}</div>
      )}
    </header>
  );
}
