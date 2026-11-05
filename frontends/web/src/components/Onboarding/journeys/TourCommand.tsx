/**
 * Shell commands in a tour card, each with a Copy button: the forge journey's
 * steps, and the demo's install sheet (Forge/Demo/InstallForgeSheet).
 */

import { useEffect, useRef, useState } from "react";
import { Check, Copy } from "lucide-react";

/** Shell commands in a step card, each with a Copy button. */
export function TourCommands({ commands }: { commands: string[] }) {
  return (
    <div className="mt-3 space-y-1.5" data-testid="tour-commands">
      {commands.map((command) => (
        <TourCommand key={command} command={command} />
      ))}
    </div>
  );
}

/** One shell command with a Copy button (also the demo's install sheet). `onCopied` hears a successful copy. */
export function TourCommand({ command, onCopied }: { command: string; onCopied?: () => void }) {
  const [copied, setCopied] = useState(false);
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  useEffect(() => () => clearTimeout(timer.current), []);
  async function copy() {
    try {
      await navigator.clipboard.writeText(command);
    } catch {
      return; // No clipboard: the command is on screen to select.
    }
    setCopied(true);
    onCopied?.();
    clearTimeout(timer.current);
    timer.current = setTimeout(() => setCopied(false), 1500);
  }
  return (
    <div className="flex items-center gap-1 rounded-md border border-border bg-muted/40 py-0.5 pl-2 pr-0.5">
      <code className="min-w-0 flex-1 overflow-x-auto whitespace-pre font-mono text-xs text-foreground">{command}</code>
      <button
        type="button"
        onClick={() => void copy()}
        aria-label={copied ? `Copied ${command}` : `Copy ${command}`}
        title={copied ? "Copied" : "Copy"}
        className="inline-flex h-6 w-6 shrink-0 items-center justify-center rounded text-muted-foreground hover:text-foreground"
      >
        {copied ? <Check className="h-3 w-3" aria-hidden="true" /> : <Copy className="h-3 w-3" aria-hidden="true" />}
      </button>
    </div>
  );
}
