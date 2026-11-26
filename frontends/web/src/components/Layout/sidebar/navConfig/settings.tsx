// Copyright (c) 2026 Reliant Labs

/**
 * Settings' sections as the nav's rows: on a Settings page they take the nav
 * column (NavConfig `sectionPages`), as Vercel's do.
 *
 * Its own file, imported only by hybrid.ts (the app's nav), because the rows
 * come from Settings' own list (Settings/SettingsNavigation), which is the
 * app's alone. items.tsx stays free of it, so a nav drawn from the shared
 * items (the public demo's) never pulls Settings into its bundle.
 */

import { SlidersHorizontal } from "lucide-react";
import type { ComponentType } from "react";

import { settingsPath, withSearch } from "@/lib/paths";

import { isSettingsItemCurrent, settingsItemEntry, settingsNavGroups, type SectionItem } from "../../../Settings/SettingsNavigation";
import type { SettingsTab } from "../../../../routeSchemas";
import { isAtOrBelow } from "../navLocation";
import { heading, isSettingsRoute } from "./items";
import type { NavArea, NavItem, NavRow } from "./types";

/**
 * One Settings entry as a row. A section with two entries (Settings → AI:
 * Personal accounts, its providers tab; Models & tools, its models and local
 * tabs) gets one row per entry, each lit on its own tabs.
 */
function settingsRow(item: SectionItem): NavRow {
  const { tab } = settingsItemEntry(item);
  const Icon: ComponentType<{ className?: string }> = item.icon;
  return {
    kind: "row",
    id: tab ? `settings-${item.id}-${tab}` : `settings-${item.id}`,
    label: item.label,
    icon: <Icon className="size-4" aria-hidden="true" />,
    href: () => withSearch(settingsPath(item.id), { tab }),
    match: (ctx) =>
      isAtOrBelow(ctx.pathname, settingsPath(item.id)) &&
      isSettingsItemCurrent(item, item.id, typeof ctx.search.tab === "string" ? (ctx.search.tab as SettingsTab) : undefined),
    // Machine housekeeping lives on the sandboxes' pages, not in the Inbox.
    ...(item.id === "machines" ? { trail: "sandboxes-housekeeping" as const } : {}),
  };
}

/** A collapsible section's icon in a flat nav (NavGroup `flatIcon`). */
const groupIcon = <SlidersHorizontal className="size-4" aria-hidden="true" />;

/** Settings' sections, from Settings' own list (SettingsNavigation), as the drill-in's rows. */
function settingsChildren(): readonly NavItem[] {
  const { groups, about } = settingsNavGroups();
  const items: NavItem[] = [];
  for (const group of groups) {
    const rows = group.items.map(settingsRow);
    const id = `settings-group-${group.label.toLowerCase().replace(/[^a-z0-9]+/g, "-")}`;
    if (group.collapsible) items.push({ kind: "group", id, label: group.label, flatIcon: groupIcon, children: rows });
    else items.push(heading(id, group.label), ...rows);
  }
  items.push(settingsRow(about));
  return items;
}

/** On a Settings page its sections take the nav column, as Vercel's do: a section page, not a drill-in row. */
export const settingsSections: NavArea = {
  kind: "area",
  id: "settings",
  label: "Settings",
  match: isSettingsRoute,
  children: settingsChildren,
};
