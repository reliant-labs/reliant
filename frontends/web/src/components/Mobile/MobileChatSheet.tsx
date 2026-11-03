/**
 * The Chat sheet — every chat-level action on mobile, behind the top bar's
 * title (mobile-chat-redesign §5.2).
 *
 * It gathers what desktop spreads across the `ChatHeader` dropdown, the thread
 * tabs, the context ring and the workflow pill. The actions themselves are
 * `useChatActions`, the same implementation the desktop dropdown calls, so
 * rename, archive-with-Undo, export, copy and compact cannot drift between
 * shells. "Open Terminal" is absent because the mobile surface has no terminal
 * (`terminal` capability), not because this sheet forgot it.
 *
 * Mounted only while open, like the composer's sheets: Forge's Modal owns
 * focus, Escape, restoration and portalling.
 */

import { useLayoutEffect, useState, type FormEvent } from "react";
import { Link } from "@tanstack/react-router";
import { formatDistanceToNow } from "date-fns";
import {
  Archive,
  Check,
  ChevronRight,
  Copy,
  Download,
  FolderGit2,
  GitBranch,
  Loader2,
  Pencil,
  PieChart,
  Workflow as WorkflowIcon,
} from "lucide-react";
import Modal from "../forge-ui/modal";
import { focusDialogOpener } from "../../lib/dialogOpener";
import { cn } from "../../lib/utils";
import { WorkflowState, WorkflowStopReason } from "../../gen/reliant/v1/chat_pb";
import type { WorkflowExecutionData } from "../../types/chat";
import { getWorkflowDisplayName } from "../workflow/useWorkflowInputs";
import { useChatActions } from "../Chat/useChatActions";
import { WorkspaceRecoveryMenuItems } from "../Chat/WorkspaceRecoveryMenuItems";
import type { ThreadInfo } from "../Chat/thread-views";
import { MOBILE_ROW, MobileCardGroup, MobileRowIcon } from "./MobileChrome";
import { contextUsageRatio, type ContextUsage } from "./MobileChatTopBar";

/** One word for where a workflow is, for the sheet's workflow row. */
export function workflowStatusLabel(execution: Pick<WorkflowExecutionData, "state" | "stopReason">): string {
  if (execution.state === WorkflowState.ACTIVE) return "Running";
  if (execution.state === WorkflowState.PENDING) return "Starting";
  switch (execution.stopReason) {
    case WorkflowStopReason.COMPLETED:
      return "Completed";
    case WorkflowStopReason.FAILED:
      return "Failed";
    case WorkflowStopReason.PAUSED:
      return "Paused";
    default:
      return "Stopped";
  }
}

function formatTokens(count: number): string {
  return count >= 1000 ? `${(count / 1000).toFixed(1)}k` : String(count);
}

function RowText({ label, detail }: { label: string; detail?: string }) {
  return (
    <span className="min-w-0 flex-1">
      <span className="block truncate text-sm font-medium text-foreground">{label}</span>
      {detail && <span className="block truncate text-xs text-muted-foreground">{detail}</span>}
    </span>
  );
}

const TRAILING_CHEVRON = <ChevronRight className="h-4 w-4 shrink-0 text-muted-foreground" aria-hidden />;

interface MobileChatSheetProps {
  chatId: string;
  title: string;
  createdAt?: string;
  worktreeId?: string;
  onClose: () => void;
  /** The chat's latest workflow execution, running or not. */
  workflow?: WorkflowExecutionData | null;
  threads: ThreadInfo[];
  /** Shown only when there is more than the main thread to choose from. */
  hasMultipleThreads: boolean;
  /** null is the all-threads view, as in `ChatPresenter`. */
  selectedThreadId: string | null;
  onSelectThread: (threadId: string | null) => void;
  contextUsage?: ContextUsage | null;
  /** Present when the chat has a workspace to open. */
  onOpenWorkspace?: () => void;
}

export function MobileChatSheet({
  chatId,
  title,
  createdAt,
  worktreeId,
  onClose,
  workflow,
  threads,
  hasMultipleThreads,
  selectedThreadId,
  onSelectThread,
  contextUsage,
  onOpenWorkspace,
}: MobileChatSheetProps) {
  // Safari never focuses a button on tap, so Forge's Modal would record <body>
  // as the opener; focusing the tapped control first makes it restore there.
  useLayoutEffect(focusDialogOpener, []);
  const actions = useChatActions(chatId, worktreeId);
  const [renameDraft, setRenameDraft] = useState<string | null>(null);
  const [showThreads, setShowThreads] = useState(false);

  // Every row that hands off to somewhere else closes the sheet first.
  const closeThen = (action: () => unknown) => () => {
    onClose();
    void action();
  };

  const submitRename = async (e: FormEvent) => {
    e.preventDefault();
    if (renameDraft === null) return;
    if (await actions.rename(renameDraft)) setRenameDraft(null);
  };

  const workflowName = workflow ? getWorkflowDisplayName(workflow.workflowName, true) : null;
  let startedAgo: string | null = null;
  if (createdAt) {
    try {
      startedAgo = `started ${formatDistanceToNow(new Date(createdAt), { addSuffix: true })}`;
    } catch {
      startedAgo = null;
    }
  }
  const summary = [workflowName, startedAgo].filter(Boolean).join(" · ");

  const selectedThread = selectedThreadId ? threads.find((t) => t.id === selectedThreadId) : undefined;
  const threadLabel = selectedThreadId === null ? "All threads" : (selectedThread?.name ?? "Thread");
  const usageRatio = contextUsageRatio(contextUsage);
  const recoverableWorktree =
    actions.worktree && !actions.worktree.is_main && !actions.worktree.deleted_at ? actions.worktree : null;

  return (
    <Modal
      open
      title={title}
      description={summary || undefined}
      onClose={onClose}
      size="md"
      sheetOnMobile
      headerActions={
        renameDraft === null && (
          <button
            type="button"
            onClick={() => setRenameDraft(title)}
            className="flex min-h-[44px] min-w-[44px] items-center justify-center rounded-md text-muted-foreground active:bg-foreground/5"
            aria-label="Rename chat"
          >
            <Pencil className="h-4 w-4" />
          </button>
        )
      }
    >
      <div className="min-w-0 space-y-6 pb-4">
        {renameDraft !== null && (
          <form onSubmit={(e) => void submitRename(e)} className="flex items-center gap-2">
            {/* index.css lifts every touch input to 16px, so iOS does not zoom on focus. */}
            <input
              aria-label="Chat title"
              value={renameDraft}
              onChange={(e) => setRenameDraft(e.target.value)}
              onKeyDown={(e) => {
                if (e.key !== "Escape") return;
                // Cancel the edit, not the sheet: Modal skips a handled Escape.
                e.preventDefault();
                setRenameDraft(null);
              }}
              autoFocus
              className="min-h-[44px] min-w-0 flex-1 rounded-md border border-border bg-background px-3 text-base text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            />
            <button
              type="submit"
              className="flex min-h-[44px] min-w-[44px] items-center justify-center rounded-md bg-primary px-3 text-sm font-medium text-primary-foreground active:opacity-80"
            >
              Save
            </button>
          </form>
        )}

        <MobileCardGroup>
          {workflow && workflowName && (
            <Link
              to="/m/chats/$chatId/workflow"
              params={{ chatId }}
              onClick={onClose}
              className={MOBILE_ROW}
            >
              <MobileRowIcon icon={WorkflowIcon} />
              <RowText label={`Workflow: ${workflowName}`} detail={workflowStatusLabel(workflow)} />
              {(workflow.state === WorkflowState.ACTIVE || workflow.state === WorkflowState.PENDING) && (
                <Loader2 className="h-4 w-4 shrink-0 animate-spin text-primary-ink" aria-hidden />
              )}
              {TRAILING_CHEVRON}
            </Link>
          )}

          {hasMultipleThreads && (
            <>
              <button
                type="button"
                onClick={() => setShowThreads((v) => !v)}
                className={MOBILE_ROW}
                aria-expanded={showThreads}
              >
                <MobileRowIcon icon={GitBranch} />
                <RowText label="Thread" detail={threadLabel} />
                <ChevronRight
                  className={cn("h-4 w-4 shrink-0 text-muted-foreground transition-transform", showThreads && "rotate-90")}
                  aria-hidden
                />
              </button>
              {showThreads && (
                <div role="group" aria-label="Threads" className="border-b border-border last:border-b-0">
                  {[{ id: null, name: "All threads", color: undefined }, ...threads.filter((t) => !t.isSpawn)].map(
                    (thread) => {
                      const selected = thread.id === selectedThreadId;
                      return (
                        <button
                          key={thread.id ?? "all"}
                          type="button"
                          onClick={closeThen(() => onSelectThread(thread.id))}
                          aria-pressed={selected}
                          className="flex min-h-[44px] w-full items-center gap-3 py-2 pl-16 pr-4 text-left text-sm text-foreground active:bg-foreground/5"
                        >
                          {thread.color && (
                            <span className="h-2 w-2 shrink-0 rounded-full" style={{ backgroundColor: thread.color }} />
                          )}
                          <span className="min-w-0 flex-1 truncate">{thread.name}</span>
                          {selected && <Check className="h-4 w-4 shrink-0 text-primary-ink" aria-hidden />}
                        </button>
                      );
                    },
                  )}
                </div>
              )}
            </>
          )}

          <div className={cn(MOBILE_ROW, "active:bg-transparent")}>
            <MobileRowIcon icon={PieChart} />
            <RowText
              label="Context"
              detail={
                contextUsage && contextUsage.threadTokenCount > 0
                  ? `${formatTokens(contextUsage.threadTokenCount)} / ${formatTokens(contextUsage.compactionThreshold)} · ${Math.round(Math.min(usageRatio, 1) * 100)}%`
                  : "No context used yet"
              }
            />
            {/* The composer's `/compact`: the thread in view, "" for main. */}
            <button
              type="button"
              onClick={closeThen(() => actions.compact(selectedThreadId ?? ""))}
              className="flex min-h-[44px] shrink-0 items-center rounded-md px-3 text-sm font-medium text-primary-ink active:bg-foreground/5"
            >
              Compact
            </button>
          </div>

          {onOpenWorkspace && (
            <button type="button" onClick={closeThen(onOpenWorkspace)} className={MOBILE_ROW}>
              <MobileRowIcon icon={FolderGit2} />
              <RowText label="Files, Git & Plan" detail={actions.workspaceName ?? undefined} />
              {TRAILING_CHEVRON}
            </button>
          )}
        </MobileCardGroup>

        <MobileCardGroup>
          <button type="button" onClick={closeThen(actions.exportChat)} className={MOBILE_ROW}>
            <MobileRowIcon icon={Download} />
            <RowText label="Export chat" />
          </button>
          <button type="button" onClick={closeThen(actions.copyChatId)} className={MOBILE_ROW}>
            <MobileRowIcon icon={Copy} />
            <RowText label="Copy chat ID" />
          </button>
        </MobileCardGroup>

        {recoverableWorktree && (
          // The same items the desktop menu renders, sized up to 44px rows
          // for a thumb rather than forked.
          <MobileCardGroup
            label="Workspace recovery"
            className="[&>button]:min-h-[44px] [&>button]:gap-3 [&>button]:px-4 [&>button]:active:bg-foreground/5"
          >
            <WorkspaceRecoveryMenuItems
              chatId={chatId}
              worktree={recoverableWorktree}
              mainWorktree={actions.mainWorktree}
              onSelect={onClose}
            />
          </MobileCardGroup>
        )}

        <MobileCardGroup>
          <button type="button" onClick={closeThen(actions.archive)} className={MOBILE_ROW}>
            <MobileRowIcon icon={Archive} />
            <RowText label="Archive chat" />
          </button>
        </MobileCardGroup>
      </div>
    </Modal>
  );
}
