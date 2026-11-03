/**
 * `/m/chats/$chatId` — a single chat on mobile.
 *
 * Deliberately thin: it renders the SAME `ChatContainer` the desktop app uses.
 * That container owns every subscription (`chatStore`, streaming messages,
 * approvals, activity, worktree/project context) and all the reconnect and
 * message-merge semantics. Forking any of that for mobile would mean
 * maintaining two copies of the hardest code in the app and debugging stream
 * resume twice.
 *
 * What differs on mobile comes from `SurfaceProvider` (set by `MobileLayout`)
 * rather than from props — most visibly, tool calls collapse by default
 * because an expanded diff is unreadable in a phone viewport.
 */

import { useEffect, useMemo, useRef, useState } from "react";
import { useParams } from "@tanstack/react-router";
import { ChatContainer } from "../Chat/ChatContainer";
import { useThreads } from "../Chat/thread-views";
import { useThreadNavigation } from "../Chat/useThreadNavigation";
import { transformWorkflowExecution } from "../Chat/ExecutionSidebar/transformApiData";
import { getWorkflowDisplayName } from "../workflow/useWorkflowInputs";
import { useChat, useContextUsage } from "../../store/chatStoreHooks";
import { useProjectStore } from "../../store/projectStore";
import { fetchProjects, getCachedProjects } from "../../hooks/project-queries";
import { useMessages } from "../../hooks/message-queries";
import { useWorkflowExecutions } from "../../hooks/useWorkflowExecutions";
import { WorkflowState } from "../../gen/reliant/v1/chat_pb";
import type { Message } from "../../api/client";
import { MobileWorkspaceSheet } from "./MobileWorkspaceSheet";
import { MobileChatTopBar } from "./MobileChatTopBar";
import { MobileChatSheet } from "./MobileChatSheet";
import { loadMainWorktree } from "../../hooks/worktree-actions";
import { useMainWorktree } from "../../hooks/worktree-queries";

const NO_MESSAGES: Message[] = [];

export function MobileChatScreen() {
  // `strict: false` rather than `from: "/m/chats/$chatId"`. The route is
  // nested under `_authenticated` → `_mobile`, so its registered id is
  // `/_authenticated/_mobile/m/chats/$chatId`; passing the *path* threw
  // "Could not find an active match" and took down the whole screen.
  const { chatId } = useParams({ strict: false });
  const chat = useChat(chatId);
  const currentProjectId = useProjectStore((s) => s.currentProject?.id);
  const currentProjectPath = useProjectStore((s) => s.currentProject?.path);
  const [showWorkspace, setShowWorkspace] = useState(false);
  const [showSheet, setShowSheet] = useState(false);

  // A deep link or notification can open a chat that is not in the project the
  // shell selected. What the chat screen reads — worktrees, files, the
  // composer's presets — follows the current project, so move it to the
  // chat's own. Once per chat: a user who then picks another
  // project on purpose is not pulled back.
  const alignedFor = useRef<string | null>(null);
  const chatProjectId = chat?.projectId;
  useEffect(() => {
    if (!chatId || !chatProjectId || alignedFor.current === chatId) return;
    alignedFor.current = chatId;
    if (useProjectStore.getState().currentProject?.id === chatProjectId) return;
    void (async () => {
      let project = getCachedProjects().find((p) => p.id === chatProjectId);
      if (!project) {
        project = (await fetchProjects()).find((p) => p.id === chatProjectId);
      }
      if (!project) return;
      // This screen's chat comes from the URL: neither the desktop workspace
      // restore nor the project's last chat may run over it (the restore would
      // select the project's remembered chat, mark it read and move the update
      // stream's single chat subscription off the chat on screen).
      await useProjectStore.getState().selectProject(project, { restoreWorkspace: false, restoreChat: false });
    })();
  }, [chatId, chatProjectId]);

  // A chat with no worktreeId runs against the project's main checkout — the
  // same default MobileNewChat resolves to when creating a chat. Without
  // loading the main workspace here, that fallback has nothing to resolve
  // against. Only that one row: the screen never lists workspaces.
  const main = useMainWorktree(currentProjectId);
  const mainWorktree = main && !main.deleted_at ? main : undefined;
  useEffect(() => {
    // Selects main when nothing is selected yet; the row is already cached.
    if (currentProjectId) void loadMainWorktree(currentProjectId);
  }, [currentProjectId]);

  const effectiveWorktreeId = chat?.worktreeId ?? mainWorktree?.id;
  const openWorkspace = effectiveWorktreeId ? () => setShowWorkspace(true) : undefined;

  // Thread selection lives here rather than in ChatPresenter, because this
  // screen's top bar and Chat sheet are what pick the thread. A new chat
  // starts on the all-threads view, as ChatPresenter does.
  const [selectedThreadId, setSelectedThreadId] = useState<string | null>(null);
  const [threadsFor, setThreadsFor] = useState(chatId);
  if (threadsFor !== chatId) {
    setThreadsFor(chatId);
    setSelectedThreadId(null);
  }
  const { data: workflowExecution, allWorkflows } = useWorkflowExecutions(chatId ?? null);
  const workflowTrees = useMemo(() => allWorkflows.map(transformWorkflowExecution), [allWorkflows]);
  const messages = useMessages(chatId).data?.messages ?? NO_MESSAGES;
  const threads = useThreads(messages, chatId ?? "", workflowTrees);
  const { hasMultipleThreads, isInSubThread, selectedThread, backTarget, backTargetId } =
    useThreadNavigation(threads, selectedThreadId);

  // Status for the bar: only a workflow that is running now. A finished one
  // stays reachable from the Chat sheet's workflow row.
  const runningWorkflow =
    workflowExecution &&
    (workflowExecution.state === WorkflowState.ACTIVE || workflowExecution.state === WorkflowState.PENDING)
      ? getWorkflowDisplayName(workflowExecution.workflowName, true)
      : undefined;
  const contextUsage = useContextUsage(chatId ?? "");
  const title = chat?.title || "Chat";

  // `useParams({ strict: false })` types every param as optional, so chatId is
  // `string | undefined` here even though this route cannot match without one.
  // Bail rather than casting: ChatContainer requires a real tabId, and a
  // chat screen with no chat has nothing to render.
  if (!chatId) return null;

  return (
    <div className="flex h-full min-h-0 flex-col">
      {/* One bar: back, the title that opens the Chat sheet, one optional
          status line, and at most two trailing icons. On a phone there is no
          persistent sidebar to go back to, so back has to live here. */}
      <MobileChatTopBar
        title={title}
        onOpenSheet={() => setShowSheet(true)}
        runningWorkflow={runningWorkflow}
        subThread={
          isInSubThread && selectedThread
            ? {
                name: selectedThread.name,
                backLabel: backTarget ? `Back to ${backTarget.name}` : "Back to main thread",
              }
            : undefined
        }
        onBackToThread={() => setSelectedThreadId(backTargetId)}
        noMachine={chat?.noMachine}
        contextUsage={contextUsage}
        onOpenWorkspace={openWorkspace}
      />

      {showSheet && (
        <MobileChatSheet
          chatId={chatId}
          title={title}
          createdAt={chat?.createdAt}
          worktreeId={chat?.worktreeId ?? undefined}
          onClose={() => setShowSheet(false)}
          workflow={workflowExecution}
          threads={threads}
          hasMultipleThreads={hasMultipleThreads}
          selectedThreadId={selectedThreadId}
          onSelectThread={setSelectedThreadId}
          contextUsage={contextUsage}
          onOpenWorkspace={openWorkspace}
        />
      )}

      {effectiveWorktreeId && (
        <MobileWorkspaceSheet
          isOpen={showWorkspace}
          onClose={() => setShowWorkspace(false)}
          chatId={chatId}
          worktreeId={effectiveWorktreeId}
          projectPath={currentProjectPath}
        />
      )}

      {/* min-h-0 is load-bearing: without it the flex child refuses to shrink
          and the virtualized message list grows past the viewport, taking the
          sticky composer off-screen.

          The inset is applied here because this screen hides the tab bar, so
          the shared chat composer is the element at the screen edge and would
          otherwise sit under the home indicator. */}
      <div
        className="mx-auto min-h-0 w-full max-w-3xl flex-1"
        style={{ paddingBottom: "var(--mobile-safe-bottom, env(safe-area-inset-bottom))" }}
      >
        {/* The top bar and Chat sheet replace ChatHeader entirely, so the
            container renders no header row; the thread they pick is owned
            here and drives the container's timeline and composer. */}
        <ChatContainer
          tabId={chatId}
          hideHeader
          selectedThreadId={selectedThreadId}
          onSelectThread={setSelectedThreadId}
        />
      </div>
    </div>
  );
}
