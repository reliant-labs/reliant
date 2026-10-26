// Copyright (c) 2025 Reliant Labs

/**
 * The ONE way a page opens the automation create/edit dialog.
 *
 * Every "make it run by itself" entry point — a workflow row's "Add trigger…",
 * the workflow page's Triggers card, a trigger's Edit, the Runs empty state —
 * calls `openAutomationDialog(...)` and nothing else. The pages never import a
 * dialog, so the dialog behind this call can be rebuilt (one When-first
 * dialog) without touching a single caller.
 *
 * `AutomationDialogHost` renders whatever dialog the request resolves to. It is
 * mounted once, in the Workflows area's layout (WorkflowsShell).
 *
 * The request names WHAT the user wants, not which dialog shows it:
 *
 *   { triggerId }                 edit that trigger
 *   { workflowRef, projectId? }   make this workflow run by itself
 *   { projectId? }                a new automation, workflow not chosen yet
 *
 * `custom: true` asks for a hand-made schedule / webhook / app event even when
 * the workflow declares triggers of its own. While the declared-trigger setup
 * and the custom form are two dialogs, that is how a caller picks the second;
 * a single dialog offers both and may ignore it.
 */

import { create } from "zustand";

import { useTriggerById } from "@/hooks/trigger-queries";
import { useWorkflowLibrary } from "@/hooks/workflow-library-queries";
import type { DeclaredTrigger } from "@/lib/declaredTriggers";
import { triggerRunsWorkflow } from "@/lib/triggerRail";
import { workflowDisplayName } from "@/lib/workflowDisplayName";
import { useProjectStore } from "@/store/projectStore";
import { AutomationFormDialog } from "./AutomationFormDialog";
import { WorkflowSetupDialog } from "./WorkflowSetupDialog";

export interface AutomationDialogRequest {
  /** The workflow to make run by itself. */
  workflowRef?: string;
  /** The trigger to edit. Wins over workflowRef. */
  triggerId?: string;
  /** The project it starts in; defaults to the current project. */
  projectId?: string;
  /** A hand-made trigger, even for a workflow that declares its own. */
  custom?: boolean;
}

interface AutomationDialogState {
  request: AutomationDialogRequest | null;
}

const useAutomationDialogStore = create<AutomationDialogState>(() => ({ request: null }));

/** Opens the automation dialog for this request, replacing any open one. */
export function openAutomationDialog(request: AutomationDialogRequest = {}): void {
  useAutomationDialogStore.setState({ request: { ...request } });
}

export function closeAutomationDialog(): void {
  useAutomationDialogStore.setState({ request: null });
}

/** The open request, for tests and for a host. */
export function useAutomationDialogRequest(): AutomationDialogRequest | null {
  return useAutomationDialogStore((state) => state.request);
}

export function AutomationDialogHost() {
  const request = useAutomationDialogRequest();
  if (!request) return null;
  // Keyed, so a second request remounts the dialog with fresh state.
  const key = `${request.triggerId ?? ""}|${request.workflowRef ?? ""}|${request.projectId ?? ""}|${request.custom ? 1 : 0}`;
  return request.triggerId ? (
    <EditTrigger key={key} triggerId={request.triggerId} />
  ) : (
    <CreateFor key={key} request={request} />
  );
}

function EditTrigger({ triggerId }: { triggerId: string }) {
  const trigger = useTriggerById(triggerId);
  if (!trigger) return null;
  // An activation of a declared trigger shares its settings with the
  // workflow's other triggers in that project: it is edited as the whole.
  if (trigger.workflowTrigger) {
    return <DeclaredSetup workflowRef={trigger.workflow} projectId={trigger.projectId} settings />;
  }
  return <AutomationFormDialog open onClose={closeAutomationDialog} trigger={trigger} />;
}

function CreateFor({ request }: { request: AutomationDialogRequest }) {
  const currentProjectId = useProjectStore((state) => state.currentProject?.id);
  const projectId = request.projectId ?? currentProjectId;
  if (!request.workflowRef || request.custom || !projectId) {
    return <CustomForm workflowRef={request.workflowRef} projectId={projectId} />;
  }
  return <DeclaredOrCustom workflowRef={request.workflowRef} projectId={projectId} />;
}

function CustomForm({ workflowRef, projectId }: { workflowRef?: string; projectId?: string }) {
  const library = useWorkflowLibrary(workflowRef ? projectId : undefined);
  const listing = workflowRef ? library.data?.workflows.find((w) => triggerRunsWorkflow({ workflow: w.name }, workflowRef)) : undefined;
  return (
    <AutomationFormDialog
      open
      onClose={closeAutomationDialog}
      prefill={workflowRef ? { workflow: workflowRef, projectId, name: listing ? workflowDisplayName(listing) : undefined } : undefined}
      defaultProjectId={projectId}
    />
  );
}

/**
 * The workflow's listing in the project: its title, and the `triggers:` it
 * declares (ListWorkflows carries them, parsed), so deciding which form to
 * open costs no read beyond the list the page already has.
 */
function useListing(workflowRef: string, projectId: string) {
  const library = useWorkflowLibrary(projectId);
  const listing = library.data?.workflows.find((w) => triggerRunsWorkflow({ workflow: w.name }, workflowRef));
  return { listing, loading: library.isLoading, declared: (listing?.triggers ?? []) as DeclaredTrigger[] };
}

/** A workflow that declares triggers is set up from them; one that declares none gets the custom form. */
function DeclaredOrCustom({ workflowRef, projectId }: { workflowRef: string; projectId: string }) {
  const { loading, declared } = useListing(workflowRef, projectId);
  if (loading) return null;
  if (declared.length === 0) return <CustomForm workflowRef={workflowRef} projectId={projectId} />;
  return <DeclaredSetup workflowRef={workflowRef} projectId={projectId} />;
}

function DeclaredSetup({ workflowRef, projectId, settings }: { workflowRef: string; projectId: string; settings?: boolean }) {
  const { listing, declared } = useListing(workflowRef, projectId);
  if (declared.length === 0) return null;
  return (
    <WorkflowSetupDialog
      open
      onClose={closeAutomationDialog}
      workflowRef={workflowRef}
      workflowTitle={listing ? workflowDisplayName(listing) : undefined}
      declared={declared}
      defaultProjectId={projectId}
      mode={settings ? "settings" : undefined}
      showOpenWorkflow={false}
    />
  );
}
