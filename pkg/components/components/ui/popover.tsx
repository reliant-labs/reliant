import React, { useCallback, useEffect, useId, useLayoutEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";

/**
 * Popover — a panel anchored to a trigger: the positioning, dismissal and
 * focus behaviour every anchored overlay needs, with no opinion about what
 * the panel holds. DropdownMenu is built on it (role="menu"); use Popover
 * directly for anything that is not a list of commands — a small form, a set
 * of toggles, a preview.
 *
 * Behaviour:
 *
 *   - The panel portals to <body> with fixed positioning, so an
 *     `overflow: hidden` ancestor (a scrolling list, a table, a card) never
 *     clips it. It opens below the trigger and flips above when there is no
 *     room, is clamped to the viewport, and follows the trigger on scroll and
 *     resize.
 *   - Escape closes it and returns focus to the trigger, wherever focus is.
 *     An open popover is the top layer, so it takes the key first (a
 *     capture listener on window) and claims it: a dialog it sits in, or an
 *     app-level shortcut bound to Escape, does not also act. Stacked
 *     popovers close one per press, newest first.
 *   - A pointer press outside the trigger and panel closes it, and so does
 *     focus leaving both (Tab out of the panel).
 *   - Opening moves focus into the panel: its first focusable element when
 *     opened from the keyboard (Enter, Space, ArrowDown), the panel itself
 *     when opened with the pointer. `onOpenFocus` overrides that.
 *
 * The trigger is either a ReactNode, which Popover wraps in a <button>
 * (`triggerLabel` names an icon-only one, `triggerClassName` styles it), or a
 * render function that receives the props to spread on YOUR element — so a
 * trigger can keep its own button, test id or tooltip wrapper:
 *
 *   <Popover trigger={(props) => <button {...props} className="…">Ports</button>}>
 *
 * Open state is uncontrolled by default; pass `open` + `onOpenChange` to own
 * it. `children` may be a function of `{ close }` for content that closes the
 * panel itself.
 *
 * Anchored to a POINT instead (a context menu at the pointer): pass `anchor`
 * ({ x, y } in viewport pixels) with a controlled `open`, and no trigger. The
 * panel opens below-right of the point and flips left or above when there is
 * no room; Escape and choosing return focus to whatever had it when the panel
 * opened. Name the panel with `label`, since there is no trigger to name it.
 */

export interface PopoverTriggerProps {
  ref: React.RefCallback<HTMLElement>;
  id: string;
  onClick: (event: React.MouseEvent<HTMLElement>) => void;
  onKeyDown: (event: React.KeyboardEvent<HTMLElement>) => void;
  "aria-haspopup": "dialog" | "menu";
  "aria-expanded": boolean;
  "aria-controls": string | undefined;
}

/** How the panel was opened: a keyboard user expects focus on the first item. */
export type PopoverOpenVia = "keyboard" | "keyboard-last" | "pointer";

export interface PopoverProps {
  /** The control that opens the panel. Omit it only with `anchor`. */
  trigger?: React.ReactNode | ((props: PopoverTriggerProps) => React.ReactElement);
  /**
   * A viewport point to open at, instead of the trigger's edge (a context
   * menu). Use with a controlled `open`.
   */
  anchor?: { x: number; y: number };
  /**
   * How an `anchor` open was triggered, which decides where focus lands:
   * "keyboard" (Shift+F10, the ContextMenu key) moves it to the first
   * focusable element. Defaults to "pointer". A trigger knows this itself.
   */
  openVia?: PopoverOpenVia;
  children: React.ReactNode | ((api: { close: () => void }) => React.ReactNode);
  /** Controlled open state; pair with onOpenChange. */
  open?: boolean;
  onOpenChange?: (open: boolean) => void;
  /** Which trigger edge the panel lines up with. Defaults to "left". */
  align?: "left" | "right";
  /** Preferred side; flips when the viewport has no room. Defaults to "bottom". */
  side?: "bottom" | "top";
  /** Accessible name of the panel. Defaults to being labelled by the trigger. */
  label?: string;
  /** The panel's role. DropdownMenu passes "menu". Defaults to "dialog". */
  role?: "dialog" | "menu";
  /** Accessible name of a ReactNode trigger with no text (an icon button). */
  triggerLabel?: string;
  /** Classes on the <button> wrapping a ReactNode trigger. */
  triggerClassName?: string;
  /** Classes on the panel (width, max-height, padding). */
  panelClassName?: string;
  /** Moves focus into the panel when it opens; replaces the default. */
  onOpenFocus?: (panel: HTMLElement, via: PopoverOpenVia) => void;
  /**
   * Extra keydown handling inside the panel (DropdownMenu's arrow keys);
   * preventDefault() claims the key.
   */
  onPanelKeyDown?: (event: KeyboardEvent) => void;
  disabled?: boolean;
}

const VIEWPORT_PADDING = 8;
const GAP = 4;

const FOCUSABLE =
  'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])';

// Open popovers, oldest first; Escape belongs to the newest.
const openStack: symbol[] = [];

// useLayoutEffect warns during server rendering; the panel only ever renders
// in the browser, so the effect is only needed there.
const useIsomorphicLayoutEffect = typeof window === "undefined" ? useEffect : useLayoutEffect;

export default function Popover({
  trigger,
  anchor,
  openVia,
  children,
  open: controlledOpen,
  onOpenChange,
  align = "left",
  side = "bottom",
  label,
  role = "dialog",
  triggerLabel,
  triggerClassName,
  panelClassName,
  onOpenFocus,
  onPanelKeyDown,
  disabled,
}: PopoverProps) {
  const [uncontrolledOpen, setUncontrolledOpen] = useState(false);
  const open = controlledOpen ?? uncontrolledOpen;
  const triggerRef = useRef<HTMLElement | null>(null);
  const panelRef = useRef<HTMLDivElement | null>(null);
  // Where focus goes back to on close: the trigger, or — anchored to a point
  // with no trigger — whatever was focused when the panel opened.
  const restoreFocusRef = useRef<HTMLElement | null>(null);
  const openedVia = useRef<PopoverOpenVia>("pointer");
  const [position, setPosition] = useState<{ top: number; left: number } | null>(null);
  const triggerId = useId();
  const panelId = useId();

  const setOpen = useCallback(
    (next: boolean) => {
      if (controlledOpen === undefined) setUncontrolledOpen(next);
      onOpenChange?.(next);
    },
    [controlledOpen, onOpenChange],
  );

  const close = useCallback(
    (returnFocus = true) => {
      setOpen(false);
      if (returnFocus) (triggerRef.current ?? restoreFocusRef.current)?.focus();
    },
    [setOpen],
  );

  const toggle = useCallback(
    (via: PopoverOpenVia) => {
      if (disabled) return;
      openedVia.current = via;
      setOpen(!open);
    },
    [disabled, open, setOpen],
  );

  const anchorX = anchor?.x;
  const anchorY = anchor?.y;
  const reposition = useCallback(() => {
    const panel = panelRef.current;
    if (!panel) return;
    const p = panel.getBoundingClientRect();
    if (anchorX !== undefined && anchorY !== undefined) {
      // A point: below-right of it, flipped left / above when it would
      // overflow, then clamped like a trigger-anchored panel.
      let left =
        anchorX + p.width <= window.innerWidth - VIEWPORT_PADDING ? anchorX : anchorX - p.width;
      let top =
        anchorY + p.height <= window.innerHeight - VIEWPORT_PADDING ? anchorY : anchorY - p.height;
      left = Math.min(
        Math.max(left, VIEWPORT_PADDING),
        Math.max(VIEWPORT_PADDING, window.innerWidth - p.width - VIEWPORT_PADDING),
      );
      top = Math.min(
        Math.max(top, VIEWPORT_PADDING),
        Math.max(VIEWPORT_PADDING, window.innerHeight - p.height - VIEWPORT_PADDING),
      );
      setPosition((cur) => (cur && cur.top === top && cur.left === left ? cur : { top, left }));
      return;
    }
    const anchorEl = triggerRef.current;
    if (!anchorEl) return;
    const a = anchorEl.getBoundingClientRect();
    const below = a.bottom + GAP;
    const above = a.top - p.height - GAP;
    const fitsBelow = below + p.height <= window.innerHeight - VIEWPORT_PADDING;
    const fitsAbove = above >= VIEWPORT_PADDING;
    let top =
      side === "top"
        ? fitsAbove || !fitsBelow
          ? above
          : below
        : fitsBelow || !fitsAbove
          ? below
          : above;
    let left = align === "right" ? a.right - p.width : a.left;
    left = Math.min(
      Math.max(left, VIEWPORT_PADDING),
      Math.max(VIEWPORT_PADDING, window.innerWidth - p.width - VIEWPORT_PADDING),
    );
    top = Math.min(
      Math.max(top, VIEWPORT_PADDING),
      Math.max(VIEWPORT_PADDING, window.innerHeight - p.height - VIEWPORT_PADDING),
    );
    setPosition((cur) => (cur && cur.top === top && cur.left === left ? cur : { top, left }));
  }, [align, side, anchorX, anchorY]);

  useIsomorphicLayoutEffect(() => {
    if (open) reposition();
    else setPosition(null);
  }, [open, reposition]);

  // Remember what had focus as the panel opens, so a trigger-less panel can
  // hand it back.
  useIsomorphicLayoutEffect(() => {
    if (!open) return;
    const active = document.activeElement;
    restoreFocusRef.current =
      active instanceof HTMLElement && active !== document.body ? active : null;
  }, [open]);

  // Follow the trigger while open: the page scrolling (any scroller — capture
  // phase), the window resizing, or the panel's own content changing size.
  useEffect(() => {
    if (!open) return;
    const observer = typeof ResizeObserver === "undefined" ? null : new ResizeObserver(reposition);
    if (panelRef.current) observer?.observe(panelRef.current);
    if (triggerRef.current) observer?.observe(triggerRef.current);
    window.addEventListener("resize", reposition);
    window.addEventListener("scroll", reposition, true);
    return () => {
      observer?.disconnect();
      window.removeEventListener("resize", reposition);
      window.removeEventListener("scroll", reposition, true);
    };
  }, [open, reposition]);

  // Move focus in once the panel is placed (a focus() on an element still at
  // its unpositioned spot would scroll the page to it).
  const focusedForOpen = useRef(false);
  useEffect(() => {
    if (!open) {
      focusedForOpen.current = false;
      return;
    }
    const panel = panelRef.current;
    if (!panel || !position || focusedForOpen.current) return;
    focusedForOpen.current = true;
    const via = anchor ? (openVia ?? "pointer") : openedVia.current;
    if (onOpenFocus) {
      onOpenFocus(panel, via);
      return;
    }
    const first = via === "pointer" ? null : panel.querySelector<HTMLElement>(FOCUSABLE);
    (first ?? panel).focus({ preventScroll: true });
  }, [open, position, onOpenFocus, anchor, openVia]);

  // A press outside both the trigger and the panel dismisses, leaving focus
  // where the press put it.
  useEffect(() => {
    if (!open) return;
    function onPointerDown(e: PointerEvent) {
      const target = e.target as Node;
      if (triggerRef.current?.contains(target) || panelRef.current?.contains(target)) return;
      close(false);
    }
    document.addEventListener("pointerdown", onPointerDown);
    return () => document.removeEventListener("pointerdown", onPointerDown);
  }, [open, close]);

  // The panel's own keys and focus loss. Native listeners rather than JSX
  // handlers: the panel is a dialog/menu container, not a control.
  const panelKeyDown = useRef(onPanelKeyDown);
  panelKeyDown.current = onPanelKeyDown;
  useEffect(() => {
    const panel = panelRef.current;
    if (!open || !panel) return;
    function onKeyDown(e: KeyboardEvent) {
      panelKeyDown.current?.(e);
    }
    function onFocusOut(e: FocusEvent) {
      const next = e.relatedTarget as Node | null;
      // null: focus went to the page itself (a click on nothing), which the
      // pointerdown handler above already decided.
      if (!next || panel?.contains(next) || triggerRef.current?.contains(next)) return;
      close(false);
    }
    panel.addEventListener("keydown", onKeyDown);
    panel.addEventListener("focusout", onFocusOut);
    return () => {
      panel.removeEventListener("keydown", onKeyDown);
      panel.removeEventListener("focusout", onFocusOut);
    };
  }, [open, close]);

  // Escape, from anywhere: window capture runs before any other listener.
  useEffect(() => {
    if (!open) return;
    const id = Symbol("popover");
    openStack.push(id);
    function onEscape(e: KeyboardEvent) {
      if (e.key !== "Escape" || openStack[openStack.length - 1] !== id) return;
      e.preventDefault();
      e.stopImmediatePropagation();
      close();
    }
    window.addEventListener("keydown", onEscape, true);
    return () => {
      window.removeEventListener("keydown", onEscape, true);
      openStack.splice(openStack.indexOf(id), 1);
    };
  }, [open, close]);

  const triggerProps: PopoverTriggerProps = {
    ref: (node) => {
      triggerRef.current = node;
    },
    id: triggerId,
    // A click with detail 0 came from Enter or Space on the focused trigger.
    onClick: (e) => toggle(e.detail === 0 ? "keyboard" : "pointer"),
    onKeyDown: (e) => {
      if (e.key === "ArrowDown" || e.key === "ArrowUp") {
        e.preventDefault();
        if (!open) toggle(e.key === "ArrowUp" ? "keyboard-last" : "keyboard");
      }
    },
    "aria-haspopup": role,
    "aria-expanded": open,
    "aria-controls": open ? panelId : undefined,
  };

  const triggerElement =
    trigger === undefined ? null : typeof trigger === "function" ? (
      trigger(triggerProps)
    ) : (
      <button
        type="button"
        {...triggerProps}
        aria-label={triggerLabel}
        disabled={disabled}
        className={triggerClassName ?? "cursor-pointer"}
      >
        {trigger}
      </button>
    );

  const panel =
    open && typeof document !== "undefined"
      ? createPortal(
          <div
            ref={panelRef}
            id={panelId}
            role={role}
            tabIndex={-1}
            aria-label={label}
            aria-labelledby={label || trigger === undefined ? undefined : triggerId}
            className={`fixed z-50 rounded-lg border border-border bg-surface shadow-lg focus:outline-none ${panelClassName ?? ""}`}
            style={{
              top: position?.top ?? 0,
              left: position?.left ?? 0,
              // Measured before it is shown, so it never flashes at 0,0.
              visibility: position ? "visible" : "hidden",
              maxWidth: `calc(100vw - ${VIEWPORT_PADDING * 2}px)`,
            }}
          >
            {typeof children === "function" ? children({ close: () => close() }) : children}
          </div>,
          document.body,
        )
      : null;

  return (
    <>
      {triggerElement}
      {panel}
    </>
  );
}
