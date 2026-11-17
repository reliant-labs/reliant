// Copyright (c) 2025 Reliant Labs

/**
 * A background process's output, streamed live from the machine it runs on.
 *
 * One hook for every process the app shows — a Commands-panel command and an
 * agent's background shell alike: both are BackgroundService processes, and
 * StreamProcessOutput sends what is already there, then each new line, then
 * the end. (The log viewer used to stream package processes and poll agent
 * ones, with a second copy of this logic per caller.)
 */

import { useCallback, useEffect, useRef, useState } from "react";
import { ProcessOutputStreamingService, type OutputLine } from "../api/process-output-streaming";
import { logger } from "../lib/logger";

/** Lines kept by default: a long-running dev server must not grow without end. */
export const DEFAULT_MAX_OUTPUT_LINES = 20_000;

export interface ProcessOutputStreamOptions {
  /** The machine the process runs on; unset streams from the default one. */
  daemonId?: string;
  /** Keep only the newest this many lines (a card's tail keeps a few). */
  maxLines?: number;
  /** Stream only while true (default true). */
  enabled?: boolean;
}

export interface ProcessOutputStreamState {
  lines: OutputLine[];
  /** No snapshot or line has arrived yet. */
  isLoading: boolean;
  /** The stream failed; lines holds what arrived before it did. */
  error: string | null;
  /** The process finished and the stream ended. */
  isComplete: boolean;
  /** Drop what was received and stream again from the start. */
  restart: () => void;
}

function keepNewest(lines: OutputLine[], max: number): OutputLine[] {
  return lines.length > max ? lines.slice(lines.length - max) : lines;
}

export function useProcessOutputStream(
  processId: string | null | undefined,
  { daemonId, maxLines = DEFAULT_MAX_OUTPUT_LINES, enabled = true }: ProcessOutputStreamOptions = {},
): ProcessOutputStreamState {
  const [lines, setLines] = useState<OutputLine[]>([]);
  const [isLoading, setIsLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [isComplete, setIsComplete] = useState(false);
  // Bumped by restart(): a new value tears the stream down and opens another.
  const [generation, setGeneration] = useState(0);
  const maxLinesRef = useRef(maxLines);
  maxLinesRef.current = maxLines;

  useEffect(() => {
    setLines([]);
    setError(null);
    setIsComplete(false);
    if (!processId || !enabled) {
      setIsLoading(false);
      return;
    }
    setIsLoading(true);
    const service = new ProcessOutputStreamingService(
      processId,
      {
        onSnapshot: (snapshot) => {
          setLines(keepNewest(snapshot, maxLinesRef.current));
          setIsLoading(false);
        },
        onLine: (line) => {
          setLines((prev) => keepNewest([...prev, line], maxLinesRef.current));
          setIsLoading(false);
        },
        onComplete: () => {
          setIsComplete(true);
          setIsLoading(false);
        },
        onError: (err) => {
          logger.warn("[useProcessOutputStream] stream error", { processId: processId.slice(0, 8), error: err });
          setError(err);
          setIsLoading(false);
        },
        onStatusChange: (status) => {
          if (status === "connected") setIsLoading(false);
        },
      },
      daemonId,
    );
    service.start();
    return () => service.stop();
  }, [processId, daemonId, enabled, generation]);

  const restart = useCallback(() => setGeneration((g) => g + 1), []);

  return { lines, isLoading, error, isComplete, restart };
}
