// Copyright (c) 2025 Reliant Labs

/**
 * The picker's full-screen sheet, used below `sm` or on a coarse pointer
 * instead of a popover: Back on the left, the
 * field's label as the title, Done with the count on the right, and the search
 * pinned at the top. It sits ABOVE the setup dialog's own sheet — one sheet
 * per level, never a popover inside a sheet.
 */

import { useEffect, useState, type ReactNode } from "react";
import { createPortal } from "react-dom";
import { ArrowLeft } from "lucide-react";

const SHEET_QUERY = "(max-width: 639px), (pointer: coarse)";

/** Whether the picker should open as a sheet on this screen. */
export function usePickerSheet(): boolean {
  const get = () => typeof window !== "undefined" && typeof window.matchMedia === "function" && window.matchMedia(SHEET_QUERY).matches;
  const [sheet, setSheet] = useState(get);
  useEffect(() => {
    if (typeof window === "undefined" || typeof window.matchMedia !== "function") return;
    const media = window.matchMedia(SHEET_QUERY);
    const onChange = () => setSheet(media.matches);
    media.addEventListener?.("change", onChange);
    return () => media.removeEventListener?.("change", onChange);
  }, []);
  return sheet;
}

export interface PickerSheetProps {
  title: string;
  count: number;
  multi: boolean;
  onDone: () => void;
  children: ReactNode;
}

export function PickerSheet({ title, count, multi, onDone, children }: PickerSheetProps) {
  return createPortal(
    <div
      role="dialog"
      aria-modal="true"
      aria-label={title}
      className="fixed inset-0 z-[60] flex h-[100dvh] flex-col bg-card"
      data-testid="picker-sheet"
    >
      <div className="flex items-center justify-between gap-2 border-b border-border/60 px-2 py-2">
        <button
          type="button"
          onClick={onDone}
          className="inline-flex h-9 items-center gap-1 rounded-md px-2 text-sm text-muted-foreground hover:bg-muted/70 focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
        >
          <ArrowLeft className="h-4 w-4" aria-hidden /> Back
        </button>
        <span className="min-w-0 truncate text-sm font-semibold text-foreground">{title}</span>
        <button
          type="button"
          onClick={onDone}
          className="inline-flex h-9 items-center rounded-md bg-primary px-3 text-sm font-medium text-primary-foreground hover:bg-primary/90 focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
        >
          {multi ? `Done · ${count}` : "Done"}
        </button>
      </div>
      <div className="min-h-0 flex-1">{children}</div>
    </div>,
    document.body,
  );
}
