/**
 * The strip under a tool's output when the transcript holds only a preview of
 * it. The server caps large tool results on the transcript's read paths (the
 * preview keeps the head and the tail); this offers the rest, fetched once
 * through GetToolResult and then rendered in place of the preview.
 */

import { memo } from "react";
import { Loader2 } from "lucide-react";
import { formatBytes } from "../../../lib/formatBytes";

interface TruncatedOutputNoticeProps {
  /** Byte length of the full output, when the server reported it. */
  originalSize?: number;
  isLoading: boolean;
  /** Set when the last fetch failed; the button then retries. */
  error?: unknown;
  onShowFull: () => void;
}

function TruncatedOutputNoticeComponent({
  originalSize,
  isLoading,
  error,
  onShowFull,
}: TruncatedOutputNoticeProps) {
  const size = originalSize ? ` (${formatBytes(originalSize)})` : "";
  return (
    <div
      className="flex items-center gap-2 border-t border-border/30 px-2 py-1 text-2xs text-muted-foreground"
      data-testid="tool-output-truncated"
    >
      <span className="min-w-0 flex-1 truncate">
        {error ? "Couldn't load the full output." : "Showing the start and end of this output."}
      </span>
      <button
        type="button"
        onClick={(e) => {
          e.stopPropagation();
          onShowFull();
        }}
        disabled={isLoading}
        className="touch-target inline-flex shrink-0 items-center gap-1 rounded px-1.5 py-0.5 font-medium text-primary transition-colors hover:bg-primary/10 disabled:cursor-default disabled:opacity-70"
      >
        {isLoading && <Loader2 className="h-3 w-3 animate-spin" aria-hidden />}
        {error ? "Retry" : `Show full output${size}`}
      </button>
    </div>
  );
}

export const TruncatedOutputNotice = memo(TruncatedOutputNoticeComponent);
