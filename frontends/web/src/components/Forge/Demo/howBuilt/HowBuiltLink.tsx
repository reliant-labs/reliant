// Copyright (c) 2025 Reliant Labs

/**
 * The way into "How this app was built" at the foot of every console view, so
 * it comes last on the page and in the tab order, after the views themselves.
 * (The banner's link stays too.) Opens `?section=built`.
 */

import { ArrowRight, Hammer } from "lucide-react";

export function HowBuiltLink({ onOpen }: { onOpen: () => void }) {
  return (
    <button
      type="button"
      onClick={onOpen}
      className="no-color flex w-full items-center gap-3 rounded-lg border border-dashed border-border px-4 py-3 text-left hover:bg-surface-muted"
      data-testid="how-built-link"
    >
      <Hammer className="h-4 w-4 shrink-0 text-ink-muted" aria-hidden="true" />
      <span className="min-w-0 flex-1">
        <span className="block text-sm font-medium text-ink">How this app was built</span>
        <span className="block text-xs text-ink-muted">The real forge run that wrote shop&apos;s code, and every file it wrote, counted.</span>
      </span>
      <ArrowRight className="h-4 w-4 shrink-0 text-ink-muted" aria-hidden="true" />
    </button>
  );
}
