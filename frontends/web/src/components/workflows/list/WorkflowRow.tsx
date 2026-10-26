// Copyright (c) 2025 Reliant Labs

/**
 * The Workflows table's columns (WORKFLOW_UI.md §2.2): what a workflow is,
 * and — inline, because an automation is a workflow's triggers, not a second
 * list — whether it runs by itself: what starts it, its health, when it last
 * ran, when it runs next, and its On/Off switch. Then Run… (attended: opens
 * the run form) and a menu.
 *
 * A real table with a header row and fixed column widths (forge-ui
 * DataTable, `layout="fixed"`), so every row's cells line up under a label.
 *
 * The NAME links to the workflow's page, where its triggers are configured;
 * editing the definition is one click from there. Every other action sits in
 * the row's menu, so nothing is hover-only and all of it is reachable from
 * the keyboard.
 */

import { Link } from "@tanstack/react-router";
import { AlertTriangle, Play } from "lucide-react";

import type { RunSummary } from "@/hooks/run-queries";
import type { AutomationCtaSpec } from "@/lib/automationHealth";
import type { Trigger } from "@/hooks/trigger-model";
import { formatRelativeTime } from "@/lib/relativeTime";
import { runStatusFromDisplayState } from "@/lib/runStatus";
import { RunStatusDot } from "../../runs/RunStatusIndicator";
import ToggleSwitch from "../../forge-ui/toggle_switch";
import { DraftStatusBadge } from "../../workflow/DraftStatusBadge";
import { stripWorkflowRefPrefix, workflowDisplayName } from "../../../lib/workflowDisplayName";
import { AutomationCta } from "../../Automations/AutomationCta";
import { HealthIndicator, NextFire } from "../automationStatus";
import { RowMenu, type RowMenuAction } from "../RowMenu";
import { WORKFLOW_SOURCE_LABEL, WorkflowBadge, WorkflowSourceBadge, type WorkflowSource } from "../WorkflowSourceBadge";
import type { WorkflowListRow } from "./workflowsView";

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
export interface WorkflowsTableRow extends Record<string, unknown> {
  key: string;
  workflow: WorkflowRowItem;
  view: WorkflowListRow;
  /** One line for what starts it ("Weekdays 09:00", "GitHub: Issue opened · 3 triggers"). */
  startsOn: string;
  /** The workflow's newest run in scope, from LastRunPerWorkflow. */
  lastRun?: RunSummary;
  /** The workflow page, scoped like this list. */
  href: { workflowRef: string; project?: string };
  /** Opens Run…; absent for a workflow that cannot run (a draft, a missing one). */
  onRun?: () => void;
  /** Opens Set up…; present for a workflow that declares triggers and none is set up in scope. */
  onSetUp?: () => void;
  /** Turns all of its triggers on or off; absent when there is nothing to switch, or they span projects. */
  onToggle?: (next: boolean) => void;
  toggling?: boolean;
  /** The state's call to action, when a trigger in scope needs the user. */
  cta?: { spec: AutomationCtaSpec; triggers: Trigger[]; projectId: string };
  actions: WorkflowRowAction[];
}

/**
 * What tells this row apart from another with the same title, for its
 * actions' accessible names: "draft, my-slug" or "built-in, agent". Titles
 * repeat (a copy of Agent is still titled Agent), so "More actions for Agent"
 * three times over leaves a screen reader user guessing; the slug is unique.
 */
export function workflowRowQualifier(workflow: Pick<WorkflowRowItem, "name" | "source" | "isDraft">, projectName?: string): string {
  const state = workflow.isDraft ? "draft" : WORKFLOW_SOURCE_LABEL[workflow.source].toLowerCase();
  return `${state}, ${stripWorkflowRefPrefix(workflow.name)}${projectName ? `, ${projectName}` : ""}`;
}

/** The column model. Widths are fixed so every row lines up; Name takes the remainder. */
export function workflowsColumns() {
  return [
    {
      key: "name",
      header: "Name",
      className: "whitespace-normal",
      render: (_: unknown, row: WorkflowsTableRow) => <NameCell row={row} />,
    },
    {
      key: "startsOn",
      header: "Runs by itself",
      width: "12rem",
      render: (_: unknown, row: WorkflowsTableRow) => <StartsOnCell row={row} />,
    },
    {
      key: "health",
      header: "Health",
      width: "8.5rem",
      render: (_: unknown, row: WorkflowsTableRow) =>
        row.view.health ? (
          <div className="min-w-0 truncate" data-testid="workflow-row-health">
            <HealthIndicator health={row.view.health} />
          </div>
        ) : (
          <Dash label="Nothing runs it by itself" />
        ),
    },
    {
      key: "lastRun",
      header: "Last run",
      width: "9.5rem",
      render: (_: unknown, row: WorkflowsTableRow) => (
        <div className="min-w-0 truncate" data-testid="workflow-row-last-run">
          <LastRun run={row.lastRun} />
        </div>
      ),
    },
    {
      key: "nextRun",
      header: "Next run",
      width: "6.5rem",
      render: (_: unknown, row: WorkflowsTableRow) => (
        <span className="block truncate text-xs text-muted-foreground" data-testid="workflow-row-next-run">
          <NextFire trigger={{ enabled: row.view.state === "on" || row.view.state === "partly", nextFireAt: row.view.nextFireAt }} />
        </span>
      ),
    },
    {
      key: "on",
      header: "On",
      width: "4rem",
      render: (_: unknown, row: WorkflowsTableRow) => <OnCell row={row} />,
    },
    {
      key: "actions",
      header: "Actions",
      srHeader: true,
      width: "10.5rem",
      className: "text-right",
      render: (_: unknown, row: WorkflowsTableRow) => <ActionsCell row={row} />,
    },
  ];
}

function Dash({ label }: { label: string }) {
  return (
    <span className="text-xs text-muted-foreground" title={label}>
      <span aria-hidden="true">—</span>
      <span className="sr-only">{label}</span>
    </span>
  );
}

function NameCell({ row }: { row: WorkflowsTableRow }) {
  const { workflow, view } = row;
  return (
    <div className="min-w-0">
      <div className="flex min-w-0 items-center gap-2">
        {view.needsYou && <AlertTriangle className="h-3.5 w-3.5 shrink-0 text-warning-ink" aria-label="Needs you" />}
        <Link
          to="/workflows/library/$workflowRef"
          params={{ workflowRef: row.href.workflowRef }}
          search={row.href.project ? { project: row.href.project } : {}}
          // The description is read on request, not as part of the row: it
          // is clipped to one line on screen and was read out in full.
          aria-description={workflow.description || undefined}
          className="truncate rounded-sm text-sm font-medium text-foreground hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
        >
          {workflowDisplayName(workflow)}
        </Link>
        {!view.missing && <WorkflowSourceBadge source={workflow.source} />}
        {view.missing && <WorkflowBadge label="Workflow not found" variant="warning" testId="workflow-missing-badge" />}
        {workflow.isDefault && <WorkflowBadge label="Default" variant="info" testId="workflow-default-badge" />}
        {workflow.isDraft && <DraftStatusBadge errorCount={workflow.draftErrorCount} />}
        {workflow.isHidden && <WorkflowBadge label="Hidden" variant="neutral" />}
      </div>
      {(view.projectName || workflow.description) && (
        <p aria-hidden="true" className="mt-0.5 truncate text-xs text-muted-foreground" title={workflow.description}>
          {view.projectName && <span className="font-medium text-foreground/80">{view.projectName}</span>}
          {view.projectName && workflow.description && " · "}
          {workflow.description}
        </p>
      )}
    </div>
  );
}

function StartsOnCell({ row }: { row: WorkflowsTableRow }) {
  const { state, projectsOn } = row.view;
  if (state === "none") {
    return (
      <span data-testid="workflow-row-starts-on" className="block truncate text-xs text-muted-foreground" title="It declares no triggers and none was added, so it runs only when someone starts it">
        Only when started
      </span>
    );
  }
  const where = projectsOn > 1 ? ` · ${projectsOn} projects` : "";
  return (
    <span
      data-testid="workflow-row-starts-on"
      className={`block truncate text-xs ${state === "off" ? "text-muted-foreground" : "text-foreground"}`}
      title={`${row.startsOn}${where}`}
    >
      {row.startsOn}
      {where}
    </span>
  );
}

function OnCell({ row }: { row: WorkflowsTableRow }) {
  const { state, projectsOn } = row.view;
  const name = workflowDisplayName(row.workflow);
  if (row.onToggle) {
    return (
      <ToggleSwitch
        checked={state === "on" || state === "partly"}
        onChange={row.onToggle}
        disabled={row.toggling}
        ariaLabel={`${name} on (${workflowRowQualifier(row.workflow, row.view.projectName)})`}
        size="md"
        className="forge-ui"
      />
    );
  }
  if (projectsOn > 1) {
    return (
      <span className="text-xs text-foreground" title={`On in ${projectsOn} projects: switch it per project on its page`}>
        On
      </span>
    );
  }
  return <span className="text-xs text-muted-foreground">{state === "none" ? "" : "Off"}</span>;
}

function ActionsCell({ row }: { row: WorkflowsTableRow }) {
  const displayName = workflowDisplayName(row.workflow);
  const qualifier = workflowRowQualifier(row.workflow, row.view.projectName);
  return (
    <div className="flex items-center justify-end gap-1">
      {row.cta ? (
        <AutomationCta
          cta={row.cta.spec}
          triggers={row.cta.triggers}
          workflowRef={row.workflow.name}
          projectId={row.cta.projectId}
          subject={displayName}
          className="h-7 px-2 text-xs"
        />
      ) : (
        row.onSetUp && (
          <button
            type="button"
            onClick={row.onSetUp}
            aria-label={`Set up ${displayName} (${qualifier})`}
            className="inline-flex h-7 items-center rounded-md border border-border px-2 text-xs font-medium text-foreground transition-colors hover:bg-muted/60 focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
          >
            Set up…
          </button>
        )
      )}
      {row.onRun && (
        <button
          type="button"
          onClick={row.onRun}
          aria-label={`Run ${displayName} (${qualifier})`}
          className="inline-flex h-7 items-center gap-1.5 rounded-md border border-border px-2 text-xs font-medium text-foreground transition-colors hover:bg-muted/60 focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
        >
          <Play className="h-3.5 w-3.5" aria-hidden="true" />
          Run…
        </button>
      )}
      <RowMenu label={`More actions for ${displayName} (${qualifier})`} actions={row.actions} />
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
