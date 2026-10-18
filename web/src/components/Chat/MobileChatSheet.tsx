import { useEffect, useId, useRef, useState, type ReactNode } from "react";
import { createPortal } from "react-dom";
import { ChevronLeft, X } from "lucide-react";

/** A viewport-bound sheet shared by the mobile composer and its settings. */
export function MobileChatSheet({ title, onClose, onBack, closeLabel = "Close chat options", children }: {
  title: string;
  onClose: () => void;
  onBack?: () => void;
  closeLabel?: string;
  children: ReactNode;
}) {
  const titleId = useId();
  const panel = useRef<HTMLDivElement>(null);
  const closeRef = useRef(onClose);
  closeRef.current = onClose;
  const [viewport, setViewport] = useState({ height: window.visualViewport?.height, top: window.visualViewport?.offsetTop ?? 0 });

  useEffect(() => {
    const previous = document.activeElement as HTMLElement | null;
    const overflow = document.body.style.overflow;
    document.body.style.overflow = "hidden";
    window.dispatchEvent(new CustomEvent("modalOpened"));
    panel.current?.focus();
    const onKey = (event: KeyboardEvent) => {
      // Portalled pickers own their own keyboard handling.
      if (event.defaultPrevented || !panel.current?.contains(event.target as Node)) return;
      if (event.key === "Escape") {
        event.preventDefault();
        event.stopPropagation();
        closeRef.current();
      }
      if (event.key === "Tab") {
        const elements = Array.from(panel.current.querySelectorAll<HTMLElement>(
          'button:not(:disabled), input:not(:disabled), textarea:not(:disabled), select:not(:disabled), a[href], [tabindex="0"]',
        )).filter(element => element.getClientRects().length > 0);
        const first = elements[0];
        const last = elements[elements.length - 1];
        if (!first) { event.preventDefault(); return; }
        if (event.shiftKey && (document.activeElement === first || document.activeElement === panel.current)) {
          event.preventDefault(); last.focus();
        } else if (!event.shiftKey && (document.activeElement === last || document.activeElement === panel.current)) {
          event.preventDefault(); first.focus();
        }
      }
    };
    const updateViewport = () => setViewport({ height: window.visualViewport?.height, top: window.visualViewport?.offsetTop ?? 0 });
    document.addEventListener("keydown", onKey);
    window.visualViewport?.addEventListener("resize", updateViewport);
    window.visualViewport?.addEventListener("scroll", updateViewport);
    return () => {
      document.body.style.overflow = overflow;
      document.removeEventListener("keydown", onKey);
      window.visualViewport?.removeEventListener("resize", updateViewport);
      window.visualViewport?.removeEventListener("scroll", updateViewport);
      if (previous?.isConnected) previous.focus({ preventScroll: true });
    };
  }, []);

  return createPortal(
    <div onClick={event => event.stopPropagation()} className="mobile-chat-sheet fixed inset-x-0 z-[10000] flex items-end justify-center" style={{ top: viewport.top, height: viewport.height ?? "100dvh" }}>
      <div className="absolute inset-0 bg-black/40" onClick={onClose} aria-hidden="true" />
      <div ref={panel} tabIndex={-1} role="dialog" aria-modal="true" aria-labelledby={titleId}
        className="relative flex max-h-[90%] w-full max-w-lg min-w-0 flex-col rounded-t-[28px] border border-border bg-popover shadow-xl outline-none"
        style={{ paddingBottom: "var(--mobile-bottom-inset, env(safe-area-inset-bottom))" }} data-modal-open="true">
        <div aria-hidden="true" className="mx-auto mt-2 h-1 w-9 shrink-0 rounded-full bg-muted-foreground/30" />
        <div className="flex shrink-0 items-center gap-2 px-3 py-2">
          {onBack && <button type="button" aria-label="Back" onClick={onBack} className="flex min-h-[44px] min-w-[44px] items-center justify-center rounded-full active:bg-muted"><ChevronLeft className="h-5 w-5" /></button>}
          <h2 id={titleId} className="min-w-0 flex-1 truncate text-base font-semibold">{title}</h2>
          <button type="button" aria-label={closeLabel} onClick={onClose} className="flex min-h-[44px] min-w-[44px] items-center justify-center rounded-full active:bg-muted"><X className="h-5 w-5" /></button>
        </div>
        <div className="min-h-0 overflow-y-auto overscroll-contain px-4 pb-4 pt-2">{children}</div>
      </div>
    </div>, document.body,
  );
}
