// Copyright (c) 2025 Reliant Labs

/**
 * THE "NOTHING HERE YET" STATE THAT SAYS WHAT TO DO.
 *
 * A console page with nothing to show meets a reader who has not set it up
 * yet more often than one who has broken it. A one-line "No releases yet"
 * answers neither of their questions: what will this page show, and how do I
 * get it there? This frame answers both, in that order — what the page is,
 * then numbered steps (a command to copy where there is one), then the page
 * of the docs that covers it.
 *
 * It is a note, not an error: dashed, recessed and neutral, like the
 * observability states beside it (Observability/ObsStates). Commands are
 * quoted exactly as `forge` spells them; every one shown here was checked
 * against `forge <verb> --help` before it was written down.
 */

import { useEffect, useRef, useState, type ReactNode } from "react";
import { ArrowUpRight, Check, Copy } from "lucide-react";

/** The docs this frame links to, so one URL is spelled once. */
export const DOCS = {
  observability: "https://docs.reliantlabs.io/forge/observability",
  dashboards: "https://docs.reliantlabs.io/forge/observability#dashboards",
  alerts: "https://docs.reliantlabs.io/forge/observability#alerts",
  sessionReplay: "https://docs.reliantlabs.io/forge/observability#session-replay",
  errors: "https://docs.reliantlabs.io/forge/observability#frontend-errors-issues-and-source-maps",
  nothingShowsUp: "https://docs.reliantlabs.io/forge/troubleshooting#nothing-shows-up-in-observability",
  deploys: "https://docs.reliantlabs.io/forge/environments-and-deploys",
} as const;

export interface GettingStartedStep {
  /** What to do, in a sentence. */
  text: ReactNode;
  /** A command to run, shown on its own line with a copy button. */
  command?: string;
}

export interface GettingStartedProps {
  icon: ReactNode;
  title: string;
  /** What this page shows once it has something: one or two sentences. */
  about: ReactNode;
  /** Anything the reader needs before the steps (why it is empty, a caveat). */
  children?: ReactNode;
  steps?: GettingStartedStep[];
  docs?: { href: string; label: string };
  /** Buttons that act in place (clear a filter, widen the window). */
  actions?: ReactNode;
  testId: string;
}

export function GettingStarted({ icon, title, about, children, steps = [], docs, actions, testId }: GettingStartedProps) {
  return (
    <div role="status" data-testid={testId} className="flex items-start gap-3 rounded-lg border border-dashed border-border bg-background px-4 py-5">
      <span className="mt-0.5 shrink-0 text-muted-foreground" aria-hidden="true">
        {icon}
      </span>
      <div className="min-w-0 flex-1 space-y-2">
        <p className="text-sm font-medium text-foreground">{title}</p>
        <div className="space-y-1.5 text-xs text-muted-foreground">
          <p data-testid={`${testId}-about`}>{about}</p>
          {children}
        </div>
        {steps.length > 0 && (
          <ol className="max-w-2xl space-y-2 pt-1 text-xs text-muted-foreground" aria-label="Get started" data-testid={`${testId}-steps`}>
            {steps.map((step, index) => (
              <li key={index} className="flex gap-2">
                <span
                  className="mt-px flex h-4 w-4 shrink-0 items-center justify-center rounded-full border border-border text-2xs font-medium text-foreground"
                  aria-hidden="true"
                >
                  {index + 1}
                </span>
                <div className="min-w-0 flex-1 space-y-1">
                  <p>{step.text}</p>
                  {step.command && <CommandLine command={step.command} />}
                </div>
              </li>
            ))}
          </ol>
        )}
        {(docs || actions) && (
          <div className="flex flex-wrap items-center gap-x-4 gap-y-2 pt-1">
            {actions}
            {docs && (
              <a
                href={docs.href}
                target="_blank"
                rel="noreferrer"
                className="inline-flex items-center gap-1 rounded-sm text-xs font-medium text-primary underline-offset-2 hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                data-testid={`${testId}-docs`}
              >
                {docs.label}
                <ArrowUpRight className="h-3 w-3" aria-hidden="true" />
              </a>
            )}
          </div>
        )}
      </div>
    </div>
  );
}

/** One shell command, whole (never truncated), with a button that copies it. */
export function CommandLine({ command }: { command: string }) {
  const [copied, setCopied] = useState(false);
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  useEffect(() => () => clearTimeout(timer.current), []);

  async function copy() {
    try {
      await navigator.clipboard.writeText(command);
    } catch {
      return; // No clipboard (insecure context, denied): the command is still on screen to select.
    }
    setCopied(true);
    clearTimeout(timer.current);
    timer.current = setTimeout(() => setCopied(false), 1500);
  }

  return (
    <div className="flex max-w-full items-center gap-1 rounded-md border border-border/60 bg-card py-0.5 pl-2 pr-0.5" data-testid="getting-started-command">
      <code className="min-w-0 flex-1 overflow-x-auto whitespace-pre font-mono text-xs text-foreground">{command}</code>
      <button
        type="button"
        onClick={() => void copy()}
        aria-label={copied ? `Copied ${command}` : `Copy ${command}`}
        title={copied ? "Copied" : "Copy"}
        className="inline-flex h-6 w-6 shrink-0 items-center justify-center rounded text-muted-foreground hover:text-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
      >
        {copied ? <Check className="h-3 w-3" aria-hidden="true" /> : <Copy className="h-3 w-3" aria-hidden="true" />}
      </button>
    </div>
  );
}
