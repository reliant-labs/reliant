import React, { useEffect, useId, useLayoutEffect, useRef } from "react";
import { createPortal } from "react-dom";

/**
 * Modal — a dialog over the page: header (title, description, header
 * actions, close button), a scrolling body, and a footer pinned below it.
 *
 * What it guarantees, because a dialog that gets these wrong is a trap:
 *
 *   - Focus moves in on open — to an element that took `autoFocus`, else the
 *     body's first focusable, else the footer's, else the panel. Never the
 *     close button: Enter on a dialog that just opened must not dismiss it.
 *   - Tab and Shift+Tab cycle inside the dialog.
 *   - Escape closes it, unless something inside already handled the key (an
 *     open menu calls preventDefault). Only the TOP dialog responds when
 *     dialogs stack; the one underneath keeps its focus trap off until the
 *     top one closes.
 *   - Closing returns focus to whatever had it before the dialog opened. If
 *     that element is gone (the menu item that opened the dialog unmounted),
 *     `fallbackFocus` decides where focus goes instead.
 *   - The page behind does not scroll. The lock is counted, so a nested
 *     dialog closing does not unlock the page under the one still open.
 *   - It portals to <body>, so a transformed or `overflow: hidden` ancestor
 *     never clips it or traps it under a sibling's stacking context.
 *     `portal={false}` renders it in place (a preview harness pinning it in a
 *     box).
 *
 * Layout: the panel is at most 90% of the visible viewport tall and the body
 * scrolls, so a long form's footer — its primary action — never scrolls out
 * of reach. `sheetOnMobile` (default on for lg and wider, whose forms are too
 * tall for a floating card on a phone) makes it a full-screen sheet below
 * the `sm` breakpoint.
 *
 * "Visible" means the VISUAL viewport, not `dvh`. A phone's software
 * keyboard shrinks (and on iOS pans) the visual viewport only; `100dvh` and
 * a fixed `inset-0` box keep the layout viewport's size, so a sheet sized
 * with them leaves its footer and focused field under the keyboard. While
 * open, the overlay follows `window.visualViewport` (its height and offset,
 * as the `--modal-vv-height` / `--modal-vv-top` custom properties), and falls
 * back to `100dvh` where the API is missing. Edges respect the device's
 * safe-area insets (notch, home indicator) once the page opts into them with
 * `viewport-fit=cover`. `overlayClassName` is the seam for anything else
 * about the overlay's geometry.
 */

const FOCUSABLE_SELECTOR =
  'a[href], button:not([disabled]), textarea:not([disabled]), input:not([disabled]), select:not([disabled]), [tabindex]:not([tabindex="-1"])';

export interface ModalProps {
  open: boolean;
  onClose: () => void;
  title?: React.ReactNode;
  description?: React.ReactNode;
  children: React.ReactNode;
  /** Pinned below the scrolling body; right-aligned row. */
  footer?: React.ReactNode;
  size?: "sm" | "md" | "lg" | "xl" | "full";
  closeOnOverlay?: boolean;
  /** Controls in the header, beside the close button. */
  headerActions?: React.ReactNode;
  hideCloseButton?: boolean;
  /** Full-screen below `sm`. Defaults to on for lg, xl and full. */
  sheetOnMobile?: boolean;
  /** Extra classes on the dialog panel. */
  className?: string;
  /**
   * Extra classes on the overlay (the scrim that positions the panel):
   * its padding, alignment or backdrop.
   */
  overlayClassName?: string;
  /** Where focus goes on close when the element that had it is gone. */
  fallbackFocus?: () => void;
  /** Render in place instead of in a portal on <body>. Defaults to true. */
  portal?: boolean;
}

const sizeStyles: Record<NonNullable<ModalProps["size"]>, string> = {
  sm: "max-w-sm",
  md: "max-w-lg",
  lg: "max-w-2xl",
  xl: "max-w-4xl",
  full: "max-w-6xl",
};

// Open dialogs, bottom to top. Escape and the focus trap belong to the top
// one only; a module-level stack because stacked dialogs are usually
// rendered by unrelated components.
const openStack: symbol[] = [];
const isTop = (id: symbol) => openStack[openStack.length - 1] === id;

// The body scroll lock, counted across stacked dialogs.
let lockCount = 0;
let savedOverflow = "";
function lockScroll() {
  if (lockCount++ === 0) {
    savedOverflow = document.body.style.overflow;
    document.body.style.overflow = "hidden";
  }
}
function unlockScroll() {
  if (--lockCount === 0) document.body.style.overflow = savedOverflow;
}

// Pins `el` to the visual viewport: the custom properties the overlay's
// classes read, kept current as the keyboard opens, closes or pans the page.
// Set on the element directly rather than through state, so a keyboard
// animation's stream of resize events never re-renders the dialog's content.
function trackVisualViewport(el: HTMLElement): () => void {
  const vv = typeof window === "undefined" ? undefined : window.visualViewport;
  if (!vv) return () => {};
  const sync = () => {
    el.style.setProperty("--modal-vv-height", `${vv.height}px`);
    el.style.setProperty("--modal-vv-top", `${vv.offsetTop}px`);
  };
  sync();
  vv.addEventListener("resize", sync);
  vv.addEventListener("scroll", sync);
  return () => {
    vv.removeEventListener("resize", sync);
    vv.removeEventListener("scroll", sync);
  };
}

export default function Modal({
  open,
  onClose,
  title,
  description,
  children,
  footer,
  size = "md",
  closeOnOverlay = true,
  headerActions,
  hideCloseButton = false,
  sheetOnMobile,
  className,
  overlayClassName,
  fallbackFocus,
  portal = true,
}: ModalProps) {
  const sheet = sheetOnMobile ?? (size === "lg" || size === "xl" || size === "full");
  const overlayRef = useRef<HTMLDivElement>(null);
  const panelRef = useRef<HTMLDivElement>(null);
  const bodyRef = useRef<HTMLDivElement>(null);
  const footerRef = useRef<HTMLDivElement>(null);
  // useId-based labelling: multiple modals on one page never collide the
  // way a hardcoded id="modal-title" did.
  const titleId = useId();
  const descriptionId = useId();
  // Read through refs so a new onClose identity on every parent render does
  // not tear the effect down — which would unlock and relock the page (a
  // visible flash) and re-run initial focus. The refs follow the props in a
  // layout effect, not during render (a render may be thrown away, and React
  // forbids touching refs there); layout effects run before the effect
  // below and before any event the committed dialog can receive.
  const onCloseRef = useRef(onClose);
  const fallbackFocusRef = useRef(fallbackFocus);
  useLayoutEffect(() => {
    onCloseRef.current = onClose;
    fallbackFocusRef.current = fallbackFocus;
  });

  // A layout effect, so the overlay has its visible size before first paint.
  useLayoutEffect(() => {
    if (!open || !overlayRef.current) return;
    return trackVisualViewport(overlayRef.current);
  }, [open]);

  useEffect(() => {
    if (!open) return;
    const id = Symbol("modal");
    openStack.push(id);
    lockScroll();

    // <body> is not an opener: focus falls back to it when the focused
    // element unmounts in the same render that opens the dialog (a menu
    // item that closes its menu), so treat it as "the opener is gone".
    const active = document.activeElement;
    const previouslyFocused =
      active instanceof HTMLElement && active !== document.body ? active : null;
    const panel = panelRef.current;
    if (panel && !panel.contains(document.activeElement)) {
      const initial =
        bodyRef.current?.querySelector<HTMLElement>(FOCUSABLE_SELECTOR) ??
        footerRef.current?.querySelector<HTMLElement>(FOCUSABLE_SELECTOR) ??
        panel;
      initial.focus();
    }

    function onKeyDown(e: KeyboardEvent) {
      if (!isTop(id) || !panelRef.current) return;
      if (e.key === "Escape") {
        if (e.defaultPrevented) return;
        e.preventDefault();
        onCloseRef.current();
        return;
      }
      if (e.key !== "Tab") return;
      const focusable = Array.from(
        panelRef.current.querySelectorAll<HTMLElement>(FOCUSABLE_SELECTOR),
      );
      if (focusable.length === 0) {
        e.preventDefault();
        panelRef.current.focus();
        return;
      }
      const first = focusable[0];
      const last = focusable[focusable.length - 1];
      const active = document.activeElement;
      // A dialog stacked above this one that is not a forge Modal (an image
      // viewer, a command palette) owns its own Tab order.
      const owner = active instanceof Element ? active.closest('[aria-modal="true"]') : null;
      if (owner && owner !== panelRef.current) return;
      const inside = panelRef.current.contains(active);
      if (e.shiftKey && (!inside || active === first || active === panelRef.current)) {
        e.preventDefault();
        last?.focus();
      } else if (!e.shiftKey && (!inside || active === last)) {
        e.preventDefault();
        first?.focus();
      }
    }
    document.addEventListener("keydown", onKeyDown);

    return () => {
      document.removeEventListener("keydown", onKeyDown);
      openStack.splice(openStack.indexOf(id), 1);
      unlockScroll();
      if (previouslyFocused?.isConnected) previouslyFocused.focus();
      else fallbackFocusRef.current?.();
    };
  }, [open]);

  if (!open) return null;

  const dialog = (
    // role="presentation" is the accurate marking, not a lint escape: the
    // scrim is a dimmed backdrop that conveys nothing, so it belongs out of
    // the accessibility tree (its child dialog stays in — presentation does
    // not inherit). Its onClick is a MOUSE CONVENIENCE on top of the real
    // dismissal, which is the Escape handler above; without that handler a
    // backdrop-click-to-close modal would be a keyboard trap.
    //
    // Top and height follow the visual viewport (trackVisualViewport), not
    // inset-0: a fixed inset-0 box resolves against the LARGEST mobile
    // viewport, so centring in it puts the panel below the visible centre
    // while browser chrome is showing, and under the keyboard while one is
    // open. 100dvh is the fallback before the first measurement, and where
    // there is no visualViewport.
    <div
      ref={overlayRef}
      role="presentation"
      className={`fixed inset-x-0 top-[var(--modal-vv-top,0px)] z-50 flex h-[var(--modal-vv-height,100dvh)] items-center justify-center bg-black/50 px-[max(1rem,env(safe-area-inset-left),env(safe-area-inset-right))] pt-[max(1rem,env(safe-area-inset-top))] pb-[max(1rem,env(safe-area-inset-bottom))] backdrop-blur-sm sm:p-6 ${
        sheet ? "max-sm:p-0" : ""
      } ${overlayClassName ?? ""}`}
      onClick={(e) => {
        if (closeOnOverlay && e.target === overlayRef.current) onCloseRef.current();
      }}
    >
      <div
        ref={panelRef}
        tabIndex={-1}
        className={`flex max-h-[min(calc(var(--modal-vv-height,100dvh)*0.9),100%)] w-full ${sizeStyles[size]} flex-col overflow-hidden rounded-xl bg-surface shadow-2xl focus:outline-none ${
          sheet
            ? "max-sm:h-full max-sm:max-h-full max-sm:max-w-none max-sm:rounded-none max-sm:pt-[env(safe-area-inset-top)] max-sm:pb-[env(safe-area-inset-bottom)]"
            : ""
        } ${className ?? ""}`}
        role="dialog"
        aria-modal="true"
        aria-labelledby={title ? titleId : undefined}
        aria-describedby={description ? descriptionId : undefined}
        data-sheet-on-mobile={sheet ? "true" : undefined}
      >
        {/* Header */}
        {(title || description || headerActions) && (
          <div className="shrink-0 border-b border-border px-6 py-4 max-sm:px-4">
            <div className="flex items-start justify-between gap-4">
              <div className="min-w-0">
                {title && (
                  <h2 id={titleId} className="truncate text-lg font-semibold text-ink">
                    {title}
                  </h2>
                )}
                {description && (
                  <p id={descriptionId} className="mt-1 text-sm text-ink-muted">
                    {description}
                  </p>
                )}
              </div>
              <div className="flex shrink-0 items-center gap-2">
                {headerActions}
                {!hideCloseButton && (
                  <button
                    type="button"
                    onClick={() => onCloseRef.current()}
                    aria-label="Close dialog"
                    className="rounded-md p-1 text-ink-subtle hover:text-ink-muted focus:outline-none focus:ring-2 focus:ring-accent"
                  >
                    <svg
                      aria-hidden
                      className="h-5 w-5"
                      fill="none"
                      viewBox="0 0 24 24"
                      strokeWidth={2}
                      stroke="currentColor"
                    >
                      <path strokeLinecap="round" strokeLinejoin="round" d="M6 18L18 6M6 6l12 12" />
                    </svg>
                  </button>
                )}
              </div>
            </div>
          </div>
        )}

        {/* Body — the only part that scrolls. */}
        <div ref={bodyRef} className="min-h-0 flex-1 overflow-y-auto px-6 py-4 max-sm:px-4">
          {children}
        </div>

        {/* Footer — pinned, so the primary action never scrolls away. */}
        {footer && (
          <div
            ref={footerRef}
            data-testid="modal-footer"
            className="flex shrink-0 items-center justify-end gap-3 border-t border-border px-6 py-4 max-sm:px-4"
          >
            {footer}
          </div>
        )}
      </div>
    </div>
  );

  return portal && typeof document !== "undefined" ? createPortal(dialog, document.body) : dialog;
}
