// Copyright (c) 2025 Reliant Labs

/**
 * One workflow in the Library (WORKFLOW_UI.md §2.2): what it is, whether it is
 * in use — how many automations run it, how its last run went — and Run….
 *
 * The NAME is a link to the workflow's detail page, not the builder: most
 * visits are "what does this do, when did it last run, run it"; editing is one
 * click from detail. Every other action sits in the row's menu, so nothing is
 * hover-only and all of it is reachable from the keyboard.
 */

import { Link } from "@tanstack/react-router";
import { Play } from "lucide-react";

import type { RunSummary } from "@/api/run-grpc";
import { formatRelativeTime } from "@/lib/relativeTime";
import { runStatusFromDisplayState } from "@/lib/runStatus";
import { cn } from "@/lib/utils";
import { RunStatusDot } from "../../ui/RunStatusIndicator";
import { DraftStatusBadge } from "../../workflow/DraftStatusBadge";
import { getWorkflowDisplayName } from "../../workflow/useWorkflowInputs";
import { RowMenu, type RowMenuAction } from "../RowMenu";
import { WorkflowBadge, WorkflowSourceBadge, type WorkflowSource } from "../WorkflowSourceBadge";

export type WorkflowRowAction = RowMenuAction;

export interface WorkflowRowItem {
  name: string;
  description?: string;
  source: WorkflowSource;
  /** A draft cannot run until it validates; absent counts as complete. */
  isDraft: boolean;
  draftErrorCount: number;
  isHidden: boolean;
  isDefault: boolean;
}

interface WorkflowRowProps {
  workflow: WorkflowRowItem;
  /** The workflow's newest run, from LastRunPerWorkflow. */
  lastRun?: RunSummary;
  /** How many automations run this workflow. */
  automationCount: number;
  /** The `project` search param the area carries, so the link keeps it. */
  project?: string;
  /** Opens Run…; absent for a workflow that cannot run (a draft). */
  onRun?: () => void;
  actions: WorkflowRowAction[];
}

export function WorkflowRow({ workflow, lastRun, automationCount, project, onRun, actions }: WorkflowRowProps) {
  const displayName = getWorkflowDisplayName(workflow.name, true);
  return (
    <li
      className={cn(
        "grid grid-cols-[minmax(0,1fr)_auto] items-center gap-x-4 gap-y-1.5 px-5 py-3.5",
        "lg:grid-cols-[minmax(0,2.2fr)_minmax(0,1fr)_minmax(0,1.2fr)_auto]",
      )}
      data-testid={`workflow-row-${workflow.name}`}
    >
      <div className="min-w-0">
        <div className="flex min-w-0 flex-wrap items-center gap-2">
          <Link
            to="/workflows/library/$workflowRef"
            params={{ workflowRef: workflow.name }}
            search={project ? { project } : {}}
            className="truncate rounded-sm text-sm font-medium text-foreground hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
          >
            {displayName}
          </Link>
          <WorkflowSourceBadge source={workflow.source} />
          {workflow.isDefault && <WorkflowBadge label="Default" variant="info" testId="workflow-default-badge" />}
          {workflow.isDraft && <DraftStatusBadge errorCount={workflow.draftErrorCount} />}
          {workflow.isHidden && (
            <span className="text-2xs font-medium uppercase text-muted-foreground">Hidden</span>
          )}
        </div>
        {workflow.description && (
          <p className="mt-0.5 truncate text-xs text-muted-foreground" title={workflow.description}>
            {workflow.description}
          </p>
        )}
      </div>

      {/* Below lg: identity and actions, then the facts on a second line. */}
      <div className="order-last col-span-2 flex min-w-0 flex-wrap items-center gap-x-4 gap-y-1 lg:contents">
        <p className="min-w-0 truncate text-xs text-muted-foreground" data-testid="workflow-row-automations">
          <span className="sr-only">Automations: </span>
          {automationCount > 0
            ? `${automationCount} ${automationCount === 1 ? "automation" : "automations"}`
            : <span aria-hidden="true">—</span>}
          {automationCount === 0 && <span className="sr-only">none</span>}
        </p>
        <div className="min-w-0" data-testid="workflow-row-last-run">
          <span className="sr-only">Last run: </span>
          <LastRun run={lastRun} />
        </div>
      </div>

      <div className="flex items-center justify-end gap-1">
        {onRun && (
          <button
            type="button"
            onClick={onRun}
            aria-label={`Run ${displayName}`}
            className="inline-flex h-8 items-center gap-1.5 rounded-md border border-border/60 px-2.5 text-xs font-medium text-foreground transition-colors hover:bg-muted/60 focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
          >
            <Play className="h-3.5 w-3.5" aria-hidden="true" />
            Run…
          </button>
        )}
        <RowMenu label={`More actions for ${displayName}`} actions={actions} />
      </div>
    </li>
  );
}

function LastRun({ run }: { run?: RunSummary }) {
  if (!run) {
    return <span className="text-xs text-muted-foreground">Not run yet</span>;
  }
  const status = runStatusFromDisplayState(run.displayState, run.outcome);
  const when = formatRelativeTime(new Date(run.createdAt).toISOString());
  return (
    <Link
      to="/workflows/runs/$runId"
      params={{ runId: run.chatId }}
      className="inline-flex min-w-0 items-center gap-2 rounded-sm text-xs text-muted-foreground hover:text-foreground hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
      title={`${status.label} · ${run.title || "Untitled run"}`}
    >
      <RunStatusDot status={status} size="sm" />
      <span className="truncate">
        {status.label} · {when}
      </span>
    </Link>
  );
}
