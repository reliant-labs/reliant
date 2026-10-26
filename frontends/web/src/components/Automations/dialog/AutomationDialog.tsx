// Copyright (c) 2025 Reliant Labs

/**
 * THE automation dialog: create or edit, every trigger kind, from anywhere.
 * It replaces the four dialogs that used to make the same thing (the custom
 * automation form, workflow Set up, the builder's Activate and its personal
 * trigger). Open it with openAutomationDialog (openAutomationDialog.ts).
 *
 * One dialog and one word, "automation"; two server models behind it (report
 * 03 §6.8, option 1), chosen here and never by a caller:
 *
 *   - the workflow DECLARES its triggers (and the person did not ask for a
 *     WHEN of their own): DeclaredAutomationForm, which turns them all on
 *     together through EnableWorkflow;
 *   - otherwise — a blank start, the default agent, a workflow with no
 *     `triggers:`, a built-in's personal trigger, "Need a different
 *     trigger?": InlineAutomationForm, an inline trigger row through
 *     CreateTrigger / UpdateTrigger.
 *
 * Editing an activation edits its workflow's whole declared set in that
 * project (the settings phase), for the same reason: they cannot drift apart.
 */

import { useState } from "react";

import type { Trigger } from "@/hooks/trigger-model";
import { useTrigger } from "@/hooks/trigger-queries";
import { useWorkflowLibrary } from "@/hooks/workflow-library-queries";
import { declaredTriggers } from "@/lib/declaredTriggers";
import { triggerRunsWorkflow } from "@/lib/triggerRail";
import { workflowDisplayName } from "@/lib/workflowDisplayName";
import { useProjectStore } from "@/store/projectStore";
import Modal from "../../forge-ui/modal";
import { useWorkflowDefinition } from "../../workflow/useWorkflowDefinition";
import { DeclaredAutomationForm } from "./DeclaredAutomationForm";
import { InlineAutomationForm } from "./InlineAutomationForm";
import type { OpenAutomationDialogOptions } from "./openAutomationDialog";

export interface AutomationDialogProps {
  request: OpenAutomationDialogOptions;
  onClose: () => void;
}

export function AutomationDialog({ request, onClose }: AutomationDialogProps) {
  const stored = useTrigger(request.trigger ? undefined : request.triggerId);
  const trigger = request.trigger ?? stored.data;

  if (request.triggerId && !request.trigger && !trigger) {
    return (
      <Modal open onClose={onClose} size="md" title="Edit automation">
        <p className="text-sm text-muted-foreground" role={stored.isError ? "alert" : undefined} aria-busy={!stored.isError}>
          {stored.isError ? "This automation could not be loaded. It may have been deleted." : "Loading…"}
        </p>
      </Modal>
    );
  }
  if (trigger) return <EditAutomation trigger={trigger} request={request} onClose={onClose} />;
  return <NewAutomation request={request} onClose={onClose} />;
}

function EditAutomation({ trigger, request, onClose }: { trigger: Trigger; request: OpenAutomationDialogOptions; onClose: () => void }) {
  if (trigger.source.kind === "activation") {
    return (
      <ForWorkflow
        workflowRef={trigger.workflow}
        projectId={trigger.projectId}
        mode={request.mode ?? "settings"}
        focus={request.focus}
        showOpenWorkflow={request.showOpenWorkflow ?? true}
        editing={trigger}
        onSaved={request.onSaved}
        onClose={onClose}
      />
    );
  }
  return <InlineAutomationForm trigger={trigger} onClose={onClose} onSaved={request.onSaved} />;
}

function NewAutomation({ request, onClose }: AutomationDialogProps) {
  const currentProjectId = useProjectStore((s) => s.currentProject?.id);
  const projectId = request.projectId ?? request.prefill?.projectId ?? currentProjectId ?? "";
  // "Use it" on a workflow that already handles the chosen event.
  const [picked, setPicked] = useState<{ workflowRef: string; workflowTitle: string; projectId: string } | null>(null);
  const [custom, setCustom] = useState(!!request.custom);

  const workflowRef = picked?.workflowRef ?? request.workflowRef;
  if (workflowRef) {
    return (
      <ForWorkflow
        workflowRef={workflowRef}
        workflowTitle={picked?.workflowTitle ?? request.workflowTitle}
        projectId={picked?.projectId ?? projectId}
        mode={request.mode}
        focus={request.focus}
        showOpenWorkflow={request.showOpenWorkflow}
        declaredTrigger={request.declaredTrigger}
        custom={custom}
        onCustomChange={setCustom}
        prefill={request.prefill}
        onSaved={request.onSaved}
        onClose={onClose}
      />
    );
  }
  return (
    <InlineAutomationForm
      prefill={request.prefill}
      projectId={projectId}
      onSaved={request.onSaved}
      onClose={onClose}
      onUseWorkflow={(ref, title, project) => {
        setCustom(false);
        setPicked({ workflowRef: ref, workflowTitle: title, projectId: project });
      }}
    />
  );
}

interface ForWorkflowProps {
  workflowRef: string;
  workflowTitle?: string;
  projectId: string;
  mode?: OpenAutomationDialogOptions["mode"];
  focus?: string;
  showOpenWorkflow?: boolean;
  declaredTrigger?: string;
  custom?: boolean;
  onCustomChange?: (custom: boolean) => void;
  prefill?: OpenAutomationDialogOptions["prefill"];
  /** An activation being edited: shown as itself if the workflow no longer declares triggers. */
  editing?: Trigger;
  onSaved?: OpenAutomationDialogOptions["onSaved"];
  onClose: () => void;
}

/** A workflow's automation: its declared triggers, unless it has none or the person wants their own WHEN. */
function ForWorkflow({
  workflowRef,
  workflowTitle,
  projectId,
  mode,
  focus,
  showOpenWorkflow,
  declaredTrigger,
  custom = false,
  onCustomChange,
  prefill,
  editing,
  onSaved,
  onClose,
}: ForWorkflowProps) {
  const definition = useWorkflowDefinition(projectId || undefined, workflowRef);
  const library = useWorkflowLibrary(projectId || undefined);
  const listing = library.data?.workflows.find((w) => triggerRunsWorkflow({ workflow: w.name }, workflowRef));
  const title = workflowTitle ?? (listing ? workflowDisplayName(listing) : undefined);
  const declared = definition.workflowDef ? declaredTriggers(definition.workflowDef) : [];

  if (!definition.workflowDef && !definition.error && projectId) {
    return (
      <Modal open onClose={onClose} size="md" title={title ? `New automation: ${title}` : "New automation"}>
        <p className="text-sm text-muted-foreground" aria-busy="true">
          Loading the workflow…
        </p>
      </Modal>
    );
  }

  if (declared.length > 0 && !custom) {
    return (
      <DeclaredAutomationForm
        onClose={onClose}
        workflowRef={workflowRef}
        workflowTitle={title}
        declared={declared}
        defaultProjectId={projectId}
        mode={mode}
        focus={focus}
        showOpenWorkflow={showOpenWorkflow}
        declaredTrigger={declaredTrigger}
        onEnabled={(result) => onSaved?.(result.results.flatMap((r) => (r.activation ? [r.activation] : [])))}
        onUseCustom={onCustomChange ? () => onCustomChange(true) : undefined}
      />
    );
  }
  if (editing) return <InlineAutomationForm trigger={editing} onClose={onClose} onSaved={onSaved} />;
  return (
    <InlineAutomationForm
      onClose={onClose}
      projectId={projectId}
      workflowRef={workflowRef}
      workflowTitle={title}
      prefill={{ ...prefill, workflow: workflowRef }}
      onSaved={onSaved}
      onBackToDeclared={declared.length > 0 && onCustomChange ? () => onCustomChange(false) : undefined}
    />
  );
}
