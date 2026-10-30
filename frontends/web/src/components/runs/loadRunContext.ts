// Copyright (c) 2025 Reliant Labs

/**
 * Put the stores in the state a chat's transcript reads from, for a chat that
 * may belong to any project. The non-navigating half of opening a chat from
 * outside the project view.
 *
 * ChatContainer reads the current project and worktree from stores, not from
 * props, and the chat stream subscribes to the active chat. So rendering one
 * chat's transcript anywhere means: select the chat's project (it may not be
 * the current one — the Runs list spans projects), load that project's
 * worktrees, switch to the chat's worktree, and select the chat.
 *
 * selectChat persists the chat as the worktree's active chat and
 * switchWorktreeContext persists the worktree, so the project view restored
 * later lands on this chat rather than whatever was open before.
 */

import type { Chat } from "@/api/client";
import { seedChatDetail } from "@/hooks/chat-queries";
import { beginWorkspaceOpen, ChatProjectUnavailableError, openChatInWorkspace } from "@/lib/openChatInWorkspace";
import * as chatActions from "../../hooks/chat-actions";

export class RunProjectUnavailableError extends Error {
  constructor() {
    super("That run's project is no longer available.");
    this.name = "RunProjectUnavailableError";
  }
}

/** Load a chat under the same navigation fence as every other workspace open. */
export async function loadRunContext(chatId: string, signal?: AbortSignal): Promise<Chat> {
  // Start the intent BEFORE the lookup: a later chat open must supersede it
  // even if that lookup is still pending. Unmount cancels this intent only.
  const isLatest = beginWorkspaceOpen();
  const isCurrent = () => isLatest() && !signal?.aborted;
  const assertCurrent = () => {
    if (!isCurrent()) throw new DOMException("Run navigation cancelled", "AbortError");
  };
  assertCurrent();
  const chat = await chatActions.getChat(chatId);
  assertCurrent();
  seedChatDetail(chat);

  try {
    // The shared opener fences project selection, workspace switching (also
    // after its internal await), chat persistence and navigation together.
    await openChatInWorkspace(chat, { navigate: true, isCurrent });
  } catch (error) {
    assertCurrent();
    if (error instanceof ChatProjectUnavailableError) throw new RunProjectUnavailableError();
    throw error;
  }
  assertCurrent();
  return chat;
}
