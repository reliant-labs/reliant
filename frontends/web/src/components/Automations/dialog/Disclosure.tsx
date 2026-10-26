// Copyright (c) 2025 Reliant Labs

/**
 * A titled, collapsible part of the automation dialog: "Advanced",
 * "Workflow settings · all defaults". Kept mounted while closed, so a field an
 * error links to (and every value set inside) still exists.
 */

import { useId, type ReactNode } from "react";
import { ChevronDown, ChevronRight } from "lucide-react";

export interface DisclosureProps {
  open: boolean;
  onToggle: () => void;
  title: string;
  summary?: string;
  children: ReactNode;
  testId?: string;
}

export function Disclosure({ open, onToggle, title, summary, children, testId }: DisclosureProps) {
  const panelId = useId();
  return (
    <div data-testid={testId}>
      <button
        type="button"
        aria-expanded={open}
        aria-controls={panelId}
        onClick={onToggle}
        className="flex w-full items-baseline gap-2 rounded-sm text-left text-sm focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
      >
        {open ? (
          <ChevronDown className="h-4 w-4 shrink-0 self-center" aria-hidden />
        ) : (
          <ChevronRight className="h-4 w-4 shrink-0 self-center" aria-hidden />
        )}
        <span className="font-semibold text-foreground">{title}</span>
        {summary && <span className="min-w-0 truncate text-xs text-muted-foreground">{summary}</span>}
      </button>
      <div id={panelId} hidden={!open} className="mt-3 space-y-4">
        {children}
      </div>
    </div>
  );
}
