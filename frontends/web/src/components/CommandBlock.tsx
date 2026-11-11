import { useCallback, useEffect, useRef, useState } from "react";
import { Check, Copy } from "lucide-react";

import { cn } from "@/lib/utils";

export interface CommandBlockProps {
  /** The exact text to show and to copy. */
  command: string;
  /** Names the command for assistive tech, e.g. "Homebrew install command". */
  label?: string;
  className?: string;
  /** Classes for the `<code>` element (size, wrapping). */
  codeClassName?: string;
}

const COPIED_MS = 2000;

/**
 * A shell command with an always-visible copy button.
 *
 * The button is a real labelled `<button>`, so it is reachable by keyboard and
 * never depends on hover. Success is shown in the label ("Copied") and
 * announced through a polite live region.
 */
export function CommandBlock({
  command,
  label = "command",
  className,
  codeClassName,
}: CommandBlockProps) {
  const [status, setStatus] = useState<"idle" | "copied" | "failed">("idle");
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);

  useEffect(
    () => () => {
      if (timer.current !== null) clearTimeout(timer.current);
    },
    [],
  );

  const handleCopy = useCallback(async () => {
    let next: "copied" | "failed" = "copied";
    try {
      await navigator.clipboard.writeText(command);
    } catch {
      next = "failed";
    }
    setStatus(next);
    if (timer.current !== null) clearTimeout(timer.current);
    timer.current = setTimeout(() => {
      timer.current = null;
      setStatus("idle");
    }, COPIED_MS);
  }, [command]);

  const copied = status === "copied";

  return (
    <div
      // The setup-surface inventory test (test/commandBlocks.ts) finds every
      // command through this marker and checks its copy button.
      data-command-block=""
      className={cn(
        "flex items-stretch overflow-hidden rounded-md border border-border/40 bg-background",
        className,
      )}
    >
      <code
        className={cn(
          "min-h-[44px] min-w-0 flex-1 select-all whitespace-pre-wrap break-words px-3 py-3 font-mono text-xs text-foreground [overflow-wrap:anywhere]",
          codeClassName,
        )}
      >
        {command}
      </code>
      <button
        type="button"
        onClick={() => void handleCopy()}
        aria-label={`Copy ${label}`}
        className={cn(
          "flex min-h-[44px] min-w-[44px] flex-shrink-0 items-center justify-center gap-1.5 border-l border-border/40 px-2.5 text-xs font-medium transition-colors",
          "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring",
          copied
            ? "bg-emerald-500/10 text-emerald-600 dark:text-emerald-400"
            : "text-muted-foreground hover:bg-muted/60 hover:text-foreground",
        )}
      >
        {copied ? (
          <Check className="h-3.5 w-3.5" aria-hidden="true" />
        ) : (
          <Copy className="h-3.5 w-3.5" aria-hidden="true" />
        )}
        <span>{copied ? "Copied" : "Copy"}</span>
      </button>
      <span role="status" aria-live="polite" className="sr-only">
        {status === "copied"
          ? `Copied ${label} to clipboard`
          : status === "failed"
            ? `Couldn't copy ${label}. Select the text and copy it manually.`
            : ""}
      </span>
    </div>
  );
}
