/**
 * The empty state of the workflow editor's chat: what to ask the agent about
 * THIS workflow (research/WORKFLOW_EDITOR_UX_REVIEW.md, issue 8).
 *
 * A suggestion fills the composer rather than sending, so the user can finish
 * the sentence or adjust it first.
 */

import { CloudOff, MessageSquareText, Plus, TestTube2, Zap, type LucideIcon } from "lucide-react";
import { NO_MACHINE_PILL } from "@/lib/chatMachine";

interface BuilderChatSuggestion {
  label: string;
  /** Composer text after the workflow reference; a trailing space invites the user to finish it. */
  text: string;
  icon: LucideIcon;
}

export const BUILDER_CHAT_SUGGESTIONS: readonly BuilderChatSuggestion[] = [
  { label: "Add a step that…", text: "Add a step that ", icon: Plus },
  { label: "Add a trigger when…", text: "Add a trigger so this runs when ", icon: Zap },
  { label: "Explain this workflow", text: "Explain what this workflow does, step by step.", icon: MessageSquareText },
  { label: "Write tests", text: "Write tests that cover this workflow's main paths.", icon: TestTube2 },
];

interface BuilderChatEmptyStateProps {
  workflowSlug: string;
  /** The chat will start with no machine; building workflows does not need one. */
  noMachine: boolean;
  onPick: (text: string) => void;
}

export function BuilderChatEmptyState({ workflowSlug, noMachine, onPick }: BuilderChatEmptyStateProps) {
  // Compact on purpose: the editor's chat panel is 500px tall, and the
  // suggestions should be on screen above the composer without scrolling.
  return (
    <div data-testid="builder-chat-empty-state" className="mx-auto flex w-full max-w-sm flex-col gap-2 text-left">
      <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
        <h2 className="text-sm font-semibold text-foreground">Build this workflow with the agent</h2>
        {noMachine && (
          <span
            data-testid="builder-chat-no-machine-pill"
            title="Editing workflows runs on Reliant's servers, so it doesn't need a machine."
            className="inline-flex items-center gap-1 rounded-full border border-border/60 bg-background px-2 py-0.5 text-xs font-medium text-muted-foreground"
          >
            <CloudOff className="h-3 w-3" aria-hidden="true" />
            {NO_MACHINE_PILL}
          </span>
        )}
      </div>
      <p className="text-xs text-muted-foreground">
        Describe a change to <code className="font-mono text-foreground">{workflowSlug}</code>, or start from one of
        these.
      </p>

      <ul aria-label="Suggested prompts" className="grid grid-cols-2 gap-1.5">
        {BUILDER_CHAT_SUGGESTIONS.map(({ label, text, icon: Icon }) => (
          <li key={label}>
            <button
              type="button"
              onClick={() => onPick(text)}
              data-testid="builder-chat-suggestion"
              className="flex h-full w-full items-center gap-1.5 rounded-md border border-border bg-card px-2.5 py-1.5 text-left text-xs text-foreground transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            >
              <Icon className="h-3.5 w-3.5 flex-shrink-0 text-muted-foreground" aria-hidden="true" />
              <span>{label}</span>
            </button>
          </li>
        ))}
      </ul>
    </div>
  );
}
