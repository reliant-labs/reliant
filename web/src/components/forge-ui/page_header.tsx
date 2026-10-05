import React from "react";

import Link from "./link";

interface Breadcrumb {
  label: string;
  href?: string;
}

interface Action {
  label: string;
  onClick?: () => void;
  href?: string;
  variant?: "primary" | "secondary" | "danger";
  icon?: React.ReactNode;
  /** A button action that cannot run right now. Say why in the subtitle or beside it. */
  disabled?: boolean;
}

/**
 * PageHeader — the title block at the top of a page.
 *
 * `title` and `subtitle` are ReactNode, not string, for the same reason
 * `<CardHeader title>` is: the moment a header needs a record's name beside a
 * status badge, an id in a `<code>`, or a count, a string prop cannot express
 * it and the caller reimplements the h1/subtitle typography by hand. Three
 * separate pages in one scaffold did exactly that. ReactNode is the library's
 * idiom for a slot the caller composes (Chip.label, ProgressBar.label,
 * StatusDot.label, CardHeader.title/description) — a plain string still
 * satisfies it, so every existing call site keeps working.
 */
interface PageHeaderProps {
  title: React.ReactNode;
  /**
   * Rendered beside the title, outside the h1 — status/kind badges that
   * describe the record but are not its NAME, so the heading's accessible
   * name stays the record's name.
   */
  titleAccessory?: React.ReactNode;
  subtitle?: React.ReactNode;
  breadcrumbs?: Breadcrumb[];
  actions?: Action[];
  /**
   * Replaces the default bottom margin, for a page that spaces its sections
   * itself (e.g. a `space-y-*` stack) and would otherwise get both.
   */
  className?: string;
}

export default function PageHeader({
  title,
  titleAccessory,
  subtitle,
  breadcrumbs = [],
  actions = [],
  className,
}: PageHeaderProps) {
  const variantStyles: Record<NonNullable<Action["variant"]>, string> = {
    primary: "bg-accent text-on-accent hover:bg-accent-hover shadow-sm",
    secondary:
      "border border-border-strong bg-surface text-ink hover:bg-surface-muted shadow-sm",
    danger:
      "border border-danger-border bg-surface text-danger hover:bg-danger-surface shadow-sm",
  };

  return (
    <div className={className ?? "mb-6"}>
      {breadcrumbs.length > 0 && (
        <nav aria-label="Breadcrumb" className="mb-2 flex items-center gap-1.5 text-sm text-ink-muted">
          {breadcrumbs.map((crumb, i) => (
            <React.Fragment key={i}>
              {i > 0 && (
                <svg
                  className="h-3.5 w-3.5 text-ink-subtle"
                  fill="none"
                  viewBox="0 0 24 24"
                  strokeWidth={2}
                  stroke="currentColor"
                >
                  <path
                    strokeLinecap="round"
                    strokeLinejoin="round"
                    d="M8.25 4.5l7.5 7.5-7.5 7.5"
                  />
                </svg>
              )}
              {crumb.href ? (
                <Link href={crumb.href} className="hover:text-ink">
                  {crumb.label}
                </Link>
              ) : (
                <span className="text-ink font-medium">{crumb.label}</span>
              )}
            </React.Fragment>
          ))}
        </nav>
      )}
      <div className="flex items-start justify-between gap-4">
        <div className="min-w-0">
          {/* tracking-tight: display sizes set at default tracking read loose. */}
          {titleAccessory ? (
            <div className="flex flex-wrap items-center gap-2">
              <h1 className="min-w-0 truncate text-2xl font-semibold tracking-tight text-ink">
                {title}
              </h1>
              {titleAccessory}
            </div>
          ) : (
            <h1 className="text-2xl font-semibold tracking-tight text-ink">
              {title}
            </h1>
          )}
          {subtitle ? (
            <p className="mt-1 text-sm text-ink-muted">{subtitle}</p>
          ) : null}
        </div>
        {actions.length > 0 && (
          <div className="flex items-center gap-2">
            {actions.map((action, i) => {
              // rounded-md, matching the control radius the theme sets for
              // buttons — rounded-lg is the CONTAINER radius, and a button
              // wearing it reads as a small card.
              const cls = `inline-flex items-center gap-1.5 rounded-md px-3.5 py-2 text-sm font-medium transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent focus-visible:ring-offset-2 disabled:cursor-not-allowed disabled:opacity-50 ${
                variantStyles[action.variant ?? "secondary"]
              }`;
              if (action.href) {
                return (
                  <Link key={i} href={action.href} className={cls}>
                    {action.icon}
                    {action.label}
                  </Link>
                );
              }
              return (
                <button
                  key={i}
                  type="button"
                  onClick={action.onClick}
                  disabled={action.disabled}
                  className={cls}
                >
                  {action.icon}
                  {action.label}
                </button>
              );
            })}
          </div>
        )}
      </div>
    </div>
  );
}
