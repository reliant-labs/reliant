// Copyright (c) 2025 Reliant Labs

/**
 * The Automations list, grouped (research/WORKFLOW_UI.md §7.2): a pinned
 * "Needs attention" group when anything is failing, waiting or skipping, then
 * one panel per workflow or project. Order and membership are decided in
 * automationGrouping.ts; this file only draws them.
 */

import { useId } from "react";
import { AlertTriangle } from "lucide-react";

import Card from "../forge-ui/card";
import type { Trigger } from "@/api/trigger-grpc";
import { cn } from "@/lib/utils";
import { AutomationRow } from "./AutomationRow";
import {
  groupAutomations,
  groupSummary,
  type AutomationGroup,
  type AutomationGroupBy,
} from "./automationGrouping";

export function AutomationGroups({ triggers, groupBy }: { triggers: Trigger[]; groupBy: AutomationGroupBy }) {
  const { attention, groups } = groupAutomations(triggers, groupBy);
  return (
    <div className="space-y-5">
      {attention && <GroupPanel group={attention} attention />}
      {groups.map((group) => (
        <GroupPanel key={group.key} group={group} />
      ))}
    </div>
  );
}

function GroupPanel({ group, attention = false }: { group: AutomationGroup; attention?: boolean }) {
  const headingId = useId();
  return (
    <section aria-labelledby={headingId} data-testid={`automation-group-${group.key}`}>
      <Card padding="none" className={cn(attention && "border-warning/60")}>
        <div className="flex items-baseline justify-between gap-4 border-b border-border/60 px-5 py-2.5">
          <h2
            id={headingId}
            className={cn(
              "flex min-w-0 items-center gap-1.5 truncate text-xs font-semibold uppercase tracking-wide",
              attention ? "text-warning-ink" : "text-muted-foreground",
            )}
          >
            {attention && <AlertTriangle className="h-3.5 w-3.5 shrink-0" aria-hidden="true" />}
            {group.label}
          </h2>
          <span className="shrink-0 text-xs text-muted-foreground">{groupSummary(group.entries)}</span>
        </div>
        <ul aria-labelledby={headingId} className="divide-y divide-border/60">
          {group.entries.map(({ trigger }) => (
            <AutomationRow key={trigger.id} trigger={trigger} />
          ))}
        </ul>
      </Card>
    </section>
  );
}

const GROUP_BY_OPTIONS: Array<{ value: AutomationGroupBy; label: string }> = [
  { value: "workflow", label: "Workflow" },
  { value: "project", label: "Project" },
];

/** The "Group by" segmented control. */
export function GroupBySwitch({
  value,
  onChange,
}: {
  value: AutomationGroupBy;
  onChange: (value: AutomationGroupBy) => void;
}) {
  const labelId = useId();
  return (
    <div className="flex items-center gap-2">
      <span id={labelId} className="text-xs font-medium text-muted-foreground">
        Group by
      </span>
      <div
        role="group"
        aria-labelledby={labelId}
        className="inline-flex rounded-md border border-border bg-background p-0.5"
      >
        {GROUP_BY_OPTIONS.map((option) => (
          <button
            key={option.value}
            type="button"
            aria-pressed={value === option.value}
            onClick={() => onChange(option.value)}
            className={cn(
              "rounded px-2.5 py-1 text-xs font-medium transition-colors focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40",
              value === option.value
                ? "bg-card text-foreground shadow-sm"
                : "text-muted-foreground hover:bg-muted/60 hover:text-foreground",
            )}
          >
            {option.label}
          </button>
        ))}
      </div>
    </div>
  );
}
