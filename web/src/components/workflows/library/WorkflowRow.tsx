// Copyright (c) 2025 Reliant Labs

/**
 * The Library table's columns (WORKFLOW_UI.md §2.2): what a workflow is,
 * where it comes from, whether it is in use — how many automations run it,
 * how its last run went — and Run….
 *
 * A real table with a header row and fixed column widths (forge-ui
 * DataTable, `layout="fixed"`), not per-row grids. The rows used to size
 * their columns as fractions of each row's own content, with no header, so
 * the Automations column's "—" (no automation runs this workflow) and the
 * "Not run yet" text sat at a different x on every row and nobody could tell
 * what the dash meant.
 *
 * The NAME links to the workflow's detail page, not the builder: most visits
 * are "what does this do, when did it last run, run it"; editing is one click
 * from detail. Every other action sits in the row's menu, so nothing is
 * hover-only and all of it is reachable from the keyboard.
 */

import { Link } from "@tanstack/react-router";
import { AlertTriangle, Play } from "lucide-react";

import type { RunSummary } from "@/api/run-grpc";
import { formatRelativeTime } from "@/lib/relativeTime";
import { runStatusFromDisplayState } from "@/lib/runStatus";
import { RunStatusDot } from "../../ui/RunStatusIndicator";
import { Tooltip } from "../../ui/Tooltip";
import { DraftStatusBadge } from "../../workflow/DraftStatusBadge";
import { workflowDisplayName } from "../../../lib/workflowDisplayName";
import { RowMenu, type RowMenuAction } from "../RowMenu";
import { WorkflowBadge, WorkflowSourceBadge, type WorkflowSource } from "../WorkflowSourceBadge";
import { automationsSummary } from "./libraryView";

export type WorkflowRowAction = RowMenuAction;

export interface WorkflowRowItem {
  name: string;
  title?: string;
  description?: string;
  source: WorkflowSource;
  /** A draft cannot run until it validates; absent counts as complete. */
  isDraft: boolean;
  draftErrorCount: number;
  isHidden: boolean;
  isDefault: boolean;
}

/** Everything one table row renders. DataTable rows are records, hence the index signature. */
export interface LibraryTableRow extends Record<string, unknown> {
  workflow: WorkflowRowItem;
  /** The workflow's newest run, from LastRunPerWorkflow. */
  lastRun?: RunSummary;
  /** How many automations run this workflow. */
  automationCount: number;
  /** How many of those are FAILING; says why the row is pinned on top. */
  failingAutomationCount: number;
  /** Pinned under "Needs attention". */
  attention: boolean;
  /** Opens Run…; absent for a workflow that cannot run (a draft). */
  onRun?: () => void;
  actions: WorkflowRowAction[];
}

/**
 * The column model. Widths are fixed so every row lines up; Name takes the
 * remainder. `project` is the area's search param, carried on every link.
 */
export function libraryColumns(project?: string) {
  return [
    {
      key: "name",
      header: "Name",
      className: "whitespace-normal",
      render: (_: unknown, row: LibraryTableRow) => <NameCell row={row} project={project} />,
    },
    {
      key: "source",
      header: "Source",
      width: "6.5rem",
      render: (_: unknown, row: LibraryTableRow) => <WorkflowSourceBadge source={row.workflow.source} />,
    },
    {
      key: "automations",
      header: "Automations",
      width: "10rem",
      render: (_: unknown, row: LibraryTableRow) => <AutomationsCell row={row} />,
    },
    {
      key: "lastRun",
      header: "Last run",
      width: "11rem",
      render: (_: unknown, row: LibraryTableRow) => (
        <div className="min-w-0 truncate" data-testid="workflow-row-last-run">
          <LastRun run={row.lastRun} />
        </div>
      ),
    },
    {
      key: "actions",
      header: "Actions",
      srHeader: true,
      width: "7rem",
      className: "text-right",
      render: (_: unknown, row: LibraryTableRow) => <ActionsCell row={row} />,
    },
  ];
}

function NameCell({ row, project }: { row: LibraryTableRow; project?: string }) {
  const { workflow } = row;
  return (
    <div className="min-w-0">
      <div className="flex min-w-0 items-center gap-2">
        {row.attention && (
          <AlertTriangle className="h-3.5 w-3.5 shrink-0 text-warning-ink" aria-label="Needs attention" />
        )}
        <Link
          to="/workflows/library/$workflowRef"
          params={{ workflowRef: workflow.name }}
          search={project ? { project } : {}}
          className="truncate rounded-sm text-sm font-medium text-foreground hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
        >
          {workflowDisplayName(workflow)}
        </Link>
        {workflow.isDefault && <WorkflowBadge label="Default" variant="info" testId="workflow-default-badge" />}
        {workflow.isDraft && <DraftStatusBadge errorCount={workflow.draftErrorCount} />}
        {workflow.isHidden && <WorkflowBadge label="Hidden" variant="neutral" />}
      </div>
      {workflow.description && (
        <p className="mt-0.5 truncate text-xs text-muted-foreground" title={workflow.description}>
          {workflow.description}
        </p>
      )}
    </div>
  );
}

function AutomationsCell({ row }: { row: LibraryTableRow }) {
  const summary = automationsSummary(row.automationCount, row.failingAutomationCount);
  if (!summary) {
    return (
      <span data-testid="workflow-row-automations" className="text-xs text-muted-foreground">
        <Tooltip content="No automation runs this workflow" placement="top" delay={300} wrapperClassName="inline-flex">
          <span aria-hidden="true">—</span>
        </Tooltip>
        <span className="sr-only">None</span>
      </span>
    );
  }
  return (
    <span data-testid="workflow-row-automations" className="block truncate text-xs text-muted-foreground" title={summary}>
      {`${row.automationCount} ${row.automationCount === 1 ? "automation" : "automations"}`}
      {row.failingAutomationCount > 0 && (
        <span className="text-destructive-ink" data-testid="workflow-row-failing">
          {` · ${row.failingAutomationCount} failing`}
        </span>
      )}
    </span>
  );
}

function ActionsCell({ row }: { row: LibraryTableRow }) {
  const displayName = workflowDisplayName(row.workflow);
  return (
    <div className="flex items-center justify-end gap-1">
      {row.onRun && (
        <button
          type="button"
          onClick={row.onRun}
          aria-label={`Run ${displayName}`}
          className="inline-flex h-7 items-center gap-1.5 rounded-md border border-border px-2 text-xs font-medium text-foreground transition-colors hover:bg-muted/60 focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
        >
          <Play className="h-3.5 w-3.5" aria-hidden="true" />
          Run…
        </button>
      )}
      <RowMenu label={`More actions for ${displayName}`} actions={row.actions} />
    </div>
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
      className="inline-flex min-w-0 max-w-full items-center gap-2 rounded-sm text-xs text-muted-foreground hover:text-foreground hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
      title={`${status.label} · ${run.title || "Untitled run"}`}
    >
      <RunStatusDot status={status} size="sm" />
      <span className="truncate">
        {status.label} · {when}
      </span>
    </Link>
  );
}
