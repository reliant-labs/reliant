// Copyright (c) 2025 Reliant Labs

/**
 * The setup dialog for a workflow named only by its ref and project — what a
 * call to action on the Automations list, the Inbox or a trigger page has —
 * once the workflow's declared triggers and title have loaded.
 */

import { useWorkflowLibrary } from "@/hooks/workflow-library-queries";
import { declaredTriggers } from "@/lib/declaredTriggers";
import { triggerRunsWorkflow } from "@/lib/triggerRail";
import { workflowDisplayName } from "@/lib/workflowDisplayName";
import { useWorkflowDefinition } from "../workflow/useWorkflowDefinition";
import { WorkflowSetupDialog, type SetupMode } from "./WorkflowSetupDialog";

export interface SetupDialogForProps {
  workflowRef: string;
  projectId: string;
  mode?: SetupMode;
  focus?: string;
  /** Offer "Open workflow" on success; true everywhere but the workflow page. */
  showOpenWorkflow?: boolean;
  onClose: () => void;
}

export function SetupDialogFor({ workflowRef, projectId, mode, focus, showOpenWorkflow = true, onClose }: SetupDialogForProps) {
  const definition = useWorkflowDefinition(projectId, workflowRef);
  const library = useWorkflowLibrary(projectId);
  const listing = library.data?.workflows.find((w) => triggerRunsWorkflow({ workflow: w.name }, workflowRef));
  if (!definition.workflowDef) return null;
  return (
    <WorkflowSetupDialog
      open
      onClose={onClose}
      workflowRef={workflowRef}
      workflowTitle={listing ? workflowDisplayName(listing) : undefined}
      declared={declaredTriggers(definition.workflowDef)}
      defaultProjectId={projectId}
      mode={mode}
      focus={focus}
      showOpenWorkflow={showOpenWorkflow}
    />
  );
}
