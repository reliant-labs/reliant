import type React from "react";

import type { MenuGroup, MenuItem } from "../components/forge-ui/dropdown_menu";
import type { PopoverOpenVia } from "../components/forge-ui/popover";

/** Where a driven forge-ui ContextMenu opens, and how (which decides focus). */
export interface ContextMenuOpening {
  x: number;
  y: number;
  via: PopoverOpenVia;
}

/**
 * Where a row's context menu opens. A right-click opens it at the pointer;
 * Shift+F10, the ContextMenu key or Enter on a "more" button carries no
 * pointer position (`detail === 0`), so it opens below the element, with
 * focus on its first item.
 */
export function contextMenuOpening(e: React.MouseEvent): ContextMenuOpening {
  if (e.detail === 0 && e.clientX === 0 && e.clientY === 0) {
    const r = e.currentTarget.getBoundingClientRect();
    return { x: r.left, y: r.bottom, via: "keyboard" };
  }
  return { x: e.clientX, y: e.clientY, via: "pointer" };
}

/** A break between groups in a flat item list (see menuGroups). */
export const MENU_SEPARATOR = "separator" as const;

/** A flat list of items and separators, as a menu's groups. Empty groups drop. */
export function menuGroups(entries: Array<MenuItem | typeof MENU_SEPARATOR>): MenuGroup[] {
  const groups: MenuGroup[] = [{ items: [] }];
  for (const entry of entries) {
    if (entry === MENU_SEPARATOR) groups.push({ items: [] });
    else groups[groups.length - 1]!.items.push(entry);
  }
  return groups.filter((g) => g.items.length > 0);
}
