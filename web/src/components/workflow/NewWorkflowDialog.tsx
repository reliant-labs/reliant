// Copyright (c) 2025 Reliant Labs

/**
 * "New workflow": name it and choose how it starts, then Create. The draft is
 * created on that click and nowhere else — never from an effect, which React
 * may run twice and which reruns whenever its inputs change (that is how one
 * click used to make two drafts, both silent clones of the Agent workflow).
 *
 * The server makes the title and slug unique ("Triage" → "Triage 2"), so two
 * new workflows never look identical in the Library.
 */

import { useRef, useState } from "react";
import { toast } from "sonner";

import { useCreateWorkflowDraft } from "@/hooks/workflow-library-queries";
import { cn } from "@/lib/utils";
import { Button } from "../ui/Button";
import { Modal } from "../ui/Modal";

/** A start the dialog offers. `template` is the server's CreateWorkflowDraft template. */
export interface NewWorkflowStart {
  id: string;
  label: string;
  description: string;
  template?: string;
}

export const NEW_WORKFLOW_STARTS: readonly NewWorkflowStart[] = [
  { id: "blank", label: "Blank", description: "No steps yet. Add them from the canvas." },
  {
    id: "agent",
    label: "Agent loop template",
    description: "A copy of the built-in Agent: an LLM that calls tools until the task is done.",
    template: "builtin://agent",
  },
];

export interface CreatedWorkflow {
  slug: string;
  title: string;
  draftId: string;
}

interface NewWorkflowDialogProps {
  open: boolean;
  projectId: string;
  onClose: () => void;
  onCreated: (workflow: CreatedWorkflow) => void;
}

export function NewWorkflowDialog({ open, projectId, onClose, onCreated }: NewWorkflowDialogProps) {
  const [title, setTitle] = useState("");
  const [startId, setStartId] = useState(NEW_WORKFLOW_STARTS[0]!.id);
  const createDraft = useCreateWorkflowDraft(projectId);

  // One create per submit, even when a double click or Enter-then-click lands
  // before React has re-rendered the button as disabled.
  const submittingRef = useRef(false);
  const submit = () => {
    if (submittingRef.current || createDraft.isPending) return;
    submittingRef.current = true;
    const start = NEW_WORKFLOW_STARTS.find((s) => s.id === startId) ?? NEW_WORKFLOW_STARTS[0]!;
    createDraft.mutate(
      { title: title.trim(), template: start.template },
      {
        onSuccess: (created) => onCreated({ slug: created.slug, title: created.title, draftId: created.draftId }),
        onError: (error) => {
          submittingRef.current = false;
          toast.error(error instanceof Error ? error.message : "Could not create the workflow");
        },
      },
    );
  };

  return (
    <Modal isOpen={open} onClose={onClose} title="New workflow" size="md">
      <form
        className="space-y-5"
        onSubmit={(event) => {
          event.preventDefault();
          submit();
        }}
      >
        <div className="space-y-1.5">
          <label htmlFor="new-workflow-title" className="block text-sm font-medium text-foreground">
            Name
          </label>
          <input
            id="new-workflow-title"
            type="text"
            value={title}
            onChange={(event) => setTitle(event.target.value)}
            placeholder="e.g. Triage new GitHub issues"
            autoFocus
            disabled={createDraft.isPending}
            className="w-full rounded-lg border border-border bg-background px-3 py-2 text-sm text-foreground placeholder:text-muted-foreground focus:border-ring focus:outline-none focus:ring-2 focus:ring-ring/20"
          />
          <p className="text-xs text-muted-foreground">You can rename it later. Left empty, it is “Untitled workflow”.</p>
        </div>

        <fieldset className="space-y-2">
          <legend className="mb-1.5 text-sm font-medium text-foreground">Start from</legend>
          {NEW_WORKFLOW_STARTS.map((start) => (
            <label
              key={start.id}
              className={cn(
                "flex cursor-pointer items-start gap-3 rounded-lg border px-3 py-2.5 transition-colors",
                startId === start.id ? "border-primary bg-primary/5" : "border-border hover:bg-muted",
              )}
            >
              <input
                type="radio"
                name="new-workflow-start"
                value={start.id}
                checked={startId === start.id}
                onChange={() => setStartId(start.id)}
                disabled={createDraft.isPending}
                className="mt-1"
              />
              <span>
                <span className="block text-sm font-medium text-foreground">{start.label}</span>
                <span className="block text-xs text-muted-foreground">{start.description}</span>
              </span>
            </label>
          ))}
        </fieldset>

        <div className="flex justify-end gap-2 border-t border-border pt-4">
          <Button type="button" variant="outline" onClick={onClose} disabled={createDraft.isPending}>
            Cancel
          </Button>
          <Button type="submit" variant="primary" disabled={createDraft.isPending}>
            {createDraft.isPending ? "Creating…" : "Create workflow"}
          </Button>
        </div>
      </form>
    </Modal>
  );
}
