/**
 * What an open tool row shows in place of its content while the body the
 * transcript left out is fetched (useElidedToolBodies), or when it could not
 * be. Never the skeleton input or the stub result: those label a call, they
 * are not its content.
 */

import { memo } from "react";
import { Loader2 } from "lucide-react";

/** Rough rendered height of `bytes` of tool content, so the row does not jump much when it arrives. */
function placeholderHeightPx(bytes: number): number {
  const lines = Math.ceil(bytes / 64);
  return Math.min(Math.max(lines * 16, 40), 480);
}

function ToolBodyPlaceholderComponent({ bytes }: { bytes: number }) {
  return (
    <div
      role="status"
      aria-label="Loading this call's details"
      data-testid="tool-body-placeholder"
      className="flex items-start gap-2 px-2 py-1.5 text-2xs text-muted-foreground"
      // Runtime geometry: sized from the body's byte length (see placeholderHeightPx).
      style={{ height: placeholderHeightPx(bytes) }}
    >
      <Loader2 className="h-3 w-3 shrink-0 animate-spin" aria-hidden />
      <span>Loading details…</span>
    </div>
  );
}

export const ToolBodyPlaceholder = memo(ToolBodyPlaceholderComponent);

function ToolBodyErrorComponent({ onRetry }: { onRetry: () => void }) {
  return (
    <div
      className="flex items-center gap-2 px-2 py-1.5 text-2xs text-muted-foreground"
      data-testid="tool-body-error"
    >
      <span className="min-w-0 flex-1 truncate">Couldn't load this call's details.</span>
      <button
        type="button"
        onClick={(e) => {
          e.stopPropagation();
          onRetry();
        }}
        className="touch-target inline-flex shrink-0 items-center gap-1 rounded px-1.5 py-0.5 font-medium text-primary-ink transition-colors hover:bg-primary/10"
      >
        Retry
      </button>
    </div>
  );
}

export const ToolBodyError = memo(ToolBodyErrorComponent);

/**
 * A viewer of a shared chat reads each call's input but never its result
 * (control-plane docs/design/org-sharing.md §4): say so where the result
 * would be, rather than leave a blank that reads as "it printed nothing".
 */
function ToolResultWithheldComponent() {
  return (
    <div className="px-2 py-1.5 text-2xs text-muted-foreground" data-testid="tool-result-withheld">
      The result stays on the owner's machine.
    </div>
  );
}

export const ToolResultWithheld = memo(ToolResultWithheldComponent);
