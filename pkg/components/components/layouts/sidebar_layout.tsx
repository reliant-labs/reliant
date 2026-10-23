"use client";

import React, { useState } from "react";

import Link from "./link";

/**
 * What a nav entry shows after its label. Structured rather than a ReactNode
 * so the collapsed icon rail can still show the part that matters (a dot) when
 * there is no room for the rest.
 *
 *   { type: "count", value: 3 }                         neutral count
 *   { type: "count", value: 1, tone: "danger" }         something needs you (firing alerts)
 *   { type: "new" }                                     dashed-outline "New" pill
 *   { type: "dot", tone: "warning", label: "Degraded" } a state, named for screen readers
 */
export type SidebarBadge =
  | { type: "count"; value: number | string; tone?: "neutral" | "danger"; label?: string }
  | { type: "new" }
  | { type: "dot"; tone?: "warning" | "danger"; label: string };

export interface SidebarNavItem {
  label: string;
  href?: string;
  icon?: React.ReactNode;
  /** This entry's own page is the current route. */
  active?: boolean;
  /**
   * Groups entries under a plain muted label. Entries sharing a section must
   * be adjacent; the first section is usually unlabelled.
   */
  section?: string;
  badge?: SidebarBadge;
  /** Anything else after the label (a "Queued" mark). Hidden on the rail. */
  trailing?: React.ReactNode;
  /**
   * Sub-pages. An entry with children shows a `›` chevron; clicking it
   * NAVIGATES to its own overview (`href`) and its children expand inline
   * under it. The group holding the current route stays open and every other
   * group closes (an accordion) — so open/closed is derived from `active`,
   * never stored. A sub-page may carry a short list of its own (starred
   * dashboards under Dashboards), shown indented and muted whenever its
   * group is open.
   */
  children?: SidebarNavItem[];
}

export interface SidebarUserInfo {
  name: string;
  email?: string;
  avatar?: React.ReactNode;
}

export interface SidebarLayoutProps {
  brand: React.ReactNode;
  /** What the brand row shows on the collapsed rail (a monogram). Empty when omitted. */
  brandCollapsed?: React.ReactNode;
  navItems: SidebarNavItem[];
  /**
   * Entries pinned to the bottom of the nav, above the user row — the place
   * for the one entry that is visited from everywhere (an activity log).
   */
  footerItems?: SidebarNavItem[];
  user?: SidebarUserInfo;
  children: React.ReactNode;
  /** The top bar (breadcrumb, scope switcher, search). Omitted when not given. */
  headerContent?: React.ReactNode;
  /** Controlled mode: the sidebar is the 56px icon rail. When omitted, uses internal state. */
  collapsed?: boolean;
  /** Controlled mode: toggle callback. When omitted, uses internal handler. */
  onToggle?: () => void;
  /**
   * "padded" (default) is a scrolling content area with the page gutter
   * (32px sides, 24px top). "fill" hands the page the whole remaining height,
   * unpadded, and lets it own scrolling and its gutter — for a page that
   * hosts its own scroller (a transcript, an editor, a dashboard canvas).
   */
  contentLayout?: "padded" | "fill";
  /** The nav landmark's accessible name, e.g. the area it navigates. */
  navLabel?: string;
  /**
   * Extra classes for the sidebar alone — e.g. a token scope that should
   * style the nav without reaching into the page content.
   */
  sidebarClassName?: string;
  /** Rendered under the brand row, above the nav (a scope switcher). Hidden on the rail. */
  brandAccessory?: React.ReactNode;
  /**
   * Rendered after the nav sections — a loading skeleton for a list still
   * arriving, or a one-line note about it. Hidden on the rail.
   */
  navAppendix?: React.ReactNode;
}

function itemKey(item: SidebarNavItem, i: number): string {
  return `${item.href ?? item.label}:${i}`;
}

function containsActive(item: SidebarNavItem): boolean {
  return !!item.active || (item.children ?? []).some(containsActive);
}

function NavBadge({ badge, rail }: { badge: SidebarBadge; rail: boolean }) {
  if (badge.type === "dot" || (rail && badge.type === "count" && badge.tone === "danger")) {
    const danger = badge.tone === "danger";
    const label = badge.type === "dot" ? badge.label : (badge.label ?? String(badge.value));
    return (
      <span
        title={label}
        className={`shrink-0 rounded-full ${danger ? "bg-danger" : "bg-warning"} ${
          rail ? "absolute right-1.5 top-1.5 h-1.5 w-1.5" : "h-2 w-2"
        }`}
      >
        <span className="sr-only">{label}</span>
      </span>
    );
  }
  if (rail) return null;
  if (badge.type === "new") {
    return (
      <span className="inline-flex h-4.5 shrink-0 items-center rounded-full border border-dashed border-ink-muted px-1.5 text-xs leading-none text-ink-muted">
        New
      </span>
    );
  }
  return (
    <span
      title={badge.label}
      className={`inline-flex h-4.5 min-w-4.5 shrink-0 items-center justify-center rounded-full px-1.5 text-xs font-medium leading-none tabular-nums ${
        badge.tone === "danger" ? "bg-danger-surface text-danger-ink" : "bg-ink/8 text-ink-muted"
      }`}
    >
      {badge.value}
      {badge.label && <span className="sr-only"> {badge.label}</span>}
    </span>
  );
}

function Chevron({ open }: { open: boolean }) {
  return (
    <svg
      aria-hidden="true"
      className={`h-3.5 w-3.5 shrink-0 text-ink-muted transition-transform duration-150 motion-reduce:transition-none ${
        open ? "rotate-90" : ""
      }`}
      fill="none"
      viewBox="0 0 24 24"
      strokeWidth={2}
      stroke="currentColor"
    >
      <path strokeLinecap="round" strokeLinejoin="round" d="M8.25 4.5l7.5 7.5-7.5 7.5" />
    </svg>
  );
}

/**
 * SidebarLayout — the app shell: a left nav, an optional top bar, and the
 * scrolling page.
 *
 * The nav is deliberately flat (the Cloudflare dashboard pattern): EVERY entry
 * is the same row — an outline icon, a regular-weight label, an optional
 * badge, and a `›` chevron only when it has sub-pages. Section labels are
 * plain muted text, not buttons, and no group is styled as more important
 * than another; only the current page is marked, with a fill.
 *
 *   <SidebarLayout
 *     brand={<ProjectSwitcher />}
 *     navItems={[
 *       { label: "Overview", href: "/", icon: <HomeIcon />, active: true },
 *       { label: "Observability", href: "/obs", icon: <GaugeIcon />, children: [
 *           { label: "Logs", href: "/obs/logs" },
 *           { label: "Alerts", href: "/obs/alerts", badge: { type: "count", value: 1, tone: "danger" } },
 *       ] },
 *       { label: "Secrets", href: "/secrets", icon: <KeyIcon />, section: "Configure" },
 *     ]}
 *     footerItems={[{ label: "Activity", href: "/activity", icon: <ActivityIcon /> }]}
 *     user={{ name: "Ada", email: "ada@example.com" }}
 *   >
 *     {page}
 *   </SidebarLayout>
 *
 * Collapsed, the nav is a 56px icon rail: labels, sub-pages and section labels
 * give way to icons (each with its label as a tooltip and accessible name)
 * and dividers; a dot badge — or a danger count, as a dot — stays visible.
 *
 * Hover and selection are an ink wash (`bg-ink/5`, `bg-ink/8`), so they read
 * as "pressed" in light and dark alike whatever the palette maps surfaces to.
 */
export default function SidebarLayout({
  brand,
  brandCollapsed,
  navItems,
  footerItems = [],
  user,
  children,
  headerContent,
  collapsed: controlledCollapsed,
  onToggle,
  contentLayout = "padded",
  navLabel,
  sidebarClassName,
  brandAccessory,
  navAppendix,
}: SidebarLayoutProps) {
  const [internalCollapsed, setInternalCollapsed] = useState(false);
  const collapsed = controlledCollapsed ?? internalCollapsed;
  const handleToggle = onToggle ?? (() => setInternalCollapsed((prev) => !prev));
  // A parent with children but no href of its own cannot open by navigating,
  // so it toggles locally instead.
  const [openLocal, setOpenLocal] = useState<Record<string, boolean>>({});

  const sections: { label: string; items: SidebarNavItem[] }[] = [];
  for (const item of navItems) {
    const label = item.section ?? "";
    const last = sections[sections.length - 1];
    if (last && last.label === label) last.items.push(item);
    else sections.push({ label, items: [item] });
  }

  const rowBase =
    "relative flex items-center rounded-lg text-sm text-ink transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-accent";

  function renderChild(item: SidebarNavItem, i: number) {
    return (
      <li key={itemKey(item, i)}>
        <Link
          href={item.href ?? "#"}
          aria-current={item.active ? "page" : undefined}
          className={`${rowBase} h-8 gap-2 pl-9.5 pr-2 ${
            item.active ? "bg-ink/8" : "hover:bg-ink/5"
          }`}
        >
          <span className="min-w-0 flex-1 truncate">{item.label}</span>
          {item.badge && <NavBadge badge={item.badge} rail={false} />}
          {item.trailing && <span className="shrink-0">{item.trailing}</span>}
        </Link>
        {item.children && item.children.length > 0 && (
          <ul className="flex flex-col gap-px">
            {item.children.map((grandchild, gi) => (
              <li key={itemKey(grandchild, gi)}>
                <Link
                  href={grandchild.href ?? "#"}
                  aria-current={grandchild.active ? "page" : undefined}
                  className={`${rowBase} h-8 pl-12.5 pr-2 ${
                    grandchild.active ? "bg-ink/8" : "text-ink-muted hover:bg-ink/5 hover:text-ink"
                  }`}
                >
                  <span className="min-w-0 flex-1 truncate">{grandchild.label}</span>
                </Link>
              </li>
            ))}
          </ul>
        )}
      </li>
    );
  }

  function renderItem(item: SidebarNavItem, i: number) {
    const key = itemKey(item, i);
    const hasChildren = !!item.children && item.children.length > 0;
    const open = hasChildren && (containsActive(item) || (!item.href && !!openLocal[key]));
    // On the rail the sub-pages are hidden, so the parent stands in for
    // whichever of them is current.
    const marked = collapsed ? containsActive(item) : !!item.active;
    const inner = (
      <>
        {item.icon && (
          <span
            aria-hidden="true"
            className={`flex h-4 w-4 shrink-0 items-center justify-center [&>svg]:h-4 [&>svg]:w-4 ${
              marked ? "text-ink" : "text-ink-muted"
            }`}
          >
            {item.icon}
          </span>
        )}
        {collapsed ? (
          <span className="sr-only">{item.label}</span>
        ) : (
          <span className="min-w-0 flex-1 truncate">{item.label}</span>
        )}
        {item.badge && <NavBadge badge={item.badge} rail={collapsed} />}
        {!collapsed && item.trailing && <span className="shrink-0">{item.trailing}</span>}
        {!collapsed && hasChildren && <Chevron open={open} />}
      </>
    );
    const cls = `${rowBase} h-9 w-full ${
      collapsed ? "justify-center px-0" : "gap-2.5 pl-3 pr-2"
    } ${marked ? "bg-ink/8" : "hover:bg-ink/5"}`;

    return (
      <li key={key}>
        {hasChildren && !item.href ? (
          <button
            type="button"
            aria-expanded={open}
            onClick={() => setOpenLocal((prev) => ({ ...prev, [key]: !prev[key] }))}
            className={`${cls} text-left`}
            title={collapsed ? item.label : undefined}
          >
            {inner}
          </button>
        ) : (
          <Link
            href={item.href ?? "#"}
            aria-current={item.active ? "page" : undefined}
            aria-expanded={hasChildren ? open : undefined}
            className={cls}
            title={collapsed ? item.label : undefined}
          >
            {inner}
          </Link>
        )}
        {open && !collapsed && (
          <ul className="mb-1 mt-px flex flex-col gap-px">{item.children!.map(renderChild)}</ul>
        )}
      </li>
    );
  }

  return (
    <div className="flex h-screen overflow-hidden bg-surface-muted">
      {/* Sidebar */}
      <aside
        className={`flex shrink-0 flex-col border-r border-border bg-surface transition-[width] duration-200 motion-reduce:transition-none ${
          collapsed ? "w-14" : "w-60"
        } ${sidebarClassName ?? ""}`}
      >
        {/* Brand */}
        <div
          className={`flex h-13 shrink-0 items-center border-b border-border ${
            collapsed ? "justify-center px-2" : "px-3"
          }`}
        >
          {collapsed ? brandCollapsed : <div className="min-w-0 flex-1 truncate">{brand}</div>}
        </div>

        {brandAccessory && !collapsed && (
          <div className="shrink-0 border-b border-border px-3 py-2">{brandAccessory}</div>
        )}

        {/* Navigation */}
        <nav
          className={`relative flex flex-1 flex-col overflow-y-auto py-2 ${
            collapsed ? "px-2.5" : "px-2"
          }`}
          aria-label={navLabel}
        >
          {sections.map((section, si) => (
            <div key={`${section.label}:${si}`}>
              {section.label &&
                (collapsed ? (
                  <div role="separator" className="mx-1.5 my-3 h-px bg-border" />
                ) : (
                  <p className="px-3 pb-1.5 pt-4 text-xs text-ink-muted">{section.label}</p>
                ))}
              <ul className="flex flex-col gap-px">{section.items.map(renderItem)}</ul>
            </div>
          ))}
          {navAppendix && !collapsed && <div className="mt-3">{navAppendix}</div>}
          {footerItems.length > 0 && (
            <ul className="mt-auto flex flex-col gap-px pt-3">{footerItems.map(renderItem)}</ul>
          )}
        </nav>

        {/* User row + collapse toggle */}
        <div
          className={`flex shrink-0 items-center gap-2 border-t border-border p-2 ${
            collapsed ? "flex-col" : ""
          }`}
        >
          {user && (
            <div className={`flex min-w-0 items-center gap-2 ${collapsed ? "" : "flex-1 px-1"}`}>
              <div
                title={collapsed ? user.name : undefined}
                className="flex h-6 w-6 shrink-0 items-center justify-center overflow-hidden rounded-full border border-border bg-ink/5 text-xs font-semibold text-ink"
              >
                {user.avatar ?? user.name.charAt(0).toUpperCase()}
              </div>
              {!collapsed && (
                <div className="min-w-0">
                  <p className="truncate text-sm text-ink">{user.name}</p>
                  {user.email && <p className="truncate text-xs text-ink-muted">{user.email}</p>}
                </div>
              )}
            </div>
          )}
          <button
            type="button"
            onClick={handleToggle}
            className={`flex h-7 w-7 shrink-0 items-center justify-center rounded-md text-ink-muted transition-colors hover:bg-ink/5 hover:text-ink focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent ${
              user || collapsed ? "" : "ml-auto"
            }`}
            aria-label={collapsed ? "Expand sidebar" : "Collapse sidebar"}
            title={collapsed ? "Expand sidebar" : "Collapse sidebar"}
          >
            <svg
              className="h-4 w-4"
              fill="none"
              stroke="currentColor"
              viewBox="0 0 24 24"
              strokeWidth={1.75}
            >
              <rect x="3.75" y="4.5" width="16.5" height="15" rx="2" />
              <path strokeLinecap="round" d="M9.75 4.5v15" />
            </svg>
          </button>
        </div>
      </aside>

      {/* Main */}
      <div className="flex min-w-0 flex-1 flex-col overflow-hidden">
        {/* Top bar — omitted when there is nothing to put in it, so a page
            that owns its own header does not sit under an empty bar. */}
        {headerContent && (
          <header className="flex h-13 shrink-0 items-center gap-3 border-b border-border bg-surface-muted px-8">
            {headerContent}
          </header>
        )}

        {/*
         * Content. `relative` is load-bearing: it makes the scroller the
         * containing block for absolutely positioned descendants. Without it
         * every `sr-only` span (position: absolute) in a long list resolves
         * against the initial containing block instead, stretches the
         * DOCUMENT past the viewport, and the whole window scrolls into blank
         * space below the content.
         */}
        {contentLayout === "fill" ? (
          <main className="relative flex min-h-0 flex-1 flex-col">{children}</main>
        ) : (
          <main className="relative flex-1 overflow-y-auto px-8 pb-12 pt-6">{children}</main>
        )}
      </div>
    </div>
  );
}
