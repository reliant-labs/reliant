// Copyright (c) 2026 Reliant Labs

/**
 * A forge-ui nav tree (`SidebarNavItem[]`: an app's console sections, the org
 * console's Observability views) drawn as the one nav's nested rows, so a
 * section list built for the console's own column reads as part of the left
 * nav instead of a second one (U6: one shell, one nav).
 *
 * What it keeps from the forge-ui model: the active row, a group's children
 * (an `href`-less group opens in place; one with a page of its own shows its
 * children while it or one of them is open), `defaultExpanded` groups open on
 * every page, a section label before the first row of a section, a count
 * badge, and a trailing mark. A group opened or closed by hand stays so while
 * the nav is mounted.
 */

import { useState, type ReactNode } from "react";

import type { SidebarBadge, SidebarNavItem } from "@/components/forge-ui/sidebar_layout";
import { cn } from "@/lib/utils";

import { SidebarSubLabel, SidebarSubRow } from "./primitives";

/** Whether `item`, or any row under it, is the current page. */
export function hasActive(item: SidebarNavItem): boolean {
  return !!item.active || (item.children ?? []).some(hasActive);
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
  // A hand-opened or hand-closed group, for this mount; unset follows the page.
  const [toggled, setToggled] = useState<boolean | undefined>(undefined);
  const children = item.children ?? [];
  const group = children.length > 0;
  // A group with no page of its own opens in place; one with a page shows its
  // rows while it, or one of them, is the page on screen.
  const opensInPlace = group && !item.href;
  const open = group && (opensInPlace ? (toggled ?? (!!item.defaultExpanded || item.open === true || hasActive(item))) : hasActive(item));
  const testId = testIdPrefix ? `${testIdPrefix}-${item.label.toLowerCase().replace(/\s+/g, "-")}` : undefined;
  const trail = (item.badge || item.trailing) && (
    <>
      {item.trailing}
      {item.badge && <Badge badge={item.badge} />}
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
        href={item.href}
        onClick={opensInPlace ? () => setToggled(!open) : item.href ? () => onNavigate(item.href!) : undefined}
        // A collapsed group whose page is open is marked, as on the console's rail.
        current={!!item.active || (opensInPlace && !open && hasActive(item))}
        expanded={opensInPlace ? open : undefined}
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
