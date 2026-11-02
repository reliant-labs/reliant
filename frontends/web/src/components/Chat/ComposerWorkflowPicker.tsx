import { useWorkflows, isOfferedInChatPicker } from "../../hooks/workflow-queries";
import { useDefaultWorkflow, useHiddenItems } from "../../hooks/settings-queries";
import { normalizeWorkflowRef } from "../workflow/useWorkflowInputs";
import { workflowDisplayName } from "../../lib/workflowDisplayName";
import { MobileSelectRow } from "../Mobile/MobileSettingsRow";

export function ComposerWorkflowPicker({ value, onChange, disabled }: React.ComponentProps<typeof import("./WorkflowSelector").WorkflowSelector>) {
  const { workflows, loading, error } = useWorkflows();
  const fallback = useDefaultWorkflow();
  const { isWorkflowHidden } = useHiddenItems();
  const seen = new Set<string>();
  const options = workflows.filter((workflow) => {
    const key = normalizeWorkflowRef(workflow.name).toLowerCase().trim();
    if (seen.has(key) || isWorkflowHidden(workflow.name) || !isOfferedInChatPicker(workflow)) return false;
    seen.add(key);
    return true;
  }).map((workflow) => ({ value: workflow.name, label: workflowDisplayName(workflow), description: workflow.description?.split("\n")[0] }));
  const selected = options.find((option) => normalizeWorkflowRef(option.value) === normalizeWorkflowRef(value ?? fallback));
  if (loading) return <p role="status">Loading workflows…</p>;
  if (error) return <p role="alert">Could not load workflows. {error}</p>;
  const fallbackLabel = (value ?? fallback).replace(/^builtin:\/\//, "").replace(/[_-]/g, " ");
  if (disabled) return <p className="text-sm text-muted-foreground">Workflow: {selected?.label ?? fallbackLabel}</p>;
  if (!options.length) return <p className="text-sm text-muted-foreground">No workflows available for this project.</p>;
  return <MobileSelectRow label="Workflow" value={selected?.value ?? value ?? fallback} options={options} onChange={(next) => onChange?.(next)} />;
}
