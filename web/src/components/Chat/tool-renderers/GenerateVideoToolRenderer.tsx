/**
 * Renderer for the generate_video tool.
 *
 * Video generation blocks the turn for 30s to several minutes and neither
 * provider reports progress, so the honest signal is elapsed time plus an
 * expectation. The finished clip is drawn by ChatMessage in the message flow
 * (MessageGeneratedVideos), not here: this card is collapsed by default.
 */

import { memo, useEffect, useState } from "react";
import { Loader2 } from "lucide-react";
import type { ToolContentProps } from "./types";
import { GenericToolRenderer } from "./GenericToolRenderer";

export function formatElapsed(totalSeconds: number): string {
  const m = Math.floor(totalSeconds / 60);
  const s = totalSeconds % 60;
  return m > 0 ? `${m}m ${String(s).padStart(2, "0")}s` : `${s}s`;
}

function useElapsedSeconds(active: boolean): number {
  const [seconds, setSeconds] = useState(0);
  useEffect(() => {
    if (!active) return;
    const startedAt = Date.now();
    setSeconds(0);
    const timer = setInterval(() => {
      setSeconds(Math.floor((Date.now() - startedAt) / 1000));
    }, 1000);
    return () => clearInterval(timer);
  }, [active]);
  return seconds;
}

function GenerateVideoToolRendererComponent({ ctx }: ToolContentProps) {
  const elapsed = useElapsedSeconds(ctx.isExecuting);

  return (
    <div className="tool-content-generate-video">
      {ctx.isExecuting && (
        <div
          className="flex items-center gap-2 px-2 py-1.5 text-xs text-muted-foreground"
          role="status"
          data-testid="generate-video-progress"
        >
          <Loader2 className="h-3.5 w-3.5 animate-spin text-primary" aria-hidden="true" />
          <span>
            Generating video ({formatElapsed(elapsed)} elapsed, typically 30s–3min)
          </span>
        </div>
      )}
      <GenericToolRenderer ctx={ctx} />
    </div>
  );
}

export const GenerateVideoToolRenderer = memo(GenerateVideoToolRendererComponent);
