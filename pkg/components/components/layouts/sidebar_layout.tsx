"use client";

import React, { useEffect, useId, useRef, useState } from "react";
import { createPortal } from "react-dom";

import Link from "./link";
import { useModalDialog } from "./modal";

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
  /** Stable sibling identity. Supply when href/label is not unique or may change. */
  id?: string;
  href?: string;
  /**
   * Opt into local disclosure control, initially expanded when true. The initial
   * value is remembered per identity (including entries added later); changing
   * it does not reset user choices. A separate disclosure button leaves href
   * navigation intact. Explicit collapse wins even over an active descendant
   * until the current route changes, which restores automatic active opening.
   * Omit to retain legacy route-driven expansion (nested lists stay visible).
   */
  defaultExpanded?: boolean;
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
   * Sub-pages. By default, clicking the row NAVIGATES to its own overview
   * (`href`); the group containing the active route opens automatically.
   * Groups without href toggle locally, but an active descendant holds them
   * open. Nested lists remain visible whenever their parent is open.
   * Set defaultExpanded to opt this entry into independent disclosure control.
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

/** Below Tailwind's `md`: the nav leaves the layout and becomes an off-canvas drawer. */
const NARROW_QUERY = "(max-width: 767px)";

function useNarrow(): boolean {
  const [narrow, setNarrow] = useState(
    () => typeof window !== "undefined" && !!window.matchMedia?.(NARROW_QUERY).matches,
  );
  useEffect(() => {
    const query = window.matchMedia?.(NARROW_QUERY);
    if (!query) return;
    const onChange = () => setNarrow(query.matches);
    onChange();
    query.addEventListener("change", onChange);
    return () => query.removeEventListener("change", onChange);
  }, []);
  return narrow;
}

function itemKey(item: SidebarNavItem, parent: string): string {
  // Hierarchical JSON tuples separate nested, section and footer namespaces;
  // position is never identity, so insertion/reordering cannot move a choice.
  return JSON.stringify([parent, item.section ?? "", item.id ?? item.href ?? item.label]);
}

type ExpansionState = Record<string, { initial: boolean; override?: boolean }>;

function seedExpansion(
  items: SidebarNavItem[],
  parent: string,
  state: ExpansionState,
): ExpansionState {
  let next = state;
  for (const item of items) {
    const key = itemKey(item, parent);
    if (item.defaultExpanded !== undefined && !Object.hasOwn(next, key)) {
      next = { ...next, [key]: { initial: item.defaultExpanded } };
    }
    next = seedExpansion(item.children ?? [], key, next);
  }
  return next;
}

/** The hrefs of every current entry, at any depth: what changes when the route does. */
function activeHrefs(items: SidebarNavItem[]): string[] {
  return items.flatMap((item) => [
    ...(item.active ? [item.href ?? item.label] : []),
    ...activeHrefs(item.children ?? []),
  ]);
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
 * Below `md` (a phone) the nav is not a column at all: a fixed 240px sidebar
 * left a 390px screen ~150px for the page. It becomes an off-canvas drawer
 * opened from a menu button in the top bar (always shown there), always
 * expanded (the rail is a desktop space-saver), closed by the scrim, Escape,
 * or a change of the current entry (the reader navigated). Rows grow to 44px
 * on a coarse pointer, and the page gutter tightens to 16px.
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
  const narrow = useNarrow();
  // The drawer is always the full nav: a rail inside a drawer saves nothing.
  const collapsed = narrow ? false : (controlledCollapsed ?? internalCollapsed);
  const [drawerOpen, setDrawerOpen] = useState(false);
  const drawerShown = narrow && drawerOpen;
  const navRef = useRef<HTMLElement>(null);
  const menuButtonRef = useRef<HTMLButtonElement>(null);
  // Following a link closes the drawer, or it would stay open over the page
  // the reader just chose. The layout knows no router, but it does know which
  // entry is current: when that changes, the reader has navigated. Widening
  // past the drawer closes it too, so narrowing again does not bring it back.
  // Both are adjusted while rendering, React's pattern for state that follows
  // a prop: an effect would commit the stale open drawer first and render
  // twice.
  const current = JSON.stringify(activeHrefs(navItems).concat(activeHrefs(footerItems)));
  const [drawerContext, setDrawerContext] = useState({ narrow, current });
  if (drawerContext.narrow !== narrow || drawerContext.current !== current) {
    setDrawerContext({ narrow, current });
    if (drawerOpen && (drawerContext.current !== current || !narrow)) setDrawerOpen(false);
  }
  const navId = useId();
  const titleId = useId();
  const descriptionId = useId();
  useModalDialog({
    open: drawerShown,
    panelRef: navRef,
    onClose: () => setDrawerOpen(false),
    // Safari pointer activation doesn't focus buttons: remember the trigger
    // explicitly rather than whichever page element happened to have focus.
    returnFocus: () => menuButtonRef.current,
    initialFocus: () =>
      navRef.current?.querySelector<HTMLElement>("nav a[href], nav button:not([disabled])") ??
      navRef.current?.querySelector<HTMLElement>('[aria-label="Close navigation"]'),
    // On a desktop transition the mobile trigger is hidden (or unmounted).
    fallbackFocus: () =>
      navRef.current?.querySelector<HTMLElement>("a[href], button:not([disabled])")?.focus(),
  });
  // A portal-to-column transition replaces the nav DOM. Its new ref isn't
  // attached during the old modal's cleanup; focus the desktop nav after commit.
  const wasDrawerShown = useRef(false);
  useEffect(() => {
    if (wasDrawerShown.current && !narrow) {
      navRef.current
        ?.querySelector<HTMLElement>("nav a[href], nav button:not([disabled])")
        ?.focus();
    }
    wasDrawerShown.current = drawerShown;
  }, [drawerShown, narrow]);
  // Following any link in the open drawer closes it, the already-current
  // route included (its active prop won't change). Delegated from the drawer
  // with a native listener: the links are the interactive elements, and a
  // click handler on the <aside> itself is what jsx-a11y rejects in a
  // scaffolded project's lint.
  useEffect(() => {
    const nav = navRef.current;
    if (!drawerShown || !nav) return;
    const closeOnLink = (event: MouseEvent) => {
      if (event.target instanceof Element && event.target.closest("a[href]")) setDrawerOpen(false);
    };
    nav.addEventListener("click", closeOnLink);
    return () => nav.removeEventListener("click", closeOnLink);
  }, [drawerShown]);
  const handleToggle = onToggle ?? (() => setInternalCollapsed((prev) => !prev));
  // Keep legacy no-href toggles independent of opt-in disclosure state.
  const [openLocal, setOpenLocal] = useState<Record<string, boolean>>({});
  const [expansion, setExpansion] = useState<{ route: string; items: ExpansionState }>({
    route: current,
    items: {},
  });
  const initialItems =
    expansion.route === current
      ? expansion.items
      : Object.fromEntries(
          Object.entries(expansion.items).map(([key, value]) => [key, { initial: value.initial }]),
        );
  const seeded = seedExpansion(footerItems, "footer", seedExpansion(navItems, "nav", initialItems));
  // Snapshot defaults once per identity, including entries loaded after mount.
  // Unlike a live fallback, later prop edits cannot undo an explicit collapse.
  // Route changes expire overrides; visiting an old route never revives them.
  if (seeded !== expansion.items) setExpansion({ route: current, items: seeded });

  const sections: { label: string; items: SidebarNavItem[] }[] = [];
  for (const item of navItems) {
    const label = item.section ?? "";
    const last = sections[sections.length - 1];
    if (last && last.label === label) last.items.push(item);
    else sections.push({ label, items: [item] });
  }

  const rowBase =
    "relative flex items-center rounded-lg text-sm text-ink transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-accent [@media(pointer:coarse)]:min-h-[44px]";

  function renderChild(item: SidebarNavItem, parent: string, depth = 1): React.ReactNode {
    if (item.defaultExpanded !== undefined) return renderItem(item, parent, depth);
    const key = itemKey(item, parent);
    return (
      <li key={key}>
        <Link
          href={item.href ?? "#"}
          aria-current={item.active ? "page" : undefined}
          className={`${rowBase} h-8 gap-2 ${depth === 1 ? "pl-9.5" : "pl-12.5"} pr-2 ${
            item.active ? "bg-ink/8" : "hover:bg-ink/5"
          }`}
        >
          <span className="min-w-0 flex-1 truncate">{item.label}</span>
          {item.badge && <NavBadge badge={item.badge} rail={false} />}
          {item.trailing && <span className="shrink-0">{item.trailing}</span>}
        </Link>
        {item.children && item.children.length > 0 && (
          <ul className="flex flex-col gap-px">
            {item.children.map((grandchild) =>
              grandchild.defaultExpanded !== undefined ? (
                renderItem(grandchild, key, depth + 1)
              ) : (
                <li key={itemKey(grandchild, key)}>
                  <Link
                    href={grandchild.href ?? "#"}
                    aria-current={grandchild.active ? "page" : undefined}
                    className={`${rowBase} h-8 pl-12.5 pr-2 ${
                      grandchild.active
                        ? "bg-ink/8"
                        : "text-ink-muted hover:bg-ink/5 hover:text-ink"
                    }`}
                  >
                    <span className="min-w-0 flex-1 truncate">{grandchild.label}</span>
                  </Link>
                </li>
              ),
            )}
          </ul>
        )}
      </li>
    );
  }

  function renderItem(item: SidebarNavItem, parent: string, depth = 0): React.ReactNode {
    const key = itemKey(item, parent);
    const hasChildren = !!item.children && item.children.length > 0;
    const localDisclosure = item.defaultExpanded !== undefined;
    const choice = seeded[key];
    const open =
      hasChildren &&
      (localDisclosure
        ? (choice?.override ?? (containsActive(item) || !!choice?.initial))
        : containsActive(item) || (!item.href && !!openLocal[key]));
    const groupId = `${navId}-${encodeURIComponent(key)}`;
    const toggle = () => {
      if (localDisclosure) {
        setExpansion((prev) => ({
          ...prev,
          items: {
            ...prev.items,
            [key]: { initial: prev.items[key]?.initial ?? !!item.defaultExpanded, override: !open },
          },
        }));
      } else {
        setOpenLocal((prev) => ({ ...prev, [key]: !prev[key] }));
      }
    };
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
        {!collapsed && hasChildren && !(localDisclosure && item.href) && <Chevron open={open} />}
      </>
    );
    const cls = `${rowBase} h-9 w-full ${
      collapsed
        ? "justify-center px-0"
        : `gap-2.5 ${depth === 0 ? "pl-3" : depth === 1 ? "pl-9.5" : "pl-12.5"} pr-2`
    } ${marked ? "bg-ink/8" : "hover:bg-ink/5"}`;

    return (
      <li key={key}>
        {localDisclosure && hasChildren && item.href && !collapsed ? (
          <div className="flex items-center">
            <Link
              href={item.href}
              aria-current={item.active ? "page" : undefined}
              className={`${cls} min-w-0 flex-1`}
            >
              {inner}
            </Link>
            <button
              type="button"
              aria-label={`${open ? "Collapse" : "Expand"} ${item.label}`}
              aria-expanded={open}
              aria-controls={groupId}
              onClick={toggle}
              className={`${rowBase} h-[44px] w-[44px] shrink-0 justify-center`}
            >
              <Chevron open={open} />
            </button>
          </div>
        ) : hasChildren && !item.href ? (
          <button
            type="button"
            aria-expanded={open}
            aria-controls={localDisclosure ? groupId : undefined}
            onClick={toggle}
            className={`${cls} text-left ${localDisclosure ? "min-h-[44px]" : ""}`}
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
        {hasChildren && !collapsed && (open || localDisclosure) && (
          <ul
            id={localDisclosure ? groupId : undefined}
            hidden={!open}
            className="mb-1 mt-px flex flex-col gap-px"
          >
            {open && item.children!.map((child) => renderChild(child, key, depth + 1))}
          </ul>
        )}
      </li>
    );
  }

  const sidebar = (
    <>
      {drawerShown && (
        // Pointer convenience; the visible close control and Escape also dismiss.
        <div
          role="presentation"
          className="fixed inset-0 z-40 bg-black/50"
          onClick={() => setDrawerOpen(false)}
        />
      )}
      {/* Sidebar — a column on desktop, an off-canvas drawer on a phone. */}
      <aside
        ref={navRef}
        id={navId}
        tabIndex={-1}
        role={drawerShown ? "dialog" : undefined}
        aria-modal={drawerShown ? true : undefined}
        aria-labelledby={drawerShown ? titleId : undefined}
        aria-describedby={drawerShown ? descriptionId : undefined}
        className={`flex shrink-0 flex-col border-r border-border bg-surface transition-[width] duration-200 motion-reduce:transition-none ${
          collapsed ? "w-14" : "w-60"
        } max-md:fixed max-md:inset-y-0 max-md:left-0 max-md:z-50 max-md:w-[min(18rem,85vw)] max-md:pb-[env(safe-area-inset-bottom)] max-md:pt-[env(safe-area-inset-top)] max-md:transition-transform ${
          drawerShown ? "max-md:translate-x-0" : "max-md:-translate-x-full max-md:invisible"
        } ${sidebarClassName ?? ""}`}
      >
        {/* Brand */}
        <div
          className={`flex h-13 shrink-0 items-center border-b border-border ${
            collapsed ? "justify-center px-2" : "px-3"
          }`}
        >
          {collapsed ? brandCollapsed : <div className="min-w-0 flex-1 truncate">{brand}</div>}
          {drawerShown && (
            <>
              <h2 id={titleId} className="sr-only">
                {navLabel ?? "Navigation"}
              </h2>
              <p id={descriptionId} className="sr-only">
                Choose a page from the navigation.
              </p>
              <button
                type="button"
                aria-label="Close navigation"
                onClick={() => setDrawerOpen(false)}
                className="flex h-[44px] w-[44px] shrink-0 items-center justify-center rounded-md text-ink-muted hover:bg-ink/5 hover:text-ink focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent"
              >
                <svg
                  aria-hidden="true"
                  className="h-5 w-5"
                  fill="none"
                  viewBox="0 0 24 24"
                  strokeWidth={2}
                  stroke="currentColor"
                >
                  <path strokeLinecap="round" d="M6 18L18 6M6 6l12 12" />
                </svg>
              </button>
            </>
          )}
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
              <ul className="flex flex-col gap-px">
                {section.items.map((item) => renderItem(item, "nav"))}
              </ul>
            </div>
          ))}
          {navAppendix && !collapsed && <div className="mt-3">{navAppendix}</div>}
          {footerItems.length > 0 && (
            <ul className="mt-auto flex flex-col gap-px pt-3">
              {footerItems.map((item) => renderItem(item, "footer"))}
            </ul>
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
            className={`flex h-7 w-7 shrink-0 items-center justify-center rounded-md text-ink-muted transition-colors hover:bg-ink/5 hover:text-ink focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent max-md:hidden ${
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
    </>
  );

  return (
    <div className="flex h-dvh overflow-hidden bg-surface-muted">
      {/* Only the mobile modal portals: desktop nav keeps its layout column.
          Portal order also lets a later Modal stack above this drawer. */}
      {drawerShown
        ? createPortal(
            <div data-modal-layer="" className="contents">
              {sidebar}
            </div>,
            document.body,
          )
        : sidebar}
      {/* Main */}
      <div className="flex min-w-0 flex-1 flex-col overflow-hidden">
        {/* Top bar — omitted when there is nothing to put in it, so a page
            that owns its own header does not sit under an empty bar. */}
        {(headerContent || narrow) && (
          <header className="flex h-13 shrink-0 items-center gap-3 border-b border-border bg-surface-muted px-8 max-md:gap-1.5 max-md:px-1.5 max-md:pt-[env(safe-area-inset-top)] max-md:box-content">
            <button
              ref={menuButtonRef}
              type="button"
              onClick={() => setDrawerOpen(true)}
              aria-label="Open navigation"
              aria-controls={navId}
              aria-expanded={drawerShown}
              className="hidden h-[44px] w-[44px] shrink-0 items-center justify-center rounded-md text-ink-muted hover:bg-ink/5 hover:text-ink focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent max-md:flex"
            >
              <svg
                aria-hidden="true"
                className="h-5 w-5"
                fill="none"
                stroke="currentColor"
                viewBox="0 0 24 24"
                strokeWidth={1.75}
              >
                <path strokeLinecap="round" d="M4 7h16M4 12h16M4 17h16" />
              </svg>
            </button>
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
          <main className="relative flex-1 overflow-y-auto px-8 pb-12 pt-6 max-md:px-4 max-md:pb-[calc(3rem+env(safe-area-inset-bottom))] max-md:pt-4">
            {children}
          </main>
        )}
      </div>
    </div>
  );
}
