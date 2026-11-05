// Copyright (c) 2025 Reliant Labs

/**
 * "Run from your terminal": what the forge shell shows where Reliant offers
 * an action that needs a daemon (deploy, promote, the local dev stack, local
 * secrets). The forge shell has no daemon by design: forge plans those
 * against a checkout, so the honest answer is the command to run in one,
 * each with a copy button.
 */

import { useState } from "react";
import { Check, Copy, Terminal } from "lucide-react";

import { cn } from "@/lib/utils";

export interface TerminalCommand {
  /** What the command does, in a few words. */
  label: string;
  command: string;
}

export function RunFromTerminal({
  title = "Run from your terminal",
  description,
  commands,
  className,
  testId = "forge-run-from-terminal",
}: {
  title?: string;
  description?: React.ReactNode;
  commands: readonly TerminalCommand[];
  className?: string;
  testId?: string;
}) {
  return (
    <section
      className={cn("rounded-lg border border-border bg-surface p-4", className)}
      data-testid={testId}
      aria-label={title}
    >
      <div className="flex items-center gap-2 text-sm font-medium text-ink">
        <Terminal className="h-4 w-4 text-ink-muted" aria-hidden="true" />
        {title}
      </div>
      {description && <p className="mt-1 text-sm text-ink-muted">{description}</p>}
      <ul className="mt-3 space-y-2">
        {commands.map((entry) => (
          <li key={entry.command} className="space-y-1">
            <span className="text-xs text-ink-muted">{entry.label}</span>
            <CommandLine command={entry.command} />
          </li>
        ))}
      </ul>
    </section>
  );
}

export function CommandLine({ command }: { command: string }) {
  const [copied, setCopied] = useState(false);
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(command);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1500);
    } catch {
      // Clipboard refused (an insecure origin, a policy): the text is selectable.
    }
  };
  return (
    <div className="flex min-w-0 items-center gap-2 rounded-md border border-border bg-surface-muted px-3 py-2 font-mono text-xs text-ink">
      <span aria-hidden="true" className="select-none text-ink-muted">$</span>
      <code className="min-w-0 flex-1 overflow-x-auto whitespace-nowrap">{command}</code>
      <button
        type="button"
        onClick={() => void copy()}
        aria-label={copied ? "Copied" : `Copy: ${command}`}
        className="shrink-0 rounded p-1 text-ink-muted transition-colors hover:bg-ink/5 hover:text-ink focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        data-testid="copy-command"
      >
        {copied ? <Check className="h-3.5 w-3.5" aria-hidden="true" /> : <Copy className="h-3.5 w-3.5" aria-hidden="true" />}
      </button>
    </div>
  );
}
