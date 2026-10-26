// Copyright (c) 2025 Reliant Labs

import { useId } from "react";

import { cn } from "@/lib/utils";

import type { AddProjectAction } from "./picker/addProjectActionModel";

/**
 * The two ways to add a project as choice tiles, each with the sentence that
 * says what it does (or, when it can't be used yet, why). The leading action
 * (addProjectActions.ts) comes first and takes the accent. Shared by the Add
 * project dialog and the first-run state, so both always offer the same.
 */
export function AddProjectOptions({ actions }: { actions: AddProjectAction[] }) {
  return (
    <div className={cn("grid gap-3", actions.length > 1 && "sm:grid-cols-2")} data-testid="add-project-options">
      {actions.map((action) => (
        <AddProjectTile key={action.kind} action={action} />
      ))}
    </div>
  );
}

function AddProjectTile({ action }: { action: AddProjectAction }) {
  const descriptionId = useId();
  const Icon = action.icon;
  return (
    <button
      type="button"
      onClick={action.onClick}
      disabled={action.disabled}
      aria-describedby={descriptionId}
      data-testid={action.testId}
      data-variant={action.lead ? "primary" : "secondary"}
      className={cn(
        "flex h-full flex-col items-start gap-2 rounded-md border p-4 text-left transition-colors",
        "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/60",
        "disabled:cursor-not-allowed disabled:opacity-60",
        action.lead
          ? "border-accent-border bg-accent-surface enabled:hover:border-accent"
          : "border-border bg-background enabled:hover:border-border-strong",
      )}
    >
      <span className="flex items-center gap-2">
        <Icon
          className={cn("h-4 w-4 shrink-0", action.lead ? "text-accent-ink" : "text-ink-muted")}
          aria-hidden="true"
        />
        <span className="text-sm font-medium text-ink">{action.label}</span>
      </span>
      <span id={descriptionId} className="text-pretty text-xs text-ink-muted">
        {action.description}
      </span>
    </button>
  );
}
