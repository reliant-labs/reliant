import { Check } from "lucide-react";
import { useWorkflows, isOfferedInChatPicker } from "../../hooks/workflow-queries";
import { useDefaultWorkflow, useHiddenItems } from "../../hooks/settings-queries";
import { normalizeWorkflowRef } from "../workflow/useWorkflowInputs";
import { workflowDisplayName } from "../../lib/workflowDisplayName";
import { cn } from "../../lib/utils";

interface WorkflowOption {
  value: string;
  label: string;
  description?: string;
}

/**
 * The workflows the chat picker offers (deduplicated, hidden ones and
 * non-chat ones left out) and which of them `value` selects — falling back to
 * the user's default workflow, as the send does.
 */
export function useComposerWorkflows(value: string | null | undefined) {
  const { workflows, loading, error } = useWorkflows();
  const fallback = useDefaultWorkflow();
  const { isWorkflowHidden } = useHiddenItems();
  const seen = new Set<string>();
  const options: WorkflowOption[] = workflows.filter((workflow) => {
    const key = normalizeWorkflowRef(workflow.name).toLowerCase().trim();
    if (seen.has(key) || isWorkflowHidden(workflow.name) || !isOfferedInChatPicker(workflow)) return false;
    seen.add(key);
    return true;
  }).map((workflow) => ({ value: workflow.name, label: workflowDisplayName(workflow), description: workflow.description?.split("\n")[0] }));
  const effective = value ?? fallback;
  const selected = options.find((option) => normalizeWorkflowRef(option.value) === normalizeWorkflowRef(effective));
  const label = selected?.label ?? effective.replace(/^builtin:\/\//, "").replace(/[_-]/g, " ");
  return { options, selected, label, loading, error };
}

/**
 * The ⊕ sheet's workflow page: every workflow the chat can start with, as
 * rows inside the sheet — never a second sheet stacked on the first.
 */
export function ComposerWorkflowPicker({ value, onChange }: {
  value: string | null;
  onChange: (workflow: string | null) => void;
}) {
  const { options, selected, loading, error } = useComposerWorkflows(value);
  if (loading) return <p role="status" className="px-4 py-3 text-sm text-muted-foreground">Loading workflows…</p>;
  if (error) return <p role="alert" className="px-4 py-3 text-sm text-destructive-ink">Could not load workflows. {error}</p>;
  if (!options.length) return <p className="px-4 py-3 text-sm text-muted-foreground">No workflows available for this project.</p>;
  return (
    <div className="py-1">
      {options.map((option) => {
        const isSelected = option.value === selected?.value;
        return (
          <button
            key={option.value}
            type="button"
            onClick={() => onChange(option.value)}
            aria-pressed={isSelected}
            className="flex min-h-[44px] w-full items-center justify-between gap-3 rounded-lg px-4 py-2.5 text-left hover:bg-foreground/5 active:bg-foreground/5 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            <span className="min-w-0">
              <span className={cn("block text-sm text-foreground", isSelected && "font-medium")}>{option.label}</span>
              {option.description && <span className="mt-0.5 block text-xs text-muted-foreground">{option.description}</span>}
            </span>
            {isSelected && <Check className="h-4 w-4 shrink-0 text-primary-ink" aria-hidden />}
          </button>
        );
      })}
    </div>
  );
}
