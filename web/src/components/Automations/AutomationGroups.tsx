// Copyright (c) 2025 Reliant Labs

/**
 * The Automations overview's lists: "Needs you", then one panel per workflow
 * or project. Order and membership are decided in automationGrouping.ts; this
 * file only draws them.
 */

import { useId } from "react";
import { AlertTriangle } from "lucide-react";

import Card from "../forge-ui/card";
import type { AutomationUnit } from "@/lib/automationUnits";
import { cn } from "@/lib/utils";
import { AutomationListHeader } from "./AutomationRow";
import { AutomationUnitRow, NeedsYouRow } from "./AutomationUnitRow";
import { groupSummary, type AutomationGroup, type AutomationGroupBy } from "./automationGrouping";

export function NeedsYouList({ units }: { units: readonly AutomationUnit[] }) {
  const headingId = useId();
  if (units.length === 0) return null;
  return (
    <section id="needs-you" aria-labelledby={headingId} data-testid="automations-needs-you" className="scroll-mt-24">
      <Card padding="none" className="overflow-hidden border-warning-border">
        <div className="flex items-baseline justify-between gap-4 border-b border-border/60 px-4 py-2">
          <h2 id={headingId} className="flex items-center gap-1.5 text-xs font-semibold uppercase tracking-wide text-warning-ink">
            <AlertTriangle className="h-3.5 w-3.5 shrink-0" aria-hidden="true" />
            Needs you
          </h2>
          <span className="text-xs text-muted-foreground">{units.length === 1 ? "1 automation" : `${units.length} automations`}</span>
        </div>
        <ul aria-labelledby={headingId} className="divide-y divide-border/60">
          {units.map((unit) => (
            <NeedsYouRow key={unit.key} unit={unit} />
          ))}
        </ul>
      </Card>
    </section>
  );
}

export function AutomationGroups({ groups }: { groups: readonly AutomationGroup[] }) {
  return (
    <div className="space-y-4">
      {groups.map((group) => (
        <GroupPanel key={group.key} group={group} />
      ))}
    </div>
  );
}

function GroupPanel({ group }: { group: AutomationGroup }) {
  const headingId = useId();
  return (
    <section aria-labelledby={headingId} data-testid={`automation-group-${group.key}`}>
      <Card padding="none" className="overflow-hidden">
        <div className="flex items-baseline justify-between gap-4 border-b border-border/60 px-4 py-2">
          <h2 id={headingId} className={cn("min-w-0 truncate text-xs font-semibold uppercase tracking-wide text-muted-foreground")}>
            {group.label}
          </h2>
          <span className="shrink-0 text-xs text-muted-foreground">{groupSummary(group.units)}</span>
        </div>
        <AutomationListHeader />
        <ul aria-labelledby={headingId} className="divide-y divide-border/60">
          {group.units.map((unit) => (
            <AutomationUnitRow key={unit.key} unit={unit} />
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
