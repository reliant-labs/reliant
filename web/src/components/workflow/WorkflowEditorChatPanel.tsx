/**
 * WorkflowEditorChatPanel - a toggleable floating panel in the workflow editor
 * whose body is the app's ordinary chat UI.
 *
 * No chat is bound to the workflow: the panel's chat id is UI state owned by
 * the caller (the route's `chat` search param). The composer is prefilled with
 * a visible reference to the workflow so the agent knows which one to load,
 * and the empty state suggests things to ask about this workflow.
 *
 * Where the chat runs is NewChatView's rule (lib/chatMachine.ts): the user's
 * machine when they have a usable one, otherwise no machine. Building a
 * workflow needs none — the workflow tools all run on the server.
 */

import { useEffect, useState } from "react";
import { Maximize2, Plus, X } from "lucide-react";
import { ReliantIcon } from "../icons/ReliantIcon";
import { cn } from "../../lib/utils";
import { NewChatView } from "../Chat/NewChatView";
import type { ComposerPrefill } from "../Chat/ChatInput";
import { ChatContainer } from "../Chat/ChatContainer";
import { BuilderChatEmptyState } from "./BuilderChatEmptyState";
import { useRunRoute } from "../runs/RunRouteLoader";
import { LoadingSpinner } from "../Layout/LoadingSpinner";
import { useProjectStore } from "../../store/projectStore";
import { useWorkspaceStateStore } from "../../store/workspaceStateStore";

export type PanelSize = "normal" | "maximized";

const WORKFLOW_REFERENCE_PATTERN = /^Workflow `[^`]*`: $/;

export function workflowReferencePrefill(workflowSlug: string): string {
  return `Workflow \`${workflowSlug}\`: `;
}

interface WorkflowEditorChatPanelProps {
  /** Slug the agent should use to address the workflow being edited. */
  workflowSlug?: string;
  /** Current chat shown in the panel (route search param); none shows a new-chat composer. */
  chatId?: string;
  onChatIdChange: (chatId: string | undefined) => void;
  /** A builder test run owns the chat stream slot; the panel's chat stays unmounted so it cannot take it back. */
  isStreamYielded?: boolean;
  isOpen: boolean;
  onOpenChange: (isOpen: boolean) => void;
  panelSize: PanelSize;
  onPanelSizeChange: (size: PanelSize) => void;
  /** Whether a config panel is open (shifts the closed-state button left). */
  isConfigPanelOpen?: boolean;
}

export function WorkflowEditorChatPanel({
  workflowSlug,
  chatId,
  onChatIdChange,
  isStreamYielded = false,
  isOpen,
  onOpenChange,
  panelSize,
  onPanelSizeChange,
  isConfigPanelOpen = false,
}: WorkflowEditorChatPanelProps) {
  if (!isOpen) {
    return (
      <button
        onClick={() => {
          onOpenChange(true);
          onPanelSizeChange("normal");
        }}
        className={cn(
          "fixed bottom-6 z-50",
          isConfigPanelOpen ? "right-[440px] px-4 py-3" : "right-6 px-6 py-4 gap-3",
          "flex items-center gap-2",
          "bg-gradient-to-r from-blue-500 to-purple-500 border border-blue-400/50",
          "text-white rounded-full shadow-lg",
          "hover:from-blue-600 hover:to-purple-600 transition-all hover:scale-105",
        )}
        title="Chat"
      >
        <ReliantIcon className="w-5 h-5 brightness-0 invert" />
        <span className="font-medium text-sm">{isConfigPanelOpen ? "Chat" : "Chat with an agent"}</span>
      </button>
    );
  }

  return (
    <div
      className={cn(
        "fixed bottom-6 right-6 z-50 flex flex-col",
        "bg-background border border-border rounded-xl shadow-2xl overflow-hidden",
        "transition-all duration-200",
        panelSize === "maximized" ? "h-[80vh] w-[600px]" : "h-[500px] w-[400px]",
      )}
    >
      <div className="flex items-center justify-between px-3 py-2 border-b border-border bg-muted/30">
        <div className="flex items-center gap-2">
          <ReliantIcon className="w-4 h-4 text-primary" />
          <span className="font-medium text-sm">Chat</span>
        </div>
        <div className="flex items-center gap-2">
          {chatId && (
            <button
              onClick={() => onChatIdChange(undefined)}
              className="p-1.5 hover:bg-muted rounded-md transition-colors text-muted-foreground hover:text-foreground"
              title="New chat"
            >
              <Plus className="w-4 h-4" />
            </button>
          )}
          <button
            onClick={() => onPanelSizeChange(panelSize === "normal" ? "maximized" : "normal")}
            className="p-1.5 hover:bg-muted rounded-md transition-colors"
            title={panelSize === "maximized" ? "Restore" : "Maximize"}
          >
            <Maximize2 className="w-4 h-4" />
          </button>
          <button
            onClick={() => onOpenChange(false)}
            className="p-1.5 hover:bg-muted rounded-md transition-colors"
            title="Close"
          >
            <X className="w-4 h-4" />
          </button>
        </div>
      </div>

      <div className="flex-1 min-h-0">
        {isStreamYielded ? (
          <div role="status" className="p-4 text-sm text-muted-foreground">
            Chat paused while the test run streams to the canvas.
          </div>
        ) : chatId ? (
          <ExistingChat key={chatId} chatId={chatId} />
        ) : (
          <NewChatWithWorkflowReference workflowSlug={workflowSlug} onChatCreated={onChatIdChange} />
        )}
      </div>
    </div>
  );
}

function ExistingChat({ chatId }: { chatId: string }) {
  const route = useRunRoute(chatId);
  if (route.status === "loading") {
    return <LoadingSpinner />;
  }
  if (route.status === "ready") {
    return <ChatContainer tabId={chatId} />;
  }
  return (
    <div role="alert" className="p-4 text-sm text-muted-foreground">
      {route.status === "not_found" ? "This chat could not be found." : route.message}
    </div>
  );
}

function NewChatWithWorkflowReference({
  workflowSlug,
  onChatCreated,
}: {
  workflowSlug?: string;
  onChatCreated: (chatId: string) => void;
}) {
  const projectId = useProjectStore((state) => state.currentProject?.id);
  if (!workflowSlug || !projectId) return <LoadingSpinner />;
  // Keyed by the reference: a new workflow gets a fresh composer and a fresh
  // reference.
  const reference = workflowReferencePrefill(workflowSlug);
  return (
    <WorkflowNewChat
      key={reference}
      projectId={projectId}
      workflowSlug={workflowSlug}
      reference={reference}
      onChatCreated={onChatCreated}
    />
  );
}

function WorkflowNewChat({
  projectId,
  workflowSlug,
  reference,
  onChatCreated,
}: {
  projectId: string;
  workflowSlug: string;
  reference: string;
  onChatCreated: (chatId: string) => void;
}) {
  // The reference is handed to the composer directly. It used to go through
  // the new-chat draft, which the composer reads when it mounts; but when the
  // slug changed (New workflow lands on /workflow/new, then on the draft's
  // slug) the composer re-mounted and read the draft before the new reference
  // was written, so it opened empty. A draft the user typed is theirs; only an
  // empty draft or a stale reference to another workflow is replaced.
  const [composerPrefill, setComposerPrefill] = useState<ComposerPrefill | undefined>(() => {
    const existing = useWorkspaceStateStore.getState().getNewChatDraft(projectId);
    return existing === "" || WORKFLOW_REFERENCE_PATTERN.test(existing) ? { text: reference, id: 0 } : undefined;
  });

  // The composer saves what it shows as the project's new-chat draft. A bare
  // reference to this workflow means nothing on the app's own new-chat screen.
  useEffect(
    () => () => {
      const drafts = useWorkspaceStateStore.getState();
      if (drafts.getNewChatDraft(projectId) === reference) drafts.clearNewChatDraft(projectId);
    },
    [projectId, reference],
  );

  const pickSuggestion = (text: string) =>
    setComposerPrefill((previous) => ({ text: reference + text, id: (previous?.id ?? 0) + 1 }));

  return (
    <NewChatView
      tabId="workflow-editor"
      onChatCreated={onChatCreated}
      composerPrefill={composerPrefill}
      emptyState={({ noMachine }) => (
        <BuilderChatEmptyState workflowSlug={workflowSlug} noMachine={noMachine} onPick={pickSuggestion} />
      )}
    />
  );
}
