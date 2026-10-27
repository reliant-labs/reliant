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

import { useEffect, useRef, useState } from "react";
import { Link, useParams } from "@tanstack/react-router";
import { ChevronLeft, FolderGit2 } from "lucide-react";
import { ChatContainer } from "../Chat/ChatContainer";
import { useChat } from "../../store/chatStoreHooks";
import { useProjectStore } from "../../store/projectStore";
import { fetchProjects, getCachedProjects } from "../../hooks/project-queries";
import { MobileWorkspaceSheet } from "./MobileWorkspaceSheet";
import { MobileWorkflowExecutionEntry } from "./MobileWorkflowExecutionEntry";
import { MobileScreenHeader } from "./MobileChrome";
import { loadMainWorktree } from "../../hooks/worktree-actions";
import { useMainWorktree } from "../../hooks/worktree-queries";

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

  // `useParams({ strict: false })` types every param as optional, so chatId is
  // `string | undefined` here even though this route cannot match without one.
  // Bail rather than casting: ChatContainer requires a real tabId, and a
  // chat screen with no chat has nothing to render.
  if (!chatId) return null;

  return (
    <div className="flex h-full min-h-0 flex-col">
      {/* Sticky header — on a phone there's no persistent sidebar to go back
          to, so the back affordance has to live in the chat itself. */}
      <MobileScreenHeader
        // A chat title is a sentence, not a label, so it gets the smaller
        // `titleClassName` — at `text-xl` a real title wraps or truncates to
        // three words, which tells the user less than the full line does.
        title={chat?.title || "Chat"}
        titleClassName="text-base font-semibold"
        leading={
          <Link
            to="/m/chats"
            // Explicit px, not `h-10 w-10`: rem sizing resolves against the
            // root font-size, and at the smallest Appearance step `h-10`
            // measures under 44px — on the only navigation control here.
            className="flex min-h-[44px] min-w-[44px] items-center justify-center rounded-md text-muted-foreground active:bg-muted"
            aria-label="Back to chats"
          >
            <ChevronLeft className="h-5 w-5" />
          </Link>
        }
        trailing={
          effectiveWorktreeId ? (
            <button
              type="button"
              onClick={() => setShowWorkspace(true)}
              className="flex min-h-[44px] min-w-[44px] items-center justify-center rounded-md text-muted-foreground active:bg-muted"
              aria-label="Open workspace"
            >
              <FolderGit2 className="h-5 w-5" />
            </button>
          ) : undefined
        }
      />

      {/* Renders only while a workflow is actually executing — "what is my
          agent doing right now" is the highest-value thing a phone can answer,
          and it's the one workflow affordance mobile has (the desktop panel is
          gated off via chatExecutionSidebar). */}
      <MobileWorkflowExecutionEntry chatId={chatId} />

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
        className="min-h-0 flex-1"
        style={{ paddingBottom: "var(--mobile-safe-bottom, env(safe-area-inset-bottom))" }}
      >
        {/* The screen header above already shows the title; ChatHeader's own
            copy was a second 50px band under it. Its menu, context ring and
            thread tabs stay. */}
        <ChatContainer tabId={chatId} hideChatTitle />
      </div>
    </div>
  );
}
