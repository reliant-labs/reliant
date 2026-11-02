// Copyright (c) 2025 Reliant Labs

/**
 * "Ready-made automations": the built-ins that exist to run by themselves
 * (readyMade.ts), offered above the Workflows table until each is on here.
 * One card per automation: what it does in a sentence, what it needs, and
 * Turn on…, which opens the same setup dialog as the row's Set up….
 * Collapsible, like the summary above it: it is an invitation, not the list.
 */

import { Link } from "@tanstack/react-router";

import Button from "@/components/forge-ui/button";
import Card from "../../forge-ui/card";
import { openAutomationDialog } from "../../Automations/dialog/openAutomationDialog";
import { workflowDisplayName } from "../../../lib/workflowDisplayName";
import { workflowSummary } from "../../../lib/workflowSummary";
import { WorkflowBadge } from "../WorkflowSourceBadge";
import { integrationsNeeded } from "./readyMade";
import type { WorkflowListRow } from "./workflowsView";

export function ReadyMadeStrip({ rows, projectId }: { rows: WorkflowListRow[]; projectId?: string }) {
  if (rows.length === 0) return null;
  return (
    <details open className="group space-y-2" data-testid="ready-made">
      <summary className="cursor-pointer select-none px-1 text-xs font-semibold uppercase tracking-wide text-muted-foreground">
        Ready-made automations
        <span className="ml-1.5 font-medium text-muted-foreground/70">{rows.length}</span>
      </summary>
      <p className="px-1 text-xs text-muted-foreground">
        Built in and ready to run by themselves. Turning one on asks what it should watch first.
        {!projectId && " Pick a project to turn one on."}
      </p>
      <ul aria-label="Ready-made automations" className="grid grid-cols-[repeat(auto-fill,minmax(16rem,1fr))] gap-2">
        {rows.map((row) => (
          <ReadyMadeCard key={row.key} row={row} projectId={projectId} />
        ))}
      </ul>
    </details>
  );
}

function ReadyMadeCard({ row, projectId }: { row: WorkflowListRow; projectId?: string }) {
  const { workflow } = row;
  const name = workflowDisplayName(workflow);
  const summary = workflowSummary(workflow.description);
  const needs = integrationsNeeded(workflow);
  const needsMachine = (workflow.needsMachine?.length ?? 0) > 0;
  return (
    <li className="min-w-0">
      <Card padding="sm" className="flex h-full flex-col gap-2" data-testid={`ready-made-${workflow.name}`}>
        <Link
          to="/workflows/library/$workflowRef"
          params={{ workflowRef: workflow.name }}
          search={projectId ? { project: projectId } : {}}
          className="truncate rounded-sm text-sm font-medium text-foreground hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
        >
          {name}
        </Link>
        {summary && <p className="line-clamp-2 text-xs text-muted-foreground">{summary}</p>}
        <div className="mt-auto flex flex-wrap items-center gap-1.5 pt-1">
          {needs.map((integration) => (
            <WorkflowBadge key={integration} label={`Needs ${integration}`} variant="neutral" />
          ))}
          {needsMachine && <WorkflowBadge label="Needs a machine" variant="neutral" />}
          {projectId && (
            <Button
              className="ml-auto"
              variant="outline"
              size="sm"
              aria-label={`Turn on ${name}…`}
              onClick={() => openAutomationDialog({ workflowRef: workflow.name, projectId, mode: "setup", showOpenWorkflow: true })}
            >
              Turn on…
            </Button>
          )}
        </div>
      </Card>
    </li>
  );
}
