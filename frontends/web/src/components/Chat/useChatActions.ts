/**
 * useChatActions - the chat-level actions, one implementation for every shell.
 *
 * The desktop `ChatHeader` dropdown and the mobile Chat sheet offer the same
 * moves on a chat: rename, export, copy its id, archive with Undo, open a
 * terminal in its workspace, compact its context, and the workspace recovery
 * items. They used to live inline in `ChatHeader`, so a second shell would have
 * meant a second copy of each handler — and the two copies drifting (the sheet
 * archiving without the Undo toast, say). Shells own only presentation: menu
 * state, inline-edit state, where a row sits.
 *
 * Capability-gated actions are `undefined` when the surface lacks them, so a
 * shell renders an item only when it does something (`openTerminal` on mobile,
 * where there is no terminal panel to show).
 */

import { useCallback, useEffect, useState } from "react";
import { useArchiveChat, useChat, useRenameChat, useUnarchiveChat } from "../../hooks/chat-queries";
import { useMessages } from "../../hooks/message-queries";
import { fetchFullToolResult } from "../../hooks/tool-result-queries";
import * as chatActions from "../../hooks/chat-actions";
import { loadMainWorktree } from "../../hooks/worktree-actions";
import { resolveWorktree, type Worktree, getCachedMainWorktree } from "../../hooks/worktree-queries";
import { useTerminalStore } from "../../store/terminalStore";
import { useCapability } from "../../lib/surfaceContext";
import { toast } from "../../lib/toast-manager";
import { logger } from "../../lib/logger";
import { ContentBlockType, MessageRole } from "../../gen/reliant/v1/chat_pb";
import type { Message } from "../../api/client";

export interface ChatActions {
  /** Workspace the chat runs in, once resolved. Feeds the recovery items. */
  worktree: Worktree | null;
  /** The project's main checkout, the recovery items' "move to" target. */
  mainWorktree: Worktree | undefined;
  /** Branch name, else workspace name. */
  workspaceName: string | null;
  /**
   * Rename the chat. Resolves true once the title is settled (saved, or there
   * was nothing to change) and false when the save failed, so an inline editor
   * knows whether to close.
   */
  rename: (title: string) => Promise<boolean>;
  /** Archive, then offer Undo in a toast (like the sidebar row). */
  archive: () => Promise<void>;
  /** Download the transcript as markdown, with truncated tool results in full. */
  exportChat: () => Promise<void>;
  copyChatId: () => Promise<void>;
  /** Compact a thread's context; "" is the main thread. */
  compact: (threadId: string) => Promise<void>;
  /** Present only where the surface has a terminal and the workspace a path. */
  openTerminal?: () => void;
}

/**
 * Compact a chat's context with its user feedback (toasts). A plain function,
 * not only the hook's `compact`: the composer's `/compact` calls it directly,
 * because `useChatActions` subscribes to the chat's messages and would
 * re-render the composer on every streamed update.
 */
export async function compactChatWithFeedback(chatId: string | null, threadId: string): Promise<void> {
  if (!chatId) {
    toast.info("Start a conversation before compacting it");
    return;
  }
  try {
    await chatActions.compactChat(chatId, threadId);
    toast.success("Compacting conversation…");
  } catch (error) {
    logger.error("[useChatActions] Compaction failed", error);
    toast.error("Could not compact the conversation");
  }
}

export function useChatActions(chatId: string | null, worktreeIdOverride?: string): ChatActions {
  const chat = useChat(chatId || undefined).data;
  const messages = useMessages(chatId || undefined).data?.messages;
  const archiveMutation = useArchiveChat();
  const unarchiveMutation = useUnarchiveChat();
  const renameMutation = useRenameChat();
  const hasTerminal = useCapability("terminal");

  // The workspace and path. Fetched by id (and cached in the store) rather than
  // read from a list, so it resolves whichever workspace the chat is in.
  const workspaceId = worktreeIdOverride || chat?.worktreeId;
  const [worktree, setWorktree] = useState<Worktree | null>(null);
  const [mainWorktree, setMainWorktree] = useState<Worktree | undefined>(undefined);
  useEffect(() => {
    let cancelled = false;
    setWorktree(null);
    setMainWorktree(undefined);
    if (!workspaceId) return;
    void (async () => {
      const resolved = await resolveWorktree(workspaceId);
      if (cancelled) return;
      setWorktree(resolved);
      if (!resolved) return;
      if (resolved.is_main) {
        setMainWorktree(resolved);
        return;
      }
      // Only the recovery items read it, so fetch it (one row) rather than
      // depend on the store happening to hold it.
      if (!resolved.project_id) return;
      await loadMainWorktree(resolved.project_id);
      if (cancelled) return;
      const main = getCachedMainWorktree(resolved.project_id);
      setMainWorktree(main && !main.deleted_at ? main : undefined);
    })();
    return () => {
      cancelled = true;
    };
  }, [workspaceId]);
  const workspaceName = worktree ? worktree.branch || worktree.name || null : null;
  const workspacePath = worktree?.path || null;

  const currentTitle = chat?.title;
  const rename = useCallback(
    async (title: string) => {
      const trimmed = title.trim();
      if (!chatId || !trimmed || trimmed === currentTitle) return true;
      try {
        await renameMutation.mutateAsync({ chatId, title: trimmed });
        return true;
      } catch (err) {
        void toast.error(err);
        return false;
      }
    },
    [chatId, currentTitle, renameMutation],
  );

  // Archive, with Undo. This was "Delete Chat" behind a native confirm() while
  // it archived; deleting for good is only offered on an archived chat, in the
  // sidebar's Archived list.
  const archive = useCallback(async () => {
    if (!chatId) return;
    try {
      await archiveMutation.mutateAsync(chatId);
    } catch (err) {
      void toast.error(err);
      return;
    }
    toast.notify("Chat archived", {
      action: { label: "Undo", onClick: () => unarchiveMutation.mutate(chatId) },
    });
  }, [chatId, archiveMutation, unarchiveMutation]);

  const exportChat = useCallback(async () => {
    if (!chatId || !chat) return;
    try {
      const markdown = await buildChatMarkdown(chatId, chat.title, workspaceName, messages ?? []);
      const blob = new Blob([markdown], { type: "text/markdown" });
      const url = URL.createObjectURL(blob);
      const link = document.createElement("a");
      link.href = url;
      link.download = `${chat.title?.replace(/[^a-z0-9]/gi, "_") || "chat"}_${new Date().toISOString().split("T")[0]}.md`;
      document.body.appendChild(link);
      link.click();
      document.body.removeChild(link);
      URL.revokeObjectURL(url);
    } catch (err) {
      logger.error("Failed to export chat:", err);
      void toast.error(err);
    }
  }, [chatId, chat, workspaceName, messages]);

  const copyChatId = useCallback(async () => {
    if (!chatId) return;
    try {
      await navigator.clipboard.writeText(chatId);
      toast.success("Chat ID copied");
    } catch (err) {
      logger.error("Failed to copy chat ID:", err);
      void toast.error(err);
    }
  }, [chatId]);

  const compact = useCallback(
    (threadId: string) => compactChatWithFeedback(chatId, threadId),
    [chatId],
  );

  const terminalWorktreeId = worktree?.id;
  const terminalProjectId = worktree?.project_id;
  const openTerminal = useCallback(() => {
    if (!workspacePath || !terminalWorktreeId) return;
    const terminal = useTerminalStore.getState();
    const existing = terminal.getWorktreeSessions(terminalWorktreeId);
    if (existing.length > 0) {
      terminal.setActiveSession(existing[0].id);
    } else {
      terminal.createSession(workspacePath, terminalProjectId, terminalWorktreeId);
    }
    terminal.showTerminal();
  }, [workspacePath, terminalWorktreeId, terminalProjectId]);

  return {
    worktree,
    mainWorktree,
    workspaceName,
    rename,
    archive,
    exportChat,
    copyChatId,
    compact,
    openTerminal: hasTerminal && workspacePath ? openTerminal : undefined,
  };
}

async function buildChatMarkdown(
  chatId: string,
  title: string | undefined,
  workspaceName: string | null,
  messages: Message[],
): Promise<string> {
  // Large tool results are held as previews; the export gets them whole.
  const fullResults = new Map<string, string>();
  await Promise.all(
    messages.flatMap((message) =>
      (message.contentBlocks || [])
        .filter((block) => block.type === ContentBlockType.TOOL_RESULT && block.truncated && block.toolCallId)
        .map(async (block) => {
          fullResults.set(block.toolCallId!, await fetchFullToolResult(chatId, block.toolCallId!));
        }),
    ),
  );

  let markdown = `# ${title || "Chat Export"}\n\n`;
  markdown += `Exported: ${new Date().toLocaleString()}\n`;
  if (workspaceName) {
    markdown += `Workspace: ${workspaceName}\n`;
  }
  markdown += `\n---\n\n`;

  for (const message of messages) {
    const role = message.sender
      ? `Message from ${message.sender.title || "another agent"}`
      : message.role === MessageRole.USER ? "User" : "Assistant";
    const timestamp = new Date(message.createdAt || "").toLocaleString();
    markdown += `## ${role} - ${timestamp}\n\n`;

    for (const block of message.contentBlocks || []) {
      if (block.type === ContentBlockType.TEXT && block.content) {
        markdown += `${block.content}\n\n`;
      } else if (block.type === ContentBlockType.TOOL_CALL) {
        markdown += `**Tool Call:** ${block.toolName}\n\n`;
        if (block.input) {
          try {
            const parsed = JSON.parse(block.input);
            markdown += `\`\`\`json\n${JSON.stringify(parsed, null, 2)}\n\`\`\`\n\n`;
          } catch {
            markdown += `\`\`\`\n${block.input}\n\`\`\`\n\n`;
          }
        }
      } else if (block.type === ContentBlockType.TOOL_RESULT) {
        markdown += `**Tool Result:** ${block.toolName || "Unknown"}\n\n`;
        const content = (block.toolCallId && fullResults.get(block.toolCallId)) || block.content;
        if (content) {
          markdown += `${content}\n\n`;
        }
      }
    }

    markdown += `---\n\n`;
  }
  return markdown;
}
