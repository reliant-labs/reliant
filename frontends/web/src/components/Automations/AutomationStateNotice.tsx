// Copyright (c) 2025 Reliant Labs

/**
 * A callout for an automation that needs the user: what is wrong, in one
 * line, and the one thing to do about it. It
 * replaces BrokenActivationNotice and covers every attention state — Broken,
 * Needs setup, Can't start, Failing, Waiting for machine, Skipping — with the
 * same words the Automations list and the Inbox use.
 */

import { AlertOctagon, AlertTriangle } from "lucide-react";

import type { Trigger } from "@/hooks/trigger-model";
import type { AutomationHealthDisplay } from "@/lib/automationHealth";
import { cn } from "@/lib/utils";
import { AutomationCta } from "./AutomationCta";

export interface AutomationStateNoticeProps {
  health: AutomationHealthDisplay;
  triggers: readonly Trigger[];
  workflowRef: string;
  projectId: string;
  /** "PR review in reliant-labs": who the notice is about, when the page is not already. */
  subject?: string;
  className?: string;
}

const HEADLINE: Partial<Record<AutomationHealthDisplay["key"], string>> = {
  broken: "is broken: nothing it receives can start a run.",
  needs_setup: "is not running yet: it needs a setting.",
  cant_start: "can't start its runs.",
  failing: "keeps failing.",
  waiting_for_machine: "is waiting for its machine.",
  skipping: "is skipping every event.",
};

export function AutomationStateNotice({ health, triggers, workflowRef, projectId, subject, className }: AutomationStateNoticeProps) {
  const danger = health.dotVariant === "danger";
  const Icon = danger ? AlertOctagon : AlertTriangle;
  const headline = HEADLINE[health.key] ?? `is ${health.label.toLowerCase()}.`;
  return (
    <div
      role="alert"
      data-testid="automation-state-notice"
      data-state={health.key}
      className={cn(
        "flex flex-wrap items-start gap-3 rounded-lg border px-4 py-3 text-sm",
        danger ? "border-danger-border bg-danger-surface" : "border-warning-border bg-warning-surface",
        className,
      )}
    >
      <Icon className={cn("mt-0.5 h-4 w-4 shrink-0", danger ? "text-danger-ink" : "text-warning-ink")} aria-hidden />
      <div className="min-w-0 flex-1 space-y-0.5">
        <p className={cn("font-medium", danger ? "text-danger-ink" : "text-warning-ink")}>
          {subject ? `${subject} ${headline}` : `This automation ${headline}`}
        </p>
        {health.detail && <p className="text-foreground">{health.detail}</p>}
      </div>
      {health.cta && <AutomationCta cta={health.cta} triggers={triggers} workflowRef={workflowRef} projectId={projectId} variant="primary" />}
    </div>
  );
}
