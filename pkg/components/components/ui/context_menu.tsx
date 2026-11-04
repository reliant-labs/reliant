import React, { useCallback, useEffect, useState } from "react";

import DropdownMenu, { type MenuGroup } from "./dropdown_menu";
import type { PopoverOpenVia } from "./popover";

/**
 * ContextMenu — the menu a right-click opens, at the pointer (WAI-ARIA
 * menu). It is DropdownMenu anchored to a point instead of a trigger, so it
 * takes the same groups (commands, links, radio and checkbox items, icons,
 * hints, danger) and the same keyboard: arrows, Home/End and typeahead move,
 * Escape and Tab close, and choosing an item closes the menu and hands focus
 * back BEFORE the item runs — to whatever had it when the menu opened.
 *
 * Two ways to use it:
 *
 *   - Wrap the region: a right-click on it opens the menu where the pointer
 *     is, and so does Shift+F10 or the ContextMenu key while focus is inside
 *     it (then at the focused element, with focus on the first item).
 *
 *       <ContextMenu label="File actions" groups={groups}>
 *         <li>…</li>
 *       </ContextMenu>
 *
 *     The region is wrapped in a `display: contents` <div>, so it adds no box
 *     and no layout of its own.
 *   - Drive it: one menu for a whole list, whose items depend on what was
 *     right-clicked. Keep the target and point in state; pass `open`,
 *     `position` and `onOpenChange`, and no children. A keyboard-opened
 *     event (Shift+F10, the ContextMenu key, Enter on a "more" button) has
 *     no pointer position — `detail === 0` — so open it at the element
 *     instead, with `openVia="keyboard"` to focus the first item.
 *
 *       <ContextMenu label="Chat actions" groups={itemsFor(target)}
 *         open={target !== null} position={point}
 *         onOpenChange={(open) => !open && setTarget(null)} />
 *
 * A second right-click while it is open moves it; a press anywhere else
 * closes it. The browser's own menu is suppressed only where this one opens,
 * and in nested regions the innermost one opens.
 */

export interface ContextMenuProps {
  groups: MenuGroup[];
  /** Accessible name of the menu, e.g. "Chat actions". */
  label: string;
  /** The region a right-click opens the menu on. Omit to drive it with open/position. */
  children?: React.ReactNode;
  /** Controlled open state; pair with position and onOpenChange. */
  open?: boolean;
  /** Where a controlled menu opens, in viewport pixels. */
  position?: { x: number; y: number };
  /** How a controlled open was triggered: "keyboard" focuses the first item. */
  openVia?: PopoverOpenVia;
  onOpenChange?: (open: boolean) => void;
  /** Classes on the menu panel (width, max-height). */
  menuClassName?: string;
  /** Shown when every group is empty. */
  emptyMessage?: React.ReactNode;
  /** "compact" for dense chrome: smaller text and rows. */
  density?: "default" | "compact";
  /** Leave the browser's own menu alone. */
  disabled?: boolean;
}

// The point a contextmenu event opens the menu at. A keyboard-opened event
// has no pointer position, so the menu opens at the bottom-left of the
// element it targets, with focus on its first item.
function openingFor(event: MouseEvent): {
  position: { x: number; y: number };
  via: PopoverOpenVia;
} {
  const fromKeyboard = event.detail === 0 && event.clientX === 0 && event.clientY === 0;
  if (fromKeyboard && event.target instanceof Element) {
    const r = event.target.getBoundingClientRect();
    return { position: { x: r.left, y: r.bottom }, via: "keyboard" };
  }
  return { position: { x: event.clientX, y: event.clientY }, via: "pointer" };
}

export default function ContextMenu({
  groups,
  label,
  children,
  open: controlledOpen,
  position: controlledPosition,
  openVia: controlledVia,
  onOpenChange,
  menuClassName,
  emptyMessage,
  density,
  disabled,
}: ContextMenuProps) {
  const [region, setRegion] = useState<HTMLDivElement | null>(null);
  const [opened, setOpened] = useState<{
    position: { x: number; y: number };
    via: PopoverOpenVia;
  } | null>(null);
  const open = controlledOpen ?? opened !== null;
  const position = controlledPosition ?? opened?.position;

  const setOpen = useCallback(
    (next: boolean) => {
      if (!next) setOpened(null);
      onOpenChange?.(next);
    },
    [onOpenChange],
  );

  useEffect(() => {
    if (!region || disabled) return;
    function onContextMenu(e: MouseEvent) {
      // A region nested inside this one (a folder inside a folder) claimed
      // the click first: the innermost menu wins.
      if (e.defaultPrevented) return;
      e.preventDefault();
      setOpened(openingFor(e));
      onOpenChange?.(true);
    }
    region.addEventListener("contextmenu", onContextMenu);
    return () => region.removeEventListener("contextmenu", onContextMenu);
  }, [region, disabled, onOpenChange]);

  const menu =
    open && position ? (
      <DropdownMenu
        anchor={position}
        openVia={controlledVia ?? opened?.via}
        open
        onOpenChange={setOpen}
        groups={groups}
        label={label}
        menuClassName={menuClassName}
        emptyMessage={emptyMessage}
        density={density}
      />
    ) : null;

  if (children === undefined) return menu;
  return (
    <>
      <div ref={setRegion} className="contents">
        {children}
      </div>
      {menu}
    </>
  );
}
