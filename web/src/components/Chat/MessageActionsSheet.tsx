/**
 * Bottom sheet for touch devices — the mobile substitute for the hover-only
 * per-message toolbar in ChatMessage.tsx. Hover has no touch equivalent, so
 * without this sheet Copy/Branch/Timestamp are unreachable on a phone; see
 * the long-press wiring in ChatMessage.tsx for how it's triggered.
 */

import { useEffect, useRef, useState, type ReactNode } from "react";
import { createPortal } from "react-dom";
import { cn } from "../../lib/utils";

export interface MessageActionsSheetAction {
  key: string;
  label: string;
  icon?: ReactNode;
  onSelect: () => void;
  danger?: boolean;
  disabled?: boolean;
}

interface MessageActionsSheetProps {
  isOpen: boolean;
  onClose: () => void;
  actions: MessageActionsSheetAction[];
  /** Full formatted timestamp shown as a non-interactive header row. */
  timestampLabel?: string;
}

export function MessageActionsSheet({
  isOpen,
  onClose,
  actions,
  timestampLabel,
}: MessageActionsSheetProps) {
  const sheetRef = useRef<HTMLDivElement>(null);
  // Mounted a frame before `open` so the transition can animate in rather
  // than snapping to its final position on first paint.
  const [visible, setVisible] = useState(false);
  // Whether the sheet will accept taps yet — see the pointer-events comment on
  // the root. Armed by the release of the finger that opened it (or by a short
  // fallback, since a press that never lifts inside the viewport would
  // otherwise leave the sheet permanently inert).
  const [acceptsInput, setAcceptsInput] = useState(false);

  useEffect(() => {
    if (!isOpen) return;
    const raf = requestAnimationFrame(() => setVisible(true));
    return () => cancelAnimationFrame(raf);
  }, [isOpen]);

  useEffect(() => {
    if (!isOpen) {
      setAcceptsInput(false);
      return;
    }

    const arm = () => setAcceptsInput(true);
    // `pointerup` rather than `touchend`: the portal steals the touch sequence
    // from the pressed element, and a document-level pointer listener is what
    // still observes the release. `once` so a later tap can't re-arm nothing.
    document.addEventListener("pointerup", arm, { once: true });
    document.addEventListener("touchend", arm, { once: true });
    // Belt-and-braces for a press whose release is never delivered (finger
    // dragged off-screen, synthetic events in tests).
    const timer = window.setTimeout(arm, 400);

    return () => {
      document.removeEventListener("pointerup", arm);
      document.removeEventListener("touchend", arm);
      window.clearTimeout(timer);
    };
  }, [isOpen]);

  useEffect(() => {
    if (!isOpen) {
      setVisible(false);
      return;
    }

    const handleEscape = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    document.addEventListener("keydown", handleEscape);
    // Matches Modal's approach — a background chat list should not scroll
    // behind an open sheet.
    document.body.style.overflow = "hidden";

    return () => {
      document.removeEventListener("keydown", handleEscape);
      document.body.style.overflow = "";
    };
  }, [isOpen, onClose]);

  if (!isOpen) return null;

  return createPortal(
    <div
      className={cn(
        "fixed inset-0 z-[9999] flex items-end justify-center",
        // The sheet opens while the finger is still down, directly underneath
        // it. Releasing then dispatches pointerup/click onto whatever button
        // landed at that spot — which silently ran Copy, and created real
        // chats via "Branch in place", without the user choosing anything.
        //
        // Guarding in useLongPress does not work: once this portal renders
        // over the message, the original element stops receiving the gesture
        // and no `touchend` arrives there at all, so preventDefault() never
        // runs. Swallowing input until the opening touch has lifted is the
        // only reliable fix, and it must cover the backdrop too — a press in
        // the upper screen lands there and dismissed the sheet instantly.
        !acceptsInput && "pointer-events-none",
      )}
    >
      {/* Backdrop */}
      <div
        className={cn(
          "absolute inset-0 bg-black/50 transition-opacity duration-200",
          visible ? "opacity-100" : "opacity-0",
        )}
        onClick={onClose}
        aria-hidden
      />

      <div
        ref={sheetRef}
        role="dialog"
        aria-modal="true"
        className={cn(
          "relative w-full max-w-lg rounded-t-2xl border-t border-border bg-popover shadow-2xl transition-transform duration-200",
          visible ? "translate-y-0" : "translate-y-full",
        )}
        style={{ paddingBottom: "env(safe-area-inset-bottom)" }}
      >
        {/* Grab handle — decorative, signals "this is a sheet" at a glance. */}
        <div className="flex justify-center pt-2">
          <div className="h-1 w-10 rounded-full bg-muted-foreground/30" />
        </div>

        {timestampLabel && (
          <div className="px-4 pb-1 pt-3 text-center text-xs text-muted-foreground">
            {timestampLabel}
          </div>
        )}

        <div className="flex flex-col px-2 pb-2 pt-1">
          {actions.map((action) => (
            <button
              key={action.key}
              type="button"
              disabled={action.disabled}
              onClick={() => {
                if (action.disabled) return;
                action.onSelect();
                onClose();
              }}
              className={cn(
                "flex min-h-[44px] w-full items-center gap-3 rounded-lg px-3 text-left text-sm font-medium transition-colors active:bg-muted",
                action.danger ? "text-destructive" : "text-foreground",
                action.disabled && "opacity-50",
              )}
            >
              {action.icon && (
                <span className="flex h-5 w-5 items-center justify-center">
                  {action.icon}
                </span>
              )}
              <span className="flex-1">{action.label}</span>
            </button>
          ))}

          <button
            type="button"
            onClick={onClose}
            className="mt-1 flex min-h-[44px] w-full items-center justify-center rounded-lg border border-border text-sm font-medium text-muted-foreground active:bg-muted"
          >
            Cancel
          </button>
        </div>
      </div>
    </div>,
    document.body,
  );
}
