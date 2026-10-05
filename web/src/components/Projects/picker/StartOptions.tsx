import { useId } from "react";

import { cn } from "@/lib/utils";

import type { AddProjectAction } from "./addProjectActionModel";

/**
 * The zero-projects state: every way to add a first project, each with the
 * sentence that says what it does (or, when it can't be used yet, why).
 *
 * On this state the page header carries no actions — these tiles ARE the
 * actions, so a second copy in the header would only compete with them. The
 * leading action (see addProjectActions.ts) comes first and takes the accent.
 */
export function StartOptions({ actions }: { actions: AddProjectAction[] }) {
  return (
    <section
      className="rounded-lg border border-dashed border-border-strong bg-card px-6 py-10"
      aria-labelledby="project-picker-empty-title"
      data-testid="project-picker-empty"
    >
      <div className="mx-auto max-w-2xl text-center">
        <h2 id="project-picker-empty-title" className="text-balance text-sm font-semibold text-ink">
          No projects yet
        </h2>
        <p className="mx-auto mt-1 max-w-md text-pretty text-sm text-ink-muted">
          {actions.length > 0
            ? "A project is a repository or folder Reliant works in. Add one to start."
            : "A project is a repository or folder Reliant works in. Connect a machine above, then add one."}
        </p>
      </div>
      {actions.length > 0 && (
        <div
          className={cn(
            "mx-auto mt-6 grid max-w-3xl gap-3",
            actions.length === 1 && "max-w-sm",
            actions.length === 2 && "sm:grid-cols-2",
            actions.length >= 3 && "sm:grid-cols-3",
          )}
        >
          {actions.map((action) => (
            <StartOptionTile key={action.kind} action={action} />
          ))}
        </div>
      )}
    </section>
  );
}

function StartOptionTile({ action }: { action: AddProjectAction }) {
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
