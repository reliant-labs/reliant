// Copyright (c) 2026 Reliant Labs

/**
 * A forge-ui nav tree (`SidebarNavItem[]`: an app's console sections, the org
 * console's Observability views) drawn as the one nav's nested rows, so a
 * section list built for the console's own column reads as part of the left
 * nav instead of a second one (U6: one shell, one nav).
 *
 * What it keeps from the forge-ui model: the active row, a group's children,
 * a section label before the first row of a section, a count badge, and a
 * trailing mark.
 *
 * What it does not: groups that open on every page (`defaultExpanded`) or by
 * hand. The open app's sections share the nav's upper region with every other
 * app, so they stay short (U6, owner preview 2026-11-19: on a 900px window an
 * expanded app pushed the chats below the fold). A group (Issues, Performance,
 * More tools, Environments, …) shows its rows only while one of them is the
 * page on screen; the group's own row is a link — to its own page when it has
 * one, else to its first row — so it is a real URL that opens the group.
 */

import type { ReactNode } from "react";

import type { SidebarBadge, SidebarNavItem } from "@/components/forge-ui/sidebar_layout";
import { cn } from "@/lib/utils";

import { SidebarSubLabel, SidebarSubRow } from "./primitives";

/** Whether `item`, or any row under it, is the current page. */
export function hasActive(item: SidebarNavItem): boolean {
  return !!item.active || (item.children ?? []).some(hasActive);
}

/**
 * The badge a closed group carries for its rows: the first one that asks for
 * attention (alerts firing), so closing Issues never hides that.
 */
function rolledUpBadge(items: readonly SidebarNavItem[]): SidebarBadge | undefined {
  for (const item of items) {
    if (item.badge && item.badge.type !== "new" && item.badge.tone === "danger") return item.badge;
    const nested = rolledUpBadge(item.children ?? []);
    if (nested) return nested;
  }
  return undefined;
}

/** Where a row goes: its own page, else (a group with none) its first row's. */
export function rowHref(item: SidebarNavItem): string | undefined {
  if (item.href) return item.href;
  for (const child of item.children ?? []) {
    const href = rowHref(child);
    if (href) return href;
  }
  return undefined;
}

function Badge({ badge }: { badge: SidebarBadge }) {
  if (badge.type === "new") return <span>New</span>;
  if (badge.type === "dot") {
    return (
      <span
        role="img"
        aria-label={badge.label}
        className={cn("size-2 rounded-full", badge.tone === "danger" ? "bg-destructive" : "bg-warning")}
      />
    );
  }
  return (
    <span className={cn("tabular-nums", badge.tone === "danger" && "font-medium text-destructive-ink")}>
      {badge.value}
      {badge.label && <span className="sr-only"> {badge.label}</span>}
    </span>
  );
}

export interface SidebarNavTreeProps {
  items: readonly SidebarNavItem[];
  /** Runs a row's page (the shell's navigate). */
  onNavigate: (href: string) => void;
  /** Depth of the first level, 1 under a top-level row. */
  depth?: 1 | 2;
  /** Prefix for each row's test id (`<prefix>-<label>`). */
  testIdPrefix?: string;
}

export function SidebarNavTree({ items, onNavigate, depth = 1, testIdPrefix }: SidebarNavTreeProps) {
  const rows: ReactNode[] = [];
  let section: string | undefined;
  for (const item of items) {
    if (item.section && item.section !== section) {
      rows.push(
        <SidebarSubLabel key={`section:${item.section}`} depth={depth}>
          {item.section}
        </SidebarSubLabel>,
      );
    }
    section = item.section;
    rows.push(
      <NavTreeItem
        key={`${item.section ?? ""}:${item.label}`}
        item={item}
        depth={depth}
        onNavigate={onNavigate}
        testIdPrefix={testIdPrefix}
      />,
    );
  }
  return <>{rows}</>;
}

function NavTreeItem({
  item,
  depth,
  onNavigate,
  testIdPrefix,
}: {
  item: SidebarNavItem;
  depth: 1 | 2 | 3;
  onNavigate: (href: string) => void;
  testIdPrefix?: string;
}) {
  const children = item.children ?? [];
  // A group's rows show only while one of them is the page on screen.
  const open = children.some(hasActive);
  const href = rowHref(item);
  const testId = testIdPrefix ? `${testIdPrefix}-${item.label.toLowerCase().replace(/\s+/g, "-")}` : undefined;
  const badge = item.badge ?? (open ? undefined : rolledUpBadge(children));
  const trail = (badge || item.trailing) && (
    <>
      {item.trailing}
      {badge && <Badge badge={badge} />}
    </>
  );
  const childDepth = Math.min(depth + 1, 3) as 1 | 2 | 3;
  return (
    <>
      <SidebarSubRow
        label={item.label}
        depth={depth}
        // Icons only at the first level: below it, labels line up instead.
        icon={depth === 1 ? item.icon : undefined}
        href={href}
        onClick={href ? () => onNavigate(href) : undefined}
        current={!!item.active}
        expanded={children.length > 0 ? open : undefined}
        trail={trail || undefined}
        testId={testId}
      />
      {open && (
        <div role="group" aria-label={item.label}>
          {children.map((child) => (
            <NavTreeItem
              key={child.label}
              item={child}
              depth={childDepth}
              onNavigate={onNavigate}
              testIdPrefix={testIdPrefix}
            />
          ))}
        </div>
      )}
    </>
  );
}
