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
import { FIX_MY_PR_TITLE, seedFixMyPr } from "@/components/Onboarding/journeys/fixMyPr";
import { cn } from "@/lib/utils";
import Modal from "../forge-ui/modal";
import Button from "@/components/forge-ui/button";

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

/**
 * The workflows journey's start (journeys-spec.md W1), offered only while
 * that tour is open: the "Fix my PR" template imported as the user's own
 * draft (Onboarding/journeys/fixMyPr.ts), not a server template.
 */
export const GUIDED_START: NewWorkflowStart = {
  id: "guided-fix-my-pr",
  label: "Fix my PR (guided)",
  description: "Reviews a pull request, fixes it until the tests pass, and asks before merging. The tour builds it with you.",
};

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
  /** The workflows tour is open: offer, preselect and prefill the guided start. */
  guided?: boolean;
}

export function NewWorkflowDialog({ open, projectId, onClose, onCreated, guided = false }: NewWorkflowDialogProps) {
  const starts = guided ? [GUIDED_START, ...NEW_WORKFLOW_STARTS] : NEW_WORKFLOW_STARTS;
  const [title, setTitle] = useState(guided ? FIX_MY_PR_TITLE : "");
  const [startId, setStartId] = useState(starts[0]!.id);
  const createDraft = useCreateWorkflowDraft(projectId);
  const [seeding, setSeeding] = useState(false);
  const pending = createDraft.isPending || seeding;

  // One create per submit, even when a double click or Enter-then-click lands
  // before React has re-rendered the button as disabled.
  const submittingRef = useRef(false);
  const submit = () => {
    if (submittingRef.current || pending) return;
    submittingRef.current = true;
    const start = starts.find((s) => s.id === startId) ?? starts[0]!;
    if (start.id === GUIDED_START.id) {
      setSeeding(true);
      seedFixMyPr(projectId, title)
        .then((seeded) => onCreated({ slug: seeded.slug, title: seeded.title, draftId: seeded.draftId }))
        .catch((error: unknown) => {
          submittingRef.current = false;
          setSeeding(false);
          toast.error(error instanceof Error ? error.message : "Could not create the workflow");
        });
      return;
    }
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
    <Modal open={open} onClose={onClose} title="New workflow" size="md">
      <form
        className="space-y-5"
        data-tour="new-workflow-dialog"
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
            disabled={pending}
            className="w-full rounded-lg border border-border bg-background px-3 py-2 text-sm text-foreground placeholder:text-muted-foreground focus:border-ring focus:outline-none focus:ring-2 focus:ring-ring"
          />
          <p className="text-xs text-muted-foreground">You can rename it later. Left empty, it is “Untitled workflow”.</p>
        </div>

        <fieldset className="space-y-2">
          <legend className="mb-1.5 text-sm font-medium text-foreground">Start from</legend>
          {starts.map((start) => (
            <label
              key={start.id}
              data-tour={start.id === GUIDED_START.id ? "new-workflow-start-guided" : undefined}
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
                disabled={pending}
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
          <Button type="button" variant="outline" onClick={onClose} disabled={pending}>
            Cancel
          </Button>
          <Button type="submit" variant="primary" disabled={pending} data-tour="new-workflow-create">
            {pending ? "Creating…" : "Create workflow"}
          </Button>
        </div>
      </form>
    </Modal>
  );
}
