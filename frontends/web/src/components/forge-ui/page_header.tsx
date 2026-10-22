import React from "react";

import Link from "./link";

export interface PageHeaderBreadcrumb {
  label: string;
  href?: string;
}

export interface PageHeaderAction {
  label: string;
  onClick?: () => void;
  href?: string;
  variant?: "primary" | "secondary" | "danger";
  icon?: React.ReactNode;
  /** A button action that cannot run right now. Say why in the meta line or beside it. */
  disabled?: boolean;
}

/**
 * PageHeader — the title block at the top of a page: a breadcrumb, the title
 * (with an optional status beside it), ONE meta line of facts, and actions.
 *
 *   <PageHeader
 *     breadcrumbs={[{ label: "Overview", href: "/" }, { label: "api" }]}
 *     title="api"
 *     titleAccessory={<Status tone="success" label="Healthy" />}
 *     meta={<><Tag>prod</Tag><span>v1.8.2</span><span>3 replicas</span></>}
 *     actions={[{ label: "Restart in prod" }]}
 *   />
 *
 * `title`, `subtitle` and `meta` are ReactNode, not string, for the same
 * reason `<CardHeader title>` is: the moment a header needs a record's name
 * beside a status, an id in a <Mono>, or a count, a string prop cannot express
 * it and the caller reimplements the typography by hand. ReactNode is the
 * library's idiom for a slot the caller composes (Chip.label,
 * ProgressBar.label, Status.label, CardHeader.title/description) — a plain
 * string still satisfies it.
 *
 * `meta` is the facts line — env, version, owner, last change — laid out as a
 * row with 16px between items, so a fragment of spans is all it takes. A value
 * that repeats on every row of the page's table belongs here instead.
 * `subtitle` is a sentence, for the rare page that needs one; prefer `meta`.
 */
export interface PageHeaderProps {
  title: React.ReactNode;
  /**
   * Rendered beside the title, outside the h1 — a <Status> or <Tag> that
   * describes the record but is not its NAME, so the heading's accessible
   * name stays the record's name.
   */
  titleAccessory?: React.ReactNode;
  subtitle?: React.ReactNode;
  /** One line of facts under the title. Items are spaced by the row; pass a fragment. */
  meta?: React.ReactNode;
  breadcrumbs?: PageHeaderBreadcrumb[];
  actions?: PageHeaderAction[];
  /**
   * Replaces the default bottom margin, for a page that spaces its sections
   * itself (e.g. a `space-y-*` stack) and would otherwise get both.
   */
  className?: string;
}

const variantStyles: Record<NonNullable<PageHeaderAction["variant"]>, string> = {
  primary: "border border-accent bg-accent text-on-accent hover:bg-accent-hover",
  secondary: "border border-border bg-surface text-ink hover:bg-ink/5",
  danger: "border border-border bg-surface text-danger-ink hover:bg-danger-surface",
};

export default function PageHeader({
  title,
  titleAccessory,
  subtitle,
  meta,
  breadcrumbs = [],
  actions = [],
  className,
}: PageHeaderProps) {
  return (
    <div className={className ?? "mb-6"}>
      {breadcrumbs.length > 0 && (
        <nav
          aria-label="Breadcrumb"
          className="mb-2 flex min-w-0 items-center gap-1.5 text-xs text-ink-muted"
        >
          {breadcrumbs.map((crumb, i) => (
            <React.Fragment key={i}>
              {i > 0 && (
                <span aria-hidden="true" className="text-ink-subtle">
                  ›
                </span>
              )}
              {crumb.href ? (
                <Link href={crumb.href} className="truncate hover:text-ink">
                  {crumb.label}
                </Link>
              ) : (
                <span aria-current="page" className="truncate text-ink">
                  {crumb.label}
                </span>
              )}
            </React.Fragment>
          ))}
        </nav>
      )}
      <div className="flex items-start justify-between gap-4">
        <div className="min-w-0">
          {/* tracking-tight: display sizes set at default tracking read loose. */}
          {titleAccessory ? (
            <div className="flex flex-wrap items-center gap-3">
              <h1 className="min-w-0 truncate text-xl font-semibold tracking-tight text-ink">
                {title}
              </h1>
              {titleAccessory}
            </div>
          ) : (
            <h1 className="text-xl font-semibold tracking-tight text-ink">{title}</h1>
          )}
          {subtitle ? <p className="mt-1 text-sm text-ink-muted">{subtitle}</p> : null}
          {meta ? (
            <div className="mt-1.5 flex flex-wrap items-center gap-x-4 gap-y-1 text-sm text-ink-muted">
              {meta}
            </div>
          ) : null}
        </div>
        {actions.length > 0 && (
          <div className="flex shrink-0 items-center gap-2">
            {actions.map((action, i) => {
              // rounded-md, matching the control radius the theme sets for
              // buttons — rounded-lg is the CONTAINER radius, and a button
              // wearing it reads as a small card. No shadow: only overlays
              // cast one.
              const cls = `inline-flex h-8 items-center gap-1.5 whitespace-nowrap rounded-md px-3 text-sm font-medium transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent focus-visible:ring-offset-2 disabled:cursor-not-allowed disabled:opacity-50 ${
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
