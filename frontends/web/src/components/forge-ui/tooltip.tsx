import React, { useCallback, useEffect, useId, useLayoutEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";

/**
 * Tooltip — a short text hint for a control, shown on hover and on keyboard
 * focus (WAI-ARIA tooltip pattern, WCAG 1.4.13 content on hover or focus).
 *
 * Use it for a name or a short explanation: an icon button's label, why a
 * control is disabled, a keyboard shortcut. It never holds anything
 * interactive — a link or a button inside a hint cannot be reached. Use
 * Popover for that.
 *
 * Behaviour:
 *
 *   - Hover shows it after `delay`; moving straight to a neighbouring
 *     tooltip shows that one at once (a toolbar reads like one control).
 *     Only one is ever visible. Touch never shows one: a tap is a press.
 *   - Keyboard focus shows it at once, but only focus the user can see
 *     (`:focus-visible`), so a click does not leave a hint hanging.
 *   - It is hoverable: the pointer can travel from the trigger onto the
 *     bubble (to read a long one, or zoom it) without it closing.
 *   - Escape hides it without moving focus or the pointer. A visible tooltip
 *     is the top layer, so it claims that Escape; the next one reaches the
 *     dialog or shortcut behind it.
 *   - Pressing the trigger hides it until the pointer leaves, and so does a
 *     press anywhere else. It never shows over the trigger's own open menu
 *     (`aria-expanded="true"`).
 *   - The bubble portals to <body> with fixed positioning, so a clipping
 *     ancestor never cuts it; it flips to the opposite side when there is no
 *     room, is clamped to the viewport, and follows the trigger on scroll.
 *
 * Accessibility: the hint's text is always in the DOM (a `hidden` copy), so
 * the trigger is described — or, with `type="label"`, NAMED — by it whether
 * or not the bubble is showing. Use `type="label"` for an icon-only control
 * whose only name is the tooltip.
 *
 * The trigger is either:
 *
 *   - a ReactNode, which Tooltip wraps in an inline-flex <span>
 *     (`triggerClassName` replaces `inline-flex`). The description lands on
 *     the first focusable element inside. When nothing inside can take focus
 *     — a disabled button, an icon, truncated text — the wrapper itself
 *     becomes focusable, so the hint is reachable from the keyboard
 *     (`focusable={false}` opts out). The wrapper is also what receives the
 *     hover, which is how a DISABLED button still explains itself.
 *   - a render function that receives the props to spread on YOUR element,
 *     with no wrapper at all:
 *
 *       <Tooltip content="Copy" type="label">
 *         {(props) => <button {...props} className="…"><CopyIcon /></button>}
 *       </Tooltip>
 *
 * Visual: an inverted bubble (`bg-ink`, `text-surface`), which reads as a
 * transient hint rather than a panel in light and dark alike, at the 7:1 the
 * palette contract holds ink and surface to. Its transparent border is what
 * draws its edge in forced-colors mode, where the fill is replaced.
 */

export type TooltipSide = "top" | "bottom" | "left" | "right";

export interface TooltipTriggerProps {
  ref: React.RefCallback<HTMLElement>;
  "aria-describedby"?: string;
  "aria-labelledby"?: string;
}

export interface TooltipProps {
  /** The hint. Short: a few words, a sentence at most. */
  content: React.ReactNode;
  children: React.ReactNode | ((props: TooltipTriggerProps) => React.ReactElement);
  /** Preferred side; flips when the viewport has no room. Defaults to "top". */
  side?: TooltipSide;
  /** Hover delay in ms before the first tooltip shows. Defaults to 400. */
  delay?: number;
  /** Keys shown after the text, e.g. "⌘K" or ["Shift", "D"]. */
  shortcut?: string | string[];
  /**
   * "description" (default): the hint describes the trigger
   * (aria-describedby). "label": the hint IS the trigger's accessible name
   * (aria-labelledby) — for an icon-only control with no other name.
   */
  type?: "description" | "label";
  /** Controlled visibility; pair with onOpenChange. */
  open?: boolean;
  onOpenChange?: (open: boolean) => void;
  /** Never show (the trigger renders unchanged). */
  disabled?: boolean;
  /**
   * Whether the wrapper of a ReactNode trigger may take focus when nothing
   * inside it can. Defaults to true; ignored for a render-function trigger.
   */
  focusable?: boolean;
  /** Classes on the <span> wrapping a ReactNode trigger. Defaults to "inline-flex". */
  triggerClassName?: string;
  /** Classes on the bubble (width, text alignment). */
  tooltipClassName?: string;
}

const VIEWPORT_PADDING = 8;
const GAP = 6;
// How long the pointer has to cross from the trigger onto the bubble.
const HOVER_GRACE_MS = 120;
// After one tooltip hides, the next shows with no delay for this long.
const SKIP_DELAY_MS = 300;

const FOCUSABLE =
  'a[href], button:not([disabled]), input:not([disabled]):not([type="hidden"]), select:not([disabled]), textarea:not([disabled]), [contenteditable="true"], [tabindex]:not([tabindex="-1"])';

const FOCUS_RING =
  "rounded-sm focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent";

// One tooltip visible at a time, and the warm-up window after it hides.
let visibleTooltip: symbol | null = null;
let hideVisibleTooltip: (() => void) | null = null;
let lastHiddenAt = 0;

const useIsomorphicLayoutEffect = typeof window === "undefined" ? useEffect : useLayoutEffect;

function addToken(el: Element, attr: string, token: string) {
  const tokens = (el.getAttribute(attr) ?? "").split(/\s+/).filter(Boolean);
  if (!tokens.includes(token)) el.setAttribute(attr, [...tokens, token].join(" "));
}

function removeToken(el: Element, attr: string, token: string) {
  const tokens = (el.getAttribute(attr) ?? "").split(/\s+/).filter((t) => t && t !== token);
  if (tokens.length > 0) el.setAttribute(attr, tokens.join(" "));
  else el.removeAttribute(attr);
}

// The first focusable element inside a wrapper. Almost every trigger IS its
// wrapper's first child (a button), so that is checked before the subtree.
function focusableIn(host: HTMLElement): HTMLElement | null {
  const first = host.firstElementChild;
  if (first instanceof HTMLElement && first.matches(FOCUSABLE)) return first;
  return host.querySelector<HTMLElement>(FOCUSABLE);
}

function isFocusVisible(el: Element): boolean {
  try {
    return el.matches(":focus-visible");
  } catch {
    // An engine without :focus-visible: treat every focus as visible.
    return true;
  }
}

// The trigger's box. A `display: contents` wrapper has none of its own, so
// its first child's stands in.
function anchorRect(host: HTMLElement): DOMRect {
  const rect = host.getBoundingClientRect();
  if (rect.width === 0 && rect.height === 0 && host.firstElementChild) {
    return host.firstElementChild.getBoundingClientRect();
  }
  return rect;
}

function place(side: TooltipSide, a: DOMRect, w: number, h: number) {
  const vw = window.innerWidth;
  const vh = window.innerHeight;
  const fits: Record<TooltipSide, boolean> = {
    top: a.top - GAP - h >= VIEWPORT_PADDING,
    bottom: a.bottom + GAP + h <= vh - VIEWPORT_PADDING,
    left: a.left - GAP - w >= VIEWPORT_PADDING,
    right: a.right + GAP + w <= vw - VIEWPORT_PADDING,
  };
  const opposite: Record<TooltipSide, TooltipSide> = {
    top: "bottom",
    bottom: "top",
    left: "right",
    right: "left",
  };
  const chosen = !fits[side] && fits[opposite[side]] ? opposite[side] : side;
  let top: number;
  let left: number;
  if (chosen === "top" || chosen === "bottom") {
    top = chosen === "top" ? a.top - GAP - h : a.bottom + GAP;
    left = a.left + a.width / 2 - w / 2;
  } else {
    left = chosen === "left" ? a.left - GAP - w : a.right + GAP;
    top = a.top + a.height / 2 - h / 2;
  }
  left = Math.min(
    Math.max(left, VIEWPORT_PADDING),
    Math.max(VIEWPORT_PADDING, vw - w - VIEWPORT_PADDING),
  );
  top = Math.min(
    Math.max(top, VIEWPORT_PADDING),
    Math.max(VIEWPORT_PADDING, vh - h - VIEWPORT_PADDING),
  );
  return { top, left, side: chosen };
}

export default function Tooltip({
  content,
  children,
  side = "top",
  delay = 400,
  shortcut,
  type = "description",
  open: controlledOpen,
  onOpenChange,
  disabled,
  focusable = true,
  triggerClassName,
  tooltipClassName,
}: TooltipProps) {
  const [uncontrolledOpen, setUncontrolledOpen] = useState(false);
  const active = !disabled && content != null && content !== false && content !== "";
  const open = active && (controlledOpen ?? uncontrolledOpen);
  const [host, setHost] = useState<HTMLElement | null>(null);
  const bubbleRef = useRef<HTMLDivElement | null>(null);
  const [position, setPosition] = useState<{ top: number; left: number; side: TooltipSide } | null>(
    null,
  );
  const textId = useId();
  const bubbleId = useId();
  const showTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const hideTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  // A press on the trigger silences it until the pointer leaves.
  const suppressed = useRef(false);
  const attr = type === "label" ? "aria-labelledby" : "aria-describedby";
  const keys = shortcut === undefined ? [] : Array.isArray(shortcut) ? shortcut : [shortcut];

  const setOpen = useCallback(
    (next: boolean) => {
      if (controlledOpen === undefined) setUncontrolledOpen(next);
      onOpenChange?.(next);
    },
    [controlledOpen, onOpenChange],
  );

  const clearTimers = useCallback(() => {
    if (showTimer.current) clearTimeout(showTimer.current);
    if (hideTimer.current) clearTimeout(hideTimer.current);
    showTimer.current = null;
    hideTimer.current = null;
  }, []);

  const hide = useCallback(() => {
    clearTimers();
    setOpen(false);
  }, [clearTimers, setOpen]);

  // The element the hint is attached to: the caller's element for a render
  // function, the first focusable element inside the wrapper, or the wrapper.
  const describedElement = useCallback((): HTMLElement | null => {
    if (!host) return null;
    if (typeof children === "function") return host;
    return focusableIn(host) ?? host;
  }, [host, children]);

  const show = useCallback(() => {
    clearTimers();
    if (!active || !host?.isConnected) return;
    if (describedElement()?.getAttribute("aria-expanded") === "true") return;
    setOpen(true);
  }, [active, host, clearTimers, describedElement, setOpen]);

  // Listeners read the latest callbacks through refs, so they attach once
  // per trigger element rather than on every render of the caller.
  const showRef = useRef(show);
  const hideRef = useRef(hide);
  showRef.current = show;
  hideRef.current = hide;

  // One visible tooltip; the warm-up window starts when it hides.
  const me = useRef(Symbol("tooltip"));
  useEffect(() => {
    if (!open) return;
    const id = me.current;
    if (visibleTooltip !== id) hideVisibleTooltip?.();
    visibleTooltip = id;
    hideVisibleTooltip = () => hideRef.current();
    return () => {
      if (visibleTooltip === id) {
        visibleTooltip = null;
        hideVisibleTooltip = null;
        lastHiddenAt = Date.now();
      }
    };
  }, [open]);

  useEffect(() => clearTimers, [clearTimers]);

  // Attach the hint to a ReactNode trigger. Checked every render — the
  // wrapped element can change (a button gains `disabled`, a child is
  // swapped) — but the DOM is written only when the attachment moves: a
  // list of a few hundred hinted rows re-renders without touching them.
  const attached = useRef<{ el: HTMLElement; attr: string; madeFocusable: boolean } | null>(null);
  const detach = useCallback(() => {
    const cur = attached.current;
    if (!cur) return;
    removeToken(cur.el, cur.attr, textId);
    if (cur.madeFocusable) cur.el.removeAttribute("tabindex");
    attached.current = null;
  }, [textId]);
  useIsomorphicLayoutEffect(() => {
    if (!active || !host || typeof children === "function") {
      detach();
      return;
    }
    const inner = focusableIn(host);
    const target = inner ?? host;
    const cur = attached.current;
    if (cur && (cur.el !== target || cur.attr !== attr)) detach();
    if (!attached.current) {
      const madeFocusable = !inner && focusable && !host.hasAttribute("tabindex");
      if (madeFocusable) host.tabIndex = 0;
      attached.current = { el: target, attr, madeFocusable };
    }
    // A re-render of the child may have rewritten its own aria-describedby.
    addToken(target, attr, textId);
  });
  useEffect(() => detach, [detach]);

  // Hover, press and focus on the trigger. Native listeners: the trigger is
  // the caller's element or a plain wrapper, not a control of ours.
  useEffect(() => {
    if (!host || !active) return;
    function onPointerEnter(e: PointerEvent) {
      if (e.pointerType === "touch" || suppressed.current) return;
      if (hideTimer.current) clearTimeout(hideTimer.current);
      hideTimer.current = null;
      if (showTimer.current) return;
      const wait = visibleTooltip || Date.now() - lastHiddenAt < SKIP_DELAY_MS ? 0 : delay;
      showTimer.current = setTimeout(() => showRef.current(), wait);
    }
    function onPointerLeave() {
      suppressed.current = false;
      if (showTimer.current) clearTimeout(showTimer.current);
      showTimer.current = null;
      hideTimer.current = setTimeout(() => hideRef.current(), HOVER_GRACE_MS);
    }
    function onPointerDown() {
      suppressed.current = true;
      hideRef.current();
    }
    function onFocusIn(e: FocusEvent) {
      if (e.target instanceof Element && isFocusVisible(e.target)) showRef.current();
    }
    function onFocusOut(e: FocusEvent) {
      const next = e.relatedTarget as Node | null;
      if (next && host?.contains(next)) return;
      hideRef.current();
    }
    host.addEventListener("pointerenter", onPointerEnter);
    host.addEventListener("pointerleave", onPointerLeave);
    host.addEventListener("pointerdown", onPointerDown);
    host.addEventListener("focusin", onFocusIn);
    host.addEventListener("focusout", onFocusOut);
    return () => {
      host.removeEventListener("pointerenter", onPointerEnter);
      host.removeEventListener("pointerleave", onPointerLeave);
      host.removeEventListener("pointerdown", onPointerDown);
      host.removeEventListener("focusin", onFocusIn);
      host.removeEventListener("focusout", onFocusOut);
    };
  }, [host, active, delay]);

  // While visible: Escape and outside presses dismiss it; it follows the
  // trigger on scroll, resize and content changes.
  const reposition = useCallback(() => {
    const bubble = bubbleRef.current;
    if (!host || !bubble) return;
    if (!host.isConnected) {
      hideRef.current();
      return;
    }
    const next = place(side, anchorRect(host), bubble.offsetWidth, bubble.offsetHeight);
    setPosition((cur) =>
      cur && cur.top === next.top && cur.left === next.left && cur.side === next.side ? cur : next,
    );
  }, [host, side]);

  useIsomorphicLayoutEffect(() => {
    if (open) reposition();
    else setPosition(null);
  }, [open, reposition]);

  useEffect(() => {
    if (!open) return;
    const bubble = bubbleRef.current;
    function onKeyDown(e: KeyboardEvent) {
      if (e.key !== "Escape") return;
      e.preventDefault();
      e.stopImmediatePropagation();
      hideRef.current();
    }
    function onPointerDown(e: PointerEvent) {
      if (host?.contains(e.target as Node)) return;
      hideRef.current();
    }
    // Hoverable: the pointer may rest on the bubble itself.
    function onBubbleEnter() {
      if (hideTimer.current) clearTimeout(hideTimer.current);
      hideTimer.current = null;
    }
    function onBubbleLeave() {
      hideTimer.current = setTimeout(() => hideRef.current(), HOVER_GRACE_MS);
    }
    const observer = typeof ResizeObserver === "undefined" ? null : new ResizeObserver(reposition);
    if (bubble) observer?.observe(bubble);
    if (host) observer?.observe(host);
    bubble?.addEventListener("pointerenter", onBubbleEnter);
    bubble?.addEventListener("pointerleave", onBubbleLeave);
    window.addEventListener("keydown", onKeyDown, true);
    document.addEventListener("pointerdown", onPointerDown, true);
    window.addEventListener("resize", reposition);
    window.addEventListener("scroll", reposition, true);
    return () => {
      observer?.disconnect();
      bubble?.removeEventListener("pointerenter", onBubbleEnter);
      bubble?.removeEventListener("pointerleave", onBubbleLeave);
      window.removeEventListener("keydown", onKeyDown, true);
      document.removeEventListener("pointerdown", onPointerDown, true);
      window.removeEventListener("resize", reposition);
      window.removeEventListener("scroll", reposition, true);
    };
  }, [open, host, reposition]);

  const shortcutKeys =
    keys.length > 0 ? (
      <span className="ml-2 inline-flex gap-0.5 align-baseline">
        {keys.map((key, i) => (
          <kbd
            key={i}
            className="rounded-sm border border-surface/30 px-1 font-sans text-[0.6875rem] leading-4 text-surface"
          >
            {key}
          </kbd>
        ))}
      </span>
    ) : null;

  // The hint's text, always present, so the trigger is described (or named)
  // whether or not the bubble shows. `hidden` keeps it out of layout and
  // out of the reading order; aria-describedby/-labelledby still read it.
  const text = active ? (
    <span id={textId} hidden>
      {content}
      {keys.length > 0 ? ` (${keys.join(" ")})` : null}
    </span>
  ) : null;

  const bubble =
    open && typeof document !== "undefined"
      ? createPortal(
          <div
            ref={bubbleRef}
            id={bubbleId}
            role="tooltip"
            data-side={position?.side ?? side}
            className={`fixed z-[60] w-max max-w-[min(20rem,calc(100vw-16px))] rounded-md border border-transparent bg-ink px-2 py-1 text-xs leading-snug font-medium break-words text-balance text-surface shadow-md ${tooltipClassName ?? ""}`}
            style={{
              top: position?.top ?? 0,
              left: position?.left ?? 0,
              // Measured before it is shown, so it never flashes at 0,0.
              visibility: position ? "visible" : "hidden",
            }}
          >
            {content}
            {shortcutKeys}
          </div>,
          document.body,
        )
      : null;

  if (typeof children === "function") {
    const props: TooltipTriggerProps = { ref: setHost };
    if (active) props[attr] = textId;
    return (
      <>
        {children(props)}
        {text}
        {bubble}
      </>
    );
  }

  return (
    <>
      <span ref={setHost} className={`${triggerClassName ?? "inline-flex"} ${FOCUS_RING}`}>
        {children}
        {text}
      </span>
      {bubble}
    </>
  );
}
