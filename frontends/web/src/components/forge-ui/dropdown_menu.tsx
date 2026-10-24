import React, { useCallback, useRef } from "react";

import Link from "./link";
import Popover, { type PopoverOpenVia, type PopoverTriggerProps } from "./popover";

/**
 * DropdownMenu — a trigger plus a menu of commands (WAI-ARIA menu button).
 * Built on Popover, so it portals out of clipping ancestors, flips above the
 * trigger when there is no room, and takes the same trigger shapes: a
 * ReactNode (wrapped in a <button>) or a render function whose props you
 * spread on your own element.
 *
 * Items are commands (`menuitem`), links (`href`, routed through ./link), or
 * choices: give a group `selection: "single"` for radio items or
 * `"multiple"` for checkbox items, and each item a `checked`. A checkbox
 * item usually sets `keepOpen`, so several can be picked in one visit.
 *
 * Keyboard: Enter/Space/ArrowDown on the trigger opens on the first item,
 * ArrowUp on the last; ArrowUp/ArrowDown/Home/End move between enabled
 * items (wrapping); a printable key jumps to the next item starting with it;
 * Escape closes and returns focus to the trigger; Tab closes. Choosing an
 * item closes the menu and returns focus to the trigger BEFORE its onClick
 * runs, so a dialog the item opens restores focus to the trigger on close.
 */

export interface MenuItem {
  label: string;
  onClick?: () => void;
  href?: string;
  icon?: React.ReactNode;
  variant?: "default" | "danger";
  disabled?: boolean;
  /** In a group with `selection`: whether this choice is on. */
  checked?: boolean;
  /** Leave the menu open after choosing (a multi-select). */
  keepOpen?: boolean;
  /** Trailing content: a shortcut, a count. */
  hint?: React.ReactNode;
  testId?: string;
}

export interface MenuGroup {
  label?: string;
  items: MenuItem[];
  /** Makes the group's items radios ("single") or checkboxes ("multiple"). */
  selection?: "single" | "multiple";
}

export interface DropdownMenuProps {
  trigger: React.ReactNode | ((props: PopoverTriggerProps) => React.ReactElement);
  groups: MenuGroup[];
  /** Which trigger edge the menu lines up with. Defaults to "right". */
  align?: "left" | "right";
  /** Accessible name of the menu. Defaults to being labelled by the trigger. */
  label?: string;
  /** Controlled open state; pair with onOpenChange. */
  open?: boolean;
  onOpenChange?: (open: boolean) => void;
  /** Accessible name of a ReactNode trigger with no text (an icon button). */
  triggerLabel?: string;
  /** Classes on the <button> wrapping a ReactNode trigger. */
  triggerClassName?: string;
  /** Classes on the menu panel (width, max-height). */
  menuClassName?: string;
  /** Shown when every group is empty. */
  emptyMessage?: React.ReactNode;
  /** "compact" for dense chrome (a sidebar header): smaller text and rows. */
  density?: "default" | "compact";
  disabled?: boolean;
}

const ITEM_SELECTOR = '[role^="menuitem"]:not([aria-disabled="true"])';

function CheckMark({ visible }: { visible: boolean }) {
  return (
    <svg
      aria-hidden
      viewBox="0 0 16 16"
      fill="none"
      stroke="currentColor"
      strokeWidth={2}
      className={`h-3.5 w-3.5 shrink-0 text-accent-ink ${visible ? "" : "invisible"}`}
    >
      <path strokeLinecap="round" strokeLinejoin="round" d="M3.5 8.5l3 3 6-7" />
    </svg>
  );
}

export default function DropdownMenu({
  trigger,
  groups,
  align = "right",
  label,
  open,
  onOpenChange,
  triggerLabel,
  triggerClassName,
  menuClassName,
  emptyMessage,
  density = "default",
  disabled,
}: DropdownMenuProps) {
  const panelRef = useRef<HTMLElement | null>(null);
  // Popover's own close (it owns open state unless the caller does), captured
  // from its render prop so Tab can close the menu the same way an item does.
  const closeRef = useRef<() => void>(() => undefined);

  const items = () =>
    panelRef.current
      ? Array.from(panelRef.current.querySelectorAll<HTMLElement>(ITEM_SELECTOR))
      : [];

  const onOpenFocus = useCallback((panel: HTMLElement, via: PopoverOpenVia) => {
    panelRef.current = panel;
    const all = Array.from(panel.querySelectorAll<HTMLElement>(ITEM_SELECTOR));
    const target =
      via === "pointer" ? null : via === "keyboard-last" ? all[all.length - 1] : all[0];
    (target ?? panel).focus({ preventScroll: true });
  }, []);

  const onPanelKeyDown = (e: KeyboardEvent) => {
    const list = items();
    if (list.length === 0) return;
    const at = list.indexOf(document.activeElement as HTMLElement);
    const move = (i: number) => {
      e.preventDefault();
      list[(i + list.length) % list.length]?.focus();
    };
    if (e.key === "ArrowDown") move(at + 1);
    else if (e.key === "ArrowUp") move(at < 0 ? list.length - 1 : at - 1);
    else if (e.key === "Home") move(0);
    else if (e.key === "End") move(list.length - 1);
    else if (e.key === "Tab") {
      // Tab leaves the menu: close it, back on the trigger.
      e.preventDefault();
      closeRef.current();
    } else if (e.key.length === 1 && /\S/.test(e.key) && !e.metaKey && !e.ctrlKey && !e.altKey) {
      const key = e.key.toLowerCase();
      for (let step = 1; step <= list.length; step++) {
        const candidate = list[(Math.max(at, 0) + step) % list.length];
        if (candidate?.textContent?.trim().toLowerCase().startsWith(key)) {
          e.preventDefault();
          candidate.focus();
          return;
        }
      }
    }
  };

  const compact = density === "compact";
  const empty = groups.every((g) => g.items.length === 0);

  return (
    <Popover
      trigger={trigger}
      open={open}
      onOpenChange={onOpenChange}
      align={align}
      label={label}
      role="menu"
      triggerLabel={triggerLabel}
      triggerClassName={triggerClassName}
      panelClassName={`min-w-[200px] py-1 ${menuClassName ?? ""}`}
      onOpenFocus={onOpenFocus}
      onPanelKeyDown={onPanelKeyDown}
      disabled={disabled}
    >
      {({ close }) => {
        closeRef.current = close;
        if (empty && emptyMessage) {
          return <div className="px-3 py-2 text-sm text-ink-muted">{emptyMessage}</div>;
        }
        return groups.map((group, gi) => (
          <div key={gi} role="group" aria-label={group.label}>
            {gi > 0 && <div role="separator" className="my-1 border-t border-border" />}
            {group.label && (
              <div
                aria-hidden
                className={`px-3 py-1.5 text-xs text-ink-muted ${compact ? "py-1" : ""}`}
              >
                {group.label}
              </div>
            )}
            {group.items.map((item, ii) => {
              const role =
                group.selection === "single"
                  ? "menuitemradio"
                  : group.selection === "multiple"
                    ? "menuitemcheckbox"
                    : "menuitem";
              const style = `flex w-full items-center gap-2 text-left transition-colors focus:outline-none ${
                compact ? "px-2.5 py-1.5 text-xs" : "px-3 py-2 text-sm"
              } ${
                item.disabled
                  ? "cursor-not-allowed text-ink-subtle"
                  : item.variant === "danger"
                    ? "text-danger hover:bg-danger-surface focus:bg-danger-surface"
                    : "text-ink hover:bg-surface-muted focus:bg-surface-muted"
              }`;
              const choose = () => {
                if (item.disabled) return;
                if (!item.keepOpen) close();
                item.onClick?.();
              };
              const body = (
                <>
                  {group.selection && <CheckMark visible={!!item.checked} />}
                  {item.icon && (
                    <span
                      aria-hidden
                      className="inline-flex h-4 w-4 shrink-0 items-center justify-center"
                    >
                      {item.icon}
                    </span>
                  )}
                  <span className="min-w-0 flex-1 truncate">{item.label}</span>
                  {item.hint && (
                    <span className="shrink-0 text-xs text-ink-muted">{item.hint}</span>
                  )}
                </>
              );
              const common = {
                role,
                tabIndex: -1,
                "aria-checked": group.selection ? !!item.checked : undefined,
                "aria-disabled": item.disabled || undefined,
                "data-testid": item.testId,
                className: style,
              };
              if (item.href && !item.disabled) {
                return (
                  <Link key={ii} href={item.href} {...common} onClick={choose}>
                    {body}
                  </Link>
                );
              }
              return (
                <button key={ii} type="button" {...common} onClick={choose}>
                  {body}
                </button>
              );
            })}
          </div>
        ));
      }}
    </Popover>
  );
}
